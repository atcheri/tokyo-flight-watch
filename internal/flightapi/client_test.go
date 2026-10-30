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

func TestParseCalendar(t *testing.T) {
	body := `{"calendar":[
		{"departure":"2027-03-22","return":"2027-04-01","price":1213},
		{"departure":"2027-03-22","return":"2027-04-02","has_no_flights":true},
		{"departure":"2027-03-23","return":"2027-04-02","price":1189,"is_lowest_price":true}
	]}`
	prices, err := ParseCalendar([]byte(body), http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 || prices[1].Price != 1189 || prices[1].Departure != "2027-03-23" {
		t.Fatalf("prices = %+v", prices)
	}
	if _, err := ParseCalendar([]byte(`{"error":"Invalid API key."}`), http.StatusUnauthorized); err == nil {
		t.Fatal("expected an error for an invalid key")
	}
}
