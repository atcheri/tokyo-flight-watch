package flightapi

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// decode renders a protobuf message as text; the fields listed in nested are decoded as
// messages, other length-delimited fields as strings.
func decode(t *testing.T, b []byte, nested map[int]bool) string {
	t.Helper()
	readVarint := func() uint64 {
		var v uint64
		for shift := 0; ; shift += 7 {
			if len(b) == 0 {
				t.Fatal("truncated varint")
			}
			c := b[0]
			b = b[1:]
			v |= uint64(c&0x7f) << shift
			if c < 0x80 {
				return v
			}
		}
	}
	var parts []string
	for len(b) > 0 {
		key := readVarint()
		field, wire := int(key>>3), key&7
		switch wire {
		case 0:
			parts = append(parts, fmt.Sprintf("%d:%d", field, readVarint()))
		case 2:
			n := int(readVarint())
			data := b[:n]
			b = b[n:]
			if nested[field] {
				parts = append(parts, fmt.Sprintf("%d{%s}", field, decode(t, data, nested)))
			} else {
				parts = append(parts, fmt.Sprintf("%d:%q", field, data))
			}
		default:
			t.Fatalf("unexpected wire type %d", wire)
		}
	}
	return strings.Join(parts, " ")
}

func TestSearchURL(t *testing.T) {
	link := SearchURL(Query{
		Filters:      Filters{Origins: []string{"FRA", "CGN"}, Destinations: []string{"HND"}, Adults: 1, MaxStops: 0, IncludedAirlines: "STAR_ALLIANCE"},
		OutboundDate: "2027-04-03", ReturnDate: "2027-04-12",
	})
	u, err := url.Parse(link)
	if err != nil || u.Host != "www.google.com" || u.Path != "/travel/flights/search" || u.Query().Get("curr") != "EUR" {
		t.Fatalf("link = %s, %v", link, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(u.Query().Get("tfs"))
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, raw, map[int]bool{3: true, 13: true, 14: true})
	want := `3{2:"2027-04-03" 5:1 6:"STAR_ALLIANCE" 13{1:1 2:"FRA"} 13{1:1 2:"CGN"} 14{1:1 2:"HND"}} ` +
		`3{2:"2027-04-12" 5:1 6:"STAR_ALLIANCE" 13{1:1 2:"HND"} 14{1:1 2:"FRA"} 14{1:1 2:"CGN"}} ` +
		`8:"\x01" 9:1 19:1`
	if got != want {
		t.Fatalf("tfs decodes to\n%s\nwant\n%s", got, want)
	}
}
