package retailer

import (
	"strings"
	"testing"
)

func TestRegistryIsConsistent(t *testing.T) {
	ids, names := map[string]bool{}, map[string]bool{}
	for _, r := range All {
		if r.ID == "" || r.Name == "" || r.SKUPattern == "" {
			t.Errorf("incomplete retailer entry: %+v", r)
		}
		if r.ID != strings.ToLower(r.ID) || strings.ContainsAny(r.ID, " -") {
			t.Errorf("retailer id %q must be lowercase with no spaces or dashes (it names systemd units)", r.ID)
		}
		if ids[r.ID] || names[r.Name] {
			t.Errorf("duplicate retailer %q / %q", r.ID, r.Name)
		}
		ids[r.ID], names[r.Name] = true, true
		if got, _ := ByID(r.ID); got != r {
			t.Errorf("ByID(%q) did not return the entry", r.ID)
		}
		if ByName(r.Name) != r {
			t.Errorf("ByName(%q) did not return the entry", r.Name)
		}
	}
	if _, err := ByID("nope"); err == nil {
		t.Error("ByID accepted an unknown id")
	}
}

func TestSKUSQLExprNamesEveryRetailer(t *testing.T) {
	expr := SKUSQLExpr()
	for _, r := range All {
		if !strings.Contains(expr, "WHEN '"+r.Name+"' THEN substring(url from '"+r.SKUPattern+"')") {
			t.Errorf("SKU SQL expression is missing %s: %s", r.Name, expr)
		}
	}
}
