// Copy rules from PRICING_CONCEPTS.md §4: every percentage names its
// reference price, "pending" always reads as visibly uncertain, and
// everything here is hedged with "usually"/"typically" rather than stated
// as fact — a promo history is a pattern, not a promise.
package web

import (
	"fmt"
	"time"

	"retail-prices/internal/pricing"
)

// recentChangeWindow: a regular-price change inside this window still leads
// the state badge ("Went up 10% on 12 Mar") instead of the plain "Regular
// price" label.
const recentChangeWindow = 21 * 24 * time.Hour

// stateBadge is the §4 "three numbers" current-price line: a single
// plain-language sentence for whatever state the item is actually in.
func stateBadge(s pricing.Snapshot, now time.Time) string {
	switch s.CurrentLabel {
	case "promo", "pending":
		discount := 0.0
		if s.RegularPrice != nil && *s.RegularPrice > 0 {
			discount = 100 * (1 - s.CurrentPrice/(*s.RegularPrice))
		}
		if s.CurrentLabel == "pending" && s.TypicalPromoDays != nil && float64(s.CurrentSpellDays) > *s.TypicalPromoDays {
			return fmt.Sprintf("Reduced for %d days — longer than this item's typical promo, could be a long promo or a new price", s.CurrentSpellDays)
		}
		if s.TypicalPromoDays != nil {
			return fmt.Sprintf("On promo, %.0f%% off regular, day %d of a typical %.0f", discount, s.CurrentSpellDays, *s.TypicalPromoDays)
		}
		return fmt.Sprintf("Reduced %.0f%% off regular, day %d — not enough promo history yet to say how long is typical", discount, s.CurrentSpellDays)

	case "regular":
		if s.LastRegularChangeAt != nil && s.LastRegularChangePct != nil && now.Sub(*s.LastRegularChangeAt) <= recentChangeWindow {
			verb := "Went up"
			if *s.LastRegularChangePct < 0 {
				verb = "Went down"
			}
			return fmt.Sprintf("%s %.0f%% on %s", verb, abs(*s.LastRegularChangePct)*100, s.LastRegularChangeAt.Format("2 Jan"))
		}
		return "Regular price"

	case "unknown":
		return "Not enough history yet to tell if this is a regular or a promo price"

	case "short_high":
		return "Briefly above its usual price"

	default:
		return ""
	}
}

// buyNowSignal is the §4 "single hedged line" derived from the item's own
// promo history — never a certainty, always "usually"/"typically".
func buyNowSignal(s pricing.Snapshot) string {
	switch s.CurrentLabel {
	case "promo", "pending":
		atLow := s.Low1y != nil && s.CurrentPrice <= *s.Low1y*1.01
		atTypical := s.TypicalPromoDepth != nil && s.RegularPrice != nil &&
			s.CurrentPrice <= *s.RegularPrice*(1-*s.TypicalPromoDepth)*1.01
		switch {
		case atLow:
			return "Great time to buy — at or near its 1-year low."
		case atTypical:
			return "Good time to buy — on promo, at or beyond its typical depth."
		default:
			return "On promo now, but not yet at this item's usual discount depth."
		}

	case "regular":
		if s.PromosPerYear != nil && *s.PromosPerYear >= 3 && s.TypicalPromoDepth != nil && s.RegularPrice != nil {
			typicalPrice := *s.RegularPrice * (1 - *s.TypicalPromoDepth)
			return fmt.Sprintf("Might be worth waiting — this item is frequently promoted, usually dropping to about €%.2f.", typicalPrice)
		}
		return "Regular price — this item is rarely promoted."

	default:
		return "Not enough history yet for a buy-now signal."
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// pctCompare renders a signed percentage change, or "" if either side is
// unknown — used for the §4 year-on-year comparisons next to regular price
// and average paid price.
func pctCompare(cur, then *float64) string {
	if cur == nil || then == nil || *then == 0 {
		return ""
	}
	pct := 100 * (*cur/(*then) - 1)
	if pct >= 0 {
		return fmt.Sprintf("+%.1f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}
