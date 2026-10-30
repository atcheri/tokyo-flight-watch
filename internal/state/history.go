// Package state persists price history between runs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// maxDays bounds how many daily stats are kept.
const maxDays = 90

// Combo is the history of one outbound/return date pair.
type Combo struct {
	LastChecked string `json:"last_checked,omitempty"` // YYYY-MM-DD
	// Cheapest price with at most max_stops, any airline.
	MinPrice  int `json:"min_price,omitempty"`
	LastPrice int `json:"last_price,omitempty"`
	// Cheapest nonstop Star Alliance price.
	DirectMinPrice  int `json:"direct_min_price,omitempty"`
	DirectLastPrice int `json:"direct_last_price,omitempty"`
	// LastDetailPrice is the calendar price when this combo was last searched in detail,
	// so an unchanged combo isn't searched again just to read Google's price level.
	LastDetailPrice int `json:"last_detail_price,omitempty"`
	// Alerted maps an offer signature to the price we last alerted for, to avoid repeat alerts.
	Alerted map[string]int `json:"alerted,omitempty"`
	// AlertedAny/AlertedDirect are the lowest prices alerted for this combo, so the calendar
	// scan skips a detail search when the price hasn't gone below them.
	AlertedAny    int `json:"alerted_any,omitempty"`
	AlertedDirect int `json:"alerted_direct,omitempty"`
}

// Best is a price and the date pair it was found for.
type Best struct {
	Key   string `json:"key,omitempty"`
	Price int    `json:"price,omitempty"`
}

// Day summarises one run, for trends and the weekly summary.
type Day struct {
	Date     string `json:"date"`
	BestAny  Best   `json:"best_any"`
	BestDir  Best   `json:"best_direct"`
	Requests int    `json:"requests"`
	Alerts   int    `json:"alerts"`
}

type History struct {
	Combos map[string]*Combo `json:"combos"`
	// LowestEver is the cheapest any-airline price ever seen across all date pairs.
	LowestEver  Best   `json:"lowest_ever"`
	Days        []Day  `json:"days,omitempty"`
	LastSummary string `json:"last_summary,omitempty"`
}

func Load(path string) (*History, error) {
	h := &History{Combos: map[string]*Combo{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	if err := json.Unmarshal(data, h); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}
	if h.Combos == nil {
		h.Combos = map[string]*Combo{}
	}
	return h, nil
}

func (h *History) Save(path string) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Get returns the combo entry for key, creating it if needed.
func (h *History) Get(key string) *Combo {
	c, ok := h.Combos[key]
	if !ok {
		c = &Combo{}
		h.Combos[key] = c
	}
	return c
}

// Peek returns the combo entry for key, or an empty one without storing it.
func (h *History) Peek(key string) Combo {
	if c, ok := h.Combos[key]; ok {
		return *c
	}
	return Combo{}
}

// AddDay records today's stats, replacing an earlier run of the same day.
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

// DayOn returns the stats recorded on date, if any.
func (h *History) DayOn(date string) (Day, bool) {
	for _, d := range h.Days {
		if d.Date == date {
			return d, true
		}
	}
	return Day{}, false
}
