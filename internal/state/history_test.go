package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigratesLegacyAlerts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	legacy := `{"combos":{"x":{"min_price":1}},"alerted_best":{"any:HND":1018,"any:NRT":756,"direct:HND":1086},
		"days":[{"date":"2026-10-30","best_any":{"key":"k","price":756},"requests":8}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.Alerts["any"].Price != 756 || h.Alerts["direct"].Price != 1086 || len(h.Alerts) != 2 {
		t.Fatalf("alerts = %+v", h.Alerts)
	}
	if len(h.Days) != 1 || h.Days[0].BestAny.Price != 756 {
		t.Fatalf("days = %+v", h.Days)
	}
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if s := string(data); contains(s, "alerted_best") || contains(s, "combos") {
		t.Fatalf("legacy fields kept after save:\n%s", s)
	}
}

func TestLoadMissingFile(t *testing.T) {
	h, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || h.Alerts == nil {
		t.Fatalf("Load = %+v, %v", h, err)
	}
}

func TestAddDayMergesSameDate(t *testing.T) {
	h := &History{}
	h.AddDay(Day{Date: "2026-11-01", Requests: 6, Alerts: 1})
	h.AddDay(Day{Date: "2026-11-01", Requests: 6})
	h.AddDay(Day{Date: "2026-11-02", Requests: 6})
	if len(h.Days) != 2 || h.Days[0].Requests != 12 || h.Days[0].Alerts != 1 {
		t.Fatalf("days = %+v", h.Days)
	}
	if got := h.DaysSince("2026-11-02"); len(got) != 1 {
		t.Fatalf("DaysSince = %+v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
