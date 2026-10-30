package schedule

import (
	"testing"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
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

func TestWindowsCoverEveryComboWithinSizeLimit(t *testing.T) {
	cfg := loadConfig(t)
	for _, today := range []time.Time{
		time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC),
	} {
		windows := Windows(cfg, today, 200)
		for _, w := range windows {
			if w.Size() > 200 {
				t.Errorf("window %v has %d pairs, want ≤ 200", w, w.Size())
			}
		}
		for _, c := range All(cfg, today) {
			covered := false
			for _, w := range windows {
				if !c.Outbound.Before(w.OutboundStart) && !c.Outbound.After(w.OutboundEnd) &&
					!c.Return.Before(w.ReturnStart) && !c.Return.After(w.ReturnEnd) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("%s: combo %s not covered", today.Format(dateLayout), c.Key())
			}
		}
	}
	// 25 departure days with a 10-day stay span fit in 3 grids of 10 outbound days (10 × 20 = 200).
	if got := len(Windows(cfg, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), 200)); got != 3 {
		t.Errorf("got %d windows, want 3", got)
	}
}

func TestParseComboAndWanted(t *testing.T) {
	cfg := loadConfig(t)
	today := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	c, err := ParseCombo("2027-03-26", "2027-04-08")
	if err != nil {
		t.Fatal(err)
	}
	if c.StayDays != 13 || c.Key() != "2027-03-26_2027-04-08" || !Wanted(cfg, c, today) {
		t.Fatalf("unexpected combo %+v (wanted=%v)", c, Wanted(cfg, c, today))
	}
	short, _ := ParseCombo("2027-03-26", "2027-03-30")
	early, _ := ParseCombo("2027-03-20", "2027-04-01")
	if Wanted(cfg, short, today) || Wanted(cfg, early, today) {
		t.Fatal("out-of-range combos must not be wanted")
	}
}

func TestRelativeWindow(t *testing.T) {
	cfg, err := config.Parse([]byte("origins: [FRA]\ndestinations: [HND]\ndeparture_in_days_min: 30\n" +
		"departure_in_days_max: 49\nmin_stay_days: 10\nmax_stay_days: 14\n"))
	if err != nil {
		t.Fatal(err)
	}
	today := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	combos := All(cfg, today)
	if len(combos) != 20*5 || combos[0].OutboundDate() != "2026-12-01" || combos[len(combos)-1].OutboundDate() != "2026-12-20" {
		t.Fatalf("got %d combos from %s to %s", len(combos), combos[0].OutboundDate(), combos[len(combos)-1].OutboundDate())
	}
	// 20 departure days with a 4-day stay span: n × (n + 4) ≤ 200 gives n = 12, so 2 windows.
	if got := len(Windows(cfg, today, 200)); got != 2 {
		t.Fatalf("got %d windows, want 2", got)
	}
	// The window moves with the run date.
	later := today.AddDate(0, 0, 10)
	if got := All(cfg, later)[0].OutboundDate(); got != "2026-12-11" {
		t.Fatalf("first departure on %s = %s, want 2026-12-11", later.Format(dateLayout), got)
	}
}
