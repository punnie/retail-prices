package scrape

import (
	"testing"

	"retail-prices/internal/retailer"
)

// Every retailer needs a scraper, and the scraper must write the same store
// name the registry (and so every derived table) expects.
func TestEveryRetailerHasAScraper(t *testing.T) {
	for _, r := range retailer.All {
		newScraper, ok := constructors[r.ID]
		if !ok {
			t.Errorf("retailer %q has no scraper", r.ID)
			continue
		}
		if got := newScraper().GetName(); got != r.Name {
			t.Errorf("scraper for %q writes store %q, registry says %q", r.ID, got, r.Name)
		}
	}
	if len(constructors) != len(retailer.All) {
		t.Errorf("%d scrapers registered for %d retailers", len(constructors), len(retailer.All))
	}
}
