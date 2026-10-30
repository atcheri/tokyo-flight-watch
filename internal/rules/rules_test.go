package rules

import (
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

var today = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

func testConfig() *config.Config {
	return &config.Config{
		PreferredDestination:  "HND",
		NaritaMinSaving:       100,
		MaxStops:              1,
		TargetPrice:           900,
		TargetPriceDirect:     1300,
		SensitivityPercent:    10,
		TypicalWindowDays:     14,
		MinImprovementEUR:     30,
		MinImprovementPercent: 3,
		AlertExpiryRuns:       3,
	}
}

// historyWithBests returns a history whose last days had these best prices (oldest first).
func historyWithBests(bestAny, bestDir []int) *state.History {
	h := &state.History{Alerts: map[string]*state.Alert{}}
	for i := range bestAny {
		date := today.AddDate(0, 0, i-len(bestAny)).Format(dateLayout)
		h.AddDay(state.Day{Date: date, BestAny: state.Best{Price: bestAny[i]}, BestDir: state.Best{Price: bestDir[i]}})
	}
	return h
}

func TestMedianAndUsualBest(t *testing.T) {
	if got := Median(map[string]int{"a": 3, "b": 1, "c": 2, "d": 10}); got != 2 {
		t.Errorf("Median = %d, want 2", got)
	}
	if got := UsualBest(historyWithBests([]int{1000, 1100}, []int{1, 1}), KindAny, today, 14); got != 0 {
		t.Errorf("UsualBest with 2 days = %d, want 0 (not enough history)", got)
	}
	h := historyWithBests([]int{1000, 1200, 1100, 9999}, []int{1300, 1250, 1280, 1290})
	h.AddDay(state.Day{Date: today.Format(dateLayout), BestAny: state.Best{Price: 1}}) // today is excluded
	if got := UsualBest(h, KindAny, today, 3); got != 1200 {
		t.Errorf("UsualBest(any, 3 days) = %d, want 1200", got)
	}
	if got := UsualBest(h, KindDirect, today, 14); got != 1285 {
		t.Errorf("UsualBest(direct) = %d, want 1285", got)
	}
}

func TestSignals(t *testing.T) {
	scan := Scan{
		KindAny:    {"2027-04-07_2027-04-16": 950, "2027-04-08_2027-04-18": 1200},
		KindDirect: {"2027-03-27_2027-04-07": 1086, "2027-03-28_2027-04-07": 1350},
	}
	tests := []struct {
		name   string
		h      *state.History
		cfg    func(*config.Config)
		want   map[string]int // kind -> price
		reason map[string]int // kind -> number of reasons
	}{
		{
			name:   "no history: only targets",
			h:      historyWithBests(nil, nil),
			want:   map[string]int{KindDirect: 1086}, // 950 is over the 900 target
			reason: map[string]int{KindDirect: 1},
		},
		{
			name:   "clearly below the usual best price",
			h:      historyWithBests([]int{1100, 1080, 1090}, []int{1250, 1240, 1260}),
			want:   map[string]int{KindAny: 950, KindDirect: 1086},
			reason: map[string]int{KindAny: 1, KindDirect: 2},
		},
		{
			name: "not enough below the usual best price",
			h:    historyWithBests([]int{1000, 990, 1010}, []int{1250, 1240, 1260}),
			cfg:  func(c *config.Config) { c.TargetPriceDirect = 0 },
			want: map[string]int{KindDirect: 1086}, // 1086 ≤ 1250 × 90 %; 950 > 1000 × 90 %
		},
		{
			name: "last alert must be beaten by max(30 €, 3 %)",
			h: func() *state.History {
				h := historyWithBests(nil, nil)
				h.Alerts[KindDirect] = &state.Alert{Best: state.Best{Price: 1110}} // margin 33 €
				return h
			}(),
			want: map[string]int{},
		},
		{
			name: "a clearly better price alerts again",
			h: func() *state.History {
				h := historyWithBests(nil, nil)
				h.Alerts[KindDirect] = &state.Alert{Best: state.Best{Price: 1120}}
				return h
			}(),
			want: map[string]int{KindDirect: 1086},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testConfig()
			if tt.cfg != nil {
				tt.cfg(c)
			}
			got := map[string]int{}
			for _, s := range Signals(c, tt.h, scan, today) {
				got[s.Kind] = s.Best.Price
				if n, ok := tt.reason[s.Kind]; ok && len(s.Reasons) != n {
					t.Errorf("%s reasons = %v, want %d", s.Kind, s.Reasons, n)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("signals = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExpireAlerts(t *testing.T) {
	cfg := testConfig()
	h := &state.History{Alerts: map[string]*state.Alert{
		KindAny:    {Best: state.Best{Price: 756}},
		KindDirect: {Best: state.Best{Price: 1086}, GoneRuns: 2},
	}}
	ExpireAlerts(cfg, h, Scan{KindAny: {"x": 800}, KindDirect: {"y": 1086}})
	if h.Alerts[KindAny].GoneRuns != 1 || h.Alerts[KindDirect].GoneRuns != 0 {
		t.Fatalf("gone runs: any %d, direct %d", h.Alerts[KindAny].GoneRuns, h.Alerts[KindDirect].GoneRuns)
	}
	ExpireAlerts(cfg, h, Scan{KindAny: {"x": 800}})                  // no direct data: unchanged
	ExpireAlerts(cfg, h, Scan{KindAny: {"x": 800}, KindDirect: nil}) // third run without the 756 fare
	if _, ok := h.Alerts[KindAny]; ok {
		t.Fatal("any alert should have expired after 3 runs")
	}
	if h.Alerts[KindDirect].GoneRuns != 0 {
		t.Fatal("direct alert must not change without data")
	}
}

func TestRecordAlert(t *testing.T) {
	h := &state.History{Alerts: map[string]*state.Alert{}}
	RecordAlert(h, Signal{Kind: KindAny, Best: state.Best{Key: "k", Price: 900}}, "2026-11-10")
	if a := h.Alerts[KindAny]; a.Price != 900 || a.Key != "k" || a.Date != "2026-11-10" {
		t.Fatalf("alert = %+v", a)
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

func TestPickOffer(t *testing.T) {
	// Fixture: FRA-HND direct LH 1250, DUS-HND TK 1100, FRA-NRT direct JL 1060,
	// CGN-NRT EK 850 (1 stop), FRA-HND CA 600 (2 stops, excluded).
	cfg := testConfig()
	resp := loadResponse(t)

	offer, pref, ok := PickOffer(cfg, resp, 1)
	if !ok || offer.Destination != "NRT" || offer.Price != 850 || pref == nil || pref.Price != 1100 {
		t.Fatalf("NRT 250 € cheaper should win: offer %+v, preferred %+v", offer, pref)
	}

	cfg.NaritaMinSaving = 300
	offer, pref, _ = PickOffer(cfg, resp, 1)
	if offer.Destination != "HND" || offer.Price != 1100 || pref != nil {
		t.Fatalf("HND should win when NRT saves < 300 €: offer %+v", offer)
	}

	offer, _, _ = PickOffer(cfg, resp, 0)
	if offer.Price != 1250 || offer.Stops != 0 || !reflect.DeepEqual(offer.AirlineNames, []string{"Lufthansa"}) {
		t.Fatalf("nonstop pick = %+v", offer)
	}

	if _, _, ok := PickOffer(cfg, &flightapi.Response{}, 1); ok {
		t.Fatal("empty response must not yield an offer")
	}
}
