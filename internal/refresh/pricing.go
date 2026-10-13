package refresh

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"retail-prices/internal/pricing"
	"retail-prices/internal/quality"
)

// classifyStore is phase C of a refresh: it re-reads the store's interval
// series built by phase A, now that phase B has derived the store's
// placeholder prices, quarantines implausible intervals (internal/quality),
// and runs the internal/pricing spell decomposition over what's left —
// writing price_spells, regular_price_changes and product_pricing_snapshot.
//
// Reading price_intervals rather than the raw log is what makes a second
// pass cheap — a few million rows instead of 28 million. Those intervals
// were written inside tx and are only visible there, so they are read on tx
// too, a batch of products at a time: each batch is read in full before its
// results are written, since a pgx connection can't write while a query's
// rows are still open.
func classifyStore(ctx context.Context, tx pgx.Tx, store string) error {
	placeholders, err := loadPlaceholders(ctx, tx, store)
	if err != nil {
		return fmt.Errorf("loading placeholder prices: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  %s: %d placeholder price(s)\n", store, len(placeholders))

	params, err := loadPricingParams(ctx, tx)
	if err != nil {
		return fmt.Errorf("loading pricing params: %w", err)
	}
	p := params.forStore(store)

	runIdx, err := loadRunIndex(ctx, tx, store)
	if err != nil {
		return fmt.Errorf("loading run index: %w", err)
	}

	writeConn := tx

	var (
		curProduct   int64
		ids          []int64
		intervals    []quality.Interval
		suspectIDs   []int64
		spellRows    [][]any
		changeRows   [][]any
		snapshotRows [][]any
		productsSeen int
		now          = time.Now()
	)

	flush := func() error {
		if len(spellRows) > 0 {
			if _, err := writeConn.CopyFrom(ctx, pgx.Identifier{"price_spells"},
				[]string{"retail_product_id", "series_index", "spell_index", "price", "start_date", "end_date", "days", "is_regular", "regular_price", "label"},
				pgx.CopyFromRows(spellRows)); err != nil {
				return fmt.Errorf("copying price_spells: %w", err)
			}
			spellRows = spellRows[:0]
		}
		if len(changeRows) > 0 {
			if _, err := writeConn.CopyFrom(ctx, pgx.Identifier{"regular_price_changes"},
				[]string{"retail_product_id", "change_date", "old_price", "new_price", "pct_change", "direction"},
				pgx.CopyFromRows(changeRows)); err != nil {
				return fmt.Errorf("copying regular_price_changes: %w", err)
			}
			changeRows = changeRows[:0]
		}
		if len(snapshotRows) > 0 {
			if _, err := writeConn.CopyFrom(ctx, pgx.Identifier{"product_pricing_snapshot"},
				[]string{"retail_product_id", "current_price", "current_label", "current_spell_days",
					"regular_price", "regular_price_1y", "avg_paid_price", "avg_paid_price_1y", "promo_share",
					"typical_promo_depth", "typical_promo_days", "promos_per_year", "low_90d", "low_1y",
					"last_regular_change_at", "last_regular_change_pct", "has_enough_history", "last_observed_at"},
				pgx.CopyFromRows(snapshotRows)); err != nil {
				return fmt.Errorf("copying product_pricing_snapshot: %w", err)
			}
			snapshotRows = snapshotRows[:0]
		}
		if len(suspectIDs) > 0 {
			if _, err := writeConn.Exec(ctx,
				`UPDATE price_intervals SET is_suspect = true WHERE id = ANY($1)`, suspectIDs); err != nil {
				return fmt.Errorf("flagging suspect intervals: %w", err)
			}
			suspectIDs = suspectIDs[:0]
		}
		return nil
	}

	processProduct := func() {
		if len(intervals) == 0 {
			return
		}
		productsSeen++

		opts := quality.Options{}
		if len(placeholders) > 0 {
			opts.IsPlaceholderPrice = func(price float64) bool {
				return placeholders[roundCents(price)]
			}
		}
		quality.FlagSuspect(intervals, opts)

		var clean []quality.Interval
		for i, iv := range intervals {
			if iv.IsSuspect {
				suspectIDs = append(suspectIDs, ids[i])
				continue
			}
			clean = append(clean, iv)
		}
		if len(clean) == 0 {
			return
		}

		hadActivity := func(from, to time.Time) bool { return runIdx.countBetween(store, from, to) > 0 }
		spells, changes := pricing.Classify(clean, p, hadActivity)
		if len(spells) == 0 {
			return
		}

		for _, s := range spells {
			spellRows = append(spellRows, []any{
				curProduct, s.SeriesIndex, s.SpellIndex, s.Price, s.Start, s.End, s.Days, s.IsRegular, s.RegularPrice, s.Label,
			})
		}
		for _, c := range changes {
			changeRows = append(changeRows, []any{
				curProduct, c.ChangeDate, c.OldPrice, c.NewPrice, c.PctChange, c.Direction,
			})
		}

		snap := pricing.Summarize(spells, changes, p, now)
		snapshotRows = append(snapshotRows, []any{
			curProduct, snap.CurrentPrice, snap.CurrentLabel, snap.CurrentSpellDays,
			snap.RegularPrice, snap.RegularPrice1y, snap.AvgPaidPrice, snap.AvgPaidPrice1y, snap.PromoShare,
			snap.TypicalPromoDepth, snap.TypicalPromoDays, snap.PromosPerYear, snap.Low90d, snap.Low1y,
			snap.LastRegularChangeAt, snap.LastRegularChangePct, snap.HasEnoughHistory, intervals[len(intervals)-1].ValidTo,
		})
	}

	type row struct {
		id, productID int64
		iv            quality.Interval
	}
	var batch []row
	var lastProduct int64
	for {
		rows, err := tx.Query(ctx, `
			SELECT pi.id, pi.retail_product_id, pi.price, pi.valid_from, pi.valid_to, pi.observation_count
			FROM price_intervals pi
			WHERE pi.retail_product_id IN (
				SELECT id FROM retail_products
				WHERE store = $1 AND NOT is_phantom AND id > $2
				ORDER BY id
				LIMIT $3
			)
			ORDER BY pi.retail_product_id, pi.valid_from`, store, lastProduct, flushEvery)
		if err != nil {
			return err
		}
		batch = batch[:0]
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.productID, &r.iv.Price, &r.iv.ValidFrom, &r.iv.ValidTo, &r.iv.ObservationCount); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}

		for _, r := range batch {
			if r.productID != curProduct {
				processProduct()
				curProduct = r.productID
				ids = ids[:0]
				intervals = intervals[:0]
			}
			ids = append(ids, r.id)
			intervals = append(intervals, r.iv)
		}
		processProduct()
		curProduct = 0
		ids = ids[:0]
		intervals = intervals[:0]
		if err := flush(); err != nil {
			return err
		}
		lastProduct = batch[len(batch)-1].productID
		fmt.Fprintf(os.Stderr, "  priced %d products\n", productsSeen)
	}

	fmt.Fprintf(os.Stderr, "done: priced %d products\n", productsSeen)
	return nil
}

// roundCents normalizes a float price to cents so it can be compared against
// the NUMERIC(.,2) prices stored in store_placeholder_prices. The raw log
// stores prices as float, so 1.90 arrives as 1.899999976158142.
func roundCents(price float64) float64 {
	return float64(int64(price*100+0.5)) / 100
}

func loadPlaceholders(ctx context.Context, tx pgx.Tx, store string) (map[float64]bool, error) {
	rows, err := tx.Query(ctx, `SELECT price FROM store_placeholder_prices WHERE store = $1`, store)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[float64]bool{}
	for rows.Next() {
		var price float64
		if err := rows.Scan(&price); err != nil {
			return nil, err
		}
		out[roundCents(price)] = true
	}
	return out, rows.Err()
}

// pricingParamSet holds the global default plus any per-store overrides
// loaded from pricing_params (see db/migrations/005_pricing_spells.sql).
type pricingParamSet struct {
	global   pricing.Params
	perStore map[string]pricing.Params
}

func (s pricingParamSet) forStore(store string) pricing.Params {
	if p, ok := s.perStore[store]; ok {
		return p
	}
	return s.global
}

func loadPricingParams(ctx context.Context, tx pgx.Tx) (pricingParamSet, error) {
	rows, err := tx.Query(ctx, `SELECT store, min_reg_days, max_promo_days, trend_window_days, max_gap_days FROM pricing_params`)
	if err != nil {
		return pricingParamSet{}, err
	}
	defer rows.Close()

	set := pricingParamSet{global: pricing.DefaultParams, perStore: map[string]pricing.Params{}}
	for rows.Next() {
		var store string
		var p pricing.Params
		if err := rows.Scan(&store, &p.MinRegDays, &p.MaxPromoDays, &p.TrendWindowDays, &p.MaxGapDays); err != nil {
			return pricingParamSet{}, err
		}
		if store == "" {
			set.global = p
		} else {
			set.perStore[store] = p
		}
	}
	return set, rows.Err()
}
