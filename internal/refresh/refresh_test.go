package refresh

import (
	"context"
	"fmt"
	"testing"
	"time"

	"retail-prices/internal/testdb"
)

// A store refresh rebuilds that store's derived rows, leaves every other
// store's alone, and gives the same result when repeated.
func TestStoreRefreshIsScopedAndRepeatable(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	start := time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)
	for day := range 30 {
		at := start.AddDate(0, 0, day)
		for i := range 10 {
			price := 2.0 + float64(i)
			if day >= 10 && day < 15 {
				price *= 0.7 // a promotion
			}
			for _, row := range []struct{ store, url string }{
				{"Continente", fmt.Sprintf("https://www.continente.pt/produto/item-%d-%d.html", i, 1000+i)},
				{"Auchan", fmt.Sprintf("https://www.auchan.pt/pt/x/item-%d/%d.html", i, 2000+i)},
			} {
				if _, err := pool.Exec(ctx, `
					INSERT INTO products (store, name, price, url, scraped_at)
					VALUES ($1, $2, $3, $4, $5)`,
					row.store, fmt.Sprintf("Item %d", i), price, row.url, at.Add(time.Duration(i)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	counts := func() map[string][3]int {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT rp.store,
			       count(DISTINCT rp.id),
			       (SELECT count(*) FROM price_intervals pi JOIN retail_products p ON p.id = pi.retail_product_id WHERE p.store = rp.store),
			       (SELECT count(*) FROM price_spells ps JOIN retail_products p ON p.id = ps.retail_product_id WHERE p.store = rp.store)
			FROM retail_products rp GROUP BY rp.store`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string][3]int{}
		for rows.Next() {
			var store string
			var c [3]int
			if err := rows.Scan(&store, &c[0], &c[1], &c[2]); err != nil {
				t.Fatal(err)
			}
			out[store] = c
		}
		return out
	}

	if err := Store(ctx, pool, "Continente", false); err != nil {
		t.Fatal(err)
	}
	got := counts()
	if _, ok := got["Auchan"]; ok {
		t.Fatalf("refreshing Continente touched Auchan: %v", got)
	}
	c := got["Continente"]
	if c[0] != 10 || c[1] != 30 || c[2] == 0 {
		t.Fatalf("Continente has %d products, %d intervals, %d spells; want 10, 30 (regular, promo, regular each), >0", c[0], c[1], c[2])
	}

	if err := Store(ctx, pool, "Auchan", false); err != nil {
		t.Fatal(err)
	}
	if err := Store(ctx, pool, "Continente", false); err != nil {
		t.Fatal(err)
	}
	if err := Store(ctx, pool, "Continente", true); err != nil {
		t.Fatal(err)
	}
	again := counts()
	if again["Continente"] != c {
		t.Errorf("repeating the Continente refresh changed it: %v, then %v", c, again["Continente"])
	}
	if again["Auchan"] != c {
		t.Errorf("Auchan, with the same history, refreshed to %v; Continente has %v", again["Auchan"], c)
	}
}
