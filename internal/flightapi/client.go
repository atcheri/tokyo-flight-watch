// Package flightapi queries a Google Flights scraping API (SearchApi.io or SerpApi).
package flightapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Provider string

const (
	SearchAPI Provider = "searchapi" // https://www.searchapi.io
	SerpAPI   Provider = "serpapi"   // https://serpapi.com
)

// KeyEnv is the environment variable holding the API key for each provider.
var KeyEnv = map[Provider]string{
	SearchAPI: "SEARCHAPI_KEY",
	SerpAPI:   "SERPAPI_KEY",
}

type Client struct {
	Provider Provider
	APIKey   string
	HTTP     *http.Client
	Requests int // API requests sent, for quota tracking
}

func NewClient(provider Provider, apiKey string) (*Client, error) {
	if _, ok := KeyEnv[provider]; !ok {
		return nil, fmt.Errorf("unknown provider %q (want %q or %q)", provider, SearchAPI, SerpAPI)
	}
	return &Client{Provider: provider, APIKey: apiKey, HTTP: &http.Client{Timeout: 90 * time.Second}}, nil
}

type Query struct {
	Origins      []string
	Destinations []string
	OutboundDate string
	ReturnDate   string
	Adults       int
	MaxStops     int
}

// Response holds the parts of the Google Flights result we use. Both providers share this shape.
type Response struct {
	BestFlights    []Itinerary    `json:"best_flights"`
	OtherFlights   []Itinerary    `json:"other_flights"`
	PriceInsights  *PriceInsights `json:"price_insights"`
	SearchMetadata struct {
		GoogleFlightsURL string `json:"google_flights_url"` // SerpApi only
	} `json:"search_metadata"`
	Error string `json:"error"`
}

// Itinerary is a round-trip offer. For round trips Google Flights lists only the outbound
// segments; Price is the full round-trip price.
type Itinerary struct {
	Flights []Segment `json:"flights"`
	Price   int       `json:"price"`
}

type Segment struct {
	DepartureAirport Airport `json:"departure_airport"`
	ArrivalAirport   Airport `json:"arrival_airport"`
	Airline          string  `json:"airline"`
	FlightNumber     string  `json:"flight_number"`
}

type Airport struct {
	ID   string `json:"id"`
	Time string `json:"time"`
}

type PriceInsights struct {
	LowestPrice       int        `json:"lowest_price"`
	PriceLevel        string     `json:"price_level"`
	TypicalPriceRange PriceRange `json:"typical_price_range"`
}

// PriceRange decodes both SerpApi's [low, high] and SearchApi's {"low_price", "high_price"}.
type PriceRange struct {
	Low, High int
}

func (p *PriceRange) UnmarshalJSON(data []byte) error {
	var arr []int
	if err := json.Unmarshal(data, &arr); err == nil {
		if len(arr) == 2 {
			p.Low, p.High = arr[0], arr[1]
		}
		return nil
	}
	var obj struct {
		Low  int `json:"low_price"`
		High int `json:"high_price"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	p.Low, p.High = obj.Low, obj.High
	return nil
}

// AirlineCode returns the IATA prefix of the flight number ("LH 716" -> "LH").
func (s Segment) AirlineCode() string {
	code, _, _ := strings.Cut(strings.TrimSpace(s.FlightNumber), " ")
	return strings.ToUpper(code)
}

func (r *Response) Itineraries() []Itinerary {
	return append(append([]Itinerary{}, r.BestFlights...), r.OtherFlights...)
}

// GoogleFlightsURL returns a link to the same search on Google Flights.
func (r *Response) GoogleFlightsURL(q Query) string {
	if r.SearchMetadata.GoogleFlightsURL != "" {
		return r.SearchMetadata.GoogleFlightsURL
	}
	return SearchURL(q)
}

// SearchURL builds a Google Flights link for the query.
func SearchURL(q Query) string {
	text := fmt.Sprintf("Flights from %s to %s on %s through %s",
		strings.Join(q.Origins, ","), strings.Join(q.Destinations, ","), q.OutboundDate, q.ReturnDate)
	return "https://www.google.com/travel/flights?hl=fr&curr=EUR&q=" + url.QueryEscape(text)
}

func (c *Client) Search(ctx context.Context, q Query) (*Response, error) {
	var endpoint string
	var params url.Values
	switch c.Provider {
	case SearchAPI:
		endpoint = "https://www.searchapi.io/api/v1/search"
		params = url.Values{
			"flight_type":  {"round_trip"},
			"travel_class": {"economy"},
		}
		if s := searchAPIStops(q.MaxStops); s != "" {
			params.Set("stops", s)
		}
	case SerpAPI:
		endpoint = "https://serpapi.com/search.json"
		params = url.Values{
			"type":         {"1"}, // round trip
			"travel_class": {"1"}, // economy
		}
		// SerpApi stops: 0 any, 1 nonstop, 2 one stop or fewer, 3 two stops or fewer.
		if q.MaxStops >= 0 && q.MaxStops <= 2 {
			params.Set("stops", strconv.Itoa(q.MaxStops+1))
		}
	}
	params.Set("engine", "google_flights")
	params.Set("departure_id", strings.Join(q.Origins, ","))
	params.Set("arrival_id", strings.Join(q.Destinations, ","))
	params.Set("outbound_date", q.OutboundDate)
	params.Set("return_date", q.ReturnDate)
	params.Set("adults", strconv.Itoa(q.Adults))
	params.Set("currency", "EUR")
	params.Set("gl", "de")
	params.Set("hl", "fr")
	body, status, err := c.get(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}
	return Parse(body, status)
}

// CalendarQuery asks for round-trip prices over a grid of outbound × return dates.
// The grid may hold at most MaxCalendarCombos date pairs.
type CalendarQuery struct {
	Origins          []string
	Destinations     []string
	OutboundStart    string
	OutboundEnd      string
	ReturnStart      string
	ReturnEnd        string
	Adults           int
	MaxStops         int
	IncludedAirlines string // e.g. "STAR_ALLIANCE"; empty for all airlines
}

const MaxCalendarCombos = 200

type CalendarPrice struct {
	Departure    string `json:"departure"`
	Return       string `json:"return"`
	Price        int    `json:"price"`
	HasNoFlights bool   `json:"has_no_flights"`
}

// Calendar returns the cheapest price for each date pair of the grid (SearchApi only).
func (c *Client) Calendar(ctx context.Context, q CalendarQuery) ([]CalendarPrice, error) {
	if c.Provider != SearchAPI {
		return nil, fmt.Errorf("the price calendar is only supported with provider %q", SearchAPI)
	}
	params := url.Values{
		"engine":            {"google_flights_calendar"},
		"flight_type":       {"round_trip"},
		"travel_class":      {"economy"},
		"departure_id":      {strings.Join(q.Origins, ",")},
		"arrival_id":        {strings.Join(q.Destinations, ",")},
		"outbound_date":     {q.OutboundStart},
		"outbound_date_end": {q.OutboundEnd},
		"return_date":       {q.ReturnStart},
		"return_date_end":   {q.ReturnEnd},
		"adults":            {strconv.Itoa(q.Adults)},
		"currency":          {"EUR"},
		"gl":                {"de"},
		"hl":                {"fr"},
	}
	if s := searchAPIStops(q.MaxStops); s != "" {
		params.Set("stops", s)
	}
	if q.IncludedAirlines != "" {
		params.Set("included_airlines", q.IncludedAirlines)
	}
	body, status, err := c.get(ctx, "https://www.searchapi.io/api/v1/search", params)
	if err != nil {
		return nil, err
	}
	return ParseCalendar(body, status)
}

func searchAPIStops(maxStops int) string {
	if stops := []string{"nonstop", "one_stop_or_fewer", "two_stops_or_fewer"}; maxStops >= 0 && maxStops < len(stops) {
		return stops[maxStops]
	}
	return ""
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values) ([]byte, int, error) {
	params.Set("api_key", c.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: invalid request", c.Provider) // the URL contains the API key
	}
	c.Requests++
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Don't wrap: *url.Error would print the URL, which contains the API key.
		return nil, 0, fmt.Errorf("%s request failed", c.Provider)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s response: %w", c.Provider, err)
	}
	return body, resp.StatusCode, nil
}

func Parse(body []byte, status int) (*Response, error) {
	var r Response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode response (status %d): %w", status, err)
	}
	if err := checkError(r.Error, status); err != nil {
		return nil, err
	}
	return &r, nil
}

func ParseCalendar(body []byte, status int) ([]CalendarPrice, error) {
	var r struct {
		Calendar []CalendarPrice `json:"calendar"`
		Error    string          `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode calendar (status %d): %w", status, err)
	}
	if err := checkError(r.Error, status); err != nil {
		return nil, err
	}
	prices := r.Calendar[:0]
	for _, p := range r.Calendar {
		if !p.HasNoFlights && p.Price > 0 {
			prices = append(prices, p)
		}
	}
	return prices, nil
}

func checkError(msg string, status int) error {
	if msg != "" {
		// "no results" is a normal outcome, not a failure.
		if m := strings.ToLower(msg); strings.Contains(m, "any results") || strings.Contains(m, "no results") {
			return nil
		}
		return fmt.Errorf("api error (status %d): %s", status, msg)
	}
	if status != http.StatusOK {
		return fmt.Errorf("api status %d", status)
	}
	return nil
}
