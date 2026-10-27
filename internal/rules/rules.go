// Package rules decides which offers are worth an alert.
package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

// starAlliance lists member airlines by IATA code. SAS (SK) left for SkyTeam in 2024.
var starAlliance = map[string]bool{
	"A3": true, "AC": true, "CA": true, "AI": true, "NZ": true, "NH": true, "OZ": true,
	"OS": true, "AV": true, "SN": true, "CM": true, "OU": true, "MS": true, "ET": true,
	"BR": true, "LO": true, "LH": true, "ZH": true, "SQ": true, "SA": true, "LX": true,
	"TP": true, "TG": true, "TK": true, "UA": true,
}

type Offer struct {
	Price        int
	Stops        int
	Origin       string
	Destination  string
	Airlines     []string // IATA codes, in segment order, deduplicated
	AirlineNames []string
	StarAlliance bool // every segment operated by a Star Alliance member
}

// Signature identifies "the same offer" across runs for de-duplication.
func (o Offer) Signature() string {
	return fmt.Sprintf("%s-%s-%d-%s", o.Origin, o.Destination, o.Stops, strings.Join(o.Airlines, "+"))
}

type Deal struct {
	Offer
	Reasons []string
}

type Result struct {
	Offers []Offer
	Deals  []Deal // alert-worthy and not already alerted at this price or lower
	// Worthy is true when any offer met the criteria, even if it was already alerted.
	Worthy   bool
	MinPrice int
}

func ToOffer(it flightapi.Itinerary) (Offer, bool) {
	if len(it.Flights) == 0 || it.Price <= 0 {
		return Offer{}, false
	}
	o := Offer{
		Price:        it.Price,
		Stops:        len(it.Flights) - 1,
		Origin:       it.Flights[0].DepartureAirport.ID,
		Destination:  it.Flights[len(it.Flights)-1].ArrivalAirport.ID,
		StarAlliance: true,
	}
	seen := map[string]bool{}
	for _, s := range it.Flights {
		code := s.AirlineCode()
		if !starAlliance[code] {
			o.StarAlliance = false
		}
		if !seen[code] {
			seen[code] = true
			o.Airlines = append(o.Airlines, code)
			o.AirlineNames = append(o.AirlineNames, s.Airline)
		}
	}
	return o, true
}

// Evaluate applies the alert rules to one search response. prev is the combo's history
// before this run (may be nil).
func Evaluate(cfg *config.Config, resp *flightapi.Response, prev *state.Combo) Result {
	var res Result
	for _, it := range resp.Itineraries() {
		if o, ok := ToOffer(it); ok && o.Stops <= cfg.MaxStops {
			res.Offers = append(res.Offers, o)
		}
	}
	sort.SliceStable(res.Offers, func(i, j int) bool { return res.Offers[i].Price < res.Offers[j].Price })

	bestPreferred := 0
	for _, o := range res.Offers {
		if res.MinPrice == 0 || o.Price < res.MinPrice {
			res.MinPrice = o.Price
		}
		if o.Destination == cfg.PreferredDestination && (bestPreferred == 0 || o.Price < bestPreferred) {
			bestPreferred = o.Price
		}
	}

	alerted := map[string]bool{}
	for _, o := range res.Offers {
		if o.Destination != cfg.PreferredDestination && bestPreferred > 0 &&
			o.Price > bestPreferred-cfg.NaritaMinSaving {
			continue
		}
		reasons := reasonsFor(cfg, o, prev)
		if len(reasons) == 0 {
			continue
		}
		res.Worthy = true
		sig := o.Signature()
		if alerted[sig] {
			continue // offers are sorted by price, keep only the cheapest per signature
		}
		alerted[sig] = true
		if prev != nil {
			if last, ok := prev.Alerted[sig]; ok && o.Price >= last {
				continue
			}
		}
		res.Deals = append(res.Deals, Deal{Offer: o, Reasons: reasons})
	}
	return res
}

func reasonsFor(cfg *config.Config, o Offer, prev *state.Combo) []string {
	var reasons []string
	switch {
	case o.Stops == 0 && o.StarAlliance && o.Price <= cfg.DirectStarAllianceMaxPrice:
		reasons = append(reasons, fmt.Sprintf("direct Star Alliance ≤ %d €", cfg.DirectStarAllianceMaxPrice))
	case o.Price <= cfg.DefaultMaxPrice:
		reasons = append(reasons, fmt.Sprintf("prix ≤ %d €", cfg.DefaultMaxPrice))
	}
	if prev != nil && prev.MinPrice > 0 && o.Price*100 <= prev.MinPrice*(100-cfg.PriceDropPercent) {
		drop := 100 - o.Price*100/prev.MinPrice
		reasons = append(reasons, fmt.Sprintf("baisse de %d %% (plus bas précédent %d €)", drop, prev.MinPrice))
	}
	return reasons
}

// Record updates the combo history after a check.
func Record(c *state.Combo, res Result, today string) {
	c.LastChecked = today
	c.Hot = res.Worthy
	if res.MinPrice > 0 {
		c.LastPrice = res.MinPrice
		if c.MinPrice == 0 || res.MinPrice < c.MinPrice {
			c.MinPrice = res.MinPrice
		}
	}
	for _, d := range res.Deals {
		if c.Alerted == nil {
			c.Alerted = map[string]int{}
		}
		c.Alerted[d.Signature()] = d.Price
	}
}
