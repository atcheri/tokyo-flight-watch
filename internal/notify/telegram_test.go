package notify

import (
	"strings"
	"testing"
)

func TestRenderTelegram(t *testing.T) {
	deal := DealRow{
		Outbound: "2027-03-22", Return: "2027-04-01", StayDays: 10,
		Origin: "FRA", Destination: "HND", Price: 1250, Airlines: "Lufthansa",
		Reasons: "direct Star Alliance ≤ 1300 €", Typical: "950–1400 €",
		URL: "https://www.google.com/travel/flights?a=1&b=2",
	}
	msgs := RenderTelegram([]DealRow{deal})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	for _, want := range []string{"<b>1250 €</b>", "FRA → HND · direct", "(10 j)", `href="https://www.google.com/travel/flights?a=1&amp;b=2"`} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("message missing %q:\n%s", want, msgs[0])
		}
	}

	many := make([]DealRow, 60)
	for i := range many {
		many[i] = deal
	}
	msgs = RenderTelegram(many)
	if len(msgs) < 2 {
		t.Fatalf("expected split into several messages, got %d", len(msgs))
	}
	for i, m := range msgs {
		if len(m) > telegramMaxLen {
			t.Errorf("message %d too long: %d", i, len(m))
		}
	}
}
