package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"retail-prices/internal/pricing"
	"retail-prices/internal/quality"
)

// Chart geometry and the status-palette colors below follow the project's
// dataviz conventions: status colors (good/warning/critical) are reserved
// for state and never reused as a generic series color, lines stay thin,
// and every encoding gets a direct legend entry rather than relying on hue
// alone.
const (
	chartW, chartH                             = 760.0, 260.0
	chartPadL, chartPadR, chartPadT, chartPadB = 46.0, 92.0, 16.0, 28.0
)

type chartPoint struct {
	At    time.Time
	Price float64
}

// observedPoints turns non-suspect price_intervals into a stepped raw-price
// line: two points per interval (a flat step, not a slanted interpolation).
func observedPoints(intervals []quality.Interval) []chartPoint {
	pts := make([]chartPoint, 0, len(intervals)*2)
	for _, iv := range intervals {
		pts = append(pts, chartPoint{iv.ValidFrom, iv.Price}, chartPoint{iv.ValidTo, iv.Price})
	}
	return pts
}

// renderChart draws the §4 product-view chart: observed price, the regular
// price as a step line, promo/pending episodes shaded, regular-price
// changes marked, and 90-day/1-year lows flagged.
func renderChart(observed []chartPoint, spells []pricing.Spell, changes []pricing.RegularChange, low90, low1y *float64) template.HTML {
	if len(spells) == 0 {
		return ""
	}

	minT, maxT := spells[0].Start, spells[len(spells)-1].End
	for _, p := range observed {
		if p.At.Before(minT) {
			minT = p.At
		}
		if p.At.After(maxT) {
			maxT = p.At
		}
	}

	minP, maxP := spells[0].Price, spells[0].Price
	widen := func(p float64) {
		if p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
	}
	for _, s := range spells {
		widen(s.Price)
		if s.RegularPrice != nil {
			widen(*s.RegularPrice)
		}
	}
	for _, p := range observed {
		widen(p.Price)
	}
	if low90 != nil {
		widen(*low90)
	}
	if low1y != nil {
		widen(*low1y)
	}

	span := maxP - minP
	if span <= 0 {
		span = maxP*0.1 + 1
	}
	minP -= span * 0.08
	maxP += span * 0.08

	innerW := chartW - chartPadL - chartPadR
	innerH := chartH - chartPadT - chartPadB
	tRange := maxT.Sub(minT).Hours()
	if tRange <= 0 {
		tRange = 1
	}

	x := func(t time.Time) float64 { return chartPadL + (t.Sub(minT).Hours()/tRange)*innerW }
	y := func(p float64) float64 { return chartPadT + innerH - ((p-minP)/(maxP-minP))*innerH }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %g %g" width="100%%" height="auto" role="img" aria-label="Price history chart" class="price-chart">`, chartW, chartH)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%g" height="%g" fill="#fcfcfb" />`, chartW, chartH)

	// Promo (good/green tint) and pending (warning/amber tint) bands, behind
	// everything else.
	for _, s := range spells {
		var fill string
		switch s.Label {
		case "promo":
			fill = "rgba(12,163,12,0.12)"
		case "pending":
			fill = "rgba(250,178,25,0.18)"
		default:
			continue
		}
		x0, x1 := x(s.Start), x(s.End.AddDate(0, 0, 1))
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" />`, x0, chartPadT, x1-x0, innerH, fill)
	}

	drawLow := func(price float64, label string) {
		yy := y(price)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#898781" stroke-width="1" stroke-dasharray="3,3" />`,
			chartPadL, yy, chartPadL+innerW, yy)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#52514e">%s</text>`, chartPadL+innerW+4, yy+3, label)
	}
	if low90 != nil {
		drawLow(*low90, fmt.Sprintf("90d low €%.2f", *low90))
	}
	if low1y != nil && (low90 == nil || *low1y != *low90) {
		drawLow(*low1y, fmt.Sprintf("1y low €%.2f", *low1y))
	}

	// Regular price as a step line (blue, slot-1 categorical — distinct from
	// both the status colors and the observed-price ink).
	b.WriteString(`<polyline points="`)
	for _, s := range spells {
		if s.RegularPrice == nil {
			continue
		}
		fmt.Fprintf(&b, "%.1f,%.1f %.1f,%.1f ", x(s.Start), y(*s.RegularPrice), x(s.End), y(*s.RegularPrice))
	}
	b.WriteString(`" fill="none" stroke="#2457c5" stroke-width="2" stroke-linecap="round" />`)

	// Observed raw price, on top.
	if len(observed) > 1 {
		b.WriteString(`<polyline points="`)
		for _, p := range observed {
			fmt.Fprintf(&b, "%.1f,%.1f ", x(p.At), y(p.Price))
		}
		b.WriteString(`" fill="none" stroke="#0b0b0b" stroke-width="1.5" stroke-linecap="round" />`)
	}

	// Regular-price change markers: critical red for an increase, good
	// green for a confirmed permanent decrease.
	for _, c := range changes {
		color := "#d03b3b"
		if c.Direction == "permanent_decrease" {
			color = "#0ca30c"
		}
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3.5" fill="%s" stroke="#fcfcfb" stroke-width="1" />`, x(c.ChangeDate), y(c.NewPrice), color)
	}

	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#898781">%s</text>`, chartPadL, chartH-8, minT.Format("2006-01-02"))
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#898781" text-anchor="end">%s</text>`, chartPadL+innerW, chartH-8, maxT.Format("2006-01-02"))
	b.WriteString(`</svg>`)

	legend := `<div class="chart-legend">
	  <span><i style="background:#0b0b0b"></i>Observed price</span>
	  <span><i style="background:#2457c5"></i>Regular price</span>
	  <span><i style="background:rgba(12,163,12,0.35)"></i>Promo</span>
	  <span><i style="background:rgba(250,178,25,0.4)"></i>Pending</span>
	  <span><i class="dot" style="background:#d03b3b"></i>Increase</span>
	  <span><i class="dot" style="background:#0ca30c"></i>Permanent decrease</span>
	</div>`

	return template.HTML(b.String() + legend)
}
