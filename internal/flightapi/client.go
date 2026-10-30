// Package flightapi queries Google Flights data through SearchApi.io.
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

// KeyEnv is the environment variable holding the SearchApi key.
const KeyEnv = "SEARCHAPI_KEY"

const baseURL = "https://www.searchapi.io/api/v1"

type Client struct {
	BaseURL  string // SearchApi base URL, overridable in tests
	APIKey   string
	HTTP     *http.Client
	Requests int // billable search requests sent, for quota tracking
}

func NewClient(apiKey string) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Filters narrows the flights of both legs.
type Filters struct {
	Origins          []string
	Destinations     []string
	Adults           int
	MaxStops         int    // 0 nonstop, 1 one stop or fewer, 2 two stops or fewer
	IncludedAirlines string // e.g. "STAR_ALLIANCE"; empty for all airlines
}

type Query struct {
	Filters
	OutboundDate string
	ReturnDate   string
}

// Response holds the parts of the Google Flights result we use.
type Response struct {
	BestFlights   []Itinerary    `json:"best_flights"`
	OtherFlights  []Itinerary    `json:"other_flights"`
	PriceInsights *PriceInsights `json:"price_insights"`
	Error         string         `json:"error"`
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

// PriceRange decodes both [low, high] and {"low_price", "high_price"}.
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

func (r *Response) Itineraries() []Itinerary {
	return append(append([]Itinerary{}, r.BestFlights...), r.OtherFlights...)
}

// SearchURL builds a Google Flights link for the query.
func SearchURL(q Query) string {
	text := fmt.Sprintf("Flights from %s to %s on %s through %s",
		strings.Join(q.Origins, ","), strings.Join(q.Destinations, ","), q.OutboundDate, q.ReturnDate)
	if q.MaxStops == 0 {
		text += " nonstop"
	}
	return "https://www.google.com/travel/flights?hl=fr&curr=EUR&q=" + url.QueryEscape(text)
}

// Search returns the offers for one outbound/return date pair.
func (c *Client) Search(ctx context.Context, q Query) (*Response, error) {
	params := q.Filters.params("google_flights")
	params.Set("outbound_date", q.OutboundDate)
	params.Set("return_date", q.ReturnDate)
	body, status, err := c.get(ctx, "/search", params, true)
	if err != nil {
		return nil, err
	}
	return Parse(body, status)
}

// CalendarQuery asks for round-trip prices over a grid of outbound × return dates.
// The grid may hold at most MaxCalendarCombos date pairs.
type CalendarQuery struct {
	Filters
	OutboundStart string
	OutboundEnd   string
	ReturnStart   string
	ReturnEnd     string
}

const MaxCalendarCombos = 200

type CalendarPrice struct {
	Departure    string `json:"departure"`
	Return       string `json:"return"`
	Price        int    `json:"price"`
	HasNoFlights bool   `json:"has_no_flights"`
}

// Calendar returns the cheapest price for each date pair of the grid.
func (c *Client) Calendar(ctx context.Context, q CalendarQuery) ([]CalendarPrice, error) {
	params := q.Filters.params("google_flights_calendar")
	params.Set("outbound_date", q.OutboundStart)
	params.Set("outbound_date_end", q.OutboundEnd)
	params.Set("return_date", q.ReturnStart)
	params.Set("return_date_end", q.ReturnEnd)
	body, status, err := c.get(ctx, "/search", params, true)
	if err != nil {
		return nil, err
	}
	return ParseCalendar(body, status)
}

// Credits returns the account's remaining search credits.
func (c *Client) Credits(ctx context.Context) (int, error) {
	body, status, err := c.get(ctx, "/me", url.Values{}, false)
	if err != nil {
		return 0, err
	}
	return ParseCredits(body, status)
}

func (f Filters) params(engine string) url.Values {
	params := url.Values{
		"engine":       {engine},
		"flight_type":  {"round_trip"},
		"travel_class": {"economy"},
		"departure_id": {strings.Join(f.Origins, ",")},
		"arrival_id":   {strings.Join(f.Destinations, ",")},
		"adults":       {strconv.Itoa(f.Adults)},
		"currency":     {"EUR"},
		"gl":           {"de"},
		"hl":           {"fr"},
	}
	if stops := []string{"nonstop", "one_stop_or_fewer", "two_stops_or_fewer"}; f.MaxStops >= 0 && f.MaxStops < len(stops) {
		params.Set("stops", stops[f.MaxStops])
	}
	if f.IncludedAirlines != "" {
		params.Set("included_airlines", f.IncludedAirlines)
	}
	return params
}

func (c *Client) get(ctx context.Context, path string, params url.Values, billable bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("searchapi: invalid request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if billable {
		c.Requests++
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("searchapi request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read searchapi response: %w", err)
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

func ParseCredits(body []byte, status int) (int, error) {
	var r struct {
		Account struct {
			RemainingCredits *int `json:"remaining_credits"`
		} `json:"account"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("decode account (status %d): %w", status, err)
	}
	if err := checkError(r.Error, status); err != nil {
		return 0, err
	}
	if r.Account.RemainingCredits == nil {
		return 0, fmt.Errorf("account response has no remaining_credits")
	}
	return *r.Account.RemainingCredits, nil
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
