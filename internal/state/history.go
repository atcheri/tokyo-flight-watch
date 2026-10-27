// Package state persists price history per date combination between runs.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Combo struct {
	LastChecked string `json:"last_checked,omitempty"` // YYYY-MM-DD
	MinPrice    int    `json:"min_price,omitempty"`
	LastPrice   int    `json:"last_price,omitempty"`
	// Hot is set when the last check found an alert-worthy offer, so the combo gets re-checked sooner.
	Hot bool `json:"hot,omitempty"`
	// Alerted maps an offer signature to the price we last alerted for, to avoid repeat e-mails.
	Alerted map[string]int `json:"alerted,omitempty"`
}

type History struct {
	Combos map[string]*Combo `json:"combos"`
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
