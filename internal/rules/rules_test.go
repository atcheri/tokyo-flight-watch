package rules

import (
	"net/http"
	"os"
	"reflect"
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
		NewLowMinPercent:           2,
		NaritaMinSaving:            100,
		MaxDetailSearches:          2,
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
		name       string
		prev       state.Combo
		lowestEver int
		googleLow  bool
		wantDeals  map[string]int
	}{
		{
			name:      "fresh combo",
			wantDeals: map[string]int{"FRA-HND-0-LH": 1250, "CGN-NRT-1-EK": 850},
		},
		{
			name:      "already alerted at same price is not repeated",
			prev:      state.Combo{Alerted: map[string]int{"FRA-HND-0-LH": 1250, "CGN-NRT-1-EK": 900}},
			wantDeals: map[string]int{"CGN-NRT-1-EK": 850}, // cheaper than last alert
		},
		{
			name:      "price drop on the same dates",
			prev:      state.Combo{MinPrice: 1300},
			wantDeals: map[string]int{"FRA-HND-0-LH": 1250, "DUS-HND-1-TK": 1100, "CGN-NRT-1-EK": 850},
		},
		{
			name:       "new overall low",
			lowestEver: 1130, // 1100 is 2.6 % below
			wantDeals:  map[string]int{"FRA-HND-0-LH": 1250, "DUS-HND-1-TK": 1100, "CGN-NRT-1-EK": 850},
		},
		{
			name:       "not a new low when less than 2 % below",
			lowestEver: 1110,
			wantDeals:  map[string]int{"FRA-HND-0-LH": 1250, "CGN-NRT-1-EK": 850},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Evaluate(testConfig(), loadResponse(t), tt.prev, tt.lowestEver)
			if got := dealSigs(res); !reflect.DeepEqual(got, tt.wantDeals) {
				t.Fatalf("deals = %v, want %v", got, tt.wantDeals)
			}
			if res.MinPrice != 850 {
				t.Errorf("MinPrice = %d, want 850", res.MinPrice)
			}
		})
	}
}

func TestEvaluateGoogleLow(t *testing.T) {
	resp := &flightapi.Response{
		BestFlights: []flightapi.Itinerary{
			itinerary("DUS", "HND", 1000, "TK 1524", "TK 198"),
			itinerary("FRA", "HND", 1150, "LX 1071", "LX 160"),
		},
		PriceInsights: &flightapi.PriceInsights{LowestPrice: 1000, PriceLevel: "low"},
	}
	cfg := testConfig()
	if res := Evaluate(cfg, resp, state.Combo{}, 0); len(res.Deals) != 0 {
		t.Fatalf("google-low disabled: got deals %v", res.Deals)
	}
	cfg.AlertOnGoogleLow = true
	res := Evaluate(cfg, resp, state.Combo{}, 0)
	if got := dealSigs(res); !reflect.DeepEqual(got, map[string]int{"DUS-HND-1-TK": 1000}) {
		t.Fatalf("deals = %v, want only the cheapest offer", got)
	}
}

func TestNonStarAllianceDirectNeedsDefaultPrice(t *testing.T) {
	resp := &flightapi.Response{BestFlights: []flightapi.Itinerary{itinerary("FRA", "HND", 1000, "JL 408")}}
	if res := Evaluate(testConfig(), resp, state.Combo{}, 0); len(res.Deals) != 0 {
		t.Fatalf("got deals %v, want none", res.Deals)
	}
}

func TestCandidates(t *testing.T) {
	cfg := testConfig()
	cfg.AlertOnGoogleLow = true
	h := &state.History{
		Combos: map[string]*state.Combo{
			"a": {MinPrice: 1200},
			"b": {MinPrice: 1000, AlertedAny: 880},
			"c": {MinPrice: 1100},
		},
		LowestEver: state.Best{Key: "c", Price: 1100},
	}
	scan := Scan{
		Any: map[string]int{
			"a": 1000, // 16 % drop and new low (1000 ≤ 1100 × 98 %)
			"b": 880,  // ≤ 900 but already alerted at 880
			"c": 1090, // < 2 % below the lowest ever: nothing
			"d": 1095,
		},
		Direct: map[string]int{"d": 1290, "c": 1350},
	}
	got := Candidates(cfg, h, scan)
	var keys []string
	for _, c := range got {
		keys = append(keys, c.Key)
	}
	// "a" (1000) and "d" (direct 1290) have reasons, cheapest first. The overall cheapest pair,
	// "b", is added last for Google's price level since it has no detail check recorded yet.
	if want := []string{"a", "d", "b"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("candidates = %v, want %v (%+v)", keys, want, got)
	}
	if len(got[0].Reasons) != 2 || len(got[2].Reasons) != 0 || got[1].Price != 1290 {
		t.Fatalf("unexpected candidates %+v", got)
	}

	// Once "b" was checked in detail at 880, it isn't re-checked while the price is unchanged.
	h.Combos["b"].LastDetailPrice = 880
	if got := Candidates(cfg, h, scan); len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
}

func TestRecordScanAndRecord(t *testing.T) {
	h := &state.History{Combos: map[string]*state.Combo{"a": {MinPrice: 900}}, LowestEver: state.Best{Key: "a", Price: 900}}
	bestAny, bestDir := RecordScan(h, Scan{
		Any:    map[string]int{"a": 950, "b": 870},
		Direct: map[string]int{"b": 1280},
	}, "2026-10-30")
	if bestAny != (state.Best{Key: "b", Price: 870}) || bestDir != (state.Best{Key: "b", Price: 1280}) {
		t.Fatalf("best = %+v / %+v", bestAny, bestDir)
	}
	if a := h.Combos["a"]; a.MinPrice != 900 || a.LastPrice != 950 || a.LastChecked != "2026-10-30" {
		t.Fatalf("combo a = %+v", a)
	}
	if h.LowestEver.Price != 870 || h.Combos["b"].DirectMinPrice != 1280 {
		t.Fatalf("history = %+v, b = %+v", h.LowestEver, h.Combos["b"])
	}

	c := &state.Combo{}
	Record(c, Evaluate(testConfig(), loadResponse(t), state.Combo{}, 0), 860)
	if c.LastDetailPrice != 860 || c.AlertedDirect != 1250 || c.AlertedAny != 850 || c.Alerted["FRA-HND-0-LH"] != 1250 {
		t.Fatalf("unexpected combo state %+v", c)
	}
}

func itinerary(from, to string, price int, flightNumbers ...string) flightapi.Itinerary {
	it := flightapi.Itinerary{Price: price}
	for i, fn := range flightNumbers {
		seg := flightapi.Segment{FlightNumber: fn}
		if i == 0 {
			seg.DepartureAirport.ID = from
		}
		if i == len(flightNumbers)-1 {
			seg.ArrivalAirport.ID = to
		}
		it.Flights = append(it.Flights, seg)
	}
	return it
}
