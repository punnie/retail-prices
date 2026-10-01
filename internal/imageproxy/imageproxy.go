// Package imageproxy caches retailer product images on local disk and
// serves them from there, so the catalogue and product pages never hotlink
// a retailer's CDN directly. Every page load that showed thumbnails straight
// from continente.pt/auchan.pt/etc. would otherwise look like repeated,
// automated traffic to the retailer — exactly the pattern that gets a
// scraper flagged or blocked. A long TTL is the point: product photos
// change rarely, so re-fetching anything more often than about once a month
// only adds request volume for no benefit.
package imageproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultTTL is how long a cached image is considered fresh before the
// proxy tries to refetch it.
const DefaultTTL = 30 * 24 * time.Hour

// maxBodyBytes caps a single fetch — generous for a product thumbnail,
// stingy for anything else a misbehaving or malicious response might send.
const maxBodyBytes = 10 << 20 // 10MB

// Proxy caches images fetched from an allowlisted set of hosts on disk
// (sharded two-level directories under Dir, keyed by sha256 of the source
// URL) and serves them back. A stale cache entry is refreshed in the
// background of the request; if the refetch fails — network error, the
// retailer blocking us, a 404 — the stale copy is served anyway rather than
// showing a broken image, since "slightly out of date" beats "missing".
type Proxy struct {
	Dir          string
	TTL          time.Duration
	AllowedHosts map[string]bool
	Client       *http.Client

	mu    sync.Mutex
	calls map[string]*call
}

// call coalesces concurrent requests for the same not-yet-cached (or
// just-expired) URL into a single outbound fetch.
type call struct {
	done chan struct{}
	err  error
}

// New builds a Proxy that only ever fetches from the given hostnames
// (exact match, e.g. "www.continente.pt") — every other source URL is
// refused before any network request is made.
func New(dir string, allowedHosts []string, ttl time.Duration) *Proxy {
	hosts := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		hosts[h] = true
	}
	return &Proxy{
		Dir:          dir,
		TTL:          ttl,
		AllowedHosts: hosts,
		Client:       &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *Proxy) cachePath(src string) string {
	sum := sha256.Sum256([]byte(src))
	hexsum := hex.EncodeToString(sum[:])
	return filepath.Join(p.Dir, hexsum[:2], hexsum+".bin")
}

// ServeHTTP handles `GET /img?src=<original image URL>`.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	if src == "" {
		http.Error(w, "missing src", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !p.AllowedHosts[u.Hostname()] {
		http.Error(w, "source not allowed", http.StatusForbidden)
		return
	}

	path := p.cachePath(src)
	fi, statErr := os.Stat(path)
	fresh := statErr == nil && time.Since(fi.ModTime()) < p.TTL

	if !fresh {
		fetchErr := p.doSingleflight(src, func() error { return p.fetch(src, path) })
		if fetchErr != nil && statErr != nil {
			// Never cached, and the fetch failed: nothing to serve.
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
		// Either the fetch succeeded, or it failed but a stale copy exists
		// on disk — serve whatever's there either way.
		fi, statErr = os.Stat(path)
		if statErr != nil {
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
	}

	serveFile(w, r, path, fi.ModTime(), p.TTL)
}

// doSingleflight runs fn for key, or waits for and shares the result of an
// identical call already in flight — so a burst of page loads for the same
// uncached image never triggers more than one outbound fetch.
func (p *Proxy) doSingleflight(key string, fn func() error) error {
	p.mu.Lock()
	if c, ok := p.calls[key]; ok {
		p.mu.Unlock()
		<-c.done
		return c.err
	}
	c := &call{done: make(chan struct{})}
	if p.calls == nil {
		p.calls = map[string]*call{}
	}
	p.calls[key] = c
	p.mu.Unlock()

	c.err = fn()
	close(c.done)

	p.mu.Lock()
	delete(p.calls, key)
	p.mu.Unlock()

	return c.err
}

func (p *Proxy) fetch(src, path string) error {
	req, err := http.NewRequest(http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "retail-prices-image-cache/1.0 (local product catalogue; see https://github.com/punnie/price-scraper)")

	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching %s: unexpected status %s", src, resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBodyBytes+1))
	closeErr := f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	if n > maxBodyBytes {
		os.Remove(tmp)
		return fmt.Errorf("fetching %s: response exceeds %d bytes", src, maxBodyBytes)
	}

	// Atomic within the cache dir, so a concurrent reader never sees a
	// partially-written file.
	return os.Rename(tmp, path)
}

func serveFile(w http.ResponseWriter, r *http.Request, path string, modTime time.Time, ttl time.Duration) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	defer f.Close()

	// Sniff content-type from the cached bytes rather than persisting it
	// separately in a sidecar file — cheap, and there's then nothing extra
	// to keep in sync with the cache entry.
	var sniff [512]byte
	n, _ := f.Read(sniff[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "image unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(sniff[:n]))
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(ttl.Seconds())))

	http.ServeContent(w, r, "", modTime, f)
}
