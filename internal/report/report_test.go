package report

import (
	"strings"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	cases := map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"4w":  4 * 7 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := parseSince(in)
		if err != nil {
			t.Errorf("parseSince(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSince(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseSince_Invalid(t *testing.T) {
	if _, err := parseSince("banana"); err == nil {
		t.Error("expected an error for an unparseable --since value")
	}
}

func TestDict(t *testing.T) {
	m, err := dict("Title", "x", "Events", 5)
	if err != nil {
		t.Fatalf("dict returned error: %v", err)
	}
	if m["Title"] != "x" || m["Events"] != 5 {
		t.Errorf("dict produced unexpected map: %+v", m)
	}

	if _, err := dict("Title"); err == nil {
		t.Error("expected an error for an odd number of arguments")
	}
	if _, err := dict(5, "x"); err == nil {
		t.Error("expected an error for a non-string key")
	}
}

func TestRenderSparkline_FlatSeriesRendersNothing(t *testing.T) {
	pts := []pricePoint{
		{At: time.Unix(0, 0), Price: 1.99},
		{At: time.Unix(1000, 0), Price: 1.99},
	}
	if html := renderSparkline(pts, "decrease"); html != "" {
		t.Errorf("expected empty output for a flat series, got %q", html)
	}
}

func TestRenderSparkline_NonFlatSeriesRendersSVG(t *testing.T) {
	pts := []pricePoint{
		{At: time.Unix(0, 0), Price: 3.99},
		{At: time.Unix(1000, 0), Price: 1.99},
	}
	html := renderSparkline(pts, "decrease")
	if html == "" {
		t.Fatal("expected non-empty SVG output")
	}
	if !strings.Contains(string(html), "<svg") || !strings.Contains(string(html), "<polyline") {
		t.Errorf("expected an SVG polyline, got %q", html)
	}
}
