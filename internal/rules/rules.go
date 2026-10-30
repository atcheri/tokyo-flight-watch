// Package rules turns calendar scans into alert signals and picks the offer to report.
package rules

import (
	"fmt"
	"sort"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

// Categories of prices. Alerts in one category replace each other.
const (
	KindAny    = "any"    // all airlines, at most max_stops
	KindDirect = "direct" // nonstop on both legs, direct_airlines only
)

// minHistoryDays is how many earlier days of data the "usual price" needs.
const minHistoryDays = 3

const dateLayout = "2006-01-02"

// Scan holds today's calendar prices per category and date-pair key.
type Scan map[string]map[string]int

// Cheapest returns the cheapest entry of prices.
func Cheapest(prices map[string]int) state.Best {
	var best state.Best
	for key, p := range prices {
		if best.Price == 0 || p < best.Price || (p == best.Price && key < best.Key) {
			best = state.Best{Key: key, Price: p}
		}
	}
	return best
}

// Median returns the median of prices, 0 if empty.
func Median(prices map[string]int) int {
	values := make([]int, 0, len(prices))
	for _, p := range prices {
		values = append(values, p)
	}
	return median(values)
}

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sort.Ints(values)
	n := len(values)
	if n%2 == 1 {
		return values[n/2]
	}
	return (values[n/2-1] + values[n/2]) / 2
}

// UsualBest is the median of the category's daily best price over the window before today,
// or 0 when fewer than minHistoryDays days are known.
func UsualBest(h *state.History, kind string, today time.Time, windowDays int) int {
	from := today.AddDate(0, 0, -windowDays).Format(dateLayout)
	to := today.Format(dateLayout)
	var bests []int
	for _, d := range h.DaysSince(from) {
		if d.Date >= to {
			continue
		}
		b := d.BestAny
		if kind == KindDirect {
			b = d.BestDir
		}
		if b.Price > 0 {
			bests = append(bests, b.Price)
		}
	}
	if len(bests) < minHistoryDays {
		return 0
	}
	return median(bests)
}

// Signal is a category whose cheapest price deserves an alert.
type Signal struct {
	Kind    string
	Best    state.Best // from the calendar
	Usual   int        // usual best price, 0 if unknown
	Reasons []string
}

// Signals returns the categories whose cheapest price today is under target or clearly below
// the usual best price, and better than the last alert of that category. Cheapest first.
func Signals(cfg *config.Config, h *state.History, scan Scan, today time.Time) []Signal {
	targets := map[string]int{KindAny: cfg.TargetPrice, KindDirect: cfg.TargetPriceDirect}
	var signals []Signal
	for _, kind := range []string{KindAny, KindDirect} {
		best := Cheapest(scan[kind])
		if best.Price == 0 {
			continue
		}
		s := Signal{Kind: kind, Best: best, Usual: UsualBest(h, kind, today, cfg.TypicalWindowDays)}
		if t := targets[kind]; t > 0 && best.Price <= t {
			s.Reasons = append(s.Reasons, fmt.Sprintf("sous votre cible de %d €", t))
		}
		if cfg.SensitivityPercent > 0 && s.Usual > 0 && best.Price*100 <= s.Usual*(100-cfg.SensitivityPercent) {
			s.Reasons = append(s.Reasons, fmt.Sprintf("%d %% sous le meilleur prix habituel (%d €)",
				100-best.Price*100/s.Usual, s.Usual))
		}
		if len(s.Reasons) == 0 {
			continue
		}
		if last, ok := h.Alerts[kind]; ok && !Improves(cfg, last.Price, best.Price) {
			continue
		}
		signals = append(signals, s)
	}
	sort.SliceStable(signals, func(i, j int) bool { return signals[i].Best.Price < signals[j].Best.Price })
	return signals
}

// Improves reports whether price beats the last alerted price by the configured margin.
func Improves(cfg *config.Config, last, price int) bool {
	margin := max(cfg.MinImprovementEUR, last*cfg.MinImprovementPercent/100)
	return price <= last-margin
}

// ExpireAlerts forgets an alert once its fare has been gone (today's best is higher) for
// alert_expiry_runs consecutive runs, so a later, still good price can alert again.
func ExpireAlerts(cfg *config.Config, h *state.History, scan Scan) {
	for kind, a := range h.Alerts {
		best := Cheapest(scan[kind])
		switch {
		case best.Price == 0:
			// no data for this category today: keep the alert as is
		case best.Price > a.Price:
			a.GoneRuns++
			if a.GoneRuns >= cfg.AlertExpiryRuns {
				delete(h.Alerts, kind)
			}
		default:
			a.GoneRuns = 0
		}
	}
}

// RecordAlert remembers the alert sent for a signal.
func RecordAlert(h *state.History, s Signal, today string) {
	h.Alerts[s.Kind] = &state.Alert{Best: s.Best, Date: today}
}

type Offer struct {
	Price        int
	Stops        int
	Origin       string
	Destination  string
	AirlineNames []string
}

func ToOffer(it flightapi.Itinerary) (Offer, bool) {
	if len(it.Flights) == 0 || it.Price <= 0 {
		return Offer{}, false
	}
	o := Offer{
		Price:       it.Price,
		Stops:       len(it.Flights) - 1,
		Origin:      it.Flights[0].DepartureAirport.ID,
		Destination: it.Flights[len(it.Flights)-1].ArrivalAirport.ID,
	}
	seen := map[string]bool{}
	for _, s := range it.Flights {
		if s.Airline != "" && !seen[s.Airline] {
			seen[s.Airline] = true
			o.AirlineNames = append(o.AirlineNames, s.Airline)
		}
	}
	return o, true
}

// PickOffer chooses the offer to report from a detailed search: the cheapest one at the
// preferred destination, unless another airport is at least narita_min_saving cheaper.
// When another airport wins, preferred is the best preferred-airport offer for comparison.
func PickOffer(cfg *config.Config, resp *flightapi.Response, maxStops int) (chosen Offer, preferred *Offer, ok bool) {
	var cheapest, bestPref *Offer
	for _, it := range resp.Itineraries() {
		o, valid := ToOffer(it)
		if !valid || o.Stops > maxStops {
			continue
		}
		if cheapest == nil || o.Price < cheapest.Price {
			cheapest = &o
		}
		if o.Destination == cfg.PreferredDestination && (bestPref == nil || o.Price < bestPref.Price) {
			bestPref = &o
		}
	}
	switch {
	case cheapest == nil:
		return Offer{}, nil, false
	case bestPref == nil || cheapest.Destination == cfg.PreferredDestination:
		return *cheapest, nil, true
	case cheapest.Price <= bestPref.Price-cfg.NaritaMinSaving:
		return *cheapest, bestPref, true
	default:
		return *bestPref, nil, true
	}
}
