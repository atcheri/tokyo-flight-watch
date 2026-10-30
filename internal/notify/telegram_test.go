package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	msgs := RenderTelegram("Vols Tokyo", []DealRow{deal})
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
	msgs = RenderTelegram("Vols Tokyo", many)
	if len(msgs) < 2 {
		t.Fatalf("expected split into several messages, got %d", len(msgs))
	}
	for i, m := range msgs {
		if len(m) > telegramMaxLen {
			t.Errorf("message %d too long: %d", i, len(m))
		}
	}
}

func TestTelegramAPI(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		calls = append(calls, r.URL.Path+" "+r.Form.Encode())
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"message_id":3,"text":"/start abc",
				"chat":{"id":42,"type":"private"},"from":{"first_name":"Alice"}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage") && r.Form.Get("chat_id") == "0":
			w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	tg := NewTelegram("TOKEN", "1")
	tg.BaseURL = srv.URL
	ctx := context.Background()

	ups, err := tg.Updates(ctx, 5)
	if err != nil || len(ups) != 1 || ups[0] != (Update{ID: 7, ChatID: 42, MessageID: 3, Text: "/start abc", Name: "Alice", Private: true}) {
		t.Fatalf("updates = %+v, %v", ups, err)
	}
	if err := tg.SendTo(ctx, "42", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := tg.SendTo(ctx, "0", "x"); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected API error, got %v", err)
	}
	if err := tg.DeleteMessage(ctx, "42", 3); err != nil {
		t.Fatal(err)
	}
	if err := tg.SetCommands(ctx, [][2]string{{"aide", "Aide"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(calls[0], "/botTOKEN/getUpdates") || !strings.Contains(calls[0], "offset=5") || len(calls) != 6 {
		t.Fatalf("calls = %q", calls)
	}
}
