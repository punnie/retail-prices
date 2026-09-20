// Package matching scores cross-retailer product-match candidates.
// Candidate generation (blocking) leans on Postgres/pg_trgm directly, since
// that's where the GIN index and the name-similarity function already live
// — this package only combines that similarity with the other independent
// signals (brand, pack size, unit price) into one score for the human
// review queue. Nothing here decides a match automatically; every candidate
// is confirmed or rejected by a person (see match_decisions).
package matching

import "math"

// Features is the subset of a retail_product's derived fields needed to
// score a candidate match.
type Features struct {
	BrandNorm string   // "" if unknown
	PackValue *float64 // canonical total value (see internal/normalize.PackSize), nil if unparsed
	PackUnit  string   // "g", "ml", or "un"; "" if unknown
	UnitPrice *float64 // price / pack value, nil if pack size is unknown
}

// packTolerance allows small rounding/labeling differences (e.g. 0.99L vs
// 1L) without treating them as different products.
const packTolerance = 0.05

// Score combines independent match signals into [0,1]. nameTrigramSimilarity
// is pg_trgm's similarity() over the two products' name_norm, computed in
// SQL — recomputing trigram similarity in Go would just duplicate work
// Postgres already indexes.
func Score(a, b Features, nameTrigramSimilarity float64) float64 {
	name := clamp01(nameTrigramSimilarity)
	brand := brandScore(a.BrandNorm, b.BrandNorm)
	pack := packScore(a, b)
	price := priceScore(a.UnitPrice, b.UnitPrice)

	weighted := clamp01(0.45*name + 0.25*brand + 0.20*pack + 0.10*price)

	// A known, differing brand or pack size is a hard veto, not just one
	// weighted vote: a 200ml and a 440ml bottle are different products
	// regardless of how similar their names read, and cross-brand matches
	// are essentially never correct. Cap rather than zero the score so the
	// review queue can still surface these low, in case the extraction
	// itself was wrong.
	if a.BrandNorm != "" && b.BrandNorm != "" && brand == 0 {
		weighted = math.Min(weighted, 0.3)
	}
	if a.PackValue != nil && b.PackValue != nil && pack == 0 {
		weighted = math.Min(weighted, 0.3)
	}

	return weighted
}

// brandScore: matching known brands is a strong positive signal; known,
// differing brands is a strong negative one (different manufacturers are
// never the same product); an unknown brand on either side is neutral
// rather than penalized, since brand extraction fails plenty on its own.
func brandScore(a, b string) float64 {
	if a == "" || b == "" {
		return 0.5
	}
	if a == b {
		return 1.0
	}
	return 0.0
}

// packScore: a size mismatch (200ml vs 400ml) means a different product,
// not the same one on sale — this is a hard, not a soft, signal. Missing
// pack data on either side is neutral.
func packScore(a, b Features) float64 {
	if a.PackValue == nil || b.PackValue == nil {
		return 0.5
	}
	if a.PackUnit != b.PackUnit {
		return 0.0
	}
	if *a.PackValue == 0 || *b.PackValue == 0 {
		return 0.5
	}
	ratio := *a.PackValue / *b.PackValue
	if ratio < 1 {
		ratio = 1 / ratio
	}
	if ratio-1 <= packTolerance {
		return 1.0
	}
	return 0.0
}

// priceScore is a mild plausibility check, not a hard signal: the same
// product sold at two Portuguese retailers should have unit prices within
// roughly the same order of magnitude, even accounting for promos.
func priceScore(a, b *float64) float64 {
	if a == nil || b == nil || *a <= 0 || *b <= 0 {
		return 0.5
	}
	ratio := *a / *b
	if ratio < 1 {
		ratio = 1 / ratio
	}
	switch {
	case ratio <= 1.5:
		return 1.0
	case ratio <= 3.0:
		return 0.5
	default:
		return 0.0
	}
}

func clamp01(f float64) float64 {
	return math.Max(0, math.Min(1, f))
}
