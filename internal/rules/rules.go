// Package rules decides which date pairs deserve a detailed search and which offers are worth an alert.
package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

// StarAllianceFilter is the alliance code accepted by the included_airlines parameter.
const StarAllianceFilter = "STAR_ALLIANCE"

// starAlliance lists member airlines by IATA code. SAS (SK) left for SkyTeam in 2024.
var starAlliance = map[string]bool{
	"A3": true, "AC": true, "CA": true, "AI": true, "NZ": true, "NH": true, "OZ": true,
	"OS": true, "AV": true, "SN": true, "CM": true, "OU": true, "MS": true, "ET": true,
	"BR": true, "LO": true, "LH": true, "ZH": true, "SQ": true, "SA": true, "LX": true,
	"TP": true, "TG": true, "TK": true, "UA": true,
}

// Scan holds today's calendar prices per date-pair key.
type Scan struct {
	Any    map[string]int // at most max_stops, any airline
	Direct map[string]int // nonstop, Star Alliance only
}

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

// Candidate is a date pair that deserves a detailed search.
type Candidate struct {
	Key     string
	Price   int      // calendar price that triggered it
	Reasons []string // empty when it is only checked for Google's "low" price level
}

// Candidates picks, from today's calendar scan, the date pairs worth a detailed search:
// cheapest first, then the overall cheapest pair to read Google's price level.
func Candidates(cfg *config.Config, h *state.History, scan Scan) []Candidate {
	byKey := map[string]*Candidate{}
	add := func(key string, price int, reason string) {
		c, ok := byKey[key]
		if !ok {
			c = &Candidate{Key: key, Price: price}
			byKey[key] = c
		}
		c.Price = min(c.Price, price)
		c.Reasons = append(c.Reasons, reason)
	}

	for key, price := range scan.Any {
		prev := h.Peek(key)
		if prev.AlertedAny > 0 && price >= prev.AlertedAny {
			continue
		}
		if price <= cfg.DefaultMaxPrice {
			add(key, price, fmt.Sprintf("prix ≤ %d €", cfg.DefaultMaxPrice))
		}
		if r, ok := dropReason(cfg, price, prev.MinPrice); ok {
			add(key, price, r)
		}
		if r, ok := newLowReason(cfg, price, h.LowestEver.Price); ok {
			add(key, price, r)
		}
	}
	for key, price := range scan.Direct {
		prev := h.Peek(key)
		if prev.AlertedDirect > 0 && price >= prev.AlertedDirect {
			continue
		}
		if price <= cfg.DirectStarAllianceMaxPrice {
			add(key, price, fmt.Sprintf("direct Star Alliance ≤ %d €", cfg.DirectStarAllianceMaxPrice))
		}
	}

	candidates := make([]Candidate, 0, len(byKey)+1)
	for _, c := range byKey {
		candidates = append(candidates, *c)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Price != candidates[j].Price {
			return candidates[i].Price < candidates[j].Price
		}
		return candidates[i].Key < candidates[j].Key
	})

	if cfg.AlertOnGoogleLow {
		best := Cheapest(scan.Any)
		if _, ok := byKey[best.Key]; best.Key != "" && !ok && h.Peek(best.Key).LastDetailPrice != best.Price {
			candidates = append(candidates, Candidate{Key: best.Key, Price: best.Price})
		}
	}
	return candidates
}

// RecordScan stores today's calendar prices and returns the day's best prices.
func RecordScan(h *state.History, scan Scan, today string) (bestAny, bestDirect state.Best) {
	for key, price := range scan.Any {
		c := h.Get(key)
		c.LastChecked = today
		c.LastPrice = price
		if c.MinPrice == 0 || price < c.MinPrice {
			c.MinPrice = price
		}
	}
	for key, price := range scan.Direct {
		c := h.Get(key)
		c.LastChecked = today
		c.DirectLastPrice = price
		if c.DirectMinPrice == 0 || price < c.DirectMinPrice {
			c.DirectMinPrice = price
		}
	}
	bestAny, bestDirect = Cheapest(scan.Any), Cheapest(scan.Direct)
	if bestAny.Price > 0 && (h.LowestEver.Price == 0 || bestAny.Price < h.LowestEver.Price) {
		h.LowestEver = bestAny
	}
	return bestAny, bestDirect
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

func (o Offer) DirectStarAlliance() bool { return o.Stops == 0 && o.StarAlliance }

type Deal struct {
	Offer
	Reasons []string
}

type Result struct {
	Offers   []Offer
	Deals    []Deal // alert-worthy and not already alerted at this price or lower
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

// Evaluate applies the alert rules to one detailed search. prev is the date pair's history
// and lowestEver the cheapest price ever seen across all pairs, both as they were before today.
func Evaluate(cfg *config.Config, resp *flightapi.Response, prev state.Combo, lowestEver int) Result {
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
		reasons := reasonsFor(cfg, o, resp.PriceInsights, prev, lowestEver)
		if len(reasons) == 0 {
			continue
		}
		sig := o.Signature()
		if alerted[sig] {
			continue // offers are sorted by price, keep only the cheapest per signature
		}
		alerted[sig] = true
		if last, ok := prev.Alerted[sig]; ok && o.Price >= last {
			continue
		}
		res.Deals = append(res.Deals, Deal{Offer: o, Reasons: reasons})
	}
	return res
}

func reasonsFor(cfg *config.Config, o Offer, pi *flightapi.PriceInsights, prev state.Combo, lowestEver int) []string {
	var reasons []string
	switch {
	case o.DirectStarAlliance() && o.Price <= cfg.DirectStarAllianceMaxPrice:
		reasons = append(reasons, fmt.Sprintf("direct Star Alliance ≤ %d €", cfg.DirectStarAllianceMaxPrice))
	case o.Price <= cfg.DefaultMaxPrice:
		reasons = append(reasons, fmt.Sprintf("prix ≤ %d €", cfg.DefaultMaxPrice))
	}
	if r, ok := dropReason(cfg, o.Price, prev.MinPrice); ok {
		reasons = append(reasons, r)
	}
	if r, ok := newLowReason(cfg, o.Price, lowestEver); ok {
		reasons = append(reasons, r)
	}
	if cfg.AlertOnGoogleLow && pi != nil && pi.PriceLevel == "low" && o.Price <= pi.LowestPrice {
		reasons = append(reasons, "Google Flights juge ce prix bas")
	}
	return reasons
}

func dropReason(cfg *config.Config, price, prevMin int) (string, bool) {
	if prevMin <= 0 || price*100 > prevMin*(100-cfg.PriceDropPercent) {
		return "", false
	}
	return fmt.Sprintf("baisse de %d %% (plus bas précédent %d €)", 100-price*100/prevMin, prevMin), true
}

func newLowReason(cfg *config.Config, price, lowestEver int) (string, bool) {
	if lowestEver <= 0 || price*100 > lowestEver*(100-cfg.NewLowMinPercent) {
		return "", false
	}
	return fmt.Sprintf("nouveau plus bas toutes dates (avant : %d €)", lowestEver), true
}

// Record updates the date pair's history after a detailed search.
func Record(c *state.Combo, res Result, calendarPrice int) {
	c.LastDetailPrice = calendarPrice
	for _, d := range res.Deals {
		if c.Alerted == nil {
			c.Alerted = map[string]int{}
		}
		c.Alerted[d.Signature()] = d.Price
		if d.DirectStarAlliance() {
			c.AlertedDirect = lowest(c.AlertedDirect, d.Price)
		} else {
			c.AlertedAny = lowest(c.AlertedAny, d.Price)
		}
	}
}

// lowest is min where 0 means "unset".
func lowest(current, price int) int {
	if current == 0 || price < current {
		return price
	}
	return current
}
