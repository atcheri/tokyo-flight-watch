package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// telegramMaxLen is Telegram's message limit (4096) minus some margin.
const telegramMaxLen = 4000

type Telegram struct {
	BaseURL string // Bot API base URL, overridable in tests
	Token   string
	ChatID  string // default chat for Send
	HTTP    *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	// The timeout leaves room for long polling (getUpdates waits up to pollTimeout).
	return &Telegram{BaseURL: "https://api.telegram.org", Token: token, ChatID: chatID, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// RenderTelegram formats deals as Telegram HTML messages, split to fit the size limit.
func RenderTelegram(name string, deals []DealRow) []string {
	subject, _, _ := Render(name, deals)
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

// Send sends messages to the default chat.
func (t *Telegram) Send(ctx context.Context, messages []string) error {
	return t.SendTo(ctx, t.ChatID, messages...)
}

// SendTo sends HTML messages to chatID.
func (t *Telegram) SendTo(ctx context.Context, chatID string, messages ...string) error {
	for _, text := range messages {
		form := url.Values{
			"chat_id":                  {chatID},
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

// Update is an incoming text message.
type Update struct {
	ID        int64
	ChatID    int64
	MessageID int64
	Text      string
	Name      string // sender's first name or username
	Private   bool   // sent in a one-to-one chat
}

const pollTimeout = 50 // seconds

// Updates long-polls for messages after offset (the last update ID + 1).
func (t *Telegram) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				MessageID int64  `json:"message_id"`
				Text      string `json:"text"`
				Chat      struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
				From struct {
					FirstName string `json:"first_name"`
					Username  string `json:"username"`
				} `json:"from"`
			} `json:"message"`
		} `json:"result"`
	}
	form := url.Values{
		"offset":          {strconv.FormatInt(offset, 10)},
		"timeout":         {strconv.Itoa(pollTimeout)},
		"allowed_updates": {`["message"]`},
	}
	if err := t.call(ctx, "getUpdates", form, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", out.Description)
	}
	updates := make([]Update, 0, len(out.Result))
	for _, r := range out.Result {
		u := Update{ID: r.UpdateID}
		if m := r.Message; m != nil {
			u.ChatID, u.MessageID, u.Text = m.Chat.ID, m.MessageID, m.Text
			u.Private = m.Chat.Type == "private"
			u.Name = m.From.FirstName
			if u.Name == "" {
				u.Name = m.From.Username
			}
		}
		updates = append(updates, u)
	}
	return updates, nil
}

// DeleteMessage deletes a message, e.g. one containing a secret.
func (t *Telegram) DeleteMessage(ctx context.Context, chatID string, messageID int64) error {
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	form := url.Values{"chat_id": {chatID}, "message_id": {strconv.FormatInt(messageID, 10)}}
	if err := t.call(ctx, "deleteMessage", form, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("telegram deleteMessage: %s", out.Description)
	}
	return nil
}

// SetCommands publishes the command menu shown by Telegram clients.
func (t *Telegram) SetCommands(ctx context.Context, commands [][2]string) error {
	type command struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	list := make([]command, len(commands))
	for i, c := range commands {
		list[i] = command{c[0], c[1]}
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := t.call(ctx, "setMyCommands", url.Values{"commands": {string(data)}}, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("telegram setMyCommands: %s", out.Description)
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
	endpoint := t.BaseURL + "/bot" + t.Token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram %s: invalid request", method) // the URL contains the token
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s request failed: %s", method, t.redact(err))
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("telegram %s: decode response (status %d): %w", method, resp.StatusCode, err)
	}
	return nil
}

// redact describes a transport error without the request URL, which contains the token.
func (t *Telegram) redact(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err // e.g. "context deadline exceeded", "dial tcp …: connection refused"
	}
	msg := err.Error()
	if t.Token != "" {
		msg = strings.ReplaceAll(msg, t.Token, "<token>")
	}
	return msg
}
