package bot

import (
	"reflect"
	"testing"
	"time"
)

var today = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func TestParseAirports(t *testing.T) {
	got, err := parseAirports("fra, CGN dus,FRA")
	if err != nil || !reflect.DeepEqual(got, []string{"FRA", "CGN", "DUS"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "Frankfurt", "FR", "AAA BBB CCC DDD EEE FFF"} {
		if _, err := parseAirports(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseWindow(t *testing.T) {
	tests := map[string]window{
		"22.03.2027-15.04.2027":      {From: "2027-03-22", To: "2027-04-15"},
		"2027-03-22 2027-04-15":      {From: "2027-03-22", To: "2027-04-15"},
		"du 1/12/2026 au 20/12/2026": {From: "2026-12-01", To: "2026-12-20"},
		"dans 60-150 jours":          {InMin: 60, InMax: 150},
		"in 30 to 90 days":           {InMin: 30, InMax: 90},
		"45-60":                      {InMin: 45, InMax: 60},
	}
	for in, want := range tests {
		got, err := parseWindow(in, today)
		if err != nil || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"22.03.2027", "15.04.2027-22.03.2027", "01.10.2026-05.11.2026", "01.12.2027-05.12.2027",
		"31.02.2027-05.03.2027", "dans 0-10 jours", "dans 300-400 jours", "bientôt"} {
		if _, err := parseWindow(bad, today); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseRange(t *testing.T) {
	for in, want := range map[string][2]int{"10-14": {10, 14}, "10 à 14": {10, 14}, "12±3": {9, 15}, "7": {7, 7}, "10-14 jours": {10, 14}} {
		lo, hi, err := parseRange(in)
		if err != nil || [2]int{lo, hi} != want {
			t.Errorf("%q = %d-%d, %v; want %v", in, lo, hi, err, want)
		}
	}
	for _, bad := range []string{"", "14-10", "dix", "0", "50-90"} {
		if _, _, err := parseRange(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseSmallAnswers(t *testing.T) {
	if n, err := parseStops("Direct"); err != nil || n != 0 {
		t.Errorf("stops direct = %d, %v", n, err)
	}
	if _, err := parseStops("3"); err == nil {
		t.Error("3 stops must be refused")
	}
	for in, want := range map[string]int{"950": 950, "1 300 €": 1300, "non": 0, "1.050": 1050} {
		if got, err := parsePrice(in); err != nil || got != want {
			t.Errorf("price %q = %d, %v", in, got, err)
		}
	}
	if _, err := parsePrice("cheap"); err == nil {
		t.Error("non-numeric price must be refused")
	}
	for in, want := range map[string]string{"Star": "STAR_ALLIANCE", "oneworld": "ONEWORLD", "non": ""} {
		if got, err := parseAlliance(in); err != nil || got != want {
			t.Errorf("alliance %q = %q, %v", in, got, err)
		}
	}
	if n, err := parseEvery("3"); err != nil || n != 3 {
		t.Errorf("every = %d, %v", n, err)
	}
	if _, err := parseEvery("10"); err == nil {
		t.Error("every 10 days must be refused")
	}
	if !isYes("Oui") || !isNo("NON") || isYes("peut-être") {
		t.Error("yes/no detection")
	}
}
