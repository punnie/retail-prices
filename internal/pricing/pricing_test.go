package pricing

import (
	"testing"
	"time"

	"retail-prices/internal/quality"
)

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func d(offset int) time.Time { return base.AddDate(0, 0, offset) }

type run struct {
	Price float64
	Days  int
}

// fromRuns builds a chronological, gapless interval series from a run
// list — the Go equivalent of price_spells.py demo's series() helper.
func fromRuns(runs []run) []quality.Interval {
	var out []quality.Interval
	cur := base
	for _, r := range runs {
		end := cur.AddDate(0, 0, r.Days-1)
		out = append(out, quality.Interval{Price: r.Price, ValidFrom: cur, ValidTo: end, ObservationCount: r.Days})
		cur = end.AddDate(0, 0, 1)
	}
	return out
}

type wantSpell struct {
	price        float64
	start        time.Time
	days         int
	label        string
	regularPrice *float64
}

func f(v float64) *float64 { return &v }

func assertSpells(t *testing.T, got []Spell, want []wantSpell) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d spells, want %d\ngot:  %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Price != w.price || !g.Start.Equal(w.start) || g.Days != w.days || g.Label != w.label {
			t.Errorf("spell %d: got {price=%v start=%s days=%d label=%s}, want {price=%v start=%s days=%d label=%s}",
				i, g.Price, g.Start.Format("2006-01-02"), g.Days, g.Label,
				w.price, w.start.Format("2006-01-02"), w.days, w.label)
		}
		switch {
		case w.regularPrice == nil && g.RegularPrice != nil:
			t.Errorf("spell %d: got regular_price=%v, want nil", i, *g.RegularPrice)
		case w.regularPrice != nil && (g.RegularPrice == nil || *g.RegularPrice != *w.regularPrice):
			t.Errorf("spell %d: got regular_price=%v, want %v", i, g.RegularPrice, *w.regularPrice)
		}
	}
}

// The reference demo in price_spells.py's __main__ block, run for real
// (python3 price_spells.py) on 2026-10-01 with pandas 2.x — these are its
// actual printed values, not re-derived. Series A: high-low with a hidden
// increase and a shallow-promo dip. B: permanent cut. C: staircase increase
// ending on a live promo. D: opens mid-promo, has a 1-day glitch.

func TestClassify_SeriesA_HiddenIncreaseAndShallowPromo(t *testing.T) {
	intervals := fromRuns([]run{
		{3.0, 7}, {2.0, 14}, {3.0, 5}, {2.0, 14}, {2.5, 5}, {2.0, 10},
		{3.0, 6}, {2.0, 14}, {3.3, 7}, {2.2, 14}, {3.3, 5},
	})
	spells, changes := Classify(intervals, DefaultParams, nil)

	assertSpells(t, spells, []wantSpell{
		{3.0, d(0), 7, "regular", f(3.0)},
		{2.0, d(7), 14, "promo", f(3.0)},
		{3.0, d(21), 5, "regular", f(3.0)},
		{2.0, d(26), 14, "promo", f(3.0)},
		{2.5, d(40), 5, "promo", f(3.0)}, // V-filtered: a shallow dip between two 3.0 regulars
		{2.0, d(45), 10, "promo", f(3.0)},
		{3.0, d(55), 6, "regular", f(3.0)},
		{2.0, d(61), 14, "promo", f(3.0)},
		{3.3, d(75), 7, "regular", f(3.3)},
		{2.2, d(82), 14, "promo", f(3.3)},
		{3.3, d(96), 5, "regular", f(3.3)},
	})

	if len(changes) != 1 {
		t.Fatalf("got %d regular changes, want 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if !c.ChangeDate.Equal(d(75)) || c.OldPrice != 3.0 || c.NewPrice != 3.3 || c.Direction != "increase" {
		t.Errorf("unexpected change: %+v (date %s)", c, c.ChangeDate.Format("2006-01-02"))
	}
	// The increase is dated at the return from the preceding promo (day 75,
	// 2026-03-17), not when that promo started — it was hidden until then.
	if got := d(75); !got.Equal(time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("test fixture bug: d(75) = %s, want 2026-03-17", got)
	}
}

func TestClassify_SeriesB_PermanentCutOutlastsPromoWindow(t *testing.T) {
	intervals := fromRuns([]run{{4.0, 10}, {3.0, 14}, {4.0, 7}, {3.5, 70}})
	spells, changes := Classify(intervals, DefaultParams, nil)

	assertSpells(t, spells, []wantSpell{
		{4.0, d(0), 10, "regular", f(4.0)},
		{3.0, d(10), 14, "promo", f(4.0)},
		{4.0, d(24), 7, "regular", f(4.0)},
		{3.5, d(31), 70, "regular", f(3.5)}, // 70 days > MaxPromoDays(56): flips permanent, not left pending
	})

	if len(changes) != 1 {
		t.Fatalf("got %d regular changes, want 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if !c.ChangeDate.Equal(d(31)) || c.OldPrice != 4.0 || c.NewPrice != 3.5 || c.Direction != "permanent_decrease" {
		t.Errorf("unexpected change: %+v", c)
	}
}

func TestClassify_SeriesC_StaircaseEndingOnLivePromo(t *testing.T) {
	intervals := fromRuns([]run{{2.0, 10}, {1.5, 7}, {2.0, 5}, {2.2, 10}, {2.5, 10}, {1.8, 6}})
	spells, changes := Classify(intervals, DefaultParams, nil)

	assertSpells(t, spells, []wantSpell{
		{2.0, d(0), 10, "regular", f(2.0)},
		{1.5, d(10), 7, "promo", f(2.0)},
		{2.0, d(17), 5, "regular", f(2.0)},
		{2.2, d(22), 10, "regular", f(2.2)}, // staircase step, no promo in between
		{2.5, d(32), 10, "regular", f(2.5)}, // staircase step
		{1.8, d(42), 6, "pending", f(2.5)},  // live promo, too recent to call
	})

	if len(changes) != 2 {
		t.Fatalf("got %d regular changes, want 2: %+v", len(changes), changes)
	}
	if changes[0].OldPrice != 2.0 || changes[0].NewPrice != 2.2 || changes[0].Direction != "increase" {
		t.Errorf("unexpected change[0]: %+v", changes[0])
	}
	if changes[1].OldPrice != 2.2 || changes[1].NewPrice != 2.5 || changes[1].Direction != "increase" {
		t.Errorf("unexpected change[1]: %+v", changes[1])
	}
}

func TestClassify_SeriesD_OpensLowWithGlitch(t *testing.T) {
	intervals := fromRuns([]run{{1.5, 6}, {2.0, 4}, {2.6, 1}, {2.0, 9}, {1.5, 20}})
	spells, changes := Classify(intervals, DefaultParams, nil)

	assertSpells(t, spells, []wantSpell{
		{1.5, d(0), 6, "unknown", nil},     // opens low: could be mid-promo or mid-staircase, can't tell
		{2.0, d(6), 14, "regular", f(2.0)}, // the 1-day 2.6 glitch is absorbed into this spell (4+1+9=14 days)
		{1.5, d(20), 20, "pending", f(2.0)},
	})

	if len(changes) != 0 {
		t.Fatalf("got %d regular changes, want 0 (only one regular spell ever established): %+v", len(changes), changes)
	}
}
