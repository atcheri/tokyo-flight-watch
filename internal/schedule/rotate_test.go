package schedule

import (
	"testing"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAll(t *testing.T) {
	cfg := loadConfig(t)
	today := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	// 25 departure days (22 Mar - 15 Apr) x 11 stays (7-17)
	if got := len(All(cfg, today)); got != 25*11 {
		t.Fatalf("len(All) = %d, want %d", got, 25*11)
	}
	// Departures on or before today are dropped.
	if got := len(All(cfg, time.Date(2027, 4, 14, 0, 0, 0, 0, time.UTC))); got != 11 {
		t.Fatalf("len(All) near end = %d, want 11", got)
	}
}

func TestPickPrefersUncheckedCoreThenStale(t *testing.T) {
	cfg := loadConfig(t)
	today := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	combos := All(cfg, today)
	h := &state.History{Combos: map[string]*state.Combo{}}

	first := Pick(cfg, h, combos, today, 8)
	for _, c := range first {
		if !cfg.IsCoreStay(c.StayDays) {
			t.Fatalf("first pick should be core stays, got %d days", c.StayDays)
		}
		h.Get(c.Key()).LastChecked = "2026-10-27"
	}

	// After every combo has been checked once, a 5-day-old core combo (5x2=10) beats
	// an 8-day-old edge combo (8x1=8), and a 4-day-old hot combo (4x3=12) beats both.
	for _, c := range combos {
		h.Get(c.Key()).LastChecked = "2026-10-26"
	}
	core := combos[3] // 10 days
	edge := combos[0] // 7 days
	hot := combos[15] // 2nd departure day, 11 days
	h.Get(core.Key()).LastChecked = "2026-10-22"
	h.Get(edge.Key()).LastChecked = "2026-10-19"
	h.Get(hot.Key()).LastChecked = "2026-10-23"
	h.Get(hot.Key()).Hot = true

	got := Pick(cfg, h, combos, today, 3)
	want := []string{hot.Key(), core.Key(), edge.Key()}
	for i := range want {
		if got[i].Key() != want[i] {
			t.Fatalf("pick[%d] = %s, want %s", i, got[i].Key(), want[i])
		}
	}
}
