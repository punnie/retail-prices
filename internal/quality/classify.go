// Package quality turns a product's raw price observations into a
// run-length-encoded interval series plus a classified event for every
// transition between intervals.
//
// Classification is anchored on a robust per-product baseline — the
// observation-weighted median of that product's own price history — not on
// the immediately preceding price. That distinction is the whole design.
// Retailers park unpublished products at placeholder prices (Continente uses
// 55.00, 349.00, 500.00, 150.00 and others; each appears once for tens of
// thousands of distinct products), and a prev-anchored comparison reads those
// backwards: placeholder -> real price looks like a huge promotion, and real
// price -> placeholder looks like the promotion ending. Anchoring on what the
// product normally costs makes a placeholder an outlier in both directions,
// whichever side of a transition it sits on.
//
// Thresholds come from the distributions measured by `retail profile` over
// the real log, and from a hard domain fact: single-step retail price
// increases of several hundred percent do not happen. Anything claiming one
// is a data artifact, so it is quarantined rather than reported.
package quality

import (
	"math"
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

type EventKind string

const (
	KindPromoStart EventKind = "promo_start"
	KindPromoEnd   EventKind = "promo_end"
	KindIncrease   EventKind = "increase"
	KindDecrease   EventKind = "decrease"
	KindSuspect    EventKind = "suspect"
)

// Event describes the transition into an interval from the one before it.
type Event struct {
	OldPrice   float64
	NewPrice   float64
	PctChange  float64
	ChangedAt  time.Time
	Kind       EventKind
	Confidence float64
}

const (
	// Outlier band around the product's own baseline. An interval outside
	// this is quarantined: no real grocery item triples its price or falls
	// to a quarter of normal and stays a legitimate observation.
	maxAboveBaseline = 3.0
	minBelowBaseline = 0.25

	// A price at or below this share of baseline counts as a discount
	// rather than ordinary drift.
	promoThreshold = 0.85

	// Hard caps on a single reported transition, applied even when both
	// endpoints are individually inside the outlier band. Asymmetric on
	// purpose: deep clearances ("saldos" at -70/-80%) are real, whereas a
	// price more than tripling in one step is not repricing, it is a feed
	// artifact.
	maxIncreaseRatio = 3.0
	minDropRatio     = 0.12

	// How far ahead to look for a return to baseline when deciding whether
	// a discount is a temporary promotion or a permanent repricing.
	promoReturnLookahead = 10

	// Tolerance for "back at baseline".
	baselineReturnBand = 0.92

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

// ClassifyEvents flags outlier intervals against the product's baseline and
// classifies every transition. It mutates IsSuspect on the slice it is given
// and returns exactly len(intervals)-1 events, aligned to intervals[1:], so
// every transition always carries a concrete Kind.
func ClassifyEvents(intervals []Interval, opts Options) []Event {
	if len(intervals) < 2 {
		return nil
	}

	baseline := BaselinePrice(intervals)
	if baseline <= 0 {
		return nil
	}

	// Pass 1: quarantine intervals that are implausible for this product,
	// independent of where they sit in the series.
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

	// Pass 2: classify transitions. A promotion is a *temporary* discount
	// below baseline, so promo_end is only ever emitted for a discount whose
	// start we actually saw — otherwise a permanent step up from a low
	// starting price would masquerade as a promotion ending.
	events := make([]Event, len(intervals)-1)
	inPromo := false

	for i := 1; i < len(intervals); i++ {
		prev, cur := intervals[i-1], intervals[i]
		at := cur.ValidFrom
		pct := pctChange(prev.Price, cur.Price)

		if prev.IsSuspect || cur.IsSuspect {
			events[i-1] = Event{prev.Price, cur.Price, pct, at, KindSuspect, 0.95}
			inPromo = false
			continue
		}

		ratio := cur.Price / prev.Price
		if ratio > maxIncreaseRatio || ratio < minDropRatio {
			events[i-1] = Event{prev.Price, cur.Price, pct, at, KindSuspect, 0.85}
			inPromo = false
			continue
		}

		rPrev := prev.Price / baseline
		rCur := cur.Price / baseline

		switch {
		case !inPromo && rCur <= promoThreshold && rPrev > promoThreshold &&
			returnsToBaseline(intervals, i+1, baseline):
			events[i-1] = Event{prev.Price, cur.Price, pct, at, KindPromoStart, 0.9}
			inPromo = true

		case inPromo && rCur > promoThreshold:
			events[i-1] = Event{prev.Price, cur.Price, pct, at, KindPromoEnd, 0.9}
			inPromo = false

		default:
			events[i-1] = plainEvent(prev.Price, cur.Price, at, 0.9)
			if rCur > promoThreshold {
				inPromo = false
			}
		}
	}

	return events
}

// returnsToBaseline reports whether the price comes back up to roughly
// baseline within a bounded lookahead — the test that separates a temporary
// promotion from a permanent price cut. Suspect intervals are skipped rather
// than treated as a return, so a placeholder spike can never "end" a promo.
func returnsToBaseline(intervals []Interval, from int, baseline float64) bool {
	limit := from + promoReturnLookahead
	if limit > len(intervals) {
		limit = len(intervals)
	}
	for j := from; j < limit; j++ {
		if intervals[j].IsSuspect {
			continue
		}
		if intervals[j].Price/baseline >= baselineReturnBand {
			return true
		}
	}
	return false
}

func plainEvent(oldPrice, newPrice float64, at time.Time, confidence float64) Event {
	kind := KindIncrease
	if newPrice < oldPrice {
		kind = KindDecrease
	}
	return Event{oldPrice, newPrice, pctChange(oldPrice, newPrice), at, kind, confidence}
}

func pctChange(oldPrice, newPrice float64) float64 {
	if oldPrice == 0 {
		return math.Inf(1)
	}
	return 100 * (newPrice - oldPrice) / oldPrice
}
