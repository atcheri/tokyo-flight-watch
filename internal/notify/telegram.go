package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// telegramMaxLen is Telegram's message limit (4096) minus some margin.
const telegramMaxLen = 4000

type Telegram struct {
	Token  string
	ChatID string
	HTTP   *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// RenderTelegram formats deals as Telegram HTML messages, split to fit the size limit.
func RenderTelegram(deals []DealRow) []string {
	subject, _, _ := Render(deals)
	header := "✈️ <b>" + html.EscapeString(subject) + "</b>"
	if len(deals) == 0 {
		return []string{header + "\nAucune offre ne correspond aux critères (message de test)."}
	}

	var msgs []string
	current := header
	for _, d := range deals {
		stops := "direct"
		if d.Stops < 0 {
			stops = "escales non vérifiées"
		} else if d.Stops > 0 {
			stops = fmt.Sprintf("%d escale(s)", d.Stops)
		}
		var b strings.Builder
		b.WriteString("\n")
		if d.Title != "" {
			fmt.Fprintf(&b, "\n<u>%s</u>", html.EscapeString(d.Title))
		}
		fmt.Fprintf(&b, "\n<b>%d €</b> · %s → %s · %s\n", d.Price, html.EscapeString(d.Origin), html.EscapeString(d.Destination), stops)
		fmt.Fprintf(&b, "📅 %s → %s (%d j)\n", d.Outbound, d.Return, d.StayDays)
		if d.Airlines != "" {
			fmt.Fprintf(&b, "🛫 %s\n", html.EscapeString(d.Airlines))
		}
		fmt.Fprintf(&b, "💡 %s", html.EscapeString(d.Reasons))
		if d.Note != "" {
			fmt.Fprintf(&b, "\nℹ️ %s", html.EscapeString(d.Note))
		}
		if d.Typical != "" {
			fmt.Fprintf(&b, "\n📊 Google : %s", html.EscapeString(d.Typical))
		}
		if d.URL != "" {
			fmt.Fprintf(&b, "\n<a href=\"%s\">Voir sur Google Flights</a>", html.EscapeString(d.URL))
		}
		block := b.String()
		if len(current)+len(block) > telegramMaxLen {
			msgs = append(msgs, current)
			current = strings.TrimPrefix(block, "\n\n")
			continue
		}
		current += block
	}
	return append(msgs, current)
}

func (t *Telegram) Send(ctx context.Context, messages []string) error {
	for _, text := range messages {
		form := url.Values{
			"chat_id":                  {t.ChatID},
			"text":                     {text},
			"parse_mode":               {"HTML"},
			"disable_web_page_preview": {"true"},
		}
		var out struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if err := t.call(ctx, "sendMessage", form, &out); err != nil {
			return err
		}
		if !out.OK {
			return fmt.Errorf("telegram sendMessage: %s", out.Description)
		}
	}
	return nil
}

type Chat struct {
	ID    int64
	Title string
}

// RecentChats lists chats that recently messaged the bot, to discover the chat ID.
func (t *Telegram) RecentChats(ctx context.Context) ([]Chat, error) {
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			Message *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Title     string `json:"title"`
					FirstName string `json:"first_name"`
					Username  string `json:"username"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := t.call(ctx, "getUpdates", url.Values{}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", out.Description)
	}
	seen := map[int64]bool{}
	var chats []Chat
	for _, u := range out.Result {
		if u.Message == nil || seen[u.Message.Chat.ID] {
			continue
		}
		c := u.Message.Chat
		seen[c.ID] = true
		title := c.Title
		if title == "" {
			title = strings.TrimSpace(c.FirstName + " @" + c.Username)
		}
		chats = append(chats, Chat{ID: c.ID, Title: title})
	}
	return chats, nil
}

func (t *Telegram) call(ctx context.Context, method string, form url.Values, out any) error {
	endpoint := "https://api.telegram.org/bot" + t.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram %s: invalid request", method) // the URL contains the token
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		// Don't wrap: *url.Error would print the URL, which contains the token.
		return fmt.Errorf("telegram %s request failed", method)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("telegram %s: decode response (status %d): %w", method, resp.StatusCode, err)
	}
	return nil
}
