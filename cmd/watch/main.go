// Command watch runs one check of the flight watch described by a YAML file and sends the
// result via Telegram (or e-mail as a fallback). It is meant for a scheduled job such as
// GitHub Actions, with the history kept in a JSON file.
package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/engine"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/logging"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

type options struct {
	configPath, statePath string
	dryRun                bool
	engine.Options
}

func main() {
	logging.Setup()
	var o options
	flag.StringVar(&o.configPath, "config", "config.yaml", "path to config file")
	flag.StringVar(&o.statePath, "state", "state/history.json", "path to price history")
	flag.BoolVar(&o.dryRun, "dry-run", false, "query prices but print the messages instead of sending them, and don't save history")
	flag.BoolVar(&o.ForceNotify, "force-notify", false, "send the alert message even when there is no deal (to test delivery)")
	flag.BoolVar(&o.ForceSummary, "summary", false, "send the summary now")
	chatID := flag.Bool("telegram-chat-id", false, "print the chat IDs that recently messaged the Telegram bot, then exit")
	flag.Parse()

	if *chatID {
		if err := printTelegramChats(); err != nil {
			logging.Fatal("list telegram chats", "err", err)
		}
		return
	}
	if err := run(o); err != nil {
		if !o.dryRun {
			msg := "⚠️ <b>Échec de la vérification des vols</b>\n" + html.EscapeString(err.Error())
			if sendErr := deliver(context.Background(), []string{msg}, "Échec de la vérification des vols", msg, false); sendErr != nil {
				slog.Error("could not report the failure", "err", sendErr)
			}
		}
		logging.Fatal("check failed", "err", err)
	}
}

func run(o options) error {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}
	history, err := state.Load(o.statePath)
	if err != nil {
		return err
	}
	apiKey := os.Getenv(flightapi.KeyEnv)
	if apiKey == "" {
		return fmt.Errorf("%s is not set", flightapi.KeyEnv)
	}

	ctx := context.Background()
	rep, err := engine.Run(ctx, cfg, history, flightapi.NewClient(apiKey), engine.Today(time.Now()), o.Options)
	if err != nil {
		return err
	}
	if rep.Expired {
		slog.Info("the departure window is in the past, nothing to check")
		return nil
	}

	if len(rep.Messages) > 0 {
		subject, mail, err := notify.Render(cfg.Name, rep.Deals)
		if err != nil {
			return err
		}
		if len(rep.Deals) == 0 {
			subject = cfg.Name + " : avertissement"
		}
		if len(rep.Warnings) > 0 {
			mail = strings.Replace(mail, "</h2>", "</h2><p>"+strings.Join(rep.Warnings, "<br>")+"</p>", 1)
		}
		if err := deliver(ctx, rep.Messages, subject, mail, o.dryRun); err != nil {
			return err
		}
	} else {
		slog.Info("no deal today")
	}
	if rep.Summary != "" {
		mail := "<html><body>" + strings.ReplaceAll(rep.Summary, "\n", "<br>") + "</body></html>"
		if err := deliver(ctx, []string{rep.Summary}, cfg.Name+" : résumé", mail, o.dryRun); err != nil {
			return err
		}
	}

	slog.Info("check done", "requests", rep.Requests, "credits_left", rep.CreditsLeft)
	if o.dryRun {
		return nil
	}
	return history.Save(o.statePath)
}

// deliver sends messages via Telegram when TELEGRAM_BOT_TOKEN is set, e-mail otherwise.
func deliver(ctx context.Context, telegramMsgs []string, subject, mail string, dryRun bool) error {
	if dryRun {
		fmt.Println(strings.Join(telegramMsgs, "\n\n-----\n\n"))
		return nil
	}
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		chatID := os.Getenv("TELEGRAM_CHAT_ID")
		if chatID == "" {
			return fmt.Errorf("TELEGRAM_CHAT_ID is not set (run with -telegram-chat-id to find it)")
		}
		if err := notify.NewTelegram(token, chatID).Send(ctx, telegramMsgs); err != nil {
			return err
		}
		slog.Info("telegram messages sent", "count", len(telegramMsgs))
		return nil
	}

	smtpCfg, err := smtpFromEnv()
	if err != nil {
		return err
	}
	if err := notify.Send(smtpCfg, subject, mail); err != nil {
		return err
	}
	slog.Info("e-mail sent", "subject", subject)
	return nil
}

func printTelegramChats() error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN is not set")
	}
	chats, err := notify.NewTelegram(token, "").RecentChats(context.Background())
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		fmt.Println("Aucun message reçu : envoyez « /start » à votre bot dans Telegram, puis relancez.")
		return nil
	}
	for _, c := range chats {
		fmt.Printf("TELEGRAM_CHAT_ID=%d  (%s)\n", c.ID, c.Title)
	}
	return nil
}

func smtpFromEnv() (notify.SMTPConfig, error) {
	cfg := notify.SMTPConfig{
		Host:     envOr("SMTP_HOST", "smtp.gmail.com"),
		User:     os.Getenv("SMTP_USER"),
		Password: os.Getenv("SMTP_APP_PASSWORD"),
	}
	port, err := strconv.Atoi(envOr("SMTP_PORT", "587"))
	if err != nil {
		return cfg, fmt.Errorf("SMTP_PORT: %w", err)
	}
	cfg.Port = port
	cfg.From = envOr("SMTP_FROM", cfg.User)
	for _, to := range strings.Split(os.Getenv("ALERT_TO"), ",") {
		if to = strings.TrimSpace(to); to != "" {
			cfg.To = append(cfg.To, to)
		}
	}
	if cfg.User == "" || cfg.Password == "" || len(cfg.To) == 0 {
		return cfg, fmt.Errorf("SMTP_USER, SMTP_APP_PASSWORD and ALERT_TO must be set")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
