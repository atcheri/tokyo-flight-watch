package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAlertHandler(t *testing.T) {
	var out bytes.Buffer
	sent := make(chan string, 10)
	h := NewAlertHandler(slog.NewTextHandler(&out, nil), time.Hour, func(text string) error {
		sent <- text
		return nil
	})
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	h.shared.now = func() time.Time { return now }
	lg := slog.New(h).With("watch", 1)

	lg.Info("fine")
	lg.Error("calendar request failed", "err", "api status 500 <html>")
	got := <-sent
	if !strings.Contains(got, `msg="calendar request failed" watch=1 err="api status 500 &lt;html&gt;"`) {
		t.Fatalf("alert = %q", got)
	}

	// Within the hour: logged, counted, not sent.
	lg.Error("second")
	lg.Error("third")
	now = now.Add(61 * time.Minute)
	lg.Error("fourth")
	got = <-sent
	if !strings.Contains(got, "fourth") || !strings.Contains(got, "+ 2 autre(s) erreur(s)") {
		t.Fatalf("alert = %q", got)
	}
	select {
	case extra := <-sent:
		t.Fatalf("unexpected alert %q", extra)
	default:
	}
	for _, msg := range []string{"fine", "second", "third", "fourth"} {
		if !strings.Contains(out.String(), msg) {
			t.Errorf("%q missing from the regular logs", msg)
		}
	}
}

func TestAlertHandlerReportsSendFailure(t *testing.T) {
	var out syncBuffer
	done := make(chan struct{})
	h := NewAlertHandler(slog.NewTextHandler(&out, nil), time.Hour, func(string) error {
		defer close(done)
		return errors.New("telegram down")
	})
	slog.New(h).Error("boom")
	<-done
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(out.String(), `level=WARN msg="send error alert" err="telegram down"`) {
		if time.Now().After(deadline) {
			t.Fatalf("logs = %q", out.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// syncBuffer is a bytes.Buffer safe for the alert goroutine and the test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
