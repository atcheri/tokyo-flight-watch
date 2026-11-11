// Package airports knows the world's airports by IATA code, from an embedded copy of the
// OurAirports data (public domain). Refresh it with: go generate ./internal/airports
package airports

//go:generate go run ./gen -out airports.csv

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed airports.csv
var data string

type Airport struct {
	Code      string
	Name      string
	City      string
	Country   string // ISO 3166 code, e.g. DE
	Kind      string // L large, M medium, S small, W seaplane base, H heliport
	Scheduled bool   // has airline flights
}

// Label describes the airport, e.g. "FRA – Frankfurt Main Airport (Frankfurt am Main, DE)".
func (a Airport) Label() string {
	if a.City == "" {
		return fmt.Sprintf("%s – %s (%s)", a.Code, a.Name, a.Country)
	}
	return fmt.Sprintf("%s – %s (%s, %s)", a.Code, a.Name, a.City, a.Country)
}

var (
	loadOnce sync.Once
	all      []Airport
	byCode   map[string]int
	keys     []string // normalised " city name " per airport, for Search
)

func load() {
	recs, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil || len(recs) < 2 {
		panic(fmt.Sprintf("airports: embedded data unreadable: %v", err)) // caught by the tests
	}
	byCode = make(map[string]int, len(recs)-1)
	for _, r := range recs[1:] {
		a := Airport{Code: r[0], Name: r[1], City: r[2], Country: r[3], Kind: r[4], Scheduled: r[5] == "1"}
		byCode[a.Code] = len(all)
		all = append(all, a)
		keys = append(keys, " "+normalize(a.City+" "+a.Name)+" ")
	}
}

// Count is the number of known airports.
func Count() int {
	loadOnce.Do(load)
	return len(all)
}

// Lookup finds an airport by IATA code, in any case.
func Lookup(code string) (Airport, bool) {
	loadOnce.Do(load)
	i, ok := byCode[strings.ToUpper(strings.TrimSpace(code))]
	if !ok {
		return Airport{}, false
	}
	return all[i], true
}

var kindRank = map[string]int{"L": 0, "M": 1, "S": 2, "W": 3, "H": 4}

// Search returns up to limit airports with airline flights whose city or name has words
// starting with term ("paris", "koln", "frank"), largest first. Accents and case are ignored.
func Search(term string, limit int) []Airport {
	loadOnce.Do(load)
	t := normalize(term)
	if len([]rune(t)) < 3 {
		return nil
	}
	var found []Airport
	for i, a := range all {
		if a.Scheduled && strings.Contains(keys[i], " "+t) {
			found = append(found, a)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return kindRank[found[i].Kind] < kindRank[found[j].Kind] })
	if len(found) > limit {
		found = found[:limit]
	}
	return found
}

var folds = strings.NewReplacer(
	"ä", "a", "à", "a", "á", "a", "â", "a", "ã", "a", "å", "a", "æ", "ae",
	"ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ñ", "n",
	"ö", "o", "ó", "o", "ò", "o", "ô", "o", "õ", "o", "ø", "o", "œ", "oe",
	"ü", "u", "ú", "u", "ù", "u", "û", "u", "ß", "ss", "ý", "y", "ÿ", "y",
)

// normalize lowercases s, folds common accents and turns punctuation into single spaces.
func normalize(s string) string {
	s = folds.Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}
