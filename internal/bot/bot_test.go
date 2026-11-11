package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/secret"
	"github.com/atch/tokyo-flight-watch/internal/store"
)

const chat = 42

// fakeMessenger records what the bot sends.
type fakeMessenger struct {
	mu      sync.Mutex
	sent    []string
	deleted []int64
}

func (f *fakeMessenger) SendTo(_ context.Context, chatID string, messages ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, messages...)
	return nil
}

func (f *fakeMessenger) DeleteMessage(_ context.Context, _ string, id int64) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeMessenger) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// fakeSearchAPI accepts the key "good-key", prices every calendar pair at 900 € (1250 € for
// direct flights) and returns one offer per detailed search.
func fakeSearchAPI(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"Invalid API key."}`))
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/me":
			w.Write([]byte(`{"account":{"remaining_credits":90}}`))
		case q.Get("engine") == "google_flights_calendar":
			price := 900
			if q.Get("stops") == "nonstop" {
				price = 1250
			}
			var cal []map[string]any
			out, _ := time.Parse(dateLayout, q.Get("outbound_date"))
			outEnd, _ := time.Parse(dateLayout, q.Get("outbound_date_end"))
			for d := out; !d.After(outEnd); d = d.AddDate(0, 0, 1) {
				for stay := 7; stay <= 8; stay++ {
					cal = append(cal, map[string]any{"departure": d.Format(dateLayout), "return": d.AddDate(0, 0, stay).Format(dateLayout), "price": price})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"calendar": cal})
		default:
			w.Write([]byte(`{"best_flights":[{"price":905,"flights":[{"departure_airport":{"id":"FRA"},"arrival_airport":{"id":"IST"},"airline":"Turkish Airlines"},{"departure_airport":{"id":"IST"},"arrival_airport":{"id":"HND"},"airline":"Turkish Airlines"}]}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestBot(t *testing.T, now *time.Time) (*Bot, *fakeMessenger) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	api := fakeSearchAPI(t)
	msg := &fakeMessenger{}
	return &Bot{
		Msg: msg, Store: st, Box: box, InviteCode: "sakura",
		NewClient: func(key string) *flightapi.Client {
			c := flightapi.NewClient(key)
			c.BaseURL = api.URL
			return c
		},
		Now: func() time.Time { return *now },
	}, msg
}

func TestBotJourney(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	b, msg := newTestBot(t, &now)
	nextID := int64(0)
	say := func(text string) string {
		t.Helper()
		nextID++
		b.HandleUpdate(ctx, notify.Update{ID: nextID, ChatID: chat, MessageID: nextID, Text: text, Name: "Alice", Private: true})
		return msg.last()
	}
	expect := func(reply, want string) {
		t.Helper()
		if !strings.Contains(reply, want) {
			t.Fatalf("reply %q does not contain %q", reply, want)
		}
	}

	// Strangers need the invite code.
	expect(say("bonjour"), "Ce bot est privé")
	expect(say("/start wrong"), "Ce bot est privé")
	expect(say("/start sakura"), "Bienvenue Alice")

	// The key message is deleted, checked and stored encrypted.
	expect(say("/cle"), "Collez votre clé")
	expect(say("bad-key"), "SearchApi refuse cette clé")
	say("/cle")
	keyMsg := nextID + 1
	expect(say("good-key"), "Crédits restants : 90")
	if len(msg.deleted) != 2 || msg.deleted[1] != keyMsg {
		t.Fatalf("deleted messages = %v", msg.deleted)
	}
	u, _ := b.Store.User(ctx, chat)
	if bytes.Contains(u.APIKey, []byte("good-key")) {
		t.Fatal("API key stored in clear")
	}

	// Wizard, with a wrong answer and a too-wide window on the way.
	expect(say("/nouveau"), "1/9")
	expect(say("Tokyo"), "2/9")
	expect(say("Frankfurt"), "pas un code")
	expect(say("fra, cgn"), "✓ CGN – Cologne Bonn Airport")
	expect(say("HND NRT"), "✓ NRT – Narita International Airport")
	expect(say("dans 30-200 jours"), "5/9")
	expect(say("7-8"), "6/9")
	expect(say("1"), "7/9")
	expect(say("950"), "8/9")
	expect(say("star"), "Prix cible en € pour ces vols directs")
	expect(say("1300"), "9/9")
	expect(say("3"), "trop large")
	reply := say("01.12.2026-05.12.2026")
	expect(reply, "FRA/CGN → HND/NRT · départ 01.12.2026–05.12.2026 · 7–8 j")
	expect(reply, "Coût : 2 requête(s) SearchApi par vérification")
	expect(reply, "environ <b>20 par mois</b>")
	expect(say("oui"), "Suivi n°1 créé")

	// The scheduler runs the new watch and sends the alert and the first summary.
	msg.sent = nil
	b.RunDue(ctx)
	all := strings.Join(msg.sent, "\n---\n")
	expect(all, "Tokyo : 2 offre(s) dès 905 €")
	expect(all, "Résumé Tokyo")
	w, _ := b.Store.Watch(ctx, chat, 1)
	if !w.NextRun.Equal(now.AddDate(0, 0, 3)) || w.LastError != "" || w.History == "" {
		t.Fatalf("after run: %+v", w)
	}

	// Nothing is due until the next run; a manual check says nothing changed.
	msg.sent = nil
	b.RunDue(ctx)
	if len(msg.sent) != 0 {
		t.Fatalf("unexpected messages %q", msg.sent)
	}
	say("/verifier 1")
	expect(msg.last(), "pas de nouvelle bonne affaire")
	expect(msg.last(), "Meilleur prix actuel : 900 € · direct 1250 €")

	expect(say("/liste"), "#1 Tokyo")
	expect(say("/pause 1"), "en pause")
	now = now.AddDate(0, 0, 4)
	msg.sent = nil
	b.RunDue(ctx)
	if len(msg.sent) != 0 {
		t.Fatal("a paused watch must not run")
	}
	expect(say("/reprendre 1"), "relancé")
	expect(say("/pause 7"), "Aucun suivi n°7")

	expect(say("/oublier"), "/oublier confirmer")
	expect(say("/oublier confirmer"), "supprimées")
	expect(say("/liste"), "Ce bot est privé")
}

func TestRunWatchWithoutKeyRetriesLater(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	b, msg := newTestBot(t, &now)
	if err := b.Store.CreateUser(ctx, chat, "Bob", now); err != nil {
		t.Fatal(err)
	}
	id, _ := b.Store.CreateWatch(ctx, store.Watch{ChatID: chat, EveryDays: 1, NextRun: now, CreatedAt: now,
		Config: "name: X\norigins: [FRA]\ndestinations: [HND]\ndeparture_from: 2026-12-01\ndeparture_to: 2026-12-05\nmin_stay_days: 7\nmax_stay_days: 8\n"})
	b.RunDue(ctx)
	if !strings.Contains(msg.last(), "aucune clé SearchApi") {
		t.Fatalf("reply = %q", msg.last())
	}
	w, _ := b.Store.Watch(ctx, chat, id)
	if !w.NextRun.Equal(now.Add(retryWithoutKey)) || w.LastError == "" {
		t.Fatalf("watch = %+v", w)
	}
}

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][2]string{
		"/pause@VolsBot 3": {"/pause", "3"},
		"/LISTE":           {"/liste", ""},
		"hello":            {"", "hello"},
	} {
		if cmd, arg := splitCommand(in); cmd != want[0] || arg != want[1] {
			t.Errorf("%q = %q, %q", in, cmd, arg)
		}
	}
}
