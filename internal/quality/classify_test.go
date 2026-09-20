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
// 55.00 placeholder, then a real bread price appears. Neither the entry into
// nor the exit from the placeholder may be reported as a promotion.
func TestClassifyEvents_PlaceholderPriceIsNeverAPromotion(t *testing.T) {
	intervals := []Interval{
		iv(0.89, 0, 60, 30),
		iv(55.00, 61, 61, 1),
		iv(0.89, 62, 120, 30),
	}
	events := ClassifyEvents(intervals, Options{})

	if !intervals[1].IsSuspect {
		t.Error("the 55.00 placeholder interval should be flagged suspect")
	}
	if intervals[0].IsSuspect || intervals[2].IsSuspect {
		t.Error("the real 0.89 price intervals must not be flagged suspect")
	}
	for i, e := range events {
		if e.Kind != KindSuspect {
			t.Errorf("event %d touching a placeholder should be suspect, got %s (%.2f -> %.2f)",
				i, e.Kind, e.OldPrice, e.NewPrice)
		}
	}
}

// "10000% increases just do not exist" — a jump of that size must be
// quarantined even though nothing later contradicts it.
func TestClassifyEvents_HugeIncreaseIsSuspectNotAnIncrease(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 30, 15),
		iv(189.99, 31, 90, 30),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind != KindSuspect {
		t.Errorf("a ~100x jump must be suspect, got %s", events[0].Kind)
	}
}

func TestClassifyEvents_GenuinePromoDetectedAndEnded(t *testing.T) {
	intervals := []Interval{
		iv(5.00, 0, 30, 20),
		iv(2.99, 31, 38, 8),
		iv(5.00, 39, 90, 30),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind != KindPromoStart {
		t.Errorf("expected promo_start, got %s", events[0].Kind)
	}
	if events[1].Kind != KindPromoEnd {
		t.Errorf("expected promo_end, got %s", events[1].Kind)
	}
	if intervals[1].IsSuspect {
		t.Error("a genuine promo interval must not be flagged suspect")
	}
}

// A permanent cut that never returns to baseline is a real decrease, not a
// promotion — so it must not emit a promo_end later either.
func TestClassifyEvents_PermanentCutIsDecreaseNotPromo(t *testing.T) {
	intervals := []Interval{
		iv(10.00, 0, 30, 20),
		iv(6.00, 31, 200, 80),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind != KindDecrease {
		t.Errorf("expected decrease for a permanent cut, got %s", events[0].Kind)
	}
}

// A permanent step *up* from a low starting price must not be reported as a
// promotion ending — there was never a promotion to end.
func TestClassifyEvents_StepUpFromLowStartIsNotPromoEnd(t *testing.T) {
	intervals := []Interval{
		iv(10.00, 0, 60, 40),
		iv(22.00, 61, 200, 60),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind == KindPromoEnd {
		t.Error("a permanent step up must not be classified promo_end")
	}
	if events[0].Kind != KindIncrease {
		t.Errorf("expected increase, got %s", events[0].Kind)
	}
}

func TestClassifyEvents_OrdinaryChangeIsReported(t *testing.T) {
	intervals := []Interval{
		iv(3.99, 0, 30, 20),
		iv(4.29, 31, 90, 30),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind != KindIncrease {
		t.Errorf("expected increase, got %s", events[0].Kind)
	}
}

// Deep but real clearance discounts must survive; only implausible drops are
// quarantined.
func TestClassifyEvents_DeepClearanceSurvives(t *testing.T) {
	intervals := []Interval{
		iv(20.00, 0, 60, 40),
		iv(6.00, 61, 80, 12),
		iv(20.00, 81, 140, 40),
	}
	events := ClassifyEvents(intervals, Options{})
	if events[0].Kind != KindPromoStart {
		t.Errorf("a -70%% clearance should be a promo_start, got %s", events[0].Kind)
	}
}

func TestClassifyEvents_EveryTransitionGetsAKind(t *testing.T) {
	// Multi-level excursion: every transition must carry a concrete Kind or
	// the price_events kind check constraint rejects the row.
	intervals := []Interval{
		iv(0.99, 0, 20, 10),
		iv(500.00, 21, 21, 1),
		iv(800.00, 22, 22, 1),
		iv(0.99, 23, 60, 20),
	}
	events := ClassifyEvents(intervals, Options{})
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, e := range events {
		if e.Kind == "" {
			t.Errorf("event %d has an empty Kind: %+v", i, e)
		}
	}
}

// Auchan parks unpublished products at 1.90 — a price that sits inside the
// ordinary outlier band for any mid-priced item (1.90 against a 5.49 baseline
// is 0.35x), so it needs the store-level placeholder prior to be caught.
func TestClassifyEvents_PlaceholderPriorCatchesMidRangeFiller(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 20, 12),
		iv(5.49, 21, 120, 60),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	events := ClassifyEvents(intervals, opts)

	if !intervals[0].IsSuspect {
		t.Error("a placeholder price far from baseline should be flagged suspect")
	}
	if events[0].Kind != KindSuspect {
		t.Errorf("transition out of a placeholder should be suspect, got %s", events[0].Kind)
	}
}

// The same price must NOT be suppressed for a product that genuinely costs
// about that much — the store-level prior alone is not sufficient evidence.
func TestClassifyEvents_PlaceholderPriceKeptWhenNearBaseline(t *testing.T) {
	intervals := []Interval{
		iv(1.90, 0, 60, 40),
		iv(2.19, 61, 120, 40),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	events := ClassifyEvents(intervals, opts)

	if intervals[0].IsSuspect {
		t.Error("a placeholder-listed price that matches the product's own baseline must be kept")
	}
	if events[0].Kind != KindIncrease {
		t.Errorf("expected a normal increase, got %s", events[0].Kind)
	}
}

// A deep discount at a price the store does not use as filler must survive:
// only the combination of store prior and off-baseline price is suppressed.
func TestClassifyEvents_DeepDiscountAtNonPlaceholderPriceSurvives(t *testing.T) {
	intervals := []Interval{
		iv(20.00, 0, 60, 40),
		iv(6.00, 61, 80, 12),
		iv(20.00, 81, 140, 40),
	}
	opts := Options{IsPlaceholderPrice: func(p float64) bool { return p == 1.90 }}
	events := ClassifyEvents(intervals, opts)
	if events[0].Kind != KindPromoStart {
		t.Errorf("expected promo_start, got %s", events[0].Kind)
	}
}
