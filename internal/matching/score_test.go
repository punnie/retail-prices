package matching

import "testing"

func f(v float64) *float64 { return &v }

func TestScore_StrongMatch(t *testing.T) {
	a := Features{BrandNorm: "heinz", PackValue: f(555), PackUnit: "g", UnitPrice: f(0.899)}
	b := Features{BrandNorm: "heinz", PackValue: f(555), PackUnit: "g", UnitPrice: f(0.85)}
	got := Score(a, b, 0.9)
	if got < 0.85 {
		t.Errorf("expected a high score for a near-identical pair, got %v", got)
	}
}

func TestScore_DifferentPackSizeIsPenalizedHard(t *testing.T) {
	sameBrandName := 0.9
	a := Features{BrandNorm: "syoss", PackValue: f(200), PackUnit: "ml"}
	b := Features{BrandNorm: "syoss", PackValue: f(440), PackUnit: "ml"}
	got := Score(a, b, sameBrandName)
	if got > 0.7 {
		t.Errorf("expected a mismatched pack size to sharply reduce the score, got %v", got)
	}
}

func TestScore_DifferentKnownBrandsIsPenalized(t *testing.T) {
	got := Score(
		Features{BrandNorm: "calve"},
		Features{BrandNorm: "heinz"},
		0.8,
	)
	want := Score(Features{BrandNorm: "calve"}, Features{BrandNorm: "calve"}, 0.8)
	if got >= want {
		t.Errorf("different known brands (%v) should score lower than matching brands (%v)", got, want)
	}
}

func TestScore_UnknownFieldsAreNeutralNotPenalized(t *testing.T) {
	got := Score(Features{}, Features{}, 0.7)
	// With everything unknown but a decent name match, the score should
	// still be usable (not crushed to near-zero by missing metadata).
	if got < 0.4 {
		t.Errorf("expected unknown brand/pack/price to be neutral, got %v", got)
	}
}

func TestPackScore_ToleratesRoundingDifferences(t *testing.T) {
	a := Features{PackValue: f(1000), PackUnit: "ml"}
	b := Features{PackValue: f(990), PackUnit: "ml"} // 0.99L vs 1L labeling
	if got := packScore(a, b); got != 1.0 {
		t.Errorf("expected a small labeling difference to still match, got %v", got)
	}
}
