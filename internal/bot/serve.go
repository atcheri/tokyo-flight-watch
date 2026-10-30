package bot

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/notify"
)

// Poller fetches incoming messages; *notify.Telegram implements it.
type Poller interface {
	Updates(ctx context.Context, offset int64) ([]notify.Update, error)
}

const offsetKey = "telegram_offset"

// Serve handles incoming messages until ctx is cancelled. The offset of the last handled
// message is stored, so a restart neither loses nor replays messages.
func (b *Bot) Serve(ctx context.Context, p Poller) {
	stored, err := b.Store.Meta(ctx, offsetKey)
	if err != nil {
		log.Printf("read telegram offset: %v", err)
	}
	offset, _ := strconv.ParseInt(stored, 10, 64)
	for ctx.Err() == nil {
		updates, err := p.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("telegram updates: %v", err)
				sleep(ctx, 5*time.Second)
			}
			continue
		}
		for _, u := range updates {
			b.handleSafely(ctx, u)
			offset = u.ID + 1
			if err := b.Store.SetMeta(ctx, offsetKey, strconv.FormatInt(offset, 10)); err != nil {
				log.Printf("save telegram offset: %v", err)
			}
		}
	}
}

func (b *Bot) handleSafely(ctx context.Context, u notify.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic handling update %d: %v", u.ID, r)
		}
	}()
	b.HandleUpdate(ctx, u)
}

// Schedule runs due watches every interval until ctx is cancelled.
func (b *Bot) Schedule(ctx context.Context, interval time.Duration) {
	for ctx.Err() == nil {
		b.RunDue(ctx)
		sleep(ctx, interval)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
