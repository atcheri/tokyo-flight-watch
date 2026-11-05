// Package engine runs one check of a flight watch: it prices the whole date grid with the
// price calendar (all airlines, and optionally nonstop flights of the direct airlines), alerts
// when a category's cheapest price is under its target or clearly below its usual best price,
// confirms each alert with one detailed search using the same filters, and prepares the
// messages to send. It never sends anything itself.
package engine

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
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

// Options force messages that would otherwise not be sent.
type Options struct {
	ForceNotify  bool // send the alert message even without deals
	ForceSummary bool // send the summary even if not due
}

// Report is what a check produced. The caller sends the messages and saves the history.
type Report struct {
	Messages    []string         // Telegram HTML: alerts, warnings or the pause notice
	Summary     string           // Telegram HTML summary, empty if none
	Deals       []notify.DealRow // for e-mail delivery
	Warnings    []string
	Requests    int // billable requests used
	CreditsLeft int // -1 if unknown
	Expired     bool
	Paused      bool
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

// Today returns the calendar day of now in Paris, as a UTC midnight.
func Today(now time.Time) time.Time {
	if loc, err := time.LoadLocation("Europe/Paris"); err == nil {
		now = now.In(loc)
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// ScanCost is the number of requests one check needs to price the date grid on today.
func ScanCost(cfg *config.Config, today time.Time) int {
	return len(schedule.Windows(cfg, today, flightapi.MaxCalendarCombos)) * len(passes(cfg))
}

// Run performs one check of the watch cfg, updating h.
func Run(ctx context.Context, cfg *config.Config, h *state.History, client *flightapi.Client, today time.Time, opts Options) (Report, error) {
	rep := Report{CreditsLeft: -1}
	startRequests := client.Requests
	used := func() int { return client.Requests - startRequests }
	todayStr := today.Format(dateLayout)
	windows := schedule.Windows(cfg, today, flightapi.MaxCalendarCombos)
	if len(windows) == 0 {
		rep.Expired = true
		return rep, nil
	}
	ps := passes(cfg)
	scanCost := len(windows) * len(ps)
	if scanCost > cfg.MaxRequestsPerRun {
		return rep, fmt.Errorf("scanning the date grid needs %d requests but max_requests_per_run is %d: "+
			"narrow the dates or raise the limit", scanCost, cfg.MaxRequestsPerRun)
	}

	credits, err := client.Credits(ctx)
	if err != nil {
		log.Printf("could not read remaining credits: %v", err)
		credits = -1
	}
	if credits >= 0 && credits < scanCost {
		rep.Paused, rep.CreditsLeft = true, credits
		if h.LastWarning != todayStr {
			rep.Messages = []string{fmt.Sprintf("⛔ <b>%s en pause</b>\nPlus que %d crédit(s) SearchApi, il en faut %d par vérification. "+
				"Rechargez le compte SearchApi (ou changez de clé) pour reprendre le suivi.", html.EscapeString(cfg.Name), credits, scanCost)}
			h.LastWarning = todayStr
		}
		return rep, nil
	}

	scan, failed, apiNote := scanCalendar(ctx, client, cfg, ps, windows, today)
	if failed == scanCost {
		return rep, fmt.Errorf("all %d calendar requests failed", scanCost)
	}
	if len(scan[rules.KindAny]) == 0 && len(scan[rules.KindDirect]) == 0 {
		// Nothing to compare: say so instead of staying silent, and keep the history as it was.
		rep.Requests = used()
		if credits >= 0 {
			rep.CreditsLeft = max(credits-used(), 0)
		}
		msg := fmt.Sprintf("⚠️ <b>%s</b> : SearchApi n'a renvoyé aucun prix pour ces dates (%d requête(s) utilisée(s)). "+
			"Vérifiez les aéroports et les dates avec /liste ; le suivi continue.", html.EscapeString(cfg.Name), used())
		if apiNote != "" {
			msg += "\nRéponse de SearchApi : <i>" + html.EscapeString(apiNote) + "</i>"
		}
		rep.Messages = []string{msg}
		return rep, nil
	}
	if failed > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("⚠️ %d requête(s) du calendrier sur %d ont échoué : résultats incomplets.", failed, scanCost))
	}
	for _, p := range ps {
		best := rules.Cheapest(scan[p.kind])
		log.Printf("%s %s: %d date pairs, best %d € on %s, median %d €",
			cfg.Name, p.kind, len(scan[p.kind]), best.Price, best.Key, rules.Median(scan[p.kind]))
	}

	rules.ExpireAlerts(cfg, h, scan)
	filters := map[string]flightapi.Filters{}
	for _, p := range ps {
		filters[p.kind] = p.filters
	}
	for _, sig := range rules.Signals(cfg, h, scan, today) {
		canDetail := used() < cfg.MaxRequestsPerRun && (credits < 0 || used() < credits)
		row, err := dealRow(ctx, client, cfg, sig, filters[sig.Kind], canDetail)
		if err != nil {
			log.Printf("%s: %v", sig.Best.Key, err)
			continue
		}
		log.Printf("%s alert %s: %d € on %s (%s)", cfg.Name, sig.Kind, row.Price, sig.Best.Key, row.Reasons)
		rules.RecordAlert(h, sig, todayStr)
		rep.Deals = append(rep.Deals, row)
	}

	h.AddDay(state.Day{
		Date:      todayStr,
		BestAny:   rules.Cheapest(scan[rules.KindAny]),
		BestDir:   rules.Cheapest(scan[rules.KindDirect]),
		MedianAny: rules.Median(scan[rules.KindAny]),
		MedianDir: rules.Median(scan[rules.KindDirect]),
		Requests:  used(),
		Alerts:    len(rep.Deals),
	})

	rep.Requests = used()
	if credits >= 0 {
		rep.CreditsLeft = max(credits-used(), 0)
		lastWarned, _ := time.Parse(dateLayout, h.LastWarning)
		if rep.CreditsLeft < cfg.MinCreditsWarning && today.Sub(lastWarned) >= 7*24*time.Hour {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("🪫 Plus que %d crédit(s) SearchApi, soit environ %d vérification(s).",
				rep.CreditsLeft, rep.CreditsLeft/scanCost))
			h.LastWarning = todayStr
		}
	}

	if len(rep.Deals) > 0 || opts.ForceNotify {
		rep.Messages = notify.RenderTelegram(cfg.Name, rep.Deals)
	}
	if len(rep.Warnings) > 0 {
		rep.Messages = append(rep.Messages, strings.Join(rep.Warnings, "\n"))
	}
	if opts.ForceSummary || summaryDue(cfg, h, today) {
		rep.Summary = notify.RenderSummary(buildSummary(cfg, h, scan, today, rep.CreditsLeft, scanCost))
		h.LastSummary = todayStr
	}
	return rep, nil
}

// scanCalendar prices every wanted date pair for each pass. It returns the prices by
// category, the number of failed requests and the last "no results" message of the API.
func scanCalendar(ctx context.Context, client *flightapi.Client, cfg *config.Config, ps []pass,
	windows []schedule.Window, today time.Time) (scan rules.Scan, failed int, apiNote string) {
	scan = rules.Scan{}
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
			if errors.Is(err, flightapi.ErrNoResults) {
				log.Printf("calendar %s %s..%s: %v", p.kind, outStart, outEnd, err)
				apiNote = strings.TrimPrefix(err.Error(), flightapi.ErrNoResults.Error()+": ")
				continue
			}
			if err != nil {
				failed++
				log.Printf("calendar %s %s..%s: %v", p.kind, outStart, outEnd, err)
				continue
			}
			kept := 0
			for _, cp := range result {
				combo, err := schedule.ParseCombo(cp.Departure, cp.Return)
				if err != nil || !schedule.Wanted(cfg, combo, today) {
					continue
				}
				kept++
				if old, ok := prices[combo.Key()]; !ok || cp.Price < old {
					prices[combo.Key()] = cp.Price
				}
			}
			log.Printf("calendar %s %s..%s: %d price(s), %d kept", p.kind, outStart, outEnd, len(result), kept)
		}
	}
	return scan, failed, apiNote
}

func anyTitle(cfg *config.Config) string {
	if cfg.MaxStops == 0 {
		return "Meilleur prix (vols directs)"
	}
	return fmt.Sprintf("Meilleur prix (≤ %d escale)", cfg.MaxStops)
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
	title := anyTitle(cfg)
	if sig.Kind == rules.KindDirect {
		title = cfg.DirectLabel() + " (aller et retour)"
	}
	row := notify.DealRow{
		Title:       title,
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
		Name:        cfg.Name,
		Date:        today.Format(dateLayout),
		DatePairs:   len(anyPrices),
		AnyTitle:    anyTitle(cfg),
		DirectTitle: cfg.DirectLabel(),
		UsualAny:    rules.UsualBest(h, rules.KindAny, today, cfg.TypicalWindowDays),
		UsualDir:    rules.UsualBest(h, rules.KindDirect, today, cfg.TypicalWindowDays),
		Credits:     credits,
		PerRun:      perRun,
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
