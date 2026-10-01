package web

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/pricing"
	"retail-prices/internal/quality"
)

type catalogueRow struct {
	ID                 int64
	Store, Name, Brand string
	ImageURL           string
	CurrentPrice       float64
	HasSnapshot        bool
	CurrentLabel       string
	LastObservedAt     time.Time
}

func listProducts(ctx context.Context, pool *pgxpool.Pool, q, store string, limit int) ([]catalogueRow, error) {
	const base = `
		SELECT rp.id, rp.store, rp.name, coalesce(rp.brand, ''), coalesce(rp.image_url, ''),
		       coalesce(pps.current_price, 0), pps.current_price IS NOT NULL, coalesce(pps.current_label, ''),
		       coalesce(pps.last_observed_at, rp.last_seen)
		FROM retail_products rp
		LEFT JOIN product_pricing_snapshot pps ON pps.retail_product_id = rp.id
		WHERE NOT rp.is_phantom`

	var rows pgx.Rows
	var err error
	switch {
	case q != "" && store != "":
		rows, err = pool.Query(ctx, base+`
			AND rp.store = $1 AND rp.name_norm % $2
			ORDER BY similarity(rp.name_norm, $2) DESC LIMIT $3`, store, normalizeQuery(q), limit)
	case q != "":
		rows, err = pool.Query(ctx, base+`
			AND rp.name_norm % $1
			ORDER BY similarity(rp.name_norm, $1) DESC LIMIT $2`, normalizeQuery(q), limit)
	case store != "":
		rows, err = pool.Query(ctx, base+`
			AND rp.store = $1
			ORDER BY rp.last_seen DESC LIMIT $2`, store, limit)
	default:
		rows, err = pool.Query(ctx, base+`
			ORDER BY rp.last_seen DESC LIMIT $1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []catalogueRow
	for rows.Next() {
		var r catalogueRow
		if err := rows.Scan(&r.ID, &r.Store, &r.Name, &r.Brand, &r.ImageURL,
			&r.CurrentPrice, &r.HasSnapshot, &r.CurrentLabel, &r.LastObservedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func listStores(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT DISTINCT store FROM retail_products ORDER BY store`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type productRow struct {
	ID                    int64
	Store, SKU, URL, Name string
	Brand, Category       string
	ImageURL              string
	PackValue             *float64
	PackUnit              string
	IsPhantom             bool
	FirstSeen, LastSeen   time.Time
}

func loadProduct(ctx context.Context, pool *pgxpool.Pool, id int64) (productRow, bool, error) {
	var r productRow
	err := pool.QueryRow(ctx, `
		SELECT id, store, sku, url, name, coalesce(brand,''), coalesce(category,''), coalesce(image_url,''),
		       pack_value, coalesce(pack_unit,''), is_phantom, first_seen, last_seen
		FROM retail_products WHERE id = $1`, id).Scan(
		&r.ID, &r.Store, &r.SKU, &r.URL, &r.Name, &r.Brand, &r.Category, &r.ImageURL,
		&r.PackValue, &r.PackUnit, &r.IsPhantom, &r.FirstSeen, &r.LastSeen)
	if err == pgx.ErrNoRows {
		return productRow{}, false, nil
	}
	if err != nil {
		return productRow{}, false, err
	}
	return r, true, nil
}

func loadSnapshot(ctx context.Context, pool *pgxpool.Pool, id int64) (pricing.Snapshot, bool, error) {
	var s pricing.Snapshot
	var lastObserved time.Time
	err := pool.QueryRow(ctx, `
		SELECT current_price, current_label, current_spell_days,
		       regular_price, regular_price_1y, avg_paid_price, avg_paid_price_1y, promo_share,
		       typical_promo_depth, typical_promo_days, promos_per_year, low_90d, low_1y,
		       last_regular_change_at, last_regular_change_pct, has_enough_history, last_observed_at
		FROM product_pricing_snapshot WHERE retail_product_id = $1`, id).Scan(
		&s.CurrentPrice, &s.CurrentLabel, &s.CurrentSpellDays,
		&s.RegularPrice, &s.RegularPrice1y, &s.AvgPaidPrice, &s.AvgPaidPrice1y, &s.PromoShare,
		&s.TypicalPromoDepth, &s.TypicalPromoDays, &s.PromosPerYear, &s.Low90d, &s.Low1y,
		&s.LastRegularChangeAt, &s.LastRegularChangePct, &s.HasEnoughHistory, &lastObserved)
	if err == pgx.ErrNoRows {
		return pricing.Snapshot{}, false, nil
	}
	if err != nil {
		return pricing.Snapshot{}, false, err
	}
	return s, true, nil
}

func loadSpells(ctx context.Context, pool *pgxpool.Pool, id int64) ([]pricing.Spell, error) {
	rows, err := pool.Query(ctx, `
		SELECT series_index, spell_index, price, start_date, end_date, days, is_regular, regular_price, label
		FROM price_spells WHERE retail_product_id = $1
		ORDER BY series_index, spell_index`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pricing.Spell
	for rows.Next() {
		var s pricing.Spell
		if err := rows.Scan(&s.SeriesIndex, &s.SpellIndex, &s.Price, &s.Start, &s.End, &s.Days, &s.IsRegular, &s.RegularPrice, &s.Label); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func loadChanges(ctx context.Context, pool *pgxpool.Pool, id int64) ([]pricing.RegularChange, error) {
	rows, err := pool.Query(ctx, `
		SELECT change_date, old_price, new_price, pct_change, direction
		FROM regular_price_changes WHERE retail_product_id = $1
		ORDER BY change_date`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pricing.RegularChange
	for rows.Next() {
		var c pricing.RegularChange
		if err := rows.Scan(&c.ChangeDate, &c.OldPrice, &c.NewPrice, &c.PctChange, &c.Direction); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func loadObservedIntervals(ctx context.Context, pool *pgxpool.Pool, id int64) ([]quality.Interval, error) {
	rows, err := pool.Query(ctx, `
		SELECT price, valid_from, valid_to, observation_count
		FROM price_intervals WHERE retail_product_id = $1 AND NOT is_suspect
		ORDER BY valid_from`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []quality.Interval
	for rows.Next() {
		var iv quality.Interval
		if err := rows.Scan(&iv.Price, &iv.ValidFrom, &iv.ValidTo, &iv.ObservationCount); err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

type siblingRow struct {
	ID           int64
	Store        string
	SKU          string
	Name         string
	SameStore    bool
	CurrentPrice float64
	HasSnapshot  bool
	LastSeen     time.Time
}

// loadGroupSiblings returns the other products in this product's
// product_group (the confirmed identity, see
// db/migrations/002_derived_schema.sql), if any — other retailers first,
// then other listings at this product's own store (relisted SKUs).
func loadGroupSiblings(ctx context.Context, pool *pgxpool.Pool, id int64) ([]siblingRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT rp.id, rp.store, rp.sku, rp.name, rp.store = mine.store,
		       coalesce(pps.current_price, 0), pps.current_price IS NOT NULL,
		       coalesce(pps.last_observed_at, rp.last_seen)
		FROM product_group_members me
		JOIN retail_products mine ON mine.id = me.retail_product_id
		JOIN product_group_members other ON other.product_group_id = me.product_group_id AND other.retail_product_id <> me.retail_product_id
		JOIN retail_products rp ON rp.id = other.retail_product_id
		LEFT JOIN product_pricing_snapshot pps ON pps.retail_product_id = rp.id
		WHERE me.retail_product_id = $1
		ORDER BY rp.store = mine.store, rp.store, rp.last_seen DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []siblingRow
	for rows.Next() {
		var s siblingRow
		if err := rows.Scan(&s.ID, &s.Store, &s.SKU, &s.Name, &s.SameStore, &s.CurrentPrice, &s.HasSnapshot, &s.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func normalizeQuery(q string) string {
	b := []byte(q)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
