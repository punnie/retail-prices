// Package report renders `retail report`: a self-contained HTML page of
// price changes over a recent window, built from price_events/price_intervals.
// Suspect events (see internal/quality) are excluded by default — they're
// data-quality noise, not price changes — but can be inspected with
// --include-suspect since nothing is ever deleted from the derived layer.
package report

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	"retail-prices/internal/db"
)

type changeEvent struct {
	Store, Name, Brand, Category, URL, ImageURL string
	OldPrice, NewPrice, PctChange               float64
	ChangedAt                                   time.Time
	Kind, Confidence                            string
	ProductID                                   int64
	Sparkline                                   template.HTML
	// CurrentPrice is the product's latest non-suspect price, and StillActive
	// reports whether it is still at the price this event moved it to. A
	// promo_start whose discount has already lapsed is useful information,
	// but it is not a promotion you can go and buy today.
	CurrentPrice float64
	StillActive  bool
}

type reportData struct {
	GeneratedAt          string
	Since                string
	IncludeSuspect       bool
	PromosActive         []changeEvent
	PromosLapsed         []changeEvent
	PromoEnds            []changeEvent
	Increases            []changeEvent
	Decreases            []changeEvent
	Suspects             []changeEvent
	TotalEventsInWindow  int
	SuspectShareOfWindow float64
}

// maxPerSection caps how many rows render per section so the report stays a
// reasonably sized static page even over a long --since window; the exact
// count is always stated, so truncation is visible, not silent.
const maxPerSection = 100

func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	since := fs.String("since", "7d", "how far back to look (e.g. 24h, 7d, 4w)")
	out := fs.String("out", "report.html", "output HTML file path")
	store := fs.String("store", "", "limit to one store (exact match, e.g. \"Pingo Doce\")")
	includeSuspect := fs.Bool("include-suspect", false, "also render the suspected-glitch section")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sinceDur, err := parseSince(*since)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-sinceDur)

	pool, err := db.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	// current_price is the product's most recent non-suspect price, used to
	// tell whether the change this event describes is still in force.
	rows, err := pool.Query(ctx, `
		WITH current AS (
			SELECT DISTINCT ON (retail_product_id) retail_product_id, price
			FROM price_intervals
			WHERE NOT is_suspect
			ORDER BY retail_product_id, valid_to DESC
		)
		SELECT rp.id, rp.store, rp.name, coalesce(rp.brand,''), coalesce(rp.category,''), rp.url, coalesce(rp.image_url,''),
		       pe.old_price, pe.new_price, pe.pct_change, pe.changed_at, pe.kind, pe.confidence,
		       coalesce(c.price, pe.new_price)
		FROM price_events pe
		JOIN retail_products rp ON rp.id = pe.retail_product_id
		LEFT JOIN current c ON c.retail_product_id = pe.retail_product_id
		WHERE pe.changed_at >= $1
		  AND NOT rp.is_phantom
		  AND pe.old_price IS NOT NULL
		  AND ($2 = '' OR rp.store = $2)
		ORDER BY pe.changed_at DESC`, cutoff, *store)
	if err != nil {
		return fmt.Errorf("querying price_events: %w", err)
	}

	var all []changeEvent
	suspectCount := 0
	for rows.Next() {
		var e changeEvent
		var oldPrice *float64
		if err := rows.Scan(&e.ProductID, &e.Store, &e.Name, &e.Brand, &e.Category, &e.URL, &e.ImageURL,
			&oldPrice, &e.NewPrice, &e.PctChange, &e.ChangedAt, &e.Kind, &e.Confidence,
			&e.CurrentPrice); err != nil {
			rows.Close()
			return err
		}
		if oldPrice != nil {
			e.OldPrice = *oldPrice
		}
		e.StillActive = math.Abs(e.CurrentPrice-e.NewPrice) < 0.005
		if e.Kind == "suspect" {
			suspectCount++
		}
		all = append(all, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	data := reportData{
		GeneratedAt:         time.Now().Format("2006-01-02 15:04 MST"),
		Since:               *since,
		IncludeSuspect:      *includeSuspect,
		TotalEventsInWindow: len(all),
	}
	if len(all) > 0 {
		data.SuspectShareOfWindow = 100 * float64(suspectCount) / float64(len(all))
	}

	productIDs := make([]int64, 0, len(all))
	seen := map[int64]bool{}
	for _, e := range all {
		switch e.Kind {
		case "promo_start":
			if e.StillActive {
				data.PromosActive = append(data.PromosActive, e)
			} else {
				data.PromosLapsed = append(data.PromosLapsed, e)
			}
		case "promo_end":
			data.PromoEnds = append(data.PromoEnds, e)
		case "increase":
			data.Increases = append(data.Increases, e)
		case "decrease":
			data.Decreases = append(data.Decreases, e)
		case "suspect":
			if *includeSuspect {
				data.Suspects = append(data.Suspects, e)
			}
		}
		if !seen[e.ProductID] {
			seen[e.ProductID] = true
			productIDs = append(productIDs, e.ProductID)
		}
	}

	sortByMagnitude(data.PromosActive)
	sortByMagnitude(data.PromosLapsed)
	sortByMagnitude(data.PromoEnds)
	sortByMagnitude(data.Increases)
	sortByMagnitude(data.Decreases)
	sortByMagnitude(data.Suspects)
	data.PromosActive = truncate(data.PromosActive)
	data.PromosLapsed = truncate(data.PromosLapsed)
	data.PromoEnds = truncate(data.PromoEnds)
	data.Increases = truncate(data.Increases)
	data.Decreases = truncate(data.Decreases)
	data.Suspects = truncate(data.Suspects)

	sparklines, err := fetchSparklines(ctx, pool, sparklineTargets(data), sinceDur)
	if err != nil {
		return fmt.Errorf("fetching sparkline data: %w", err)
	}
	attachSparklines(data.PromosActive, sparklines)
	attachSparklines(data.PromosLapsed, sparklines)
	attachSparklines(data.PromoEnds, sparklines)
	attachSparklines(data.Increases, sparklines)
	attachSparklines(data.Decreases, sparklines)
	attachSparklines(data.Suspects, sparklines)

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d events in window, %d rendered)\n", *out, len(all),
		len(data.PromosActive)+len(data.PromosLapsed)+len(data.PromoEnds)+len(data.Increases)+len(data.Decreases)+len(data.Suspects))
	return nil
}

func sortByMagnitude(events []changeEvent) {
	sort.Slice(events, func(i, j int) bool {
		return abs(events[i].PctChange) > abs(events[j].PctChange)
	})
}

func truncate(events []changeEvent) []changeEvent {
	if len(events) > maxPerSection {
		return events[:maxPerSection]
	}
	return events
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func sparklineTargets(d reportData) []changeEvent {
	var all []changeEvent
	all = append(all, d.PromosActive...)
	all = append(all, d.PromosLapsed...)
	all = append(all, d.PromoEnds...)
	all = append(all, d.Increases...)
	all = append(all, d.Decreases...)
	all = append(all, d.Suspects...)
	return all
}

var sinceSuffix = regexp.MustCompile(`^(\d+)([hdw])$`)

func parseSince(s string) (time.Duration, error) {
	m := sinceSuffix.FindStringSubmatch(s)
	if m == nil {
		return time.ParseDuration(s)
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unrecognized --since value %q", s)
}
