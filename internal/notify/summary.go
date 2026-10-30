package notify

import (
	"fmt"
	"html"
	"strings"
)

type PriceRow struct {
	Outbound string
	Return   string
	StayDays int
	Price    int
	URL      string
}

type Summary struct {
	Name        string
	Date        string
	DatePairs   int        // date pairs priced today
	AnyTitle    string     // e.g. "Moins chers (≤ 1 escale)"
	DirectTitle string     // e.g. "Vol direct Star Alliance"
	Cheapest    []PriceRow // cheapest pairs, at most max_stops
	BestDirect  *PriceRow  // cheapest direct pair, if watched and found
	UsualAny    int        // usual best prices, 0 if not enough history
	UsualDir    int
	WeekAgoAny  int // best prices 7 days ago, 0 if unknown
	WeekAgoDir  int
	RequestsWk  int // API requests over the last 7 days
	AlertsWk    int
	DaysTracked int
	Credits     int // remaining SearchApi credits, -1 if unknown
	PerRun      int // requests used by one run
}

// RenderSummary formats the periodic summary as one Telegram HTML message.
func RenderSummary(s Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Résumé %s</b> (%s)\n", html.EscapeString(s.Name), s.Date)
	fmt.Fprintf(&b, "%d combinaisons de dates suivies à chaque lancement.\n", s.DatePairs)

	if len(s.Cheapest) > 0 {
		fmt.Fprintf(&b, "\n<b>%s</b>%s\n", html.EscapeString(s.AnyTitle), trend(s.Cheapest[0].Price, s.WeekAgoAny))
		for _, r := range s.Cheapest {
			fmt.Fprintf(&b, "• %s\n", row(r))
		}
		if s.UsualAny > 0 {
			fmt.Fprintf(&b, "Meilleur prix habituel : %d €\n", s.UsualAny)
		}
	} else {
		b.WriteString("\nAucun prix relevé aujourd'hui.\n")
	}

	if s.BestDirect != nil {
		fmt.Fprintf(&b, "\n<b>%s</b>%s\n• %s\n", html.EscapeString(s.DirectTitle), trend(s.BestDirect.Price, s.WeekAgoDir), row(*s.BestDirect))
		if s.UsualDir > 0 {
			fmt.Fprintf(&b, "Meilleur prix habituel : %d €\n", s.UsualDir)
		}
	}

	fmt.Fprintf(&b, "\n7 derniers jours : %d alerte(s), %d requête(s) API sur %d jour(s) de suivi.",
		s.AlertsWk, s.RequestsWk, s.DaysTracked)
	if s.Credits >= 0 {
		fmt.Fprintf(&b, "\nCrédits SearchApi restants : %d", s.Credits)
		if s.PerRun > 0 {
			fmt.Fprintf(&b, " (≈ %d lancements)", s.Credits/s.PerRun)
		}
	}
	return b.String()
}

func row(r PriceRow) string {
	text := fmt.Sprintf("<b>%d €</b> · %s → %s (%d j)", r.Price, r.Outbound, r.Return, r.StayDays)
	if r.URL != "" {
		text += fmt.Sprintf(` · <a href="%s">voir</a>`, html.EscapeString(r.URL))
	}
	return text
}

func trend(now, weekAgo int) string {
	switch {
	case weekAgo == 0 || now == 0:
		return ""
	case now < weekAgo:
		return fmt.Sprintf(" ↘️ %d € vs il y a 7 j", now-weekAgo)
	case now > weekAgo:
		return fmt.Sprintf(" ↗️ +%d € vs il y a 7 j", now-weekAgo)
	default:
		return " → stable vs il y a 7 j"
	}
}
