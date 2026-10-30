package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRepoConfig(t *testing.T) {
	c, err := Load("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.WatchDirect || c.DirectAirlines != "STAR_ALLIANCE" || c.MaxRequestsPerRun != 8 || c.TypicalWindowDays != 14 {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := "origins: [FRA]\ndestinations: [HND]\ndeparture_from: 2027-03-22\ndeparture_to: 2027-04-15\nmin_stay_days: 7\nmax_stay_days: 17\n"

	c, err := Load(write(base + "sensitivity_percent: 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Adults != 1 || c.AlertExpiryRuns != 3 || c.MaxRequestsPerRun != 8 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if _, err := Load(write(base)); err == nil {
		t.Fatal("a config without any alert rule must be rejected")
	}
	if _, err := Load(write(base + "target_price: 900\ndeparture_to: 2027-03-01\n")); err == nil {
		t.Fatal("departure_to before departure_from must be rejected")
	}
}
