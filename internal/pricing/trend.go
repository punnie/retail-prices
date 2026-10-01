package pricing

import "time"

// minTrendCoverageDays mirrors price_spells.py's rolling(..., min_periods=28):
// a trend window with less than this many covered days reports no number
// rather than a wobbly one from a handful of points.
const minTrendCoverageDays = 28

// Trend is the average-paid-price / promo-share / promo-depth trio from
// PRICING_CONCEPTS.md §1, computed as a day-weighted mean over a window
// ending at a given date. Computed on demand from spells rather than
// materializing a per-product-per-day row (price_spells.sql's price_daily):
// product pages only ever need a handful of point-in-time reads (now, a
// year ago), not a full daily series.
type Trend struct {
	AvgPaidPrice  float64
	PromoShare    float64
	AvgPromoDepth float64
	OK            bool
}

// ComputeTrend day-weights every spell's overlap with [at-windowDays+1, at].
func ComputeTrend(spells []Spell, at time.Time, windowDays int) Trend {
	windowStart := at.AddDate(0, 0, -(windowDays - 1))

	var sumDays, sumPromoDays int
	var sumPriceDays, sumDepthDays float64
	for _, s := range spells {
		start, end := s.Start, s.End
		if start.Before(windowStart) {
			start = windowStart
		}
		if end.After(at) {
			end = at
		}
		if end.Before(start) {
			continue
		}
		overlap := daysInclusive(start, end)
		sumDays += overlap
		sumPriceDays += s.Price * float64(overlap)

		if s.Label == "promo" || s.Label == "pending" {
			sumPromoDays += overlap
			if s.RegularPrice != nil && *s.RegularPrice > 0 {
				sumDepthDays += (1 - s.Price/(*s.RegularPrice)) * float64(overlap)
			}
		}
	}

	if sumDays < minTrendCoverageDays {
		return Trend{}
	}
	t := Trend{AvgPaidPrice: sumPriceDays / float64(sumDays), PromoShare: float64(sumPromoDays) / float64(sumDays), OK: true}
	if sumPromoDays > 0 {
		t.AvgPromoDepth = sumDepthDays / float64(sumPromoDays)
	}
	return t
}

// Episode is a promo/pending run, merged across adjacent promo+pending
// spells (a short_high or regular spell always ends one). Price/Depth track
// the deepest point reached, which is what "typical depth" and the buy-now
// signal (§4) care about.
type Episode struct {
	Start, End time.Time
	Days       int
	Price      float64
	Depth      float64
	Pending    bool
}

// Episodes groups consecutive promo/pending spells into discrete episodes.
func Episodes(spells []Spell) []Episode {
	var eps []Episode
	var cur *Episode
	flush := func() {
		if cur != nil {
			eps = append(eps, *cur)
			cur = nil
		}
	}
	for _, s := range spells {
		if s.Label != "promo" && s.Label != "pending" {
			flush()
			continue
		}
		depth := 0.0
		if s.RegularPrice != nil && *s.RegularPrice > 0 {
			depth = 1 - s.Price/(*s.RegularPrice)
		}
		if cur == nil {
			cur = &Episode{Start: s.Start, End: s.End, Days: s.Days, Price: s.Price, Depth: depth, Pending: s.Label == "pending"}
			continue
		}
		cur.End = s.End
		cur.Days += s.Days
		cur.Pending = s.Label == "pending"
		if s.Price < cur.Price {
			cur.Price, cur.Depth = s.Price, depth
		}
	}
	flush()
	return eps
}

// Snapshot is the product-page header in one struct — what
// product_pricing_snapshot stores. Every *float64/*time.Time field is nil
// when there isn't enough history to say anything confident about it (§4:
// "show 'not enough history' rather than a confident number").
type Snapshot struct {
	CurrentPrice         float64
	CurrentLabel         string
	CurrentSpellDays     int
	RegularPrice         *float64
	RegularPrice1y       *float64
	AvgPaidPrice         *float64
	AvgPaidPrice1y       *float64
	PromoShare           *float64
	TypicalPromoDepth    *float64
	TypicalPromoDays     *float64
	PromosPerYear        *float64
	Low90d               *float64
	Low1y                *float64
	LastRegularChangeAt  *time.Time
	LastRegularChangePct *float64
	HasEnoughHistory     bool
}

// minHistoryDays and minRegularSpells are the §4 guard: "fewer than two
// regular spells or under ~6 months of data" ⇒ no confident numbers.
const (
	minHistoryDays   = 182
	minRegularSpells = 2
)

// Summarize builds the product-page snapshot from one product's full spell
// history (all series concatenated) and regular-price changes.
func Summarize(spells []Spell, changes []RegularChange, params Params, now time.Time) Snapshot {
	if len(spells) == 0 {
		return Snapshot{}
	}

	last := spells[len(spells)-1]
	snap := Snapshot{
		CurrentPrice:     last.Price,
		CurrentLabel:     last.Label,
		CurrentSpellDays: last.Days,
		RegularPrice:     last.RegularPrice,
	}

	regCount := 0
	for _, s := range spells {
		if s.IsRegular {
			regCount++
		}
	}
	histDays := daysInclusive(spells[0].Start, now)
	snap.HasEnoughHistory = regCount >= minRegularSpells && histDays >= minHistoryDays

	if cur := ComputeTrend(spells, now, params.TrendWindowDays); cur.OK {
		v, p := cur.AvgPaidPrice, cur.PromoShare
		snap.AvgPaidPrice, snap.PromoShare = &v, &p
	}
	if y := ComputeTrend(spells, now.AddDate(-1, 0, 0), params.TrendWindowDays); y.OK {
		v := y.AvgPaidPrice
		snap.AvgPaidPrice1y = &v
	}
	snap.RegularPrice1y = regularPriceAt(spells, now.AddDate(-1, 0, 0))

	var completed []Episode
	for _, e := range Episodes(spells) {
		if !e.Pending {
			completed = append(completed, e)
		}
	}
	if len(completed) > 0 {
		var sumDepth, sumDays float64
		for _, e := range completed {
			sumDepth += e.Depth
			sumDays += float64(e.Days)
		}
		depth, days := sumDepth/float64(len(completed)), sumDays/float64(len(completed))
		snap.TypicalPromoDepth, snap.TypicalPromoDays = &depth, &days
		if histDays > 0 {
			perYear := float64(len(completed)) / (float64(histDays) / 365.0)
			snap.PromosPerYear = &perYear
		}
	}

	snap.Low90d = lowSince(spells, now.AddDate(0, 0, -90))
	snap.Low1y = lowSince(spells, now.AddDate(-1, 0, 0))

	if len(changes) > 0 {
		lc := changes[len(changes)-1]
		at, pct := lc.ChangeDate, lc.PctChange
		snap.LastRegularChangeAt, snap.LastRegularChangePct = &at, &pct
	}

	return snap
}

func regularPriceAt(spells []Spell, at time.Time) *float64 {
	for i := len(spells) - 1; i >= 0; i-- {
		if !at.Before(spells[i].Start) && !at.After(spells[i].End) {
			return spells[i].RegularPrice
		}
	}
	return nil
}

func lowSince(spells []Spell, since time.Time) *float64 {
	var low *float64
	for _, s := range spells {
		if s.End.Before(since) {
			continue
		}
		if low == nil || s.Price < *low {
			p := s.Price
			low = &p
		}
	}
	return low
}
