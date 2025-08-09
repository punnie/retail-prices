package scraper

import (
	"testing"
)

func TestParsePrice(t *testing.T) {
	base := NewBaseScraper("Test", "https://example.com", "test-agent")
	
	tests := []struct {
		input    string
		expected float64
		hasError bool
	}{
		{"€1,99", 1.99, false},
		{"1,50 €", 1.50, false},
		{"€ 2.99", 2.99, false},
		{"3,45EUR", 3.45, false},
		{"Price: €10,00", 10.00, false},
		{"€0,99 each", 0.99, false},
		{"no price here", 0, true},
		{"", 0, true},
	}
	
	for _, test := range tests {
		result, err := base.ParsePrice(test.input)
		
		if test.hasError {
			if err == nil {
				t.Errorf("Expected error for input '%s', but got none", test.input)
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error for input '%s': %v", test.input, err)
			}
			if result != test.expected {
				t.Errorf("For input '%s', expected %.2f but got %.2f", test.input, test.expected, result)
			}
		}
	}
}

func TestCleanText(t *testing.T) {
	base := NewBaseScraper("Test", "https://example.com", "test-agent")
	
	tests := []struct {
		input    string
		expected string
	}{
		{"  Hello World  ", "Hello World"},
		{"Multiple   spaces   here", "Multiple spaces here"},
		{"\n\tTabbed\n\tText\t", "Tabbed Text"},
		{"", ""},
		{"Normal text", "Normal text"},
	}
	
	for _, test := range tests {
		result := base.CleanText(test.input)
		if result != test.expected {
			t.Errorf("For input '%s', expected '%s' but got '%s'", test.input, test.expected, result)
		}
	}
}
