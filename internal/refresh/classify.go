package refresh

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"retail-prices/internal/quality"
)

// classifyAll is phase C of a refresh: it re-reads the interval series built
// by phase A, now that phase B has derived each store's placeholder prices,
// flags the intervals that are implausible for their product, and emits one
// price_events row per transition.
//
// Reading price_intervals rather than the raw log is what makes a second pass
// cheap — a few million rows instead of 28 million.
func classifyAll(ctx context.Context, pool *pgxpool.Pool) error {
	placeholders, err := loadPlaceholders(ctx, pool)
	if err != nil {
		return fmt.Errorf("loading placeholder prices: %w", err)
	}
	for store, prices := range placeholders {
		fmt.Fprintf(os.Stderr, "  %s: %d placeholder price(s)\n", store, len(prices))
	}

	readConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer readConn.Release()
	if _, err := readConn.Exec(ctx, "SET work_mem = '512MB'"); err != nil {
		return err
	}

	writeConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer writeConn.Release()

	rows, err := readConn.Query(ctx, `
		SELECT pi.id, pi.retail_product_id, rp.store, pi.price, pi.valid_from, pi.valid_to, pi.observation_count
		FROM price_intervals pi
		JOIN retail_products rp ON rp.id = pi.retail_product_id
		ORDER BY pi.retail_product_id, pi.valid_from`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		curProduct   int64
		curStore     string
		ids          []int64
		intervals    []quality.Interval
		suspectIDs   []int64
		eventRows    [][]any
		productsSeen int
	)

	flush := func() error {
		if len(eventRows) > 0 {
			if _, err := writeConn.CopyFrom(ctx, pgx.Identifier{"price_events"},
				[]string{"retail_product_id", "old_price", "new_price", "pct_change", "changed_at", "kind", "confidence"},
				pgx.CopyFromRows(eventRows)); err != nil {
				return fmt.Errorf("copying price_events: %w", err)
			}
			eventRows = eventRows[:0]
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

		storePlaceholders := placeholders[curStore]
		opts := quality.Options{}
		if len(storePlaceholders) > 0 {
			opts.IsPlaceholderPrice = func(price float64) bool {
				return storePlaceholders[roundCents(price)]
			}
		}

		events := quality.ClassifyEvents(intervals, opts)
		for i, iv := range intervals {
			if iv.IsSuspect {
				suspectIDs = append(suspectIDs, ids[i])
			}
		}
		for _, ev := range events {
			eventRows = append(eventRows, []any{
				curProduct, ev.OldPrice, ev.NewPrice, ev.PctChange, ev.ChangedAt, string(ev.Kind), ev.Confidence,
			})
		}
	}

	for rows.Next() {
		var (
			id, productID int64
			store         string
			iv            quality.Interval
		)
		if err := rows.Scan(&id, &productID, &store, &iv.Price, &iv.ValidFrom, &iv.ValidTo, &iv.ObservationCount); err != nil {
			return err
		}

		if productID != curProduct {
			processProduct()
			if productsSeen%flushEvery == 0 && productsSeen > 0 {
				if err := flush(); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "  classified %d products\n", productsSeen)
			}
			curProduct, curStore = productID, store
			ids = ids[:0]
			intervals = intervals[:0]
		}
		ids = append(ids, id)
		intervals = append(intervals, iv)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	processProduct()
	if err := flush(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "done: classified %d products\n", productsSeen)
	return nil
}

// roundCents normalizes a float price to cents so it can be compared against
// the NUMERIC(.,2) prices stored in store_placeholder_prices. The raw log
// stores prices as float, so 1.90 arrives as 1.899999976158142.
func roundCents(price float64) float64 {
	return math.Round(price*100) / 100
}

func loadPlaceholders(ctx context.Context, pool *pgxpool.Pool) (map[string]map[float64]bool, error) {
	rows, err := pool.Query(ctx, `SELECT store, price FROM store_placeholder_prices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[float64]bool{}
	for rows.Next() {
		var store string
		var price float64
		if err := rows.Scan(&store, &price); err != nil {
			return nil, err
		}
		if out[store] == nil {
			out[store] = map[float64]bool{}
		}
		out[store][roundCents(price)] = true
	}
	return out, rows.Err()
}
