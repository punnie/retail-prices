// Package retailer is the single list of supported retailers: the id used on
// the command line and in service names, the name stored in products.store
// (and therefore in every derived table), and how to recover the retailer's
// own product id from a product URL.
//
// Adding a retailer means adding an entry here and a scraper for its ID in
// internal/scrape; the SKU expression used by `retail refresh` and
// normalize.ExtractSKU both derive from this list.
package retailer

import (
	"fmt"
	"regexp"
	"strings"
)

// Retailer describes one supported store.
type Retailer struct {
	// ID is the lowercase identifier used on the command line
	// (`retail run pingodoce`) and in systemd unit names.
	ID string
	// Name is the value stored in products.store. It is the display name the
	// scraper has always written, so existing rows need no rewrite.
	Name string
	// SKUPattern captures the retailer's numeric product id from a product
	// URL in its first group. It must mean the same thing as a Go regexp and
	// as a Postgres regular expression, because refresh evaluates it in SQL
	// (see SKUSQLExpr); keep to the common subset ([0-9], \., anchors).
	SKUPattern string

	sku *regexp.Regexp
}

// All lists every supported retailer, in display order.
//
// SKU patterns: Auchan and Celeiro use the SKU as its own path segment;
// Continente and Pingo Doce append it as a hyphenated suffix before ".html".
// Verified at ~100% coverage over a 3% sample of the full log (see profile
// step 8).
var All = []*Retailer{
	{ID: "auchan", Name: "Auchan", SKUPattern: `/([0-9]+)\.html$`},
	{ID: "continente", Name: "Continente", SKUPattern: `-([0-9]+)\.html$`},
	{ID: "pingodoce", Name: "Pingo Doce", SKUPattern: `-([0-9]+)\.html$`},
	{ID: "celeiro", Name: "Celeiro", SKUPattern: `\.pt/([0-9]+)-`},
}

func init() {
	for _, r := range All {
		r.sku = regexp.MustCompile(r.SKUPattern)
	}
}

// ByID returns the retailer with the given command-line id.
func ByID(id string) (*Retailer, error) {
	for _, r := range All {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("unknown retailer %q (available: %s)", id, strings.Join(IDs(), ", "))
}

// ByName returns the retailer whose stored name is name, or nil.
func ByName(name string) *Retailer {
	for _, r := range All {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// IDs returns every retailer id, in display order.
func IDs() []string {
	ids := make([]string, len(All))
	for i, r := range All {
		ids[i] = r.ID
	}
	return ids
}

// Names returns every stored retailer name, in display order.
func Names() []string {
	names := make([]string, len(All))
	for i, r := range All {
		names[i] = r.Name
	}
	return names
}

// ExtractSKU recovers the retailer's own product id from a product URL.
func (r *Retailer) ExtractSKU(url string) (string, bool) {
	m := r.sku.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// SKUSQLExpr is a SQL expression over the products columns `store` and `url`
// that yields the same SKU as ExtractSKU, or NULL when the URL has none.
func SKUSQLExpr() string {
	var b strings.Builder
	b.WriteString("CASE store")
	for _, r := range All {
		fmt.Fprintf(&b, " WHEN %s THEN substring(url from %s)", quote(r.Name), quote(r.SKUPattern))
	}
	b.WriteString(" END")
	return b.String()
}

// quote renders s as a standard-conforming SQL string literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
