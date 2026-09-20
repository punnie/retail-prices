package report

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type pricePoint struct {
	At    time.Time
	Price float64
}

// fetchSparklines loads recent price_intervals for every product appearing
// in the report, so each row can show its trend rather than just the one
// transition being reported.
func fetchSparklines(ctx context.Context, pool *pgxpool.Pool, targets []changeEvent, sinceDur time.Duration) (map[int64][]pricePoint, error) {
	if len(targets) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(targets))
	seen := map[int64]bool{}
	for _, t := range targets {
		if !seen[t.ProductID] {
			seen[t.ProductID] = true
			ids = append(ids, t.ProductID)
		}
	}

	window := sinceDur
	if window < 90*24*time.Hour {
		window = 90 * 24 * time.Hour
	}
	cutoff := time.Now().Add(-window)

	// Suspect intervals are excluded: a single placeholder price (Continente
	// parks products at 55.00, 349.00, 500.00) would otherwise dominate the
	// y-scale and flatten the real price history into a straight line.
	rows, err := pool.Query(ctx, `
		SELECT retail_product_id, price, valid_from, valid_to
		FROM price_intervals
		WHERE retail_product_id = ANY($1) AND valid_to >= $2 AND NOT is_suspect
		ORDER BY retail_product_id, valid_from`, ids, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[int64][]pricePoint{}
	for rows.Next() {
		var id int64
		var price float64
		var from, to time.Time
		if err := rows.Scan(&id, &price, &from, &to); err != nil {
			return nil, err
		}
		// Two points per interval (a flat step, not a slanted line) so the
		// sparkline reads as a price history, not a smoothed trend.
		result[id] = append(result[id], pricePoint{At: from, Price: price}, pricePoint{At: to, Price: price})
	}
	return result, rows.Err()
}

func attachSparklines(events []changeEvent, sparklines map[int64][]pricePoint) {
	for i := range events {
		events[i].Sparkline = renderSparkline(sparklines[events[i].ProductID], events[i].Kind)
	}
}

const sparkW, sparkH = 140.0, 32.0

func renderSparkline(points []pricePoint, kind string) template.HTML {
	if len(points) < 2 {
		return ""
	}

	minT, maxT := points[0].At, points[0].At
	minP, maxP := points[0].Price, points[0].Price
	for _, p := range points {
		if p.At.Before(minT) {
			minT = p.At
		}
		if p.At.After(maxT) {
			maxT = p.At
		}
		if p.Price < minP {
			minP = p.Price
		}
		if p.Price > maxP {
			maxP = p.Price
		}
	}
	tRange := maxT.Sub(minT).Seconds()
	pRange := maxP - minP
	if tRange <= 0 || pRange <= 0 {
		return "" // flat or single-instant series: nothing useful to draw
	}

	x := func(t time.Time) float64 { return (t.Sub(minT).Seconds() / tRange) * sparkW }
	y := func(price float64) float64 { return sparkH - ((price-minP)/pRange)*sparkH }

	var b strings.Builder
	fmt.Fprintf(&b, `<polyline points="`)
	for i, p := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x(p.At), y(p.Price))
	}
	color := colorForKind(kind)
	fmt.Fprintf(&b, `" fill="none" stroke="%s" stroke-width="1.5" /></polyline>`, color)

	return template.HTML(fmt.Sprintf(
		`<svg width="%d" height="%d" viewBox="0 0 %g %g" class="spark">%s</svg>`,
		int(sparkW), int(sparkH), sparkW, sparkH, b.String()))
}

func colorForKind(kind string) string {
	switch kind {
	case "promo_start", "promo_end", "decrease":
		return "#1a7f37"
	case "increase":
		return "#b02a37"
	default:
		return "#8a6d00"
	}
}
