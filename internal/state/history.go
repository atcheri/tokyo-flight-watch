// Package state persists price statistics and alert memory between runs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// maxDays bounds how many daily stats are kept.
const maxDays = 90

// Best is a price and the date pair ("2027-04-07_2027-04-16") it was found for.
type Best struct {
	Key   string `json:"key,omitempty"`
	Price int    `json:"price,omitempty"`
}

// Day summarises one day of scans.
type Day struct {
	Date      string `json:"date"`
	BestAny   Best   `json:"best_any"`
	BestDir   Best   `json:"best_direct"`
	MedianAny int    `json:"median_any,omitempty"` // median price over all date pairs
	MedianDir int    `json:"median_direct,omitempty"`
	Requests  int    `json:"requests"`
	Alerts    int    `json:"alerts"`
}

// Alert remembers the last alert of a category, to only alert again for a clearly better price.
type Alert struct {
	Best
	Date string `json:"date,omitempty"`
	// GoneRuns counts consecutive runs where the category's best price was above this alert.
	GoneRuns int `json:"gone_runs,omitempty"`
}

type History struct {
	Days        []Day             `json:"days,omitempty"`
	Alerts      map[string]*Alert `json:"alerts,omitempty"` // by category: "any", "direct"
	LastSummary string            `json:"last_summary,omitempty"`
	LastWarning string            `json:"last_credit_warning,omitempty"`

	// LegacyAlertedBest is read from older history files and migrated into Alerts.
	LegacyAlertedBest map[string]int `json:"alerted_best,omitempty"`
}

func Load(path string) (*History, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Decode(nil)
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return Decode(data)
}

// Decode parses a history saved as JSON; empty data gives an empty history.
func Decode(data []byte) (*History, error) {
	h := &History{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, h); err != nil {
			return nil, fmt.Errorf("parse history: %w", err)
		}
	}
	if h.Alerts == nil {
		h.Alerts = map[string]*Alert{}
	}
	h.migrate()
	return h, nil
}

// Encode returns the history as compact JSON.
func (h *History) Encode() ([]byte, error) {
	return json.Marshal(h)
}

// migrate turns the old per-airport alert floors ("any:NRT": 756) into per-category alerts.
func (h *History) migrate() {
	for cat, price := range h.LegacyAlertedBest {
		kind, _, _ := strings.Cut(cat, ":")
		if a, ok := h.Alerts[kind]; !ok || price < a.Price {
			h.Alerts[kind] = &Alert{Best: Best{Price: price}}
		}
	}
	h.LegacyAlertedBest = nil
}

func (h *History) Save(path string) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// AddDay records today's stats, merging with an earlier run of the same day.
func (h *History) AddDay(d Day) {
	if n := len(h.Days); n > 0 && h.Days[n-1].Date == d.Date {
		d.Requests += h.Days[n-1].Requests
		d.Alerts += h.Days[n-1].Alerts
		h.Days = h.Days[:n-1]
	}
	h.Days = append(h.Days, d)
	if len(h.Days) > maxDays {
		h.Days = h.Days[len(h.Days)-maxDays:]
	}
}

// DaysSince returns the stats of the days on or after date (YYYY-MM-DD).
func (h *History) DaysSince(date string) []Day {
	var days []Day
	for _, d := range h.Days {
		if d.Date >= date {
			days = append(days, d)
		}
	}
	return days
}
