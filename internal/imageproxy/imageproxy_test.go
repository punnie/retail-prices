package imageproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func newTestProxy(t *testing.T, allowedHost string, ttl time.Duration) *Proxy {
	t.Helper()
	return New(t.TempDir(), []string{allowedHost}, ttl)
}

func TestProxy_FetchesCachesAndServesFromAllowedHost(t *testing.T) {
	var hits int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("fake-image-bytes"))
	}))
	defer origin.Close()

	host := mustHost(t, origin.URL)
	p := newTestProxy(t, host, DefaultTTL)

	req1 := httptest.NewRequest(http.MethodGet, "/img?src="+url.QueryEscape(origin.URL+"/pic.jpg"), nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: got status %d, body %q", rec1.Code, rec1.Body.String())
	}
	if rec1.Body.String() != "fake-image-bytes" {
		t.Fatalf("first request: got body %q", rec1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/img?src="+url.QueryEscape(origin.URL+"/pic.jpg"), nil)
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "fake-image-bytes" {
		t.Fatalf("second request: got status %d, body %q", rec2.Code, rec2.Body.String())
	}

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("origin hit %d times, want 1 (second request should be served from cache)", got)
	}
}

func TestProxy_RejectsDisallowedHost(t *testing.T) {
	p := newTestProxy(t, "www.continente.pt", DefaultTTL)

	req := httptest.NewRequest(http.MethodGet, "/img?src="+url.QueryEscape("https://evil.example.com/pic.jpg"), nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("got status %d, want 403", rec.Code)
	}
}

func TestProxy_MissingSrcIsBadRequest(t *testing.T) {
	p := newTestProxy(t, "www.continente.pt", DefaultTTL)

	req := httptest.NewRequest(http.MethodGet, "/img", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", rec.Code)
	}
}

// A refetch that fails (origin down, now blocking us, whatever) must still
// serve the stale copy rather than a broken image — "out of date" beats
// "missing".
func TestProxy_StaleCacheServedWhenRefetchFails(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer origin.Close()

	host := mustHost(t, origin.URL)
	p := newTestProxy(t, host, 1*time.Hour)

	src := origin.URL + "/pic.jpg"
	path := p.cachePath(src)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Older than the 1-hour TTL, so the proxy will attempt a refresh.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/img?src="+url.QueryEscape(src), nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (stale copy should still be served)", rec.Code)
	}
	if rec.Body.String() != "stale-bytes" {
		t.Errorf("got body %q, want the stale cached bytes", rec.Body.String())
	}
}

func TestProxy_FreshCacheSkipsOriginEntirely(t *testing.T) {
	p := newTestProxy(t, "www.continente.pt", DefaultTTL)
	src := "https://www.continente.pt/img/pic.jpg"
	path := p.cachePath(src)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cached-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/img?src="+url.QueryEscape(src), nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "cached-bytes" {
		t.Fatalf("got status %d, body %q", rec.Code, rec.Body.String())
	}
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}
