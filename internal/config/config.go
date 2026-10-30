// Package config describes one flight watch: route, dates, alert rules and request budget.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const dateLayout = "2006-01-02"

type Config struct {
	Name                  string   `yaml:"name"`
	Origins               []string `yaml:"origins"`
	Destinations          []string `yaml:"destinations"`
	PreferredDestination  string   `yaml:"preferred_destination"`
	OtherAirportMinSaving int      `yaml:"other_airport_min_saving"` // other airports only if at least this much cheaper

	// Departure window: either fixed dates, or relative to the day of the run.
	DepartureFrom      string `yaml:"departure_from,omitempty"`
	DepartureTo        string `yaml:"departure_to,omitempty"`
	DepartureInDaysMin int    `yaml:"departure_in_days_min,omitempty"`
	DepartureInDaysMax int    `yaml:"departure_in_days_max,omitempty"`
	MinStayDays        int    `yaml:"min_stay_days"`
	MaxStayDays        int    `yaml:"max_stay_days"`

	Adults   int `yaml:"adults"`
	MaxStops int `yaml:"max_stops"`
	// WatchDirect adds a scan restricted to nonstop flights of DirectAirlines on both legs.
	WatchDirect    bool   `yaml:"watch_direct"`
	DirectAirlines string `yaml:"direct_airlines"` // STAR_ALLIANCE, ONEWORLD, SKYTEAM or airline codes

	// An alert fires when the cheapest price of a category is under its target,
	// or at least SensitivityPercent below the usual best price.
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

	departureFrom time.Time // parsed fixed window
	departureTo   time.Time
}

// Default returns a configuration with the default rule settings and no route.
func Default() Config {
	return Config{
		Name:                  "Vols",
		OtherAirportMinSaving: 100,
		Adults:                1,
		MaxStops:              1,
		DirectAirlines:        "STAR_ALLIANCE",
		SensitivityPercent:    10,
		TypicalWindowDays:     14,
		MinImprovementEUR:     30,
		MinImprovementPercent: 3,
		AlertExpiryRuns:       3,
		MaxRequestsPerRun:     8,
		MinCreditsWarning:     30,
		SummaryEveryDays:      7,
	}
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse reads a YAML configuration over the defaults and validates it.
func Parse(data []byte) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &c, nil
}

// Marshal returns the configuration as YAML.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Marshal(c)
}

func (c *Config) Validate() error {
	fixed := c.DepartureFrom != "" || c.DepartureTo != ""
	relative := c.DepartureInDaysMin > 0 || c.DepartureInDaysMax > 0
	switch {
	case fixed && relative:
		return fmt.Errorf("use either departure_from/departure_to or departure_in_days_min/max, not both")
	case fixed:
		var err error
		if c.departureFrom, err = time.Parse(dateLayout, c.DepartureFrom); err != nil {
			return fmt.Errorf("departure_from: %w", err)
		}
		if c.departureTo, err = time.Parse(dateLayout, c.DepartureTo); err != nil {
			return fmt.Errorf("departure_to: %w", err)
		}
		if c.departureTo.Before(c.departureFrom) {
			return fmt.Errorf("departure_to is before departure_from")
		}
	case relative:
		if c.DepartureInDaysMin <= 0 || c.DepartureInDaysMax < c.DepartureInDaysMin {
			return fmt.Errorf("invalid relative departure window %d-%d days", c.DepartureInDaysMin, c.DepartureInDaysMax)
		}
	default:
		return fmt.Errorf("a departure window is required")
	}
	switch {
	case len(c.Origins) == 0 || len(c.Destinations) == 0:
		return fmt.Errorf("origins and destinations are required")
	case c.MinStayDays <= 0 || c.MaxStayDays < c.MinStayDays:
		return fmt.Errorf("invalid stay range %d-%d", c.MinStayDays, c.MaxStayDays)
	case c.Adults <= 0:
		return fmt.Errorf("adults must be positive")
	case c.MaxStops < 0 || c.MaxStops > 2:
		return fmt.Errorf("max_stops must be 0, 1 or 2")
	case c.TargetPrice <= 0 && c.TargetPriceDirect <= 0 && c.SensitivityPercent <= 0:
		return fmt.Errorf("set at least one of target_price, target_price_direct or sensitivity_percent")
	case c.TypicalWindowDays <= 0 || c.AlertExpiryRuns <= 0:
		return fmt.Errorf("typical_window_days and alert_expiry_runs must be positive")
	case c.MaxRequestsPerRun <= 0:
		return fmt.Errorf("max_requests_per_run must be positive")
	}
	if c.PreferredDestination == "" {
		c.PreferredDestination = c.Destinations[0]
	}
	return nil
}

// Window returns the departure window for a run on today.
func (c *Config) Window(today time.Time) (from, to time.Time) {
	if c.DepartureFrom != "" {
		return c.departureFrom, c.departureTo
	}
	return today.AddDate(0, 0, c.DepartureInDaysMin), today.AddDate(0, 0, c.DepartureInDaysMax)
}

// Expired reports whether the departure window is entirely in the past.
func (c *Config) Expired(today time.Time) bool {
	_, to := c.Window(today)
	return !to.After(today)
}

var alliances = map[string]string{"STAR_ALLIANCE": "Star Alliance", "ONEWORLD": "oneworld", "SKYTEAM": "SkyTeam"}

// DirectLabel names the direct-flight category, e.g. "Vol direct Star Alliance".
func (c *Config) DirectLabel() string {
	if name, ok := alliances[strings.ToUpper(c.DirectAirlines)]; ok {
		return "Vol direct " + name
	}
	if c.DirectAirlines != "" {
		return "Vol direct " + c.DirectAirlines
	}
	return "Vol direct"
}

// Describe summarises the watch in one line, e.g. "FRA/CGN → HND/NRT · départ 22.03–15.04.2027 · 7–17 j".
func (c *Config) Describe() string {
	var window string
	if c.DepartureFrom != "" {
		window = fmt.Sprintf("départ %s–%s", c.departureFrom.Format("02.01.2006"), c.departureTo.Format("02.01.2006"))
	} else {
		window = fmt.Sprintf("départ dans %d–%d jours", c.DepartureInDaysMin, c.DepartureInDaysMax)
	}
	return fmt.Sprintf("%s → %s · %s · %d–%d j · ≤ %d escale(s)",
		strings.Join(c.Origins, "/"), strings.Join(c.Destinations, "/"), window, c.MinStayDays, c.MaxStayDays, c.MaxStops)
}
