package flightapi

import (
	"net/http"
	"testing"
)

func TestParsePriceRangeFormats(t *testing.T) {
	for name, body := range map[string]string{
		"serpapi":   `{"price_insights":{"typical_price_range":[950,1400]}}`,
		"searchapi": `{"price_insights":{"typical_price_range":{"low_price":950,"high_price":1400}}}`,
	} {
		r, err := Parse([]byte(body), http.StatusOK)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := r.PriceInsights.TypicalPriceRange; got != (PriceRange{950, 1400}) {
			t.Errorf("%s: range = %+v", name, got)
		}
	}
}
