package bot

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

var (
	airportRe  = regexp.MustCompile(`^[A-Za-z]{3}$`)
	dateRe     = regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b|\b(\d{1,2})[./](\d{1,2})[./](\d{4})\b`)
	numRangeRe = regexp.MustCompile(`^(\d+)\s*(?:-|–|à|a|to|bis)\s*(\d+)$`)
	plusMinus  = regexp.MustCompile(`^(\d+)\s*(?:±|\+/-|\+-)\s*(\d+)$`)
	nonWords   = map[string]bool{"non": true, "no": true, "aucun": true, "aucune": true, "nein": true, "-": true, "0": true}
	yesWords   = map[string]bool{"oui": true, "o": true, "yes": true, "y": true, "ok": true, "ja": true}
)

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isNo(s string) bool  { return nonWords[normalize(s)] }
func isYes(s string) bool { return yesWords[normalize(s)] }

// parseAirports reads 1 to 5 IATA codes separated by commas or spaces ("FRA, cgn DUS").
func parseAirports(s string) ([]string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '/' || r == ' ' })
	var codes []string
	seen := map[string]bool{}
	for _, f := range fields {
		if !airportRe.MatchString(f) {
			return nil, fmt.Errorf("« %s » n'est pas un code d'aéroport à 3 lettres", f)
		}
		code := strings.ToUpper(f)
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	switch {
	case len(codes) == 0:
		return nil, errors.New("indiquez au moins un code d'aéroport, par exemple FRA")
	case len(codes) > 5:
		return nil, errors.New("5 aéroports au maximum")
	}
	return codes, nil
}

// window is a departure window, fixed (From/To) or relative (InMin/InMax days after the run).
type window struct {
	From, To     string
	InMin, InMax int
}

// maxHorizonDays is how far ahead Google Flights has fares.
const maxHorizonDays = 330

// parseWindow reads "22.03.2027-15.04.2027", "2027-03-22 2027-04-15" or "dans 60-150 jours".
func parseWindow(s string, today time.Time) (window, error) {
	if m := dateRe.FindAllStringSubmatch(s, -1); len(m) > 0 {
		if len(m) != 2 {
			return window{}, errors.New("indiquez deux dates : la première et la dernière date de départ possibles")
		}
		from, err := matchDate(m[0])
		if err != nil {
			return window{}, err
		}
		to, err := matchDate(m[1])
		if err != nil {
			return window{}, err
		}
		switch {
		case to.Before(from):
			return window{}, errors.New("la seconde date doit être après la première")
		case !from.After(today):
			return window{}, errors.New("la première date doit être dans le futur")
		case to.After(today.AddDate(0, 0, maxHorizonDays)):
			return window{}, fmt.Errorf("les vols ne sont proposés qu'environ %d jours à l'avance", maxHorizonDays)
		}
		return window{From: from.Format(dateLayout), To: to.Format(dateLayout)}, nil
	}

	text := normalize(s)
	for _, word := range []string{"dans", "in", "jours", "jour", "days", "day", "j"} {
		text = strings.ReplaceAll(" "+text+" ", " "+word+" ", " ")
	}
	lo, hi, err := parseRangeText(strings.TrimSpace(text))
	if err != nil {
		return window{}, errors.New("format non reconnu : « 22.03.2027-15.04.2027 » ou « dans 60-150 jours »")
	}
	switch {
	case lo < 1:
		return window{}, errors.New("le départ doit être au moins dans 1 jour")
	case hi > maxHorizonDays:
		return window{}, fmt.Errorf("les vols ne sont proposés qu'environ %d jours à l'avance", maxHorizonDays)
	}
	return window{InMin: lo, InMax: hi}, nil
}

func matchDate(m []string) (time.Time, error) {
	y, mo, d := m[1], m[2], m[3]
	if y == "" {
		y, mo, d = m[6], m[5], m[4]
	}
	t, err := time.Parse("2006-1-2", y+"-"+mo+"-"+d)
	if err != nil {
		return time.Time{}, fmt.Errorf("date invalide : %s", m[0])
	}
	return t, nil
}

// parseRange reads "10-14", "10 à 14", "12±3" or "10" into an inclusive range of days.
func parseRange(s string) (lo, hi int, err error) {
	lo, hi, err = parseRangeText(normalize(strings.TrimSuffix(strings.TrimSuffix(normalize(s), "jours"), "j")))
	if err != nil {
		return 0, 0, errors.New("format non reconnu : « 10-14 », « 12±3 » ou « 10 »")
	}
	if lo < 1 || hi > 60 {
		return 0, 0, errors.New("la durée doit être entre 1 et 60 jours")
	}
	return lo, hi, nil
}

func parseRangeText(s string) (lo, hi int, err error) {
	s = strings.TrimSpace(s)
	if m := numRangeRe.FindStringSubmatch(s); m != nil {
		lo, _ = strconv.Atoi(m[1])
		hi, _ = strconv.Atoi(m[2])
	} else if m := plusMinus.FindStringSubmatch(s); m != nil {
		mid, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		lo, hi = mid-d, mid+d
	} else if n, convErr := strconv.Atoi(s); convErr == nil {
		lo, hi = n, n
	} else {
		return 0, 0, errors.New("not a range")
	}
	if hi < lo {
		return 0, 0, errors.New("reversed range")
	}
	return lo, hi, nil
}

// parseStops reads 0, 1, 2 or "direct".
func parseStops(s string) (int, error) {
	switch normalize(s) {
	case "0", "direct", "directs", "aucune":
		return 0, nil
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	}
	return 0, errors.New("répondez 0 (direct), 1 ou 2")
}

// parsePrice reads a price in euros ("950", "950 €"), or "non" for no target (0).
func parsePrice(s string) (int, error) {
	if isNo(s) {
		return 0, nil
	}
	text := strings.NewReplacer("€", "", "eur", "", " ", "", "'", "", ".", "").Replace(normalize(s))
	n, err := strconv.Atoi(text)
	if err != nil || n < 50 || n > 20000 {
		return 0, errors.New("indiquez un prix en euros (par exemple 950), ou « non »")
	}
	return n, nil
}

var allianceWords = map[string]string{
	"star": "STAR_ALLIANCE", "star alliance": "STAR_ALLIANCE", "staralliance": "STAR_ALLIANCE",
	"oneworld": "ONEWORLD", "one world": "ONEWORLD", "skyteam": "SKYTEAM", "sky team": "SKYTEAM",
}

// parseAlliance reads an alliance name, or "non" (empty result) to skip direct flights.
func parseAlliance(s string) (string, error) {
	if isNo(s) {
		return "", nil
	}
	if a, ok := allianceWords[normalize(s)]; ok {
		return a, nil
	}
	return "", errors.New("répondez star, oneworld, skyteam ou non")
}

// parseEvery reads the number of days between checks (1 to 7).
func parseEvery(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(normalize(s), "j")))
	if err != nil || n < 1 || n > 7 {
		return 0, errors.New("indiquez un nombre de jours entre 1 et 7")
	}
	return n, nil
}
