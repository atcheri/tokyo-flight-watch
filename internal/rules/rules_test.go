package rules

import (
	"net/http"
	"os"
	"testing"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

func testConfig() *config.Config {
	return &config.Config{
		PreferredDestination:       "HND",
		MaxStops:                   1,
		DirectStarAllianceMaxPrice: 1300,
		DefaultMaxPrice:            900,
		PriceDropPercent:           15,
		NaritaMinSaving:            100,
	}
}

func loadResponse(t *testing.T) *flightapi.Response {
	t.Helper()
	body, err := os.ReadFile("testdata/response.json")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := flightapi.Parse(body, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func dealSigs(res Result) map[string]int {
	m := map[string]int{}
	for _, d := range res.Deals {
		m[d.Signature()] = d.Price
	}
	return m
}

func TestEvaluate(t *testing.T) {
	// Fixture offers (HND best = 1100 via TK; the 2-stop CA offer at 600 is excluded):
	//   FRA-HND direct LH 1250   -> direct Star Alliance under 1300: alert
	//   DUS-HND 1 stop TK 1100   -> over 900, not direct: no alert
	//   FRA-NRT direct JL 1060   -> only 40 € cheaper than HND: ignored
	//   CGN-NRT 1 stop EK 850    -> 250 € cheaper than HND and under 900: alert
	tests := []struct {
		name      string
		prev      *state.Combo
		wantDeals map[string]int
		wantMin   int
	}{
		{
			name: "fresh combo",
			wantDeals: map[string]int{
				"FRA-HND-0-LH": 1250,
				"CGN-NRT-1-EK": 850,
			},
			wantMin: 850,
		},
		{
			name: "already alerted at same price is not repeated",
			prev: &state.Combo{Alerted: map[string]int{"FRA-HND-0-LH": 1250, "CGN-NRT-1-EK": 900}},
			wantDeals: map[string]int{
				"CGN-NRT-1-EK": 850, // cheaper than last alert
			},
			wantMin: 850,
		},
		{
			name: "price drop triggers alert on otherwise uninteresting offer",
			prev: &state.Combo{MinPrice: 1300},
			wantDeals: map[string]int{
				"FRA-HND-0-LH": 1250,
				"DUS-HND-1-TK": 1100, // 15 % below 1300
				"CGN-NRT-1-EK": 850,
			},
			wantMin: 850,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Evaluate(testConfig(), loadResponse(t), tt.prev)
			got := dealSigs(res)
			if len(got) != len(tt.wantDeals) {
				t.Fatalf("deals = %v, want %v", got, tt.wantDeals)
			}
			for sig, price := range tt.wantDeals {
				if got[sig] != price {
					t.Errorf("deal %s = %d, want %d (all: %v)", sig, got[sig], price, got)
				}
			}
			if res.MinPrice != tt.wantMin {
				t.Errorf("MinPrice = %d, want %d", res.MinPrice, tt.wantMin)
			}
			if !res.Worthy {
				t.Error("Worthy = false, want true")
			}
		})
	}
}

func TestNonStarAllianceDirectNeedsDefaultPrice(t *testing.T) {
	resp := &flightapi.Response{BestFlights: []flightapi.Itinerary{{
		Price: 1000,
		Flights: []flightapi.Segment{{
			DepartureAirport: flightapi.Airport{ID: "FRA"},
			ArrivalAirport:   flightapi.Airport{ID: "HND"},
			FlightNumber:     "JL 408",
		}},
	}}}
	if res := Evaluate(testConfig(), resp, nil); len(res.Deals) != 0 || res.Worthy {
		t.Fatalf("got deals %v, want none", res.Deals)
	}
}

func TestRecord(t *testing.T) {
	c := &state.Combo{MinPrice: 800}
	res := Evaluate(testConfig(), loadResponse(t), nil)
	Record(c, res, "2026-10-27")
	if c.MinPrice != 800 || c.LastPrice != 850 || c.LastChecked != "2026-10-27" || !c.Hot {
		t.Fatalf("unexpected combo state %+v", c)
	}
	if c.Alerted["FRA-HND-0-LH"] != 1250 {
		t.Fatalf("alert not recorded: %v", c.Alerted)
	}
}
