package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

var today = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// fakeAPI imitates SearchApi: calendar prices grow with the outbound day, direct flights
// cost more, and detailed searches return fixed offers.
type fakeAPI struct {
	mu           sync.Mutex
	credits      int
	failAll      bool
	failDirect   bool
	searches     []map[string]string
	unauthorized int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-key" || r.URL.Query().Has("api_key") {
		f.unauthorized++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"Invalid API key."}`))
		return
	}
	if r.URL.Path == "/me" {
		json.NewEncoder(w).Encode(map[string]any{"account": map[string]int{"remaining_credits": f.credits}})
		return
	}
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	f.searches = append(f.searches, q)
	f.credits--
	direct := q["stops"] == "nonstop"
	if f.failAll || (direct && f.failDirect) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
		return
	}

	switch q["engine"] {
	case "google_flights_calendar":
		var cal []map[string]any
		out, _ := time.Parse("2006-01-02", q["outbound_date"])
		outEnd, _ := time.Parse("2006-01-02", q["outbound_date_end"])
		ret, _ := time.Parse("2006-01-02", q["return_date"])
		retEnd, _ := time.Parse("2006-01-02", q["return_date_end"])
		for d, i := out, 0; !d.After(outEnd); d, i = d.AddDate(0, 0, 1), i+1 {
			for r := ret; !r.After(retEnd); r = r.AddDate(0, 0, 1) {
				price := 900 + 10*i
				if direct {
					price = 1250 + 10*i
				}
				cal = append(cal, map[string]any{"departure": d.Format("2006-01-02"), "return": r.Format("2006-01-02"), "price": price})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"calendar": cal})
	case "google_flights":
		if direct {
			w.Write([]byte(`{"best_flights":[{"price":1240,"flights":[{"departure_airport":{"id":"FRA"},"arrival_airport":{"id":"HND"},"airline":"Lufthansa","flight_number":"LH 716"}]}],
				"price_insights":{"lowest_price":1240,"price_level":"low","typical_price_range":{"low_price":1300,"high_price":1600}}}`))
			return
		}
		w.Write([]byte(`{"best_flights":[
			{"price":880,"flights":[{"departure_airport":{"id":"FRA"},"arrival_airport":{"id":"PEK"},"airline":"Air China"},{"departure_airport":{"id":"PEK"},"arrival_airport":{"id":"NRT"},"airline":"Air China"}]},
			{"price":910,"flights":[{"departure_airport":{"id":"CGN"},"arrival_airport":{"id":"IST"},"airline":"Turkish Airlines"},{"departure_airport":{"id":"IST"},"arrival_airport":{"id":"HND"},"airline":"Turkish Airlines"}]}]}`))
	}
}

func setup(t *testing.T, credits int) (*config.Config, *fakeAPI, *flightapi.Client) {
	t.Helper()
	cfg, err := config.Parse([]byte(`name: Test Tokyo
origins: [FRA, CGN]
destinations: [HND, NRT]
departure_from: 2026-12-01
departure_to: 2026-12-05
min_stay_days: 7
max_stay_days: 8
watch_direct: true
target_price: 950
target_price_direct: 1300
summary_every_days: 7
`))
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{credits: credits}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	client := flightapi.NewClient("test-key")
	client.BaseURL = srv.URL
	return cfg, api, client
}

func TestRunAlertsThenStaysQuiet(t *testing.T) {
	cfg, api, client := setup(t, 100)
	h := &state.History{Alerts: map[string]*state.Alert{}}

	rep, err := Run(context.Background(), cfg, h, client, today, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if api.unauthorized > 0 {
		t.Fatalf("%d request(s) without the Bearer key", api.unauthorized)
	}
	// 1 calendar request per pass (5 days × 2 stays fits one grid) + 1 detail search per alert.
	if rep.Requests != 4 || rep.CreditsLeft != 96 || len(rep.Deals) != 2 {
		t.Fatalf("requests %d, credits %d, deals %+v", rep.Requests, rep.CreditsLeft, rep.Deals)
	}

	any, direct := rep.Deals[0], rep.Deals[1]
	// Any: NRT 880 is only 30 € cheaper than HND 910 (< 100 €), so Haneda is reported.
	if any.Price != 910 || any.Destination != "HND" || any.Stops != 1 || any.Title != "Meilleur prix (≤ 1 escale)" {
		t.Errorf("any deal = %+v", any)
	}
	if direct.Price != 1240 || direct.Stops != 0 || direct.Airlines != "Lufthansa" ||
		!strings.Contains(direct.Typical, "prix jugé bas") || !strings.Contains(direct.Title, "Star Alliance") {
		t.Errorf("direct deal = %+v", direct)
	}
	// The detail search for the direct alert keeps the calendar filters (both legs).
	var directDetail map[string]string
	for _, s := range api.searches {
		if s["engine"] == "google_flights" && s["stops"] == "nonstop" {
			directDetail = s
		}
	}
	if directDetail["included_airlines"] != "STAR_ALLIANCE" || directDetail["outbound_date"] != "2026-12-01" {
		t.Errorf("direct detail search = %v", directDetail)
	}
	if len(rep.Messages) != 1 || !strings.Contains(rep.Messages[0], "Test Tokyo : 2 offre(s) dès 910 €") {
		t.Errorf("messages = %q", rep.Messages)
	}
	if rep.Summary == "" || !strings.Contains(rep.Summary, "Résumé Test Tokyo") {
		t.Errorf("first run should send a summary, got %q", rep.Summary)
	}
	if h.Alerts["any"].Price != 900 || h.Alerts["direct"].Price != 1250 || len(h.Days) != 1 {
		t.Fatalf("history = %+v / %+v", h.Alerts, h.Days)
	}

	// Same prices the next day: no alert, no summary, only the calendar requests.
	rep, err = Run(context.Background(), cfg, h, client, today.AddDate(0, 0, 1), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Messages) != 0 || rep.Summary != "" || rep.Requests != 2 {
		t.Fatalf("second run: messages %q, summary %q, requests %d", rep.Messages, rep.Summary, rep.Requests)
	}
}

func TestRunPausesWithoutCredits(t *testing.T) {
	cfg, api, client := setup(t, 1)
	h := &state.History{Alerts: map[string]*state.Alert{}}
	rep, err := Run(context.Background(), cfg, h, client, today, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Paused || len(rep.Messages) != 1 || !strings.Contains(rep.Messages[0], "en pause") || len(api.searches) != 0 {
		t.Fatalf("report %+v, searches %d", rep, len(api.searches))
	}
	rep, _ = Run(context.Background(), cfg, h, client, today, Options{})
	if len(rep.Messages) != 0 {
		t.Fatal("the pause notice must be sent once a day")
	}
}

func TestRunReportsFailures(t *testing.T) {
	cfg, api, client := setup(t, 100)
	h := &state.History{Alerts: map[string]*state.Alert{}}

	api.failDirect = true
	rep, err := Run(context.Background(), cfg, h, client, today, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Messages[len(rep.Messages)-1], "1 requête(s) du calendrier sur 2") {
		t.Fatalf("warnings %q, messages %q", rep.Warnings, rep.Messages)
	}

	api.failAll = true
	if _, err := Run(context.Background(), cfg, h, client, today, Options{}); err == nil {
		t.Fatal("a run where every calendar request fails must return an error")
	}
}

func TestRunExpiredAndOverBudget(t *testing.T) {
	cfg, _, client := setup(t, 100)
	h := &state.History{Alerts: map[string]*state.Alert{}}
	rep, err := Run(context.Background(), cfg, h, client, time.Date(2026, 12, 6, 0, 0, 0, 0, time.UTC), Options{})
	if err != nil || !rep.Expired {
		t.Fatalf("expected an expired watch, got %+v, %v", rep, err)
	}

	cfg.MaxRequestsPerRun = 1
	if _, err := Run(context.Background(), cfg, h, client, today, Options{}); err == nil {
		t.Fatal("a grid costing more than max_requests_per_run must be refused")
	}
	if got := ScanCost(cfg, today); got != 2 {
		t.Fatalf("ScanCost = %d, want 2", got)
	}
}
