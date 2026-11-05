// Package bot is a Telegram bot through which invited people manage their own flight watches.
// Each user brings their own SearchApi key; the bot runs each watch on its schedule with the
// engine and sends the alerts to the user's chat.
package bot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/engine"
	"github.com/atch/tokyo-flight-watch/internal/flightapi"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/secret"
	"github.com/atch/tokyo-flight-watch/internal/state"
	"github.com/atch/tokyo-flight-watch/internal/store"
)

// Messenger sends Telegram messages; *notify.Telegram implements it.
type Messenger interface {
	SendTo(ctx context.Context, chatID string, messages ...string) error
	DeleteMessage(ctx context.Context, chatID string, messageID int64) error
}

type Bot struct {
	Msg        Messenger
	Store      *store.Store
	Box        *secret.Box
	InviteCode string
	MaxWatches int
	NewClient  func(apiKey string) *flightapi.Client
	Now        func() time.Time

	runMu sync.Mutex // one watch check at a time
}

// Retry delays after a failed check, and while a user has no usable key or credits.
const (
	retryAfterError   = 6 * time.Hour
	retryWithoutKey   = 24 * time.Hour
	defaultMaxWatches = 5
)

// Commands is the menu published to Telegram.
var Commands = [][2]string{
	{"nouveau", "Créer un suivi de vols"},
	{"liste", "Voir mes suivis"},
	{"cle", "Enregistrer ma clé SearchApi"},
	{"credits", "Crédits SearchApi restants"},
	{"verifier", "Vérifier un suivi maintenant : /verifier N"},
	{"bilan", "Résumé d'un suivi : /bilan N"},
	{"pause", "Mettre un suivi en pause : /pause N"},
	{"reprendre", "Reprendre un suivi : /reprendre N"},
	{"supprimer", "Supprimer un suivi : /supprimer N"},
	{"annuler", "Annuler la saisie en cours"},
	{"aide", "Aide"},
}

const helpText = `✈️ <b>Suivi des prix de vols</b>

Je surveille les prix aller-retour pour vos dates (via Google Flights) et je vous préviens quand un prix est bas : sous votre prix cible, ou nettement sous le meilleur prix habituel.

<b>Pour commencer</b>
1. Créez un compte gratuit sur searchapi.io (100 requêtes offertes) et copiez votre clé API.
2. /cle pour l'enregistrer (elle est chiffrée, et votre message est supprimé).
3. /nouveau pour créer un suivi.

<b>Commandes</b>
/liste – vos suivis et leur numéro
/verifier N – vérifier maintenant
/bilan N – résumé des prix
/pause N, /reprendre N, /supprimer N
/credits – crédits SearchApi restants
/annuler – annuler la saisie en cours
/oublier – supprimer toutes mes données`

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bot) maxWatches() int {
	if b.MaxWatches > 0 {
		return b.MaxWatches
	}
	return defaultMaxWatches
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.Msg.SendTo(ctx, strconv.FormatInt(chatID, 10), text); err != nil {
		slog.Error("send message", "chat", chatID, "err", err)
	}
}

// HandleUpdate processes one incoming message.
func (b *Bot) HandleUpdate(ctx context.Context, u notify.Update) {
	if !u.Private || strings.TrimSpace(u.Text) == "" {
		return
	}
	cmd, arg := splitCommand(u.Text)

	user, err := b.Store.User(ctx, u.ChatID)
	if errors.Is(err, store.ErrNotFound) {
		b.handleStranger(ctx, u, cmd, arg)
		return
	}
	if err != nil {
		slog.Error("load user", "chat", u.ChatID, "err", err)
		b.reply(ctx, u.ChatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}

	if cmd == "" {
		if user.Wizard == "" {
			b.reply(ctx, u.ChatID, "Je n'ai pas compris. /aide pour la liste des commandes.")
			return
		}
		b.handleWizard(ctx, user, u)
		return
	}
	if user.Wizard != "" && cmd != "/cle" && cmd != "/key" {
		b.setWizard(ctx, u.ChatID, nil) // any other command abandons the ongoing input
	}

	switch cmd {
	case "/start", "/aide", "/help":
		b.reply(ctx, u.ChatID, helpText)
	case "/cle", "/key":
		if arg != "" {
			b.saveKey(ctx, u, arg)
			return
		}
		b.setWizard(ctx, u.ChatID, &wizard{Step: stepKey})
		b.reply(ctx, u.ChatID, prompts[stepKey])
	case "/nouveau", "/new":
		b.startWizard(ctx, user)
	case "/liste", "/list":
		b.listWatches(ctx, u.ChatID)
	case "/credits":
		b.showCredits(ctx, user)
	case "/pause", "/reprendre", "/resume":
		b.setPaused(ctx, u.ChatID, arg, cmd == "/pause")
	case "/supprimer", "/delete":
		b.deleteWatch(ctx, u.ChatID, arg)
	case "/verifier", "/check":
		b.checkNow(ctx, u.ChatID, arg, engine.Options{})
	case "/bilan", "/summary":
		b.checkNow(ctx, u.ChatID, arg, engine.Options{ForceSummary: true})
	case "/annuler", "/cancel":
		b.reply(ctx, u.ChatID, "Saisie annulée.")
	case "/oublier", "/forget":
		b.forget(ctx, u.ChatID, arg)
	default:
		b.reply(ctx, u.ChatID, "Commande inconnue. /aide pour la liste des commandes.")
	}
}

// splitCommand splits "/pause@MyBot 3" into ("/pause", "3"); plain text gives ("", text).
func splitCommand(text string) (cmd, arg string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	cmd, arg, _ = strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@")
	return strings.ToLower(cmd), strings.TrimSpace(arg)
}

func (b *Bot) handleStranger(ctx context.Context, u notify.Update, cmd, arg string) {
	if cmd == "/start" && b.InviteCode != "" && subtle.ConstantTimeCompare([]byte(arg), []byte(b.InviteCode)) == 1 {
		if err := b.Store.CreateUser(ctx, u.ChatID, u.Name, b.now()); err != nil {
			slog.Error("create user", "chat", u.ChatID, "err", err)
			b.reply(ctx, u.ChatID, "⚠️ Erreur interne, réessayez plus tard.")
			return
		}
		slog.Info("new user", "chat", u.ChatID, "name", u.Name)
		b.reply(ctx, u.ChatID, "Bienvenue "+html.EscapeString(u.Name)+" !\n\n"+helpText)
		return
	}
	b.reply(ctx, u.ChatID, "🔒 Ce bot est privé. Envoyez <code>/start</code> suivi du code d'invitation.")
}

// --- API key

func (b *Bot) saveKey(ctx context.Context, u notify.Update, key string) {
	chat := strconv.FormatInt(u.ChatID, 10)
	if err := b.Msg.DeleteMessage(ctx, chat, u.MessageID); err != nil {
		slog.Warn("delete key message", "chat", u.ChatID, "err", err)
	}
	key = strings.TrimSpace(key)
	credits, err := b.NewClient(key).Credits(ctx)
	if err != nil {
		slog.Warn("check api key", "chat", u.ChatID, "err", err)
		b.reply(ctx, u.ChatID, "❌ SearchApi refuse cette clé. Vérifiez-la sur searchapi.io (API Key), puis renvoyez /cle.")
		return
	}
	sealed, err := b.Box.Seal(key)
	if err == nil {
		err = b.Store.SetAPIKey(ctx, u.ChatID, sealed)
	}
	if err != nil {
		slog.Error("save api key", "chat", u.ChatID, "err", err)
		b.reply(ctx, u.ChatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	b.reply(ctx, u.ChatID, fmt.Sprintf("✅ Clé enregistrée (chiffrée) et votre message a été supprimé. Crédits restants : %d.\n"+
		"Créez maintenant un suivi avec /nouveau.", credits))
}

func (b *Bot) apiKey(user store.User) (string, error) {
	if len(user.APIKey) == 0 {
		return "", errors.New("aucune clé SearchApi : envoyez /cle")
	}
	return b.Box.Open(user.APIKey)
}

func (b *Bot) showCredits(ctx context.Context, user store.User) {
	key, err := b.apiKey(user)
	if err != nil {
		b.reply(ctx, user.ChatID, html.EscapeString(err.Error()))
		return
	}
	credits, err := b.NewClient(key).Credits(ctx)
	if err != nil {
		b.reply(ctx, user.ChatID, "⚠️ Impossible de lire les crédits : "+html.EscapeString(err.Error()))
		return
	}
	b.reply(ctx, user.ChatID, fmt.Sprintf("Crédits SearchApi restants : <b>%d</b>.", credits))
}

// --- watches

func (b *Bot) watchFromArg(ctx context.Context, chatID int64, arg string) (store.Watch, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil {
		b.reply(ctx, chatID, "Indiquez le numéro du suivi, par exemple <code>/pause 1</code>. Voir /liste.")
		return store.Watch{}, false
	}
	w, err := b.Store.Watch(ctx, chatID, id)
	if err != nil {
		b.reply(ctx, chatID, fmt.Sprintf("Aucun suivi n°%d. Voir /liste.", id))
		return store.Watch{}, false
	}
	return w, true
}

func (b *Bot) listWatches(ctx context.Context, chatID int64) {
	ws, err := b.Store.Watches(ctx, chatID)
	if err != nil {
		slog.Error("list watches", "chat", chatID, "err", err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	if len(ws) == 0 {
		b.reply(ctx, chatID, "Aucun suivi pour l'instant. /nouveau pour en créer un.")
		return
	}
	var sb strings.Builder
	sb.WriteString("<b>Vos suivis</b>\n")
	for _, w := range ws {
		sb.WriteString("\n" + b.describeWatch(w) + "\n")
	}
	b.reply(ctx, chatID, sb.String())
}

func (b *Bot) describeWatch(w store.Watch) string {
	cfg, err := config.Parse([]byte(w.Config))
	if err != nil {
		return fmt.Sprintf("#%d (configuration illisible)", w.ID)
	}
	status := "▶️ prochaine vérification " + w.NextRun.In(paris()).Format("02.01 à 15h04")
	if w.Paused {
		status = "⏸ en pause"
	}
	text := fmt.Sprintf("<b>#%d %s</b> – tous les %d j\n%s\n%s", w.ID, html.EscapeString(cfg.Name), w.EveryDays,
		html.EscapeString(cfg.Describe()), status)
	if h, err := state.Decode([]byte(w.History)); err == nil && len(h.Days) > 0 {
		last := h.Days[len(h.Days)-1]
		text += fmt.Sprintf("\nMeilleur prix au %s : %d €", last.Date, last.BestAny.Price)
		if last.BestDir.Price > 0 {
			text += fmt.Sprintf(" · direct %d €", last.BestDir.Price)
		}
	}
	if w.LastError != "" {
		text += "\n⚠️ " + html.EscapeString(w.LastError)
	}
	return text
}

func (b *Bot) setPaused(ctx context.Context, chatID int64, arg string, paused bool) {
	w, ok := b.watchFromArg(ctx, chatID, arg)
	if !ok {
		return
	}
	w.Paused = paused
	if !paused && w.NextRun.Before(b.now()) {
		w.NextRun = b.now()
	}
	if err := b.Store.SaveWatch(ctx, w); err != nil {
		slog.Error("save watch", "watch", w.ID, "err", err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	if paused {
		b.reply(ctx, chatID, fmt.Sprintf("⏸ Suivi n°%d en pause. /reprendre %d pour le relancer.", w.ID, w.ID))
	} else {
		b.reply(ctx, chatID, fmt.Sprintf("▶️ Suivi n°%d relancé.", w.ID))
	}
}

func (b *Bot) deleteWatch(ctx context.Context, chatID int64, arg string) {
	w, ok := b.watchFromArg(ctx, chatID, arg)
	if !ok {
		return
	}
	if err := b.Store.DeleteWatch(ctx, chatID, w.ID); err != nil {
		slog.Error("delete watch", "watch", w.ID, "chat", w.ChatID, "err", err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	b.reply(ctx, chatID, fmt.Sprintf("🗑 Suivi n°%d supprimé.", w.ID))
}

func (b *Bot) checkNow(ctx context.Context, chatID int64, arg string, opts engine.Options) {
	w, ok := b.watchFromArg(ctx, chatID, arg)
	if !ok {
		return
	}
	b.reply(ctx, chatID, fmt.Sprintf("🔎 Vérification du suivi n°%d en cours…", w.ID))
	b.runWatch(ctx, w, opts, false)
}

func (b *Bot) forget(ctx context.Context, chatID int64, arg string) {
	if normalize(arg) != "confirmer" {
		b.reply(ctx, chatID, "Cela supprime votre clé et tous vos suivis. Pour confirmer : <code>/oublier confirmer</code>")
		return
	}
	if err := b.Store.DeleteUser(ctx, chatID); err != nil {
		slog.Error("delete user", "chat", chatID, "err", err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	b.reply(ctx, chatID, "Toutes vos données ont été supprimées. Au revoir !")
}

// --- scheduled checks

// RunDue checks every watch whose next run has come.
func (b *Bot) RunDue(ctx context.Context) {
	due, err := b.Store.DueWatches(ctx, b.now())
	if err != nil {
		slog.Error("load due watches", "err", err)
		return
	}
	for _, w := range due {
		if ctx.Err() != nil {
			return
		}
		b.runWatch(ctx, w, engine.Options{}, true)
	}
}

// runWatch checks one watch, sends the results to its owner and schedules the next check.
// scheduled is false for checks requested by the user, which keep the current schedule.
func (b *Bot) runWatch(ctx context.Context, w store.Watch, opts engine.Options, scheduled bool) {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	now := b.now()
	lg := slog.With("watch", w.ID, "chat", w.ChatID, "scheduled", scheduled)
	opts.Log = lg

	next := w.NextRun
	if scheduled {
		next = now.AddDate(0, 0, w.EveryDays)
	}
	fail := func(msg string, retry time.Duration) {
		w.LastError = msg
		lg.Warn("watch check not done", "reason", msg, "retry_in", retry)
		if scheduled {
			next = now.Add(retry)
		}
		b.reply(ctx, w.ChatID, fmt.Sprintf("⚠️ Suivi n°%d : %s", w.ID, html.EscapeString(msg)))
	}

	user, err := b.Store.User(ctx, w.ChatID)
	if err != nil {
		lg.Error("load user", "err", err)
		return
	}
	cfg, err := config.Parse([]byte(w.Config))
	if err != nil {
		w.Paused = true
		fail("configuration invalide, suivi mis en pause ("+err.Error()+")", 0)
		b.save(ctx, w, next)
		return
	}
	key, err := b.apiKey(user)
	if err != nil {
		fail(err.Error(), retryWithoutKey)
		b.save(ctx, w, next)
		return
	}
	h, err := state.Decode([]byte(w.History))
	if err != nil {
		lg.Warn("history unreadable, starting over", "err", err)
		h, _ = state.Decode(nil)
	}

	rep, err := engine.Run(ctx, cfg, h, b.NewClient(key), engine.Today(now), opts)
	switch {
	case err != nil:
		fail("la vérification a échoué ("+err.Error()+"), nouvel essai plus tard", retryAfterError)
	case rep.Expired:
		w.Paused, w.LastError = true, "terminé : la période de départ est passée"
		b.reply(ctx, w.ChatID, fmt.Sprintf("🏁 Le suivi n°%d « %s » est terminé : la période de départ est passée. /supprimer %d",
			w.ID, html.EscapeString(cfg.Name), w.ID))
	default:
		w.LastError = ""
		if rep.Paused {
			w.LastError = fmt.Sprintf("crédits SearchApi insuffisants (%d)", rep.CreditsLeft)
			if scheduled {
				next = now.Add(retryWithoutKey)
			}
		}
		msgs := rep.Messages
		if rep.Summary != "" {
			msgs = append(msgs, rep.Summary)
		}
		if len(msgs) == 0 && !scheduled {
			msgs = []string{nothingNew(w.ID, h, rep)}
		}
		if len(msgs) > 0 {
			if err := b.Msg.SendTo(ctx, strconv.FormatInt(w.ChatID, 10), msgs...); err != nil {
				lg.Error("send results", "err", err)
			}
		}
	}

	if data, err := h.Encode(); err == nil {
		w.History = string(data)
	}
	w.LastRun = now
	lg.Info("watch checked", "requests", rep.Requests, "credits_left", rep.CreditsLeft,
		"alerts", len(rep.Deals), "messages", len(rep.Messages), "next_run", next.Format(time.RFC3339))
	b.save(ctx, w, next)
}

// nothingNew answers a manual check without alerts with the current best prices.
func nothingNew(id int64, h *state.History, rep engine.Report) string {
	text := fmt.Sprintf("Suivi n°%d : pas de nouvelle bonne affaire.", id)
	if n := len(h.Days); n > 0 {
		last := h.Days[n-1]
		text += fmt.Sprintf("\nMeilleur prix actuel : %d €", last.BestAny.Price)
		if last.BestDir.Price > 0 {
			text += fmt.Sprintf(" · direct %d €", last.BestDir.Price)
		}
	}
	text += fmt.Sprintf("\n%d requête(s) utilisée(s)", rep.Requests)
	if rep.CreditsLeft >= 0 {
		text += fmt.Sprintf(", %d crédit(s) restant(s)", rep.CreditsLeft)
	}
	return text + "."
}

func (b *Bot) save(ctx context.Context, w store.Watch, next time.Time) {
	w.NextRun = next
	if err := b.Store.SaveWatch(ctx, w); err != nil {
		slog.Error("save watch", "watch", w.ID, "err", err)
	}
}

// --- wizard

func (b *Bot) setWizard(ctx context.Context, chatID int64, wz *wizard) {
	data := ""
	if wz != nil {
		raw, _ := json.Marshal(wz)
		data = string(raw)
	}
	if err := b.Store.SetWizard(ctx, chatID, data); err != nil {
		slog.Error("save wizard", "chat", chatID, "err", err)
	}
}

func paris() *time.Location {
	if loc, err := time.LoadLocation("Europe/Paris"); err == nil {
		return loc
	}
	return time.UTC
}
