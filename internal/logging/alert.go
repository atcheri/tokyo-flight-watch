package logging

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// escapeHTML escapes what Telegram's HTML mode requires, keeping quotes readable.
var escapeHTML = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// maxAlertLen keeps an alert well under Telegram's 4096-character message limit.
const maxAlertLen = 3000

// AlertHandler passes every record to the next handler and also sends error records to the
// operator, at most one alert per interval; the errors skipped meanwhile are counted in the next one.
type AlertHandler struct {
	next   slog.Handler
	format slog.Handler // renders a record as one key=value line into shared.buf
	shared *alertState
}

type alertState struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	send      func(text string) error
	every     time.Duration
	now       func() time.Time
	last      time.Time
	skipped   int
	errorsLog slog.Handler // where failures to send an alert are reported
}

// NewAlertHandler wraps next. send receives Telegram HTML and runs in its own goroutine; it must
// not log at error level itself, or a failing channel would keep alerting about itself.
func NewAlertHandler(next slog.Handler, every time.Duration, send func(text string) error) *AlertHandler {
	st := &alertState{send: send, every: every, now: time.Now, errorsLog: next}
	return &AlertHandler{
		next:   next,
		format: slog.NewTextHandler(&st.buf, &slog.HandlerOptions{ReplaceAttr: dropTime}),
		shared: st,
	}
}

func dropTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{}
	}
	return a
}

func (h *AlertHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelError || h.next.Enabled(ctx, level)
}

func (h *AlertHandler) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if h.next.Enabled(ctx, r.Level) {
		err = h.next.Handle(ctx, r)
	}
	if r.Level >= slog.LevelError {
		h.alert(ctx, r)
	}
	return err
}

func (h *AlertHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &AlertHandler{next: h.next.WithAttrs(attrs), format: h.format.WithAttrs(attrs), shared: h.shared}
}

func (h *AlertHandler) WithGroup(name string) slog.Handler {
	return &AlertHandler{next: h.next.WithGroup(name), format: h.format.WithGroup(name), shared: h.shared}
}

func (h *AlertHandler) alert(ctx context.Context, r slog.Record) {
	st := h.shared
	st.mu.Lock()
	now := st.now()
	if !st.last.IsZero() && now.Sub(st.last) < st.every {
		st.skipped++
		st.mu.Unlock()
		return
	}
	st.buf.Reset()
	_ = h.format.Handle(ctx, r)
	line := strings.TrimSpace(st.buf.String())
	skipped := st.skipped
	st.last, st.skipped = now, 0
	st.mu.Unlock()

	if len(line) > maxAlertLen {
		line = line[:maxAlertLen] + "…"
	}
	text := "🚨 <b>Erreur du bot</b>\n<code>" + escapeHTML.Replace(line) + "</code>"
	if skipped > 0 {
		text += fmt.Sprintf("\n(+ %d autre(s) erreur(s) non envoyée(s) depuis la dernière alerte, voir les journaux)", skipped)
	}
	go func() {
		if err := st.send(text); err != nil {
			rec := slog.NewRecord(time.Now(), slog.LevelWarn, "send error alert", 0)
			rec.AddAttrs(slog.Any("err", err))
			_ = st.errorsLog.Handle(context.Background(), rec)
		}
	}()
}
