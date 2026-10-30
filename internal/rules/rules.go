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

// Candidates picks, from today's calendar scan, the date pairs worth a detailed search, most
// important first: the cheapest pair under the price limit and the cheapest direct Star Alliance
// pair (each only if it beats what was already alerted), a new overall low, price drops on the
// same dates, and finally the overall cheapest pair to read Google's price level.
func Candidates(cfg *config.Config, h *state.History, scan Scan) []Candidate {
	var ordered []*Candidate
	byKey := map[string]*Candidate{}
	add := func(key string, price int, reason string) {
		c, ok := byKey[key]
		if !ok {
			c = &Candidate{Key: key, Price: price}
			byKey[key] = c
			ordered = append(ordered, c)
		}
		c.Price = min(c.Price, price)
		if reason != "" {
			c.Reasons = append(c.Reasons, reason)
		}
	}

	if best := Cheapest(scan.Any); best.Price > 0 && best.Price <= cfg.DefaultMaxPrice &&
		beatsAlerted(cfg, h, kindAny, best.Price) {
		add(best.Key, best.Price, fmt.Sprintf("prix ≤ %d €", cfg.DefaultMaxPrice))
	}
	if best := Cheapest(scan.Direct); best.Price > 0 && best.Price <= cfg.DirectStarAllianceMaxPrice &&
		beatsAlerted(cfg, h, kindDirect, best.Price) {
		add(best.Key, best.Price, fmt.Sprintf("direct Star Alliance ≤ %d €", cfg.DirectStarAllianceMaxPrice))
	}
	if best := Cheapest(scan.Any); best.Price > 0 {
		if r, ok := newLowReason(cfg, best.Price, h.LowestEver.Price); ok {
			add(best.Key, best.Price, r)
		}
	}

	var drops []Candidate
	for key, price := range scan.Any {
		if r, ok := dropReason(cfg, price, h.Peek(key).MinPrice); ok {
			drops = append(drops, Candidate{Key: key, Price: price, Reasons: []string{r}})
		}
	}
	sort.Slice(drops, func(i, j int) bool {
		if drops[i].Price != drops[j].Price {
			return drops[i].Price < drops[j].Price
		}
		return drops[i].Key < drops[j].Key
	})
	for _, d := range drops {
		add(d.Key, d.Price, d.Reasons[0])
	}

	if best := Cheapest(scan.Any); cfg.AlertOnGoogleLow && best.Key != "" && h.Peek(best.Key).LastDetailPrice != best.Price {
		add(best.Key, best.Price, "")
	}

	candidates := make([]Candidate, len(ordered))
	for i, c := range ordered {
		candidates[i] = *c
	}
	return candidates
}

const (
	kindAny    = "any"
	kindDirect = "direct"
)

// category groups offers whose alerts replace each other, e.g. "direct:HND" or "any:NRT".
func category(o Offer) string {
	kind := kindAny
	if o.DirectStarAlliance() {
		kind = kindDirect
	}
	return kind + ":" + o.Destination
}

// beatsAlerted reports whether a calendar price (cheapest over all destinations) could beat the
// best price already alerted for at least one destination of that kind.
func beatsAlerted(cfg *config.Config, h *state.History, kind string, price int) bool {
	for _, dest := range cfg.Destinations {
		best, ok := h.AlertedBest[kind+":"+dest]
		if !ok || price < best {
			return true
		}
	}
	return false
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

// Evaluate applies the alert rules to one detailed search. prev is the date pair's history,
// lowestEver the cheapest price ever seen across all pairs (both as they were before today), and
// alertedBest the lowest price already alerted per category. It keeps at most one deal per category.
func Evaluate(cfg *config.Config, resp *flightapi.Response, prev state.Combo, lowestEver int, alertedBest map[string]int) Result {
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

	seen := map[string]bool{}
	for _, o := range res.Offers {
		if o.Destination != cfg.PreferredDestination && bestPreferred > 0 &&
			o.Price > bestPreferred-cfg.NaritaMinSaving {
			continue
		}
		cat := category(o)
		if seen[cat] {
			continue // offers are sorted by price: one deal per category is enough
		}
		reasons := reasonsFor(cfg, o, resp.PriceInsights, prev, lowestEver, alertedBest[cat])
		if len(reasons) == 0 {
			continue
		}
		if last, ok := prev.Alerted[o.Signature()]; ok && o.Price >= last {
			continue
		}
		seen[cat] = true
		res.Deals = append(res.Deals, Deal{Offer: o, Reasons: reasons})
	}
	return res
}

func reasonsFor(cfg *config.Config, o Offer, pi *flightapi.PriceInsights, prev state.Combo, lowestEver, alertedBest int) []string {
	var reasons []string
	beats := alertedBest == 0 || o.Price < alertedBest
	switch {
	case !beats:
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

// Record updates the history after a detailed search of the date pair key.
func Record(h *state.History, key string, res Result, calendarPrice int) {
	c := h.Get(key)
	c.LastDetailPrice = calendarPrice
	for _, d := range res.Deals {
		if c.Alerted == nil {
			c.Alerted = map[string]int{}
		}
		c.Alerted[d.Signature()] = d.Price
		if h.AlertedBest == nil {
			h.AlertedBest = map[string]int{}
		}
		if best, ok := h.AlertedBest[category(d.Offer)]; !ok || d.Price < best {
			h.AlertedBest[category(d.Offer)] = d.Price
		}
	}
}
