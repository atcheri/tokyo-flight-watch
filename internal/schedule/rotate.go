// Package schedule generates outbound/return date combinations and picks which to check today.
package schedule

import (
	"sort"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

const dateLayout = "2006-01-02"

type Combo struct {
	Outbound time.Time
	Return   time.Time
	StayDays int
}

func (c Combo) Key() string {
	return c.Outbound.Format(dateLayout) + "_" + c.Return.Format(dateLayout)
}

func (c Combo) OutboundDate() string { return c.Outbound.Format(dateLayout) }
func (c Combo) ReturnDate() string   { return c.Return.Format(dateLayout) }

// All returns every combination in the configured window whose outbound date is after today.
func All(cfg *config.Config, today time.Time) []Combo {
	var combos []Combo
	for d := cfg.DepartureFromDate(); !d.After(cfg.DepartureToDate()); d = d.AddDate(0, 0, 1) {
		if !d.After(today) {
			continue
		}
		for stay := cfg.MinStayDays; stay <= cfg.MaxStayDays; stay++ {
			combos = append(combos, Combo{Outbound: d, Return: d.AddDate(0, 0, stay), StayDays: stay})
		}
	}
	return combos
}

// neverChecked outranks any realistic staleness so unseen combos go first.
const neverChecked = 10_000

// Pick returns the n combos most due for a check.
// Due-ness is days since last check, weighted: edge stays ×1, core stays ×2, hot combos ×3.
func Pick(cfg *config.Config, h *state.History, combos []Combo, today time.Time, n int) []Combo {
	type scored struct {
		combo Combo
		score int
	}
	ranked := make([]scored, 0, len(combos))
	for _, c := range combos {
		ranked = append(ranked, scored{c, dueScore(cfg, h.Combos[c.Key()], c, today)})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].combo.Key() < ranked[j].combo.Key()
	})
	if n > len(ranked) {
		n = len(ranked)
	}
	picked := make([]Combo, n)
	for i := range picked {
		picked[i] = ranked[i].combo
	}
	return picked
}

func dueScore(cfg *config.Config, s *state.Combo, c Combo, today time.Time) int {
	weight := 1
	if cfg.IsCoreStay(c.StayDays) {
		weight = 2
	}
	if s == nil || s.LastChecked == "" {
		return neverChecked * weight
	}
	if s.Hot {
		weight = 3
	}
	last, err := time.Parse(dateLayout, s.LastChecked)
	if err != nil {
		return neverChecked * weight
	}
	return int(today.Sub(last).Hours()/24) * weight
}
