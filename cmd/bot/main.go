// Command bot runs the shared Telegram flight-watch bot: invited users register their own
// SearchApi key and create watches, which are checked on their schedule.
//
// Environment:
//
//	TELEGRAM_BOT_TOKEN  token from @BotFather (required)
//	BOT_SECRET_KEY      base64 32-byte key encrypting users' API keys, e.g. `openssl rand -base64 32` (required)
//	INVITE_CODE         code new users send with /start (required)
//	DATA_DIR            directory for the database and its daily backups (default /data)
//	MAX_WATCHES         watches per user (default 5)
//	LOG_FORMAT          text (default) or json; LOG_LEVEL: debug, info (default), warn or error
//	ADMIN_CHAT_ID       Telegram chat that receives error alerts, at most one per hour (optional)
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // Europe/Paris in minimal containers

	"github.com/atch/tokyo-flight-watch/internal/bot"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/logging"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/secret"
	"github.com/atch/tokyo-flight-watch/internal/store"
)

const (
	scheduleInterval = 2 * time.Minute
	alertEvery       = time.Hour
	backupsKept      = 7
)

func main() {
	logging.Setup()
	token := mustEnv("TELEGRAM_BOT_TOKEN")
	invite := mustEnv("INVITE_CODE")
	box, err := secret.New(mustEnv("BOT_SECRET_KEY"))
	if err != nil {
		logging.Fatal("invalid BOT_SECRET_KEY", "err", err)
	}
	dataDir := envOr("DATA_DIR", "/data")
	maxWatches, _ := strconv.Atoi(os.Getenv("MAX_WATCHES"))

	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
		logging.Fatal("create data directory", "dir", dataDir, "err", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "bot.db"))
	if err != nil {
		logging.Fatal("open database", "err", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tg := notify.NewTelegram(token, "")
	adminChat := os.Getenv("ADMIN_CHAT_ID")
	if adminChat != "" {
		alerts := logging.NewAlertHandler(slog.Default().Handler(), alertEvery, func(text string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return tg.SendTo(ctx, adminChat, text)
		})
		slog.SetDefault(slog.New(alerts))
	}
	if err := tg.SetCommands(ctx, bot.Commands); err != nil {
		slog.Warn("set command menu", "err", err)
	}
	b := &bot.Bot{
		Msg: tg, Store: st, Box: box, InviteCode: invite, MaxWatches: maxWatches,
		NewClient: flightapi.NewClient,
	}

	go b.Schedule(ctx, scheduleInterval)
	go backups(ctx, st, filepath.Join(dataDir, "backups"))
	slog.Info("bot started", "data_dir", dataDir, "schedule_every", scheduleInterval, "error_alerts", adminChat != "")
	b.Serve(ctx, tg)
	slog.Info("bot stopped")
}

// backups writes a copy of the database every day and keeps the last few.
func backups(ctx context.Context, st *store.Store, dir string) {
	for {
		path := filepath.Join(dir, "bot-"+time.Now().UTC().Format("2006-01-02")+".db")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := st.Backup(ctx, path); err != nil {
				slog.Error("backup", "path", path, "err", err)
			}
		}
		old, _ := filepath.Glob(filepath.Join(dir, "bot-*.db"))
		sort.Strings(old)
		for len(old) > backupsKept {
			os.Remove(old[0])
			old = old[1:]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		logging.Fatal("missing environment variable", "name", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
