// Command watch checks flight prices to Tokyo and sends good deals via Telegram or e-mail.
//
// Each run prices the whole date grid with the price calendar (all airlines, then nonstop
// Star Alliance only), picks the promising date pairs, confirms them with a detailed search,
// and sends alerts plus a weekly summary.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
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

type options struct {
	configPath, statePath string
	dryRun                bool
	forceNotify, summary  bool
}

func main() {
	var o options
	flag.StringVar(&o.configPath, "config", "config.yaml", "path to config file")
	flag.StringVar(&o.statePath, "state", "state/history.json", "path to price history")
	flag.BoolVar(&o.dryRun, "dry-run", false, "query prices but print the messages instead of sending them, and don't save history")
	flag.BoolVar(&o.forceNotify, "force-notify", false, "send the alert even when there is no deal (to test delivery)")
	flag.BoolVar(&o.summary, "summary", false, "send the weekly summary now, whatever the day")
	chatID := flag.Bool("telegram-chat-id", false, "print the chat IDs that recently messaged the Telegram bot, then exit")
	flag.Parse()

	var err error
	if *chatID {
		err = printTelegramChats()
	} else {
		err = run(o)
	}
	if err != nil {
		log.Fatal(err)
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
	provider := flightapi.Provider(cfg.Provider)
	client, err := flightapi.NewClient(provider, os.Getenv(flightapi.KeyEnv[provider]))
	if err != nil {
		return err
	}
	if client.APIKey == "" {
		return fmt.Errorf("%s is not set", flightapi.KeyEnv[provider])
	}

	ctx := context.Background()
	today := todayInParis()
	todayStr := today.Format("2006-01-02")
	windows := schedule.Windows(cfg, today, flightapi.MaxCalendarCombos)
	if len(windows) == 0 {
		log.Println("the travel window is in the past, nothing to check")
		return nil
	}

	scan, err := scanCalendar(ctx, client, cfg, windows)
	if err != nil {
		return err
	}
	cheapest := rules.Cheapest(scan.Any)
	log.Printf("calendar: %d date pairs (best %d € on %s), %d with a direct Star Alliance flight (best %d €)",
		len(scan.Any), cheapest.Price, cheapest.Key, len(scan.Direct), rules.Cheapest(scan.Direct).Price)

	// Detailed searches compare against history as it was before today's scan.
	candidates := rules.Candidates(cfg, history, scan)
	log.Printf("%d candidate(s), checking up to %d in detail", len(candidates), cfg.MaxDetailSearches)
	lowestBefore := history.LowestEver.Price
	var rows []notify.DealRow
	for _, cand := range candidates[:min(len(candidates), cfg.MaxDetailSearches)] {
		dealRows, err := checkCandidate(ctx, client, cfg, history, cand, lowestBefore)
		if err != nil {
			log.Printf("%s: %v", cand.Key, err)
			continue
		}
		rows = append(rows, dealRows...)
	}

	bestAny, bestDirect := rules.RecordScan(history, scan, todayStr)
	history.AddDay(state.Day{Date: todayStr, BestAny: bestAny, BestDir: bestDirect, Requests: client.Requests, Alerts: len(rows)})

	if len(rows) > 0 || o.forceNotify {
		subject, html, err := notify.Render(rows)
		if err != nil {
			return err
		}
		if err := deliver(ctx, notify.RenderTelegram(rows), subject, html, o.dryRun); err != nil {
			return err
		}
	} else {
		log.Println("no deal today")
	}

	if o.summary || (cfg.IsSummaryDay(today) && history.LastSummary != todayStr) {
		msg := notify.RenderSummary(buildSummary(cfg, history, scan, today))
		html := "<html><body>" + strings.ReplaceAll(msg, "\n", "<br>") + "</body></html>"
		if err := deliver(ctx, []string{msg}, "Vols Tokyo : résumé hebdo", html, o.dryRun); err != nil {
			return err
		}
		history.LastSummary = todayStr
	}

	log.Printf("%d API request(s) used", client.Requests)
	if o.dryRun {
		return nil
	}
	return history.Save(o.statePath)
}

// scanCalendar prices every date pair, once for all airlines and once for nonstop Star Alliance.
func scanCalendar(ctx context.Context, client *flightapi.Client, cfg *config.Config, windows []schedule.Window) (rules.Scan, error) {
	scan := rules.Scan{Any: map[string]int{}, Direct: map[string]int{}}
	passes := []struct {
		prices   map[string]int
		maxStops int
		airlines string
	}{
		{scan.Any, cfg.MaxStops, ""},
		{scan.Direct, 0, rules.StarAllianceFilter},
	}
	failures, total := 0, 0
	for _, p := range passes {
		for _, w := range windows {
			total++
			outStart, outEnd, retStart, retEnd := w.Dates()
			prices, err := client.Calendar(ctx, flightapi.CalendarQuery{
				Origins:          cfg.Origins,
				Destinations:     cfg.Destinations,
				OutboundStart:    outStart,
				OutboundEnd:      outEnd,
				ReturnStart:      retStart,
				ReturnEnd:        retEnd,
				Adults:           cfg.Adults,
				MaxStops:         p.maxStops,
				IncludedAirlines: p.airlines,
			})
			if err != nil {
				failures++
				log.Printf("calendar %s..%s: %v", outStart, outEnd, err)
				continue
			}
			for _, cp := range prices {
				combo, err := schedule.ParseCombo(cp.Departure, cp.Return)
				if err != nil || !schedule.Wanted(cfg, combo) {
					continue
				}
				if old, ok := p.prices[combo.Key()]; !ok || cp.Price < old {
					p.prices[combo.Key()] = cp.Price
				}
			}
		}
	}
	if failures == total {
		return scan, fmt.Errorf("all %d calendar requests failed", total)
	}
	return scan, nil
}

// checkCandidate runs a detailed search for one date pair and returns its new deals.
func checkCandidate(ctx context.Context, client *flightapi.Client, cfg *config.Config, h *state.History,
	cand rules.Candidate, lowestBefore int) ([]notify.DealRow, error) {
	parts := strings.SplitN(cand.Key, "_", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid key")
	}
	combo, err := schedule.ParseCombo(parts[0], parts[1])
	if err != nil {
		return nil, err
	}
	query := searchQuery(cfg, combo)
	resp, err := client.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	res := rules.Evaluate(cfg, resp, h.Peek(cand.Key), lowestBefore)
	level := ""
	if resp.PriceInsights != nil {
		level = resp.PriceInsights.PriceLevel
	}
	log.Printf("%s (%d j): calendar %d € %v → %d offres, min %d €, Google %q, %d alerte(s)",
		cand.Key, combo.StayDays, cand.Price, cand.Reasons, len(res.Offers), res.MinPrice, level, len(res.Deals))
	rules.Record(h.Get(cand.Key), res, cand.Price)

	rows := make([]notify.DealRow, 0, len(res.Deals))
	for _, d := range res.Deals {
		rows = append(rows, toRow(combo, d, resp, query))
	}
	return rows, nil
}

func searchQuery(cfg *config.Config, c schedule.Combo) flightapi.Query {
	return flightapi.Query{
		Origins:      cfg.Origins,
		Destinations: cfg.Destinations,
		OutboundDate: c.OutboundDate(),
		ReturnDate:   c.ReturnDate(),
		Adults:       cfg.Adults,
		MaxStops:     cfg.MaxStops,
	}
}

func buildSummary(cfg *config.Config, h *state.History, scan rules.Scan, today time.Time) notify.Summary {
	priceRow := func(key string, price int) notify.PriceRow {
		parts := strings.SplitN(key, "_", 2)
		combo, _ := schedule.ParseCombo(parts[0], parts[len(parts)-1])
		return notify.PriceRow{
			Outbound: combo.OutboundDate(), Return: combo.ReturnDate(), StayDays: combo.StayDays,
			Price: price, URL: flightapi.SearchURL(searchQuery(cfg, combo)),
		}
	}

	s := notify.Summary{Date: today.Format("2006-01-02"), DatePairs: len(scan.Any), LowestEver: h.LowestEver.Price}
	keys := make([]string, 0, len(scan.Any))
	for k := range scan.Any {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if scan.Any[keys[i]] != scan.Any[keys[j]] {
			return scan.Any[keys[i]] < scan.Any[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:min(5, len(keys))] {
		s.Cheapest = append(s.Cheapest, priceRow(k, scan.Any[k]))
	}
	if best := rules.Cheapest(scan.Direct); best.Key != "" {
		r := priceRow(best.Key, best.Price)
		s.BestDirect = &r
	}

	if d, ok := h.DayOn(today.AddDate(0, 0, -7).Format("2006-01-02")); ok {
		s.WeekAgoAny, s.WeekAgoDir = d.BestAny.Price, d.BestDir.Price
	}
	weekStart := today.AddDate(0, 0, -6).Format("2006-01-02")
	for _, d := range h.Days {
		if d.Date >= weekStart {
			s.RequestsWk += d.Requests
			s.AlertsWk += d.Alerts
			s.DaysTracked++
		}
	}
	return s
}

// deliver sends messages via Telegram when TELEGRAM_BOT_TOKEN is set, e-mail otherwise.
func deliver(ctx context.Context, telegramMsgs []string, subject, html string, dryRun bool) error {
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
		log.Printf("telegram: %d message(s) sent", len(telegramMsgs))
		return nil
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
