// Package pricing decomposes a product's cleaned price history into regular
// price, promo episodes and average-paid trend, per PRICING_CONCEPTS.md. It
// is a port of the reference implementations vendored at
// internal/pricing/reference/{price_spells.py,price_spells.sql} — see
// pricing_test.go for the parity fixtures run against the real script.
//
// The core idea: a high-low retailer's raw price is a wave, so no
// single-transition rule (compare against the previous price, or against a
// running baseline) can separate a promotion from a permanent repricing.
// Instead, work in spells (runs of constant price): the regular price is the
// price a spell sequence keeps returning to, not "the usual price" but "the
// price it keeps coming back to".
//
// Input adaptation: the references take daily (date, price) rows and
// run-length-encode them (fill_daily + build_spells in price_spells.py).
// internal/quality.BuildIntervals already run-length-encodes the raw
// observations, so Classify starts from that — building spell spans
// directly from interval date ranges (end_date = day before the next spell
// starts) rather than expanding every calendar day into a row. That span
// arithmetic *is* the gap-fill: a missing day is silently attributed to
// whichever price preceded it, exactly as the reference's ffill does.
//
// Deliberate addition over the references: series breaks. PRICING_CONCEPTS.md
// §2 requires "product delisted for months then relisted ⇒ treat as a series
// break; don't carry regular price across it", but neither price_spells.py
// nor price_spells.sql implement it (their daily fill ffills straight across
// any gap). Classify splits a product's history into series whenever a gap
// exceeds Params.MaxGapDays *and* the store ran complete scrapes during that
// gap (so a scraper outage is never mistaken for a delisting) — see
// HadActivityBetween.
package pricing

import (
	"time"

	"retail-prices/internal/quality"
)

// Params are the three knobs PRICING_CONCEPTS.md §2 calls out as needing
// per-retailer (and ideally per-category) calibration, plus the series-break
// threshold. Defaults match the reference implementation's.
type Params struct {
	// MinRegDays: a high must persist at least this many days to count as a
	// regular price — short highs are glitches or dead-cat bounces.
	MinRegDays int
	// MaxPromoDays: a low lasting longer than this is a permanent change,
	// not a promotion; also the V-filter's lookaround window.
	MaxPromoDays int
	// TrendWindowDays: the averaging window for the average-paid-price
	// trend, a multiple of the promo cycle.
	TrendWindowDays int
	// MaxGapDays: a gap longer than this, confirmed by the store actually
	// scraping during it, starts a new series.
	MaxGapDays int
}

// DefaultParams mirrors price_spells.py's MIN_REG_DAYS / MAX_PROMO_DAYS /
// TREND_WINDOW, plus a 90-day series-break threshold.
var DefaultParams = Params{MinRegDays: 3, MaxPromoDays: 56, TrendWindowDays: 84, MaxGapDays: 90}

// Spell is one constant-price run, labelled against the regular price it is
// judged relative to.
type Spell struct {
	SeriesIndex  int
	SpellIndex   int // 0-based position within its series
	Price        float64
	Start, End   time.Time // calendar dates (UTC midnight)
	Days         int
	IsRegular    bool
	RegularPrice *float64 // nil only for "unknown" spells before any regular spell is established
	Label        string   // "regular" | "promo" | "pending" | "unknown" | "short_high"
}

// RegularChange is a real price move: an increase, or a decrease that
// outlasted the promo window and was confirmed permanent. ChangeDate is the
// regular spell's start — for an increase only visible on return from a
// promo, that's the return date, not when the promo itself started.
type RegularChange struct {
	SeriesIndex                   int
	ChangeDate                    time.Time
	OldPrice, NewPrice, PctChange float64
	Direction                     string // "increase" | "permanent_decrease"
}

// HadActivityBetween reports whether the store ran complete scrapes between
// two timestamps — the signal that distinguishes "the product was delisted"
// from "we just didn't scrape for a while". A nil func treats every gap
// exceeding MaxGapDays as a genuine series break.
type HadActivityBetween func(from, to time.Time) bool

// Classify runs the full spell decomposition over one product's cleaned,
// chronologically ordered, non-suspect price intervals (quality.FlagSuspect
// already applied by the caller — a placeholder price must never reach
// here).
func Classify(intervals []quality.Interval, params Params, hadActivity HadActivityBetween) ([]Spell, []RegularChange) {
	if len(intervals) == 0 {
		return nil, nil
	}

	var allSpells []Spell
	var allChanges []RegularChange
	for si, seg := range splitSeries(intervals, params.MaxGapDays, hadActivity) {
		runs := buildRuns(seg)
		if len(runs) == 0 {
			continue
		}
		seriesEnd := dateOnly(seg[len(seg)-1].ValidTo)

		spells := deglitch(buildSpellSpans(runs, seriesEnd), seriesEnd, params.MinRegDays)
		classifySpells(spells, params)
		changes := regularChanges(spells)

		for i := range spells {
			spells[i].SeriesIndex = si
			spells[i].SpellIndex = i
		}
		for i := range changes {
			changes[i].SeriesIndex = si
		}
		allSpells = append(allSpells, spells...)
		allChanges = append(allChanges, changes...)
	}
	return allSpells, allChanges
}

// splitSeries breaks a product's interval history at gaps the store actually
// had the chance to observe (see HadActivityBetween), so a delisted-then-
// relisted product never carries its old regular price across the gap.
func splitSeries(intervals []quality.Interval, maxGapDays int, hadActivity HadActivityBetween) [][]quality.Interval {
	var segments [][]quality.Interval
	var cur []quality.Interval
	for i, iv := range intervals {
		if i > 0 {
			gapDays := int(iv.ValidFrom.Sub(intervals[i-1].ValidTo).Hours() / 24)
			if gapDays > maxGapDays && (hadActivity == nil || hadActivity(intervals[i-1].ValidTo, iv.ValidFrom)) {
				segments = append(segments, cur)
				cur = nil
			}
		}
		cur = append(cur, iv)
	}
	if len(cur) > 0 {
		segments = append(segments, cur)
	}
	return segments
}

type rawRun struct {
	price float64
	start time.Time // date
}

// buildRuns collapses intervals to one price per calendar date (the later
// observation wins on a date two intervals both touch) and run-length-
// encodes the result — price_spells.py's fill_daily + build_spells, done
// without expanding to one row per day.
func buildRuns(intervals []quality.Interval) []rawRun {
	var runs []rawRun
	for _, iv := range intervals {
		d := dateOnly(iv.ValidFrom)
		if len(runs) > 0 && !d.After(runs[len(runs)-1].start) {
			runs[len(runs)-1].price = iv.Price
			continue
		}
		runs = append(runs, rawRun{price: iv.Price, start: d})
	}
	return mergeAdjacentRuns(runs)
}

func mergeAdjacentRuns(runs []rawRun) []rawRun {
	out := runs[:0:0]
	for _, r := range runs {
		if len(out) > 0 && out[len(out)-1].price == r.price {
			continue
		}
		out = append(out, r)
	}
	return out
}

// buildSpellSpans turns a run sequence into spells: each spell's end is the
// day before the next run starts (or the series end for the last spell),
// which is what makes a gap in the data read as "carried forward", never as
// a price change.
func buildSpellSpans(runs []rawRun, seriesEnd time.Time) []Spell {
	n := len(runs)
	spells := make([]Spell, n)
	for i, r := range runs {
		end := seriesEnd
		if i+1 < n {
			end = runs[i+1].start.AddDate(0, 0, -1)
		}
		spells[i] = Spell{Price: r.price, Start: r.start, End: end, Days: daysInclusive(r.start, end)}
	}
	return spells
}

// deglitch drops short spikes (a high under MinRegDays that sits above both
// neighbours) and re-encodes, so a glitch can't split a regular spell in two
// — it's absorbed into whichever spell precedes it. Short *lows* are left
// alone; they may be real one-day promos.
func deglitch(spells []Spell, seriesEnd time.Time, minRegDays int) []Spell {
	n := len(spells)
	runs := make([]rawRun, 0, n)
	for i, s := range spells {
		isGlitch := s.Days < minRegDays &&
			i > 0 && i < n-1 &&
			s.Price > spells[i-1].Price && s.Price > spells[i+1].Price
		if isGlitch {
			continue
		}
		runs = append(runs, rawRun{price: s.Price, start: s.Start})
	}
	return buildSpellSpans(mergeAdjacentRuns(runs), seriesEnd)
}

// classifySpells runs passes 1-3 from PRICING_CONCEPTS.md §2 and labels
// every spell. Mutates spells in place.
func classifySpells(spells []Spell, params Params) {
	n := len(spells)
	if n == 0 {
		return
	}

	// Pass 1: a regular spell is a local high that persists, or any spell
	// too long to plausibly be a promo.
	isReg := make([]bool, n)
	for i := range spells {
		localHigh := (i == 0 || spells[i].Price > spells[i-1].Price) &&
			(i == n-1 || spells[i].Price > spells[i+1].Price)
		isReg[i] = (localHigh && spells[i].Days >= params.MinRegDays) || spells[i].Days > params.MaxPromoDays
	}

	// Pass 2: V-filter. A lower high sandwiched between two equal-or-higher
	// highs within the promo window is a shallow promo, not a price cut
	// followed by a rise — demote it. Only evaluated among pass-1
	// candidates; the first and last candidate have no "prev"/"next" to
	// compare against and are never demoted.
	var regIdx []int
	for i, r := range isReg {
		if r {
			regIdx = append(regIdx, i)
		}
	}
	for k := 1; k < len(regIdx)-1; k++ {
		gi, prevI, nextI := regIdx[k], regIdx[k-1], regIdx[k+1]
		prevReg, nextReg := spells[prevI].Price, spells[nextI].Price
		gapDays := int(spells[nextI].Start.Sub(spells[prevI].End).Hours()/24) - 1
		if spells[gi].Price < prevReg && nextReg >= prevReg &&
			gapDays <= params.MaxPromoDays && spells[gi].Days <= params.MaxPromoDays {
			isReg[gi] = false
		}
	}

	// Pass 3: staircase. A persistent spell at or above the regular price
	// carried from pass 2 can't be a promo — handles increases with no
	// promo in between. The carry used here is computed once from the
	// post-pass-2 results, not updated as pass 3 promotes rows (matching
	// the reference's single vectorized ffill).
	carryAfterPass2 := carryForward(spells, isReg)
	isReg3 := make([]bool, n)
	for i := range spells {
		if isReg[i] {
			isReg3[i] = true
			continue
		}
		if spells[i].Days >= params.MinRegDays && carryAfterPass2[i] != nil && spells[i].Price >= *carryAfterPass2[i] {
			isReg3[i] = true
		}
	}

	regularPrice := carryForward(spells, isReg3)

	for i := range spells {
		spells[i].IsRegular = isReg3[i]
		spells[i].RegularPrice = regularPrice[i]
		spells[i].Label = labelFor(spells[i], isReg3[i], regularPrice[i], i == n-1)
	}
}

// labelFor is the final classification: regular; unknown (before any
// regular spell is established — a series that opens low could be mid-promo
// or mid-staircase, and the data can't say which); pending (below regular,
// and it's the series' current spell — too recent to call permanent or
// temporary); promo (below regular, already superseded); short_high
// (briefly at or above regular but not demoted to promo and not persistent
// enough to count as regular itself).
func labelFor(s Spell, isReg bool, regularPrice *float64, isLastInSeries bool) string {
	switch {
	case isReg:
		return "regular"
	case regularPrice == nil:
		return "unknown"
	case isLastInSeries && s.Price < *regularPrice:
		return "pending"
	case s.Price < *regularPrice:
		return "promo"
	default:
		return "short_high"
	}
}

// carryForward returns, for every index, the price of the most recent spell
// up to and including it for which flag is true (nil before the first).
func carryForward(spells []Spell, flag []bool) []*float64 {
	out := make([]*float64, len(spells))
	var cur *float64
	for i := range spells {
		if flag[i] {
			p := spells[i].Price
			cur = &p
		}
		out[i] = cur
	}
	return out
}

// regularChanges emits one event per actual move between consecutive
// regular spells — not every transition, only the regular-to-regular ones,
// so a promo dip and its return never register as two changes.
func regularChanges(spells []Spell) []RegularChange {
	var changes []RegularChange
	var prev *float64
	for _, s := range spells {
		if !s.IsRegular {
			continue
		}
		if prev != nil && s.Price != *prev {
			pct := s.Price/(*prev) - 1
			direction := "increase"
			if pct < 0 {
				direction = "permanent_decrease"
			}
			changes = append(changes, RegularChange{
				ChangeDate: s.Start, OldPrice: *prev, NewPrice: s.Price, PctChange: pct, Direction: direction,
			})
		}
		p := s.Price
		prev = &p
	}
	return changes
}

func daysInclusive(a, b time.Time) int {
	return int(b.Sub(a).Hours()/24) + 1
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
