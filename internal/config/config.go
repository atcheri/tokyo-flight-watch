// Package config loads and validates the watcher configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const dateLayout = "2006-01-02"

type Config struct {
	Origins              []string `yaml:"origins"`
	Destinations         []string `yaml:"destinations"`
	PreferredDestination string   `yaml:"preferred_destination"`
	NaritaMinSaving      int      `yaml:"narita_min_saving"` // other airports only if at least this much cheaper

	DepartureFrom string `yaml:"departure_from"`
	DepartureTo   string `yaml:"departure_to"`
	MinStayDays   int    `yaml:"min_stay_days"`
	MaxStayDays   int    `yaml:"max_stay_days"`

	Adults   int `yaml:"adults"`
	MaxStops int `yaml:"max_stops"`
	// WatchDirect adds a scan restricted to nonstop flights of DirectAirlines on both legs.
	WatchDirect    bool   `yaml:"watch_direct"`
	DirectAirlines string `yaml:"direct_airlines"`

	// An alert fires when the cheapest price of a category is under its target,
	// or at least SensitivityPercent below the usual price.
	TargetPrice        int `yaml:"target_price"`
	TargetPriceDirect  int `yaml:"target_price_direct"`
	SensitivityPercent int `yaml:"sensitivity_percent"`
	TypicalWindowDays  int `yaml:"typical_window_days"`
	// A new alert in the same category must beat the previous one by this much.
	MinImprovementEUR     int `yaml:"min_improvement_eur"`
	MinImprovementPercent int `yaml:"min_improvement_percent"`
	// The previous alert is forgotten once its fare has been gone this many runs.
	AlertExpiryRuns int `yaml:"alert_expiry_runs"`

	MaxRequestsPerRun int `yaml:"max_requests_per_run"`
	MinCreditsWarning int `yaml:"min_credits_warning"`
	SummaryEveryDays  int `yaml:"summary_every_days"` // 0 disables the summary

	departureFrom time.Time
	departureTo   time.Time
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := Config{
		Adults:            1,
		DirectAirlines:    "STAR_ALLIANCE",
		TypicalWindowDays: 14,
		AlertExpiryRuns:   3,
		MaxRequestsPerRun: 8,
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	var err error
	if c.departureFrom, err = time.Parse(dateLayout, c.DepartureFrom); err != nil {
		return fmt.Errorf("departure_from: %w", err)
	}
	if c.departureTo, err = time.Parse(dateLayout, c.DepartureTo); err != nil {
		return fmt.Errorf("departure_to: %w", err)
	}
	switch {
	case len(c.Origins) == 0 || len(c.Destinations) == 0:
		return fmt.Errorf("origins and destinations are required")
	case c.departureTo.Before(c.departureFrom):
		return fmt.Errorf("departure_to is before departure_from")
	case c.MinStayDays <= 0 || c.MaxStayDays < c.MinStayDays:
		return fmt.Errorf("invalid stay range %d-%d", c.MinStayDays, c.MaxStayDays)
	case c.Adults <= 0:
		return fmt.Errorf("adults must be positive")
	case c.TargetPrice <= 0 && c.TargetPriceDirect <= 0 && c.SensitivityPercent <= 0:
		return fmt.Errorf("set at least one of target_price, target_price_direct or sensitivity_percent")
	case c.TypicalWindowDays <= 0 || c.AlertExpiryRuns <= 0:
		return fmt.Errorf("typical_window_days and alert_expiry_runs must be positive")
	case c.MaxRequestsPerRun <= 0:
		return fmt.Errorf("max_requests_per_run must be positive")
	}
	return nil
}

func (c *Config) DepartureFromDate() time.Time { return c.departureFrom }
func (c *Config) DepartureToDate() time.Time   { return c.departureTo }
