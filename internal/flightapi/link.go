package flightapi

import (
	"encoding/base64"
	"net/url"
)

// SearchURL builds a Google Flights results link for the query: round trip, economy, with all
// origin and destination airports, the dates, the stop limit and the airline filter.
//
// Google Flights encodes a search as a protobuf message in the tfs parameter. The format is not
// documented; this follows the layout used by Google's own links (and by libraries such as
// fast-flights):
//
//	Info       { repeated Leg legs = 3; repeated Passenger passengers = 8; Seat seat = 9; Trip trip = 19; }
//	Leg        { string date = 2; int32 stops = 5; repeated string airlines = 6;
//	             repeated Place from = 13; repeated Place to = 14; }
//	Place      { int32 kind = 1 (1 = airport); string code = 2; }
//	stops: 1 nonstop, 2 one stop or fewer, 3 two stops or fewer; absent = any.
//	Passenger ADULT = 1, Seat ECONOMY = 1, Trip ROUND_TRIP = 1.
func SearchURL(q Query) string {
	var info pb
	info.message(3, leg(q.OutboundDate, q.Origins, q.Destinations, q.Filters))
	info.message(3, leg(q.ReturnDate, q.Destinations, q.Origins, q.Filters))
	var adults pb
	for i := 0; i < max(q.Adults, 1); i++ {
		adults.varint(1)
	}
	info.bytes(8, adults)
	info.uint(9, 1)  // economy
	info.uint(19, 1) // round trip

	params := url.Values{
		"tfs":  {base64.RawURLEncoding.EncodeToString(info)},
		"hl":   {"fr"},
		"curr": {"EUR"},
	}
	return "https://www.google.com/travel/flights/search?" + params.Encode()
}

func leg(date string, from, to []string, f Filters) pb {
	var l pb
	l.bytes(2, pb(date))
	if f.MaxStops >= 0 && f.MaxStops <= 2 {
		l.uint(5, uint64(f.MaxStops)+1)
	}
	if f.IncludedAirlines != "" {
		l.bytes(6, pb(f.IncludedAirlines))
	}
	for _, code := range from {
		l.message(13, place(code))
	}
	for _, code := range to {
		l.message(14, place(code))
	}
	return l
}

func place(code string) pb {
	var p pb
	p.uint(1, 1) // airport
	p.bytes(2, pb(code))
	return p
}

// pb is a minimal protobuf encoder: just what SearchURL needs.
type pb []byte

func (b *pb) varint(v uint64) {
	for v >= 0x80 {
		*b = append(*b, byte(v)|0x80)
		v >>= 7
	}
	*b = append(*b, byte(v))
}

func (b *pb) uint(field int, v uint64) {
	b.varint(uint64(field) << 3) // wire type 0: varint
	b.varint(v)
}

func (b *pb) bytes(field int, data []byte) {
	b.varint(uint64(field)<<3 | 2) // wire type 2: length-delimited
	b.varint(uint64(len(data)))
	*b = append(*b, data...)
}

func (b *pb) message(field int, m pb) { b.bytes(field, m) }
