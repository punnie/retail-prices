// Package web serves the product catalogue, product pages (price history +
// pricing analysis per PRICING_CONCEPTS.md §4) and the cross-retailer
// linking review queue. Stdlib net/http + html/template only.
package web

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/db"
	"retail-prices/internal/imageproxy"
	"retail-prices/internal/pricing"
)

type server struct {
	pool *pgxpool.Pool
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
	if err := fs.Parse(args); err != nil {
		return err
	}

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	s := &server{pool: pool}
	imgProxy := imageproxy.New(*imageCacheDir, allowedImageHosts, *imageCacheTTL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleCatalogue)
	mux.HandleFunc("GET /products", s.handleCatalogue)
	mux.HandleFunc("GET /products/{id}", s.handleProduct)
	mux.HandleFunc("GET /match", s.handleMatchQueue)
	mux.HandleFunc("POST /match/decide", s.handleMatchDecide)
	mux.HandleFunc("GET /img", imgProxy.ServeHTTP)

	fmt.Fprintf(os.Stderr, "listening on %s (image cache: %s, ttl %s)\n", *addr, *imageCacheDir, *imageCacheTTL)
	return http.ListenAndServe(*addr, mux)
}

func httpError(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

const catalogueLimit = 60

type catalogueView struct {
	Title        string
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := catalogueTmpl.ExecuteTemplate(w, "layout", catalogueView{
		Title: "Catalogue", Query: q, Store: store, Stores: stores, Products: products,
	}); err != nil {
		httpError(w, err)
	}
}

type productView struct {
	Title             string
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

	view := productView{Title: product.Name, Product: product}

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := productTmpl.ExecuteTemplate(w, "layout", view); err != nil {
		httpError(w, err)
	}
}
