package quality

import (
	"testing"
	"time"
)

func day(n int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// iv builds an interval spanning [from, to] days with n observations.
func iv(price float64, from, to, n int) Interval {
	return Interval{Price: price, ValidFrom: day(from), ValidTo: day(to), ObservationCount: n}
}

func TestBuildIntervals(t *testing.T) {
	obs := []Observation{
		{1.99, day(0)}, {1.99, day(1)}, {1.99, day(2)},
		{0.99, day(3)},
		{1.99, day(4)}, {1.99, day(5)},
	}
	intervals := BuildIntervals(obs)
	if len(intervals) != 3 {
		t.Fatalf("got %d intervals, want 3: %+v", len(intervals), intervals)
	}
	if intervals[0].ObservationCount != 3 || intervals[1].ObservationCount != 1 || intervals[2].ObservationCount != 2 {
		t.Errorf("unexpected observation counts: %+v", intervals)
	}
}

func TestBaselinePrice_WeightsByObservationCount(t *testing.T) {
	// A placeholder seen once must not drag the baseline away from the real
	// price the product sat at for months.
	intervals := []Interval{
		iv(0.89, 0, 100, 50),
		iv(55.00, 101, 101, 1),
		iv(0.89, 102, 200, 40),
	}
	if got := BaselinePrice(intervals); got != 0.89 {
		t.Errorf("BaselinePrice = %v, want 0.89 (the long-held real price)", got)
	}
}

// The exact pattern the user flagged: Continente parks a product at its
// 55.00 placeholder, then a real bread price appears. Neither interval
// touching the placeholder may reach internal/pricing as real price points.
func TestFlagSuspect_PlaceholderPriceIsQuarantined(t *testing.T) {
	intervals := []Interval{
		iv(0.89, 0, 60, 30),
		iv(55.00, 61, 61, 1),
		iv(0.89, 62, 120, 30),
	}
	FlagSuspect(intervals, Options{})

	if !intervals[1].IsSuspect {
		t.Error("the 55.00 placeholder interval should be flagged suspect")
	}
	if intervals[0].IsSuspect || intervals[2].IsSuspect {
		t.Error("the real 0.89 price intervals must not be flagged suspect")
	}
}

// "10000% increases just do not exist" — a jump of that size must be
// quarantined even though nothing later contradicts it. The weighted
// baseline here lands on 189.99 (30 observations vs. 15), so it's the 1.90
// side that reads as the ~100x outlier — either way, one side of a jump
// this size must always be flagged.
func TestFlagSuspect_HugeIncreaseIsQuarantined(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 30, 15),
		iv(189.99, 31, 90, 30),
	}
	FlagSuspect(intervals, Options{})
	if !intervals[0].IsSuspect {
		t.Error("a ~100x jump from baseline must be flagged suspect")
	}
}

// Deep but real clearance discounts must survive; only implausible drops are
// quarantined.
func TestFlagSuspect_DeepClearanceSurvives(t *testing.T) {
	intervals := []Interval{
		iv(20.00, 0, 60, 40),
		iv(6.00, 61, 80, 12),
		iv(20.00, 81, 140, 40),
	}
	FlagSuspect(intervals, Options{})
	if intervals[1].IsSuspect {
		t.Error("a -70% clearance at a plausible price must not be flagged suspect")
	}
}

// Auchan parks unpublished products at 1.90 — a price that sits inside the
// ordinary outlier band for any mid-priced item (1.90 against a 5.49 baseline
// is 0.35x), so it needs the store-level placeholder prior to be caught.
func TestFlagSuspect_PlaceholderPriorCatchesMidRangeFiller(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 20, 12),
		iv(5.49, 21, 120, 60),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	FlagSuspect(intervals, opts)

	if !intervals[0].IsSuspect {
		t.Error("a placeholder price far from baseline should be flagged suspect")
	}
}

// The same price must NOT be suppressed for a product that genuinely costs
// about that much — the store-level prior alone is not sufficient evidence.
func TestFlagSuspect_PlaceholderPriceKeptWhenNearBaseline(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 60, 40),
		iv(2.19, 61, 120, 40),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	FlagSuspect(intervals, opts)

	if intervals[0].IsSuspect {
		t.Error("a placeholder-listed price that matches the product's own baseline must be kept")
	}
}

// A deep discount at a price the store does not use as filler must survive:
// only the combination of store prior and off-baseline price is suppressed.
func TestFlagSuspect_DeepDiscountAtNonPlaceholderPriceSurvives(t *testing.T) {
	intervals := []Interval{
		iv(20.00, 0, 60, 40),
		iv(6.00, 61, 80, 12),
		iv(20.00, 81, 140, 40),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	FlagSuspect(intervals, opts)
	if intervals[1].IsSuspect {
		t.Error("a deep discount at a non-placeholder price must survive")
	}
}
