// Command watch checks flight prices to Tokyo and sends good deals via Telegram or e-mail.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/rules"
	"github.com/atch/tokyo-flight-watch/internal/schedule"
	"github.com/atch/tokyo-flight-watch/internal/state"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	statePath := flag.String("state", "state/history.json", "path to price history")
	dryRun := flag.Bool("dry-run", false, "query prices but print the alert instead of sending it, and don't save history")
	limit := flag.Int("limit", 0, "number of searches to run (default: searches_per_day from config)")
	forceNotify := flag.Bool("force-notify", false, "send the alert even when there is no deal (to test delivery)")
	chatID := flag.Bool("telegram-chat-id", false, "print the chat IDs that recently messaged the Telegram bot, then exit")
	flag.Parse()

	var err error
	if *chatID {
		err = printTelegramChats()
	} else {
		err = run(*configPath, *statePath, *dryRun, *limit, *forceNotify)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func run(configPath, statePath string, dryRun bool, limit int, forceNotify bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	history, err := state.Load(statePath)
	if err != nil {
		return err
	}
	provider := flightapi.Provider(cfg.Provider)
	client, err := flightapi.NewClient(provider, os.Getenv(flightapi.KeyEnv[provider]))
	if err != nil {
		return err
	}
	if client.APIKey == "" {
		return fmt.Errorf("%s is not set", flightapi.KeyEnv[provider])
	}

	today := todayInParis()
	todayStr := today.Format("2006-01-02")
	n := cfg.SearchesPerDay
	if limit > 0 {
		n = limit
	}
	picked := schedule.Pick(cfg, history, schedule.All(cfg, today), today, n)
	if len(picked) == 0 {
		log.Println("no date combination left to check (window is in the past)")
		return nil
	}

	ctx := context.Background()
	var rows []notify.DealRow
	failures := 0
	for _, combo := range picked {
		query := flightapi.Query{
			Origins:      cfg.Origins,
			Destinations: cfg.Destinations,
			OutboundDate: combo.OutboundDate(),
			ReturnDate:   combo.ReturnDate(),
			Adults:       cfg.Adults,
			MaxStops:     cfg.MaxStops,
		}
		resp, err := client.Search(ctx, query)
		if err != nil {
			failures++
			log.Printf("%s: %v", combo.Key(), err)
			continue
		}
		entry := history.Get(combo.Key())
		res := rules.Evaluate(cfg, resp, entry)
		log.Printf("%s (%d j): %d offres, min %d €, %d alerte(s)",
			combo.Key(), combo.StayDays, len(res.Offers), res.MinPrice, len(res.Deals))
		rules.Record(entry, res, todayStr)
		for _, d := range res.Deals {
			rows = append(rows, toRow(combo, d, resp, query))
		}
	}
	if failures == len(picked) {
		return fmt.Errorf("all %d searches failed", failures)
	}

	if !dryRun {
		if err := history.Save(statePath); err != nil {
			return err
		}
	}
	if len(rows) == 0 && !forceNotify {
		log.Println("no deal today")
		return nil
	}
	return sendAlert(ctx, rows, dryRun)
}

// sendAlert uses Telegram when TELEGRAM_BOT_TOKEN is set, e-mail otherwise.
func sendAlert(ctx context.Context, rows []notify.DealRow, dryRun bool) error {
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" || dryRun {
		msgs := notify.RenderTelegram(rows)
		if dryRun {
			fmt.Println(strings.Join(msgs, "\n\n-----\n\n"))
			return nil
		}
		chatID := os.Getenv("TELEGRAM_CHAT_ID")
		if chatID == "" {
			return fmt.Errorf("TELEGRAM_CHAT_ID is not set (run with -telegram-chat-id to find it)")
		}
		if err := notify.NewTelegram(token, chatID).Send(ctx, msgs); err != nil {
			return err
		}
		log.Printf("telegram: %d message(s) sent", len(msgs))
		return nil
	}

	subject, html, err := notify.Render(rows)
	if err != nil {
		return err
	}
	smtpCfg, err := smtpFromEnv()
	if err != nil {
		return err
	}
	if err := notify.Send(smtpCfg, subject, html); err != nil {
		return err
	}
	log.Printf("e-mail sent: %s", subject)
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

func toRow(c schedule.Combo, d rules.Deal, resp *flightapi.Response, q flightapi.Query) notify.DealRow {
	row := notify.DealRow{
		Outbound:    c.OutboundDate(),
		Return:      c.ReturnDate(),
		StayDays:    c.StayDays,
		Origin:      d.Origin,
		Destination: d.Destination,
		Price:       d.Price,
		Stops:       d.Stops,
		Airlines:    strings.Join(d.AirlineNames, ", "),
		Reasons:     strings.Join(d.Reasons, " ; "),
		URL:         resp.GoogleFlightsURL(q),
	}
	if pi := resp.PriceInsights; pi != nil && pi.TypicalPriceRange.High > 0 {
		row.Typical = fmt.Sprintf("%d–%d €", pi.TypicalPriceRange.Low, pi.TypicalPriceRange.High)
	}
	return row
}

func todayInParis() time.Time {
	now := time.Now()
	if loc, err := time.LoadLocation("Europe/Paris"); err == nil {
		now = now.In(loc)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
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
