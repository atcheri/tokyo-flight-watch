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
	Date        string
	DatePairs   int        // date pairs priced today
	Cheapest    []PriceRow // cheapest pairs, at most max_stops
	BestDirect  *PriceRow  // cheapest nonstop Star Alliance pair, if any
	LowestEver  int
	WeekAgoAny  int // best prices 7 days ago, 0 if unknown
	WeekAgoDir  int
	RequestsWk  int // API requests over the last 7 days
	AlertsWk    int
	DaysTracked int
}

// RenderSummary formats the weekly summary as one Telegram HTML message.
func RenderSummary(s Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Résumé hebdo vols Tokyo</b> (%s)\n", s.Date)
	fmt.Fprintf(&b, "%d combinaisons de dates suivies chaque jour.\n", s.DatePairs)

	if len(s.Cheapest) > 0 {
		fmt.Fprintf(&b, "\n<b>Moins chers (≤ 1 escale)</b>%s\n", trend(s.Cheapest[0].Price, s.WeekAgoAny))
		for _, r := range s.Cheapest {
			fmt.Fprintf(&b, "• %s\n", row(r))
		}
	} else {
		b.WriteString("\nAucun prix relevé aujourd'hui.\n")
	}

	b.WriteString("\n<b>Direct Star Alliance</b>")
	if s.BestDirect != nil {
		fmt.Fprintf(&b, "%s\n• %s\n", trend(s.BestDirect.Price, s.WeekAgoDir), row(*s.BestDirect))
	} else {
		b.WriteString("\n• aucun vol trouvé sur la période\n")
	}

	if s.LowestEver > 0 {
		fmt.Fprintf(&b, "\nPlus bas jamais vu : %d €\n", s.LowestEver)
	}
	fmt.Fprintf(&b, "7 derniers jours : %d alerte(s), %d requête(s) API sur %d jour(s) de suivi.",
		s.AlertsWk, s.RequestsWk, s.DaysTracked)
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
