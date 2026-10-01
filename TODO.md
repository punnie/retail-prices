# TODO

## Merge relisted SKUs within a retailer into one price series

**Problem.** Retailers sometimes relist the same item under a new SKU and often
keep both listings live for weeks or months. Each SKU is its own
`retail_products` row, so its price history, spells and snapshot are computed
in isolation, and the handover distorts the analysis.

Example: Continente "Cabeça de Toiro Reserva Tejo Vinho Tinto", id 189996
(SKU 2165330) and id 190006 (SKU 6418169). They have the same name, brand and
"garrafa 75 cl" and overlapped from June to September 2026. Full price
(€7.49–9.99) appears to have moved to the new SKU while the old one sat at
€5.59–5.65. The old listing's analysis therefore reports a −25.6% "permanent
decrease" on 2026-06-23 and a +34% increase on 2026-09-01. Neither is real.

Since commit 800583e these can be linked by hand into the same
`product_group`, but that only says "same product" for display. Pricing still
runs per SKU.

**Scale.** As of the 2026-09-20 dump, about 6.2k same-store pairs share an
identical `name_norm` and pack size, mostly overlapping in time. Most are
genuinely different products (cosmetic shades, book editions, multipacks).
Only a minority are true relistings, so this must never be automatic.

**Proposed approach.**
- Add a separate relation for "same listing over time within one store"
  (e.g. a `listing_lineages` table), distinct from cross-retailer
  `product_groups`. The two mean different things: pricing depends on
  lineages, not on cross-retailer groups.
- Human-confirmed only, like matching. Reuse the manual link UI, or add a
  "this is a relisting" action there.
- In `retail refresh` phase C, build one interval series per lineage, stitched
  from all member SKUs, before `quality.FlagSuspect` → `pricing.Classify`. Its
  spells, regular-price changes and snapshot replace those of the individual
  SKUs.

**Decisions to make.**
- **Overlap rule:** which price represents the lineage while both listings are
  live?
  - *Newest listing wins once it appears* is closest to the shelf price, but
    it hides the old SKU's clearance.
  - *Lowest of the two* turns the clearance into a fake months-long promo.
    Likely not this one.
  - Possibly show the clearance as an annotation on the chart instead.
- **Canonical page:** probably the newest SKU, with older members linking or
  redirecting to it. Decide what the catalogue lists (one card per lineage?).
- **Suggestions:** whether `retail match` should propose same-store
  candidates (identical name and pack, one listing ending as the other starts
  or a short overlap). That timing signature may separate relistings from
  shade/edition/multipack look-alikes. Profile it before trusting it.
- **Cross-retailer comparison:** the planned per-retailer chart lines should
  use the lineage series, not pick a single SKU per store.

**Verify with:** the Cabeça de Toiro pair (the June/September artifacts should
disappear), plus a spot-check of a few dozen confirmed relistings for
plausible regular prices.
