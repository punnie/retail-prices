// Package quality turns a product's raw price observations into a
// run-length-encoded interval series and flags the intervals that are
// implausible for that product, so later stages (internal/pricing) never see
// a retailer placeholder as a real price point.
//
// Flagging is anchored on a robust per-product baseline — the
// observation-weighted median of that product's own price history — not on
// the immediately preceding price. That distinction is the whole design.
// Retailers park unpublished products at placeholder prices (Continente uses
// 55.00, 349.00, 500.00, 150.00 and others; each appears once for tens of
// thousands of distinct products), and a prev-anchored comparison reads those
// backwards: placeholder -> real price looks like a huge promotion, and real
// price -> placeholder looks like the promotion ending. Anchoring on what the
// product normally costs makes a placeholder an outlier regardless of which
// side of a transition it sits on.
//
// Thresholds come from the distributions measured by `retail profile` over
// the real log, and from a hard domain fact: single-step retail price
// increases of several hundred percent do not happen. Anything claiming one
// is a data artifact, so it is quarantined rather than passed on to pricing
// analysis.
package quality

import (
	"sort"
	"time"
)

// Observation is one raw (price, scraped_at) row for a single product,
// already filtered to price > 0 and sorted by ScrapedAt ascending.
type Observation struct {
	Price     float64
	ScrapedAt time.Time
}

// Interval is a maximal run of consecutive observations at the same price.
type Interval struct {
	Price            float64
	ValidFrom        time.Time
	ValidTo          time.Time
	ObservationCount int
	IsSuspect        bool
}

const (
	// Outlier band around the product's own baseline. An interval outside
	// this is quarantined: no real grocery item triples its price or falls
	// to a quarter of normal and stays a legitimate observation.
	maxAboveBaseline = 3.0
	minBelowBaseline = 0.25

	// Tighter band applied only to prices the store is known to use as
	// filler (see store_placeholder_prices). A placeholder like Auchan's
	// 1.90 sits inside the ordinary outlier band for any mid-priced product
	// — 1.90 against a 5.49 baseline is only 0.35x — so it needs a store-level
	// prior plus this narrower per-product test to be caught. Requiring both
	// signals is what keeps genuine deep discounts at non-placeholder prices
	// from being suppressed.
	placeholderMaxAbove = 1.5
	placeholderMinBelow = 1.0 / 1.5
)

// Options carries per-store context into classification.
type Options struct {
	// IsPlaceholderPrice reports whether a price is one this store uses as
	// filler for unpublished products. May be nil.
	IsPlaceholderPrice func(price float64) bool
}

func (o Options) isPlaceholder(price float64) bool {
	return o.IsPlaceholderPrice != nil && o.IsPlaceholderPrice(price)
}

// BuildIntervals run-length-encodes a product's observation history.
// obs must be sorted by ScrapedAt ascending.
func BuildIntervals(obs []Observation) []Interval {
	if len(obs) == 0 {
		return nil
	}

	intervals := make([]Interval, 0, len(obs))
	cur := Interval{
		Price:            obs[0].Price,
		ValidFrom:        obs[0].ScrapedAt,
		ValidTo:          obs[0].ScrapedAt,
		ObservationCount: 1,
	}
	for _, o := range obs[1:] {
		if o.Price == cur.Price {
			cur.ValidTo = o.ScrapedAt
			cur.ObservationCount++
			continue
		}
		intervals = append(intervals, cur)
		cur = Interval{
			Price:            o.Price,
			ValidFrom:        o.ScrapedAt,
			ValidTo:          o.ScrapedAt,
			ObservationCount: 1,
		}
	}
	intervals = append(intervals, cur)
	return intervals
}

// BaselinePrice returns the observation-weighted median price across a
// product's intervals: the price it actually sat at for most of the time it
// was observed. Weighting by observation count is what makes placeholders
// harmless — Continente's 55.00 shows up for a single observation against a
// real price held for dozens, so it cannot drag the baseline.
func BaselinePrice(intervals []Interval) float64 {
	if len(intervals) == 0 {
		return 0
	}

	sorted := make([]Interval, len(intervals))
	copy(sorted, intervals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Price < sorted[j].Price })

	total := 0
	for _, iv := range sorted {
		total += iv.ObservationCount
	}
	if total == 0 {
		return sorted[len(sorted)/2].Price
	}

	half := float64(total) / 2
	cum := 0
	for _, iv := range sorted {
		cum += iv.ObservationCount
		if float64(cum) >= half {
			return iv.Price
		}
	}
	return sorted[len(sorted)-1].Price
}

// FlagSuspect quarantines intervals that are implausible for this product,
// independent of where they sit in the series: outside the outlier band
// around the product's own baseline, or a store-known placeholder price far
// from this product's baseline. It mutates IsSuspect on the slice it is
// given and returns the baseline used, so callers (internal/pricing) can
// drop suspect intervals before spell classification ever sees them.
func FlagSuspect(intervals []Interval, opts Options) (baseline float64) {
	if len(intervals) == 0 {
		return 0
	}
	baseline = BaselinePrice(intervals)
	if baseline <= 0 {
		return baseline
	}

	for i := range intervals {
		r := intervals[i].Price / baseline
		switch {
		case r > maxAboveBaseline || r < minBelowBaseline:
			intervals[i].IsSuspect = true
		case opts.isPlaceholder(intervals[i].Price) &&
			(r > placeholderMaxAbove || r < placeholderMinBelow):
			// A price the store uses as filler, and this product does not
			// normally cost anything like it.
			intervals[i].IsSuspect = true
		}
	}
	return baseline
}
