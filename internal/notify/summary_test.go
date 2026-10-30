package notify

import (
	"strings"
	"testing"
)

func TestRenderSummary(t *testing.T) {
	msg := RenderSummary(Summary{
		Date:      "2026-11-02",
		DatePairs: 275,
		Cheapest: []PriceRow{
			{Outbound: "2027-03-26", Return: "2027-04-08", StayDays: 13, Price: 1099, URL: "https://x?a=1&b=2"},
		},
		LowestEver: 1080, WeekAgoAny: 1150, RequestsWk: 56, AlertsWk: 1, DaysTracked: 7,
	})
	for _, want := range []string{
		"275 combinaisons", "<b>1099 €</b> · 2027-03-26 → 2027-04-08 (13 j)", "↘️ -51 €",
		"aucun vol trouvé", "Plus bas jamais vu : 1080 €", "56 requête(s)", `href="https://x?a=1&amp;b=2"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("summary missing %q:\n%s", want, msg)
		}
	}
}
