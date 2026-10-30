// Package config loads and validates the watcher configuration.
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
	Provider string `yaml:"provider"` // "searchapi" (default) or "serpapi"

	Origins              []string `yaml:"origins"`
	Destinations         []string `yaml:"destinations"`
	PreferredDestination string   `yaml:"preferred_destination"`

	DepartureFrom string `yaml:"departure_from"`
	DepartureTo   string `yaml:"departure_to"`
	MinStayDays   int    `yaml:"min_stay_days"`
	MaxStayDays   int    `yaml:"max_stay_days"`

	Adults   int `yaml:"adults"`
	MaxStops int `yaml:"max_stops"`

	DirectStarAllianceMaxPrice int  `yaml:"direct_star_alliance_max_price"`
	DefaultMaxPrice            int  `yaml:"default_max_price"`
	PriceDropPercent           int  `yaml:"price_drop_percent"`
	NewLowMinPercent           int  `yaml:"new_low_min_percent"`
	AlertOnGoogleLow           bool `yaml:"alert_on_google_low"`
	NaritaMinSaving            int  `yaml:"narita_min_saving"`

	MaxDetailSearches int    `yaml:"max_detail_searches"`
	SummaryWeekday    string `yaml:"summary_weekday"` // e.g. "monday"; empty disables the weekly summary

	departureFrom time.Time
	departureTo   time.Time
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	if c.Provider == "" {
		c.Provider = "searchapi"
	}
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
	case c.MaxDetailSearches < 0:
		return fmt.Errorf("max_detail_searches must not be negative")
	}
	if c.SummaryWeekday != "" {
		if _, ok := weekdays[strings.ToLower(c.SummaryWeekday)]; !ok {
			return fmt.Errorf("summary_weekday %q is not an English weekday name", c.SummaryWeekday)
		}
	}
	return nil
}

var weekdays = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

// IsSummaryDay reports whether the weekly summary is due on day.
func (c *Config) IsSummaryDay(day time.Time) bool {
	wd, ok := weekdays[strings.ToLower(c.SummaryWeekday)]
	return ok && day.Weekday() == wd
}

func (c *Config) DepartureFromDate() time.Time { return c.departureFrom }
func (c *Config) DepartureToDate() time.Time   { return c.departureTo }
