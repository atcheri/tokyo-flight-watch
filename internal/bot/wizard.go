package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"strings"

	"github.com/atch/tokyo-flight-watch/internal/config"
	"github.com/atch/tokyo-flight-watch/internal/engine"
	"github.com/atch/tokyo-flight-watch/internal/notify"
	"github.com/atch/tokyo-flight-watch/internal/store"
)

// wizard is the state of a multi-message input, stored as JSON with the user.
type wizard struct {
	Step      string        `json:"step"`
	Draft     config.Config `json:"draft"`
	EveryDays int           `json:"every_days"`
}

const (
	stepKey          = "key"
	stepName         = "name"
	stepOrigins      = "origins"
	stepDestinations = "destinations"
	stepDates        = "dates"
	stepStay         = "stay"
	stepStops        = "stops"
	stepTarget       = "target"
	stepDirect       = "direct"
	stepTargetDirect = "target_direct"
	stepEvery        = "every"
	stepConfirm      = "confirm"
)

var prompts = map[string]string{
	stepKey: "Collez votre clé API SearchApi (page « API Key » sur searchapi.io). " +
		"Je supprimerai votre message aussitôt et la clé sera stockée chiffrée.",
	stepName:         "1/9 – Quel nom pour ce suivi ? (par exemple <i>Tokyo printemps</i>)",
	stepOrigins:      "2/9 – Aéroports de départ ? Codes à 3 lettres, par exemple <code>FRA, CGN, DUS</code>",
	stepDestinations: "3/9 – Aéroports d'arrivée ? Le premier est votre préféré, par exemple <code>HND, NRT</code>",
	stepDates: "4/9 – Quand voulez-vous partir ?\n" +
		"• des dates fixes : <code>22.03.2027-15.04.2027</code> (première et dernière date de départ possibles)\n" +
		"• ou une période glissante : <code>dans 60-150 jours</code>",
	stepStay:  "5/9 – Durée du séjour en jours ? Par exemple <code>10-14</code> ou <code>12±3</code>",
	stepStops: "6/9 – Nombre d'escales maximum ? <code>0</code> (direct), <code>1</code> ou <code>2</code>",
	stepTarget: "7/9 – Prix cible en € (aller-retour par personne) ? Je préviens dès qu'un prix passe dessous. " +
		"Répondez <code>non</code> pour être prévenu seulement quand un prix est nettement sous le prix habituel.",
	stepDirect: "8/9 – Surveiller aussi les <b>vols directs</b> (aller et retour) d'une alliance ? " +
		"<code>star</code>, <code>oneworld</code>, <code>skyteam</code> ou <code>non</code>",
	stepTargetDirect: "8/9 – Prix cible en € pour ces vols directs ? (ou <code>non</code>)",
	stepEvery:        "9/9 – Vérifier tous les combien de jours ? (1 à 7 ; chaque vérification consomme des crédits SearchApi)",
}

func (b *Bot) startWizard(ctx context.Context, user store.User) {
	ws, err := b.Store.Watches(ctx, user.ChatID)
	if err != nil {
		log.Printf("count watches of %d: %v", user.ChatID, err)
		b.reply(ctx, user.ChatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	if len(ws) >= b.maxWatches() {
		b.reply(ctx, user.ChatID, fmt.Sprintf("Vous avez déjà %d suivis (maximum). Supprimez-en un avec /supprimer N.", len(ws)))
		return
	}
	intro := "Créons un suivi. /annuler à tout moment.\n\n"
	if len(user.APIKey) == 0 {
		intro = "ℹ️ Pensez à enregistrer votre clé SearchApi avec /cle, sans quoi le suivi ne pourra pas tourner.\n\n" + intro
	}
	b.setWizard(ctx, user.ChatID, &wizard{Step: stepName, Draft: config.Default()})
	b.reply(ctx, user.ChatID, intro+prompts[stepName])
}

func (b *Bot) handleWizard(ctx context.Context, user store.User, u notify.Update) {
	var wz wizard
	if err := json.Unmarshal([]byte(user.Wizard), &wz); err != nil {
		b.setWizard(ctx, user.ChatID, nil)
		b.reply(ctx, user.ChatID, "La saisie a été réinitialisée. /nouveau pour recommencer.")
		return
	}
	if wz.Step == stepKey {
		b.setWizard(ctx, user.ChatID, nil)
		b.saveKey(ctx, u, u.Text)
		return
	}

	next, err := b.advance(&wz, u.Text)
	if err != nil {
		b.reply(ctx, user.ChatID, "❌ "+html.EscapeString(err.Error())+"\n\n"+prompts[wz.Step])
		return
	}
	switch next {
	case "":
		b.setWizard(ctx, user.ChatID, nil)
		b.reply(ctx, user.ChatID, "Création annulée. /nouveau pour recommencer.")
	case "save":
		b.setWizard(ctx, user.ChatID, nil)
		b.createWatch(ctx, user.ChatID, wz)
	case stepConfirm:
		wz.Step = stepConfirm
		text, ok := b.recap(wz)
		if !ok {
			wz.Step = stepDates // too many requests per check: narrow the dates
		}
		b.setWizard(ctx, user.ChatID, &wz)
		b.reply(ctx, user.ChatID, text)
	default:
		wz.Step = next
		b.setWizard(ctx, user.ChatID, &wz)
		b.reply(ctx, user.ChatID, prompts[next])
	}
}

// advance applies an answer to the draft and returns the next step: a step name,
// "save" to create the watch, or "" when the user declined at confirmation.
func (b *Bot) advance(wz *wizard, answer string) (string, error) {
	d := &wz.Draft
	switch wz.Step {
	case stepName:
		name := strings.TrimSpace(answer)
		if name == "" || len([]rune(name)) > 40 {
			return "", fmt.Errorf("un nom de 1 à 40 caractères")
		}
		d.Name = name
		return stepOrigins, nil
	case stepOrigins:
		codes, err := parseAirports(answer)
		if err != nil {
			return "", err
		}
		d.Origins = codes
		return stepDestinations, nil
	case stepDestinations:
		codes, err := parseAirports(answer)
		if err != nil {
			return "", err
		}
		d.Destinations, d.PreferredDestination = codes, codes[0]
		return stepDates, nil
	case stepDates:
		w, err := parseWindow(answer, engine.Today(b.now()))
		if err != nil {
			return "", err
		}
		d.DepartureFrom, d.DepartureTo, d.DepartureInDaysMin, d.DepartureInDaysMax = w.From, w.To, w.InMin, w.InMax
		if d.MinStayDays > 0 {
			return stepConfirm, nil // dates changed after a too-expensive recap
		}
		return stepStay, nil
	case stepStay:
		lo, hi, err := parseRange(answer)
		if err != nil {
			return "", err
		}
		d.MinStayDays, d.MaxStayDays = lo, hi
		return stepStops, nil
	case stepStops:
		n, err := parseStops(answer)
		if err != nil {
			return "", err
		}
		d.MaxStops = n
		return stepTarget, nil
	case stepTarget:
		p, err := parsePrice(answer)
		if err != nil {
			return "", err
		}
		d.TargetPrice = p
		return stepDirect, nil
	case stepDirect:
		a, err := parseAlliance(answer)
		if err != nil {
			return "", err
		}
		d.WatchDirect, d.DirectAirlines = a != "", a
		if a == "" {
			return stepEvery, nil
		}
		return stepTargetDirect, nil
	case stepTargetDirect:
		p, err := parsePrice(answer)
		if err != nil {
			return "", err
		}
		d.TargetPriceDirect = p
		return stepEvery, nil
	case stepEvery:
		n, err := parseEvery(answer)
		if err != nil {
			return "", err
		}
		wz.EveryDays = n
		d.SummaryEveryDays = max(7, n)
		return stepConfirm, nil
	case stepConfirm:
		switch {
		case isYes(answer):
			return "save", nil
		case isNo(answer):
			return "", nil
		}
		return "", fmt.Errorf("répondez oui pour créer le suivi, ou non pour annuler")
	}
	return "", fmt.Errorf("étape inconnue, /annuler puis /nouveau")
}

// recap describes the draft with its cost. ok is false when a check would need more
// requests than allowed, in which case the text asks for narrower dates.
func (b *Bot) recap(wz wizard) (text string, ok bool) {
	cfg := wz.Draft
	if err := cfg.Validate(); err != nil {
		return "❌ " + html.EscapeString(err.Error()) + "\n\n" + prompts[stepDates], false
	}
	cost := engine.ScanCost(&cfg, engine.Today(b.now()))
	if cost > cfg.MaxRequestsPerRun {
		return fmt.Sprintf("❌ Cette période est trop large : il faudrait %d requêtes par vérification (maximum %d). "+
			"Réduisez la période de départ ou l'écart de durée de séjour.\n\n%s", cost, cfg.MaxRequestsPerRun, prompts[stepDates]), false
	}

	var rules []string
	if cfg.TargetPrice > 0 {
		rules = append(rules, fmt.Sprintf("prix sous %d €", cfg.TargetPrice))
	}
	if cfg.WatchDirect && cfg.TargetPriceDirect > 0 {
		rules = append(rules, fmt.Sprintf("%s sous %d €", strings.ToLower(cfg.DirectLabel()), cfg.TargetPriceDirect))
	}
	rules = append(rules, fmt.Sprintf("prix au moins %d %% sous le meilleur prix habituel", cfg.SensitivityPercent))
	perMonth := (cost*30 + wz.EveryDays - 1) / wz.EveryDays

	return fmt.Sprintf("<b>%s</b>\n%s\n\nAlerte si : %s.\nVérification tous les %d jour(s), résumé tous les %d jours.\n\n"+
		"Coût : %d requête(s) SearchApi par vérification (+1 par alerte), soit environ <b>%d par mois</b>.\n\n"+
		"Créer ce suivi ? (oui / non)",
		html.EscapeString(cfg.Name), html.EscapeString(cfg.Describe()), strings.Join(rules, " ; "),
		wz.EveryDays, cfg.SummaryEveryDays, cost, perMonth), true
}

func (b *Bot) createWatch(ctx context.Context, chatID int64, wz wizard) {
	data, err := wz.Draft.Marshal()
	if err != nil {
		log.Printf("marshal watch config: %v", err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	now := b.now()
	id, err := b.Store.CreateWatch(ctx, store.Watch{
		ChatID: chatID, Config: string(data), EveryDays: wz.EveryDays, NextRun: now, CreatedAt: now,
	})
	if err != nil {
		log.Printf("create watch for %d: %v", chatID, err)
		b.reply(ctx, chatID, "⚠️ Erreur interne, réessayez plus tard.")
		return
	}
	b.reply(ctx, chatID, fmt.Sprintf("✅ Suivi n°%d créé. La première vérification a lieu dans quelques minutes ; "+
		"je vous écris dès qu'un prix intéressant apparaît, et un résumé arrive après la première vérification.", id))
}
