package refresh

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// genericNameMinSKUs is the minimum number of distinct SKUs within one store
// sharing the exact same product name before that name is treated as site
// noise rather than a real product title.
//
// Derived from the data, not guessed: across Auchan/Continente/Pingo Doce/
// Celeiro, every *legitimate* shared name (generic items like "Água sem
// Gás" sold under many SKUs, or a book/CD title repeated across editions)
// tops out at 35 distinct SKUs. The next data point up is Continente's
// "ZU.pt - Cuidados para Cães e Gatos" (a cross-sell banner to a sister
// site) at 115, then "Supermercado Online" and "Continente, o seu
// supermercado online" — generic site taglines, not product names — at
// 14,082 and 25,922. There is no middle ground in the real distribution, so
// any threshold between 36 and 114 draws the same line; 50 sits in the
// middle of that gap.
//
// This is what caused the exact failure mode flagged before (a product
// jumping 1000%+ in one step): these aren't real items, so their "prices"
// are effectively noise, and internal/pricing dutifully found a "regular
// price" in that noise like it would for any other series.
const genericNameMinSKUs = 50

// flagGenericNames marks retail_products whose name is shared by an
// implausible number of distinct SKUs within the same store as phantom —
// reusing is_phantom's existing meaning ("not a trustworthy product") and
// its existing exclusions everywhere it's already checked (the catalogue,
// cross-retailer matching), rather than adding a second flag. Nothing is
// deleted; the rows and their full observation history stay queryable.
func flagGenericNames(ctx context.Context, tx pgx.Tx, store string) error {
	_, err := tx.Exec(ctx, `
		UPDATE retail_products rp
		SET is_phantom = true
		FROM (
			SELECT store, name
			FROM retail_products
			WHERE store = $2
			GROUP BY store, name
			HAVING count(*) >= $1
		) junk
		WHERE rp.store = junk.store AND rp.name = junk.name AND NOT rp.is_phantom`,
		genericNameMinSKUs, store)
	return err
}
