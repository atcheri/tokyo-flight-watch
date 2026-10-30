package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRepoConfig(t *testing.T) {
	c, err := Load("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.WatchDirect || c.DirectAirlines != "STAR_ALLIANCE" || c.MaxRequestsPerRun != 8 || c.Name != "Vols Tokyo" {
		t.Fatalf("unexpected config %+v", c)
	}
	if got := c.DirectLabel(); got != "Vol direct Star Alliance" {
		t.Errorf("DirectLabel = %q", got)
	}
	if got := c.Describe(); got != "FRA/CGN/DUS → HND/NRT · départ 22.03.2027–15.04.2027 · 7–17 j · ≤ 1 escale(s)" {
		t.Errorf("Describe = %q", got)
	}
}

const route = "origins: [FRA]\ndestinations: [HND, NRT]\nmin_stay_days: 7\nmax_stay_days: 17\n"

func TestParseDefaultsAndValidation(t *testing.T) {
	c, err := Parse([]byte(route + "departure_from: 2027-03-22\ndeparture_to: 2027-04-15\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Adults != 1 || c.SensitivityPercent != 10 || c.PreferredDestination != "HND" || c.MaxStops != 1 {
		t.Fatalf("defaults not applied: %+v", c)
	}

	for name, body := range map[string]string{
		"no alert rule":     route + "departure_from: 2027-03-22\ndeparture_to: 2027-04-15\nsensitivity_percent: 0\n",
		"window reversed":   route + "departure_from: 2027-04-22\ndeparture_to: 2027-04-15\n",
		"no window":         route,
		"both window kinds": route + "departure_from: 2027-03-22\ndeparture_to: 2027-04-15\ndeparture_in_days_min: 30\ndeparture_in_days_max: 60\n",
		"relative reversed": route + "departure_in_days_min: 60\ndeparture_in_days_max: 30\n",
		"too many stops":    route + "departure_in_days_min: 30\ndeparture_in_days_max: 60\nmax_stops: 3\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRelativeWindowAndRoundTrip(t *testing.T) {
	c, err := Parse([]byte(route + "name: Japon\ndeparture_in_days_min: 30\ndeparture_in_days_max: 60\n"))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	from, to := c.Window(today)
	if from.Format("2006-01-02") != "2026-12-01" || to.Format("2006-01-02") != "2026-12-31" || c.Expired(today) {
		t.Fatalf("window = %s..%s", from, to)
	}
	if !strings.Contains(c.Describe(), "départ dans 30–60 jours") {
		t.Errorf("Describe = %q", c.Describe())
	}

	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil || back.Name != "Japon" || back.DepartureInDaysMax != 60 || back.SensitivityPercent != 10 {
		t.Fatalf("round trip = %+v, %v", back, err)
	}

	fixed, _ := Parse([]byte(route + "departure_from: 2026-11-10\ndeparture_to: 2026-11-20\n"))
	if !fixed.Expired(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) || fixed.Expired(today) {
		t.Error("fixed window expiry is wrong")
	}
}
