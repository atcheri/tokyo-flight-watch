package airports

import (
	"slices"
	"testing"
)

func codes(as []Airport) []string {
	var cs []string
	for _, a := range as {
		cs = append(cs, a.Code)
	}
	return cs
}

func TestLookup(t *testing.T) {
	if n := Count(); n < 5000 {
		t.Fatalf("only %d airports embedded", n)
	}
	for _, code := range []string{"FRA", "cgn", "DUS", "HND", "NRT", "CDG", "ORY", "BER", "OSL"} {
		if a, ok := Lookup(code); !ok || !a.Scheduled || a.Name == "" {
			t.Errorf("%s = %+v, %v", code, a, ok)
		}
	}
	for _, code := range []string{"PAR", "TYO", "LON", "QQQ", ""} {
		if a, ok := Lookup(code); ok {
			t.Errorf("%s must be unknown, got %+v", code, a)
		}
	}
	if a, _ := Lookup("FRA"); a.Label() != "FRA – Frankfurt Main Airport (Frankfurt am Main, DE)" {
		t.Errorf("label = %q", a.Label())
	}
}

func TestSearch(t *testing.T) {
	for term, want := range map[string][]string{
		"Paris":      {"CDG", "ORY"},
		"koln":       {"CGN"},
		"Cologne":    {"CGN"},
		"düsseldorf": {"DUS"},
		"frank":      {"FRA"},
		"Tokyo":      {"HND"},
	} {
		got := codes(Search(term, 5))
		for _, code := range want {
			if !slices.Contains(got, code) {
				t.Errorf("Search(%q) = %v, missing %s", term, got, code)
			}
		}
	}
	if got := Search("Paris", 5); got[0].Kind != "L" {
		t.Errorf("large airports must come first: %v", codes(got))
	}
	if got := Search("pa", 5); got != nil {
		t.Errorf("terms under 3 letters must not match: %v", codes(got))
	}
}
