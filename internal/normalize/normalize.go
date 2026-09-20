// Package normalize turns the raw log's free-text store/url/name/description
// fields into the structured signals the rest of the platform needs: a
// stable per-store SKU, a canonical pack size, and accent-folded names for
// matching. Every pattern here was derived from internal/profile's coverage
// report over the real data (see profile_report.md), not guessed.
package normalize

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// --- SKU extraction ----------------------------------------------------------

// skuPatterns captures the numeric product id embedded in each store's URL.
// Auchan and Celeiro use the SKU as its own path segment; Continente and
// Pingo Doce append it as a hyphenated suffix before ".html". Verified at
// ~100% coverage over a 3% sample of the full log (see profile step 8).
var skuPatterns = map[string]*regexp.Regexp{
	"Auchan":     regexp.MustCompile(`/(\d+)\.html$`),
	"Continente": regexp.MustCompile(`-(\d+)\.html$`),
	"Pingo Doce": regexp.MustCompile(`-(\d+)\.html$`),
	"Celeiro":    regexp.MustCompile(`\.pt/(\d+)-`),
}

// ExtractSKU recovers the retailer's own product id from a product URL. The
// small fraction of Continente URLs that don't match are non-product pages
// (campaign/landing pages) that leaked into the sitemap scrape, not a parser
// bug — ok=false for those is correct and should exclude the row.
func ExtractSKU(store, url string) (sku string, ok bool) {
	re, known := skuPatterns[store]
	if !known {
		return "", false
	}
	m := re.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// --- Name/brand normalization -------------------------------------------------

var stopwords = map[string]bool{
	"emb": true, "un": true, "uni": true, "unidade": true, "unidades": true,
	"pack": true, "garrafa": true, "de": true, "da": true, "do": true,
}

var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

// foldAccents strips combining marks after NFD decomposition, e.g. "Champô"
// -> "champo", so the same product spelled differently across retailers'
// catalogues still compares equal.
func foldAccents(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// NormalizeName folds accents, lowercases, strips punctuation to single
// spaces, and drops packaging stopwords, producing a string suitable for
// trigram similarity and exact-match blocking.
func NormalizeName(name string) string {
	folded := strings.ToLower(foldAccents(name))
	folded = nonAlnumRun.ReplaceAllString(folded, " ")

	words := strings.Fields(folded)
	kept := words[:0]
	for _, w := range words {
		if !stopwords[w] {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// NormalizeBrand is NormalizeName without stopword removal — brand names are
// short enough that stripping words risks conflating distinct brands.
func NormalizeBrand(brand string) string {
	folded := strings.ToLower(foldAccents(brand))
	folded = nonAlnumRun.ReplaceAllString(folded, " ")
	return strings.TrimSpace(folded)
}

// --- Pack size parsing ---------------------------------------------------------

// Unit is the canonical pack unit every store's format collapses to.
type Unit string

const (
	UnitGram Unit = "g"
	UnitMl   Unit = "ml"
	UnitUn   Unit = "un" // count units: comprimidos, capsulas, ampolas, unidades...
)

// PackSize is the canonical, cross-retailer-comparable size of a product:
// TotalValue is the total quantity across the whole pack (e.g. a "2x1.75L"
// multipack has TotalValue=3.5, Unit=ml*1000... see ParsePackSize), and
// UnitCount is how many discrete items make up the pack (1 for a single
// bottle, 2 for the multipack, 60 for a jar of capsules).
type PackSize struct {
	TotalValue float64
	Unit       Unit
	UnitCount  int
}

func normalizeUnit(raw string) (Unit, float64) {
	switch strings.ToLower(raw) {
	case "kg":
		return UnitGram, 1000
	case "g", "gr", "gramas":
		return UnitGram, 1
	case "l", "lt", "ltr", "litros", "litro":
		return UnitMl, 1000
	case "ml":
		return UnitMl, 1
	case "cl":
		return UnitMl, 10
	default:
		// un, comprimidos, capsulas/cápsulas, ampolas, saquetas, unidades,
		// comp, cps, amp — all count units.
		return UnitUn, 1
	}
}

func parseDecimal(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

const unitAlt = `kg|g|gr|gramas|l|lt|ltr|litros|litro|ml|cl|un|comprimidos|c[aá]psulas|ampolas|saquetas|unidades|comp|cps|amp`

var (
	// Pingo Doce: "0.555 Kg | 8,99 €/Kg | Categoria: X" or, for fresh
	// produce with no printed unit price, just "2.05 Kg | Categoria: X".
	// Multipacks: "2 x1.75 L | 1,05 €/L | Categoria: X".
	pingoDoceMulti  = regexp.MustCompile(`(?i)^\s*(\d+)\s*x\s*([\d.,]+)\s*(` + unitAlt + `)\b`)
	pingoDoceSingle = regexp.MustCompile(`(?i)^\s*([\d.,]+)\s*(` + unitAlt + `)\b`)

	// Celeiro: "150 ML • 97.66€ por Ltr." / "60 Comprimidos • 0.27€ por Comp."
	// / "24 Saquetas • 0.38€ por Unid." — the leading qty+unit is what we
	// need; the unit-price clause after the bullet is not required to match.
	celeiroLeading = regexp.MustCompile(`(?i)^\s*([\d.,]+)\s*(` + unitAlt + `)\b`)

	// Auchan's description only carries a stated unit price ("9.07 €/Lt |
	// ..."), never the pack size itself — that lives at the end of the
	// product name instead ("... 440 ML", "... 1 UN").
	auchanNameSize = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(kg|g|gr|l|lt|ml|cl|un)\.?\s*$`)

	// Continente: "emb. 200 ml" / "garrafa 75 cl" anchor the common case.
	continenteAnchored = regexp.MustCompile(`(?i)^\s*(?:emb\.|garrafa)\s*([\d.,]+)\s*(` + unitAlt + `)\b`)
	// Fallback for richer marketing-copy descriptions where the quantity
	// isn't at the start: take the first "<number> <unit>" token anywhere.
	continenteAnywhere = regexp.MustCompile(`(?i)([\d.,]+)\s*(kg|g|gr|l|lt|ml|cl|un)\b`)
)

// ParsePackSize extracts a canonical pack size for a product. Every store
// has its own text format (see internal/profile step 8); this dispatches on
// store and tries each store's known sub-formats in order of specificity.
// ok=false is expected and fine for descriptions that genuinely carry no
// pack information (e.g. Continente's book-price-regulation blurbs).
func ParsePackSize(store, name, description string) (PackSize, bool) {
	switch store {
	case "Pingo Doce":
		if m := pingoDoceMulti.FindStringSubmatch(description); m != nil {
			count, _ := strconv.Atoi(m[1])
			qty, ok := parseDecimal(m[2])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[3])
			return PackSize{TotalValue: qty * mult * float64(count), Unit: unit, UnitCount: count}, true
		}
		if m := pingoDoceSingle.FindStringSubmatch(description); m != nil {
			qty, ok := parseDecimal(m[1])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[2])
			return PackSize{TotalValue: qty * mult, Unit: unit, UnitCount: 1}, true
		}

	case "Celeiro":
		if m := celeiroLeading.FindStringSubmatch(description); m != nil {
			qty, ok := parseDecimal(m[1])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[2])
			unitCount := 1
			if unit == UnitUn {
				unitCount = int(qty)
			}
			return PackSize{TotalValue: qty * mult, Unit: unit, UnitCount: unitCount}, true
		}

	case "Auchan":
		if m := auchanNameSize.FindStringSubmatch(name); m != nil {
			qty, ok := parseDecimal(m[1])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[2])
			unitCount := 1
			if unit == UnitUn {
				unitCount = int(qty)
			}
			return PackSize{TotalValue: qty * mult, Unit: unit, UnitCount: unitCount}, true
		}

	case "Continente":
		if m := continenteAnchored.FindStringSubmatch(description); m != nil {
			qty, ok := parseDecimal(m[1])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[2])
			return PackSize{TotalValue: qty * mult, Unit: unit, UnitCount: 1}, true
		}
		if m := continenteAnywhere.FindStringSubmatch(description); m != nil {
			qty, ok := parseDecimal(m[1])
			if !ok {
				break
			}
			unit, mult := normalizeUnit(m[2])
			return PackSize{TotalValue: qty * mult, Unit: unit, UnitCount: 1}, true
		}
	}

	return PackSize{}, false
}
