// Package schedule splits the travel window into date grids for the price calendar.
package schedule

import (
	"fmt"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
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

// ParseCombo builds a combo from "YYYY-MM-DD" outbound and return dates.
func ParseCombo(outbound, ret string) (Combo, error) {
	out, err := time.Parse(dateLayout, outbound)
	if err != nil {
		return Combo{}, fmt.Errorf("outbound date: %w", err)
	}
	back, err := time.Parse(dateLayout, ret)
	if err != nil {
		return Combo{}, fmt.Errorf("return date: %w", err)
	}
	return Combo{Outbound: out, Return: back, StayDays: int(back.Sub(out).Hours() / 24)}, nil
}

// Wanted reports whether the combo is inside the departure window of a run on today,
// after today, and within the stay range.
func Wanted(cfg *config.Config, c Combo, today time.Time) bool {
	from, to := cfg.Window(today)
	return !c.Outbound.Before(from) && !c.Outbound.After(to) && c.Outbound.After(today) &&
		c.StayDays >= cfg.MinStayDays && c.StayDays <= cfg.MaxStayDays
}

// All returns every wanted combination whose outbound date is after today.
func All(cfg *config.Config, today time.Time) []Combo {
	var combos []Combo
	_, last := cfg.Window(today)
	for d := firstDeparture(cfg, today); !d.After(last); d = d.AddDate(0, 0, 1) {
		for stay := cfg.MinStayDays; stay <= cfg.MaxStayDays; stay++ {
			combos = append(combos, Combo{Outbound: d, Return: d.AddDate(0, 0, stay), StayDays: stay})
		}
	}
	return combos
}

// Window is a rectangular outbound × return date grid for one calendar request.
type Window struct {
	OutboundStart, OutboundEnd time.Time
	ReturnStart, ReturnEnd     time.Time
}

// Size is the number of date pairs the window asks for.
func (w Window) Size() int {
	return days(w.OutboundStart, w.OutboundEnd) * days(w.ReturnStart, w.ReturnEnd)
}

func (w Window) Dates() (outStart, outEnd, retStart, retEnd string) {
	return w.OutboundStart.Format(dateLayout), w.OutboundEnd.Format(dateLayout),
		w.ReturnStart.Format(dateLayout), w.ReturnEnd.Format(dateLayout)
}

// Windows covers every wanted combination with as few grids of at most maxSize pairs as possible.
// A grid of n outbound days needs n + (max-min stay) return days, so n is the largest value
// with n × (n + span) ≤ maxSize.
func Windows(cfg *config.Config, today time.Time, maxSize int) []Window {
	span := cfg.MaxStayDays - cfg.MinStayDays
	n := 1
	for (n+1)*(n+1+span) <= maxSize {
		n++
	}
	var windows []Window
	_, last := cfg.Window(today)
	for d := firstDeparture(cfg, today); !d.After(last); d = d.AddDate(0, 0, n) {
		end := d.AddDate(0, 0, n-1)
		if end.After(last) {
			end = last
		}
		windows = append(windows, Window{
			OutboundStart: d,
			OutboundEnd:   end,
			ReturnStart:   d.AddDate(0, 0, cfg.MinStayDays),
			ReturnEnd:     end.AddDate(0, 0, cfg.MaxStayDays),
		})
	}
	return windows
}

func firstDeparture(cfg *config.Config, today time.Time) time.Time {
	from, _ := cfg.Window(today)
	if tomorrow := today.AddDate(0, 0, 1); tomorrow.After(from) {
		return tomorrow
	}
	return from
}

func days(from, to time.Time) int {
	return int(to.Sub(from).Hours()/24) + 1
}
