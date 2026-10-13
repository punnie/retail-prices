// Package web serves the product catalogue, product pages (price history +
// pricing analysis per PRICING_CONCEPTS.md §4) and the cross-retailer
// linking review queue. Stdlib net/http + html/template only.
package web

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/auth"
	"retail-prices/internal/db"
	"retail-prices/internal/imageproxy"
	"retail-prices/internal/pricing"
)

type server struct {
	pool     *pgxpool.Pool
	requests auth.Requests
	limiter  *auth.Limiter
}

// Guest submission limits per client IP. Generous for a person clicking
// through suggestions, tight enough that a script can't flood moderation.
var guestLimits = []auth.Limit{
	{Max: 30, Window: time.Hour},
	{Max: 200, Window: 24 * time.Hour},
}

// allowedImageHosts is the exact set of CDN/site hosts retail_products.image_url
// actually uses (profiled from the real dump: www.auchan.pt, www.celeiro.pt,
// www.continente.pt, www.zu.pt — Continente's cross-sell partner —,
// static.pingodoce.pt, www.pingodoce.pt). The image proxy refuses every
// other source, so it can never be used to fetch arbitrary URLs.
var allowedImageHosts = []string{
	"www.auchan.pt",
	"www.celeiro.pt",
	"www.continente.pt",
	"www.zu.pt",
	"static.pingodoce.pt",
	"www.pingodoce.pt",
}

func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	imageCacheDir := fs.String("image-cache-dir", ".imagecache", "directory to cache retailer product images in")
	imageCacheTTL := fs.Duration("image-cache-ttl", imageproxy.DefaultTTL,
		"how long a cached image is served before refetching (default 1 month — "+
			"long on purpose, so the catalogue never hits retailer CDNs on every page view)")
	trustProxy := fs.Bool("trust-proxy", false,
		"read the client IP and scheme from X-Forwarded-For/-Proto; only behind a reverse proxy that sets them")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	ipSecret, err := auth.LoadOrCreateSecret(ctx, pool, "ip_hash")
	if err != nil {
		return fmt.Errorf("loading server secret (run `retail migrate`?): %w", err)
	}
	s := &server{
		pool:     pool,
		requests: auth.Requests{TrustProxy: *trustProxy, IPSecret: ipSecret},
		limiter:  auth.NewLimiter(guestLimits...),
	}
	imgProxy := imageproxy.New(*imageCacheDir, allowedImageHosts, *imageCacheTTL)

	fmt.Fprintf(os.Stderr, "listening on %s (image cache: %s, ttl %s, trust proxy: %v)\n",
		*addr, *imageCacheDir, *imageCacheTTL, *trustProxy)
	return http.ListenAndServe(*addr, s.handler(imgProxy))
}

// handler builds the full middleware chain: security headers, then
// cross-origin request rejection (Go's CSRF protection, based on
// Sec-Fetch-Site/Origin, so forms need no tokens), then viewer resolution.
func (s *server) handler(img http.Handler) http.Handler {
	admin := auth.RequireAdmin

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleCatalogue)
	mux.HandleFunc("GET /products", s.handleCatalogue)
	mux.HandleFunc("GET /products/{id}", s.handleProduct)
	mux.HandleFunc("POST /products/{id}/report", s.handleReport)
	mux.HandleFunc("GET /match", s.handleMatchQueue)
	mux.HandleFunc("POST /match/decide", s.handleMatchDecide)
	mux.HandleFunc("GET /match/manual", s.handleManualLink)
	mux.HandleFunc("POST /match/manual", s.handleManualLinkPost)
	mux.HandleFunc("GET /match/bulk", admin(s.handleBulkReview))
	mux.HandleFunc("POST /match/bulk", admin(s.handleBulkDecide))
	mux.HandleFunc("GET /admin/moderation", admin(s.handleModeration))
	mux.HandleFunc("POST /admin/moderation/review", admin(s.handleModerationReview))
	mux.HandleFunc("GET /admin/login", s.handleLoginPage)
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)
	mux.Handle("GET /img", img)

	lookup := func(ctx context.Context, raw string) (string, bool, error) {
		return auth.SessionAdmin(ctx, s.pool, raw)
	}
	return securityHeaders(http.NewCrossOriginProtection().Handler(auth.Middleware(lookup, mux)))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// page is the part of every view the shared layout needs.
type page struct {
	Title   string
	Viewer  auth.Viewer
	Pending int // pending moderation items, administrators only
	Flash   string
}

func (s *server) newPage(r *http.Request, title string) (page, error) {
	p := page{Title: title, Viewer: auth.ViewerFrom(r.Context()), Flash: flashText(r.URL.Query().Get("flash"))}
	if p.Viewer.Admin {
		n, err := s.pendingCount(r.Context())
		if err != nil {
			return p, err
		}
		p.Pending = n
	}
	return p, nil
}

// Flash messages are passed as short codes, never free text, so a crafted
// link can't put words on the page.
const (
	flashSent      = "sent"
	flashLinked    = "linked"
	flashUnlinked  = "unlinked"
	flashSignedIn  = "signedin"
	flashSignedOut = "signedout"
)

func flashText(code string) string {
	switch code {
	case flashSent:
		return "Thanks! Your suggestion was sent for review. It will appear once a moderator accepts it."
	case flashLinked:
		return "Linked."
	case flashUnlinked:
		return "Unlinked."
	case flashSignedIn:
		return "Signed in as administrator."
	case flashSignedOut:
		return "Signed out."
	}
	return ""
}

func withFlash(path, code string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "flash=" + code
}

// render executes a page template; errors after headers are sent can only
// be logged.
func (s *server) render(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		fmt.Fprintf(os.Stderr, "error rendering: %v\n", err)
	}
}

type messageView struct {
	page
	Heading, Body, Back string
}

func (s *server) renderMessage(w http.ResponseWriter, r *http.Request, status int, heading, body, back string) {
	p, err := s.newPage(r, heading)
	if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, status, messageTmpl, messageView{page: p, Heading: heading, Body: body, Back: back})
}

type loginView struct {
	page
	Token, Name, Error string
}

// handleLoginPage only shows a confirmation button: link previewers and
// mail scanners fetch URLs with GET, and must not consume the token.
func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.newPage(r, "Administrator sign-in")
	if err != nil {
		httpError(w, err)
		return
	}
	v := loginView{page: p, Token: r.URL.Query().Get("token")}
	status := http.StatusOK
	if v.Token == "" {
		v.Error = "Sign-in links are created with `retail admin login-link` on the server."
		status = http.StatusBadRequest
	} else if v.Name, err = auth.PeekLoginToken(r.Context(), s.pool, v.Token); errors.Is(err, auth.ErrInvalidToken) {
		v.Error = err.Error() + ". Create a new one with `retail admin login-link`."
		status = http.StatusBadRequest
	} else if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, status, loginTmpl, v)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	_, session, err := auth.RedeemLoginToken(r.Context(), s.pool, r.FormValue("token"))
	if errors.Is(err, auth.ErrInvalidToken) {
		s.renderMessage(w, r, http.StatusBadRequest, "Couldn't sign in", err.Error()+".", "/")
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	s.requests.SetAdminCookie(w, r, session)
	http.Redirect(w, r, withFlash("/admin/moderation", flashSignedIn), http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.AdminCookie); err == nil {
		if err := auth.DeleteSession(r.Context(), s.pool, c.Value); err != nil {
			httpError(w, err)
			return
		}
	}
	s.requests.ClearAdminCookie(w, r)
	http.Redirect(w, r, withFlash("/", flashSignedOut), http.StatusSeeOther)
}

// handleReport is "this link is wrong" on a product page's linked listings:
// an administrator unlinks the sibling immediately, a guest's report goes
// to moderation.
func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	id, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil || err1 != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sibling, err := strconv.ParseInt(r.FormValue("sibling"), 10, 64)
	if err != nil {
		http.Error(w, "bad product id", http.StatusBadRequest)
		return
	}
	back := "/products/" + strconv.FormatInt(id, 10)
	v := auth.ViewerFrom(r.Context())
	if !v.Admin {
		s.guestDone(w, r, s.submitContribution(w, r, kindUnlink, id, sibling), back)
		return
	}
	err = s.unlink(r.Context(), id, sibling, v.DecidedBy())
	var le linkError
	switch {
	case errors.As(err, &le):
		s.renderMessage(w, r, http.StatusConflict, "Couldn't unlink", string(le), back)
	case err != nil:
		httpError(w, fmt.Errorf("unlinking: %w", err))
	default:
		http.Redirect(w, r, withFlash(back, flashUnlinked), http.StatusSeeOther)
	}
}

func httpError(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

const catalogueLimit = 60

type catalogueView struct {
	page
	Query, Store string
	Stores       []string
	Products     []catalogueRow
}

func (s *server) handleCatalogue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query().Get("q")
	store := r.URL.Query().Get("store")

	products, err := listProducts(ctx, s.pool, q, store, catalogueLimit)
	if err != nil {
		httpError(w, err)
		return
	}
	stores, err := listStores(ctx, s.pool)
	if err != nil {
		httpError(w, err)
		return
	}

	p, err := s.newPage(r, "Catalogue")
	if err != nil {
		httpError(w, err)
		return
	}
	s.render(w, http.StatusOK, catalogueTmpl, catalogueView{page: p, Query: q, Store: store, Stores: stores, Products: products})
}

type productView struct {
	page
	Product           productRow
	HasSnapshot       bool
	Snapshot          pricing.Snapshot
	StateBadge        string
	BuyNowSignal      string
	Chart             template.HTML
	Episodes          []pricing.Episode
	Changes           []pricing.RegularChange
	Siblings          []siblingRow
	ObservationFooter string
}

func (s *server) handleProduct(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	product, ok, err := loadProduct(ctx, s.pool, id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	p, err := s.newPage(r, product.Name)
	if err != nil {
		httpError(w, err)
		return
	}
	view := productView{page: p, Product: product}

	snap, hasSnap, err := loadSnapshot(ctx, s.pool, id)
	if err != nil {
		httpError(w, err)
		return
	}
	view.HasSnapshot = hasSnap
	view.Snapshot = snap

	if hasSnap {
		spells, err := loadSpells(ctx, s.pool, id)
		if err != nil {
			httpError(w, err)
			return
		}
		changes, err := loadChanges(ctx, s.pool, id)
		if err != nil {
			httpError(w, err)
			return
		}
		intervals, err := loadObservedIntervals(ctx, s.pool, id)
		if err != nil {
			httpError(w, err)
			return
		}

		now := time.Now()
		view.StateBadge = stateBadge(snap, now)
		view.BuyNowSignal = buyNowSignal(snap)
		view.Chart = renderChart(observedPoints(intervals), spells, changes, snap.Low90d, snap.Low1y)
		view.Changes = changes

		for _, e := range pricing.Episodes(spells) {
			view.Episodes = append(view.Episodes, e)
		}

		lastObserved := product.LastSeen
		if len(intervals) > 0 {
			lastObserved = intervals[len(intervals)-1].ValidTo
		}
		staleness := ""
		if now.Sub(lastObserved) > 14*24*time.Hour {
			staleness = " — data may be stale"
		}
		view.ObservationFooter = fmt.Sprintf("Last observed %s%s.", lastObserved.Format("2006-01-02"), staleness)
	} else {
		view.ObservationFooter = fmt.Sprintf("Last observed %s.", product.LastSeen.Format("2006-01-02"))
	}

	siblings, err := loadGroupSiblings(ctx, s.pool, id)
	if err != nil {
		httpError(w, err)
		return
	}
	view.Siblings = siblings

	s.render(w, http.StatusOK, productTmpl, view)
}
