// Command watch checks flight prices to Tokyo and sends good deals via Telegram or e-mail.
//
// Each run prices the whole date grid with the price calendar (all airlines, and optionally
// nonstop flights of the direct airlines), alerts when a category's cheapest price is under
// its target or clearly below its usual best price, and confirms each alert with one detailed
// search using the same filters. Quota problems and failures are reported on Telegram.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
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

const dateLayout = "2006-01-02"

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
	flag.BoolVar(&o.forceNotify, "force-notify", false, "send the alert message even when there is no deal (to test delivery)")
	flag.BoolVar(&o.summary, "summary", false, "send the summary now")
	chatID := flag.Bool("telegram-chat-id", false, "print the chat IDs that recently messaged the Telegram bot, then exit")
	flag.Parse()

	if *chatID {
		if err := printTelegramChats(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(o); err != nil {
		if !o.dryRun {
			msg := "⚠️ <b>Vols Tokyo : échec du lancement</b>\n" + html.EscapeString(err.Error())
			if sendErr := deliver(context.Background(), []string{msg}, "Vols Tokyo : échec du lancement", msg, false); sendErr != nil {
				log.Printf("could not report the failure: %v", sendErr)
			}
		}
		log.Fatal(err)
	}
}

// pass is one calendar scan of the date grid for a price category.
type pass struct {
	kind    string
	filters flightapi.Filters
}

func passes(cfg *config.Config) []pass {
	ps := []pass{{rules.KindAny, flightapi.Filters{
		Origins: cfg.Origins, Destinations: cfg.Destinations, Adults: cfg.Adults, MaxStops: cfg.MaxStops,
	}}}
	if cfg.WatchDirect {
		ps = append(ps, pass{rules.KindDirect, flightapi.Filters{
			Origins: cfg.Origins, Destinations: cfg.Destinations, Adults: cfg.Adults, MaxStops: 0,
			IncludedAirlines: cfg.DirectAirlines,
		}})
	}
	return ps
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
	client := flightapi.NewClient(apiKey)

	ctx := context.Background()
	today := todayInParis()
	todayStr := today.Format(dateLayout)
	windows := schedule.Windows(cfg, today, flightapi.MaxCalendarCombos)
	if len(windows) == 0 {
		log.Println("the travel window is in the past, nothing to check")
		return nil
	}
	ps := passes(cfg)
	scanCost := len(windows) * len(ps)
	if scanCost > cfg.MaxRequestsPerRun {
		return fmt.Errorf("scanning the date grid needs %d requests but max_requests_per_run is %d: "+
			"narrow the dates or raise the limit", scanCost, cfg.MaxRequestsPerRun)
	}

	credits, err := client.Credits(ctx)
	if err != nil {
		log.Printf("could not read remaining credits: %v", err)
		credits = -1
	}
	if credits >= 0 && credits < scanCost {
		log.Printf("only %d credit(s) left, a run needs %d: skipping", credits, scanCost)
		if history.LastWarning == todayStr || o.dryRun {
			return nil
		}
		msg := fmt.Sprintf("⛔ <b>Vols Tokyo en pause</b>\nPlus que %d crédit(s) SearchApi, il en faut %d par lancement. "+
			"Rechargez le compte SearchApi (ou changez de clé) pour reprendre le suivi.", credits, scanCost)
		if err := deliver(ctx, []string{msg}, "Vols Tokyo en pause", msg, false); err != nil {
			return err
		}
		history.LastWarning = todayStr
		return history.Save(o.statePath)
	}

	scan, failed := scanCalendar(ctx, client, cfg, ps, windows)
	if failed == scanCost {
		return fmt.Errorf("all %d calendar requests failed", scanCost)
	}
	var warnings []string
	if failed > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️ %d requête(s) du calendrier sur %d ont échoué : résultats incomplets aujourd'hui.", failed, scanCost))
	}
	for _, p := range ps {
		best := rules.Cheapest(scan[p.kind])
		log.Printf("%s: %d date pairs, best %d € on %s, median %d €",
			p.kind, len(scan[p.kind]), best.Price, best.Key, rules.Median(scan[p.kind]))
	}

	rules.ExpireAlerts(cfg, history, scan)
	signals := rules.Signals(cfg, history, scan, today)
	filters := map[string]flightapi.Filters{}
	for _, p := range ps {
		filters[p.kind] = p.filters
	}
	var rows []notify.DealRow
	for _, sig := range signals {
		canDetail := client.Requests < cfg.MaxRequestsPerRun && (credits < 0 || client.Requests < credits)
		row, err := dealRow(ctx, client, cfg, sig, filters[sig.Kind], canDetail)
		if err != nil {
			log.Printf("%s: %v", sig.Best.Key, err)
			continue
		}
		log.Printf("alert %s: %d € on %s (%s)", sig.Kind, row.Price, sig.Best.Key, row.Reasons)
		rules.RecordAlert(history, sig, todayStr)
		rows = append(rows, row)
	}

	history.AddDay(state.Day{
		Date:      todayStr,
		BestAny:   rules.Cheapest(scan[rules.KindAny]),
		BestDir:   rules.Cheapest(scan[rules.KindDirect]),
		MedianAny: rules.Median(scan[rules.KindAny]),
		MedianDir: rules.Median(scan[rules.KindDirect]),
		Requests:  client.Requests,
		Alerts:    len(rows),
	})

	creditsLeft := -1
	if credits >= 0 {
		creditsLeft = max(credits-client.Requests, 0)
		lastWarned, _ := time.Parse(dateLayout, history.LastWarning)
		if creditsLeft < cfg.MinCreditsWarning && today.Sub(lastWarned) >= 7*24*time.Hour {
			warnings = append(warnings, fmt.Sprintf("🪫 Plus que %d crédit(s) SearchApi, soit environ %d lancement(s).",
				creditsLeft, creditsLeft/scanCost))
			history.LastWarning = todayStr
		}
	}

	var msgs []string
	if len(rows) > 0 || o.forceNotify {
		msgs = notify.RenderTelegram(rows)
	} else {
		log.Println("no deal today")
	}
	if len(warnings) > 0 {
		msgs = append(msgs, strings.Join(warnings, "\n"))
	}
	if len(msgs) > 0 {
		subject, mail, err := notify.Render(rows)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			subject = "Vols Tokyo : avertissement"
		}
		if len(warnings) > 0 {
			mail = strings.Replace(mail, "</h2>", "</h2><p>"+strings.Join(warnings, "<br>")+"</p>", 1)
		}
		if err := deliver(ctx, msgs, subject, mail, o.dryRun); err != nil {
			return err
		}
	}

	if o.summary || summaryDue(cfg, history, today) {
		msg := notify.RenderSummary(buildSummary(cfg, history, scan, today, creditsLeft, scanCost))
		mail := "<html><body>" + strings.ReplaceAll(msg, "\n", "<br>") + "</body></html>"
		if err := deliver(ctx, []string{msg}, "Vols Tokyo : résumé", mail, o.dryRun); err != nil {
			return err
		}
		history.LastSummary = todayStr
	}

	log.Printf("%d API request(s) used, %d credit(s) left", client.Requests, creditsLeft)
	if o.dryRun {
		return nil
	}
	return history.Save(o.statePath)
}

// scanCalendar prices every wanted date pair for each pass. It returns the prices by
// category and the number of failed requests.
func scanCalendar(ctx context.Context, client *flightapi.Client, cfg *config.Config, ps []pass, windows []schedule.Window) (rules.Scan, int) {
	scan := rules.Scan{}
	failed := 0
	for _, p := range ps {
		prices := map[string]int{}
		scan[p.kind] = prices
		for _, w := range windows {
			outStart, outEnd, retStart, retEnd := w.Dates()
			result, err := client.Calendar(ctx, flightapi.CalendarQuery{
				Filters:       p.filters,
				OutboundStart: outStart, OutboundEnd: outEnd,
				ReturnStart: retStart, ReturnEnd: retEnd,
			})
			if err != nil {
				failed++
				log.Printf("calendar %s %s..%s: %v", p.kind, outStart, outEnd, err)
				continue
			}
			for _, cp := range result {
				combo, err := schedule.ParseCombo(cp.Departure, cp.Return)
				if err != nil || !schedule.Wanted(cfg, combo) {
					continue
				}
				if old, ok := prices[combo.Key()]; !ok || cp.Price < old {
					prices[combo.Key()] = cp.Price
				}
			}
		}
	}
	return scan, failed
}

var titles = map[string]string{
	rules.KindAny:    "Meilleur prix (≤ 1 escale)",
	rules.KindDirect: "Vol direct Star Alliance (aller et retour)",
}

var priceLevels = map[string]string{"low": "bas", "typical": "normal", "high": "élevé"}

// dealRow describes a signal. With detail, one detailed search with the same filters as the
// calendar pass picks the actual offer; otherwise, or if it fails, the calendar price is reported.
func dealRow(ctx context.Context, client *flightapi.Client, cfg *config.Config, sig rules.Signal,
	filters flightapi.Filters, detail bool) (notify.DealRow, error) {
	combo, err := comboFromKey(sig.Best.Key)
	if err != nil {
		return notify.DealRow{}, err
	}
	query := flightapi.Query{Filters: filters, OutboundDate: combo.OutboundDate(), ReturnDate: combo.ReturnDate()}
	row := notify.DealRow{
		Title:       titles[sig.Kind],
		Outbound:    combo.OutboundDate(),
		Return:      combo.ReturnDate(),
		StayDays:    combo.StayDays,
		Origin:      strings.Join(filters.Origins, "/"),
		Destination: strings.Join(filters.Destinations, "/"),
		Price:       sig.Best.Price,
		Stops:       -1,
		Reasons:     strings.Join(sig.Reasons, " ; "),
		URL:         flightapi.SearchURL(query),
	}
	if !detail {
		row.Note = "détails non vérifiés (quota de requêtes atteint)"
		return row, nil
	}
	resp, err := client.Search(ctx, query)
	if err != nil {
		log.Printf("detail %s: %v", sig.Best.Key, err)
		row.Note = "détails non vérifiés (la recherche détaillée a échoué)"
		return row, nil
	}
	offer, preferred, ok := rules.PickOffer(cfg, resp, filters.MaxStops)
	if !ok {
		row.Note = "aucune offre trouvée en recherche détaillée : prix du calendrier"
		return row, nil
	}
	row.Price, row.Stops = offer.Price, offer.Stops
	row.Origin, row.Destination = offer.Origin, offer.Destination
	row.Airlines = strings.Join(offer.AirlineNames, ", ")
	if preferred != nil {
		row.Note = fmt.Sprintf("%s dès %d € (%s)", preferred.Destination, preferred.Price, strings.Join(preferred.AirlineNames, ", "))
	}
	if pi := resp.PriceInsights; pi != nil {
		var parts []string
		if level, ok := priceLevels[pi.PriceLevel]; ok {
			parts = append(parts, "prix jugé "+level)
		}
		if pi.TypicalPriceRange.High > 0 {
			parts = append(parts, fmt.Sprintf("habituel %d–%d €", pi.TypicalPriceRange.Low, pi.TypicalPriceRange.High))
		}
		row.Typical = strings.Join(parts, " · ")
	}
	return row, nil
}

func comboFromKey(key string) (schedule.Combo, error) {
	out, ret, ok := strings.Cut(key, "_")
	if !ok {
		return schedule.Combo{}, errors.New("invalid date pair key " + key)
	}
	return schedule.ParseCombo(out, ret)
}

func summaryDue(cfg *config.Config, h *state.History, today time.Time) bool {
	if cfg.SummaryEveryDays <= 0 {
		return false
	}
	last, err := time.Parse(dateLayout, h.LastSummary)
	return err != nil || today.Sub(last) >= time.Duration(cfg.SummaryEveryDays)*24*time.Hour
}

func buildSummary(cfg *config.Config, h *state.History, scan rules.Scan, today time.Time, credits, perRun int) notify.Summary {
	filters := map[string]flightapi.Filters{}
	for _, p := range passes(cfg) {
		filters[p.kind] = p.filters
	}
	priceRow := func(key string, price int, f flightapi.Filters) notify.PriceRow {
		combo, _ := comboFromKey(key)
		q := flightapi.Query{Filters: f, OutboundDate: combo.OutboundDate(), ReturnDate: combo.ReturnDate()}
		return notify.PriceRow{
			Outbound: combo.OutboundDate(), Return: combo.ReturnDate(), StayDays: combo.StayDays,
			Price: price, URL: flightapi.SearchURL(q),
		}
	}

	anyPrices := scan[rules.KindAny]
	s := notify.Summary{
		Date:      today.Format(dateLayout),
		DatePairs: len(anyPrices),
		UsualAny:  rules.UsualBest(h, rules.KindAny, today, cfg.TypicalWindowDays),
		UsualDir:  rules.UsualBest(h, rules.KindDirect, today, cfg.TypicalWindowDays),
		Credits:   credits,
		PerRun:    perRun,
	}
	keys := make([]string, 0, len(anyPrices))
	for k := range anyPrices {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if anyPrices[keys[i]] != anyPrices[keys[j]] {
			return anyPrices[keys[i]] < anyPrices[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys[:min(5, len(keys))] {
		s.Cheapest = append(s.Cheapest, priceRow(k, anyPrices[k], filters[rules.KindAny]))
	}
	if best := rules.Cheapest(scan[rules.KindDirect]); best.Key != "" {
		r := priceRow(best.Key, best.Price, filters[rules.KindDirect])
		s.BestDirect = &r
	}

	weekAgo := today.AddDate(0, 0, -7).Format(dateLayout)
	for _, d := range h.DaysSince(weekAgo) {
		if d.Date == weekAgo {
			s.WeekAgoAny, s.WeekAgoDir = d.BestAny.Price, d.BestDir.Price
			continue
		}
		s.RequestsWk += d.Requests
		s.AlertsWk += d.Alerts
		s.DaysTracked++
	}
	return s
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
		log.Printf("telegram: %d message(s) sent", len(telegramMsgs))
		return nil
	}

	smtpCfg, err := smtpFromEnv()
	if err != nil {
		return err
	}
	if err := notify.Send(smtpCfg, subject, mail); err != nil {
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
