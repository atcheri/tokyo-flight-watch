# tokyo-flight-watch

Surveille chaque jour les prix aller-retour **FRA / CGN / DUS → Tokyo (HND, NRT)** pour un départ entre le 22 mars et le 15 avril 2027 (séjour de 7 à 17 jours). Envoie un message **Telegram** quand une offre correspond aux critères de `config.yaml`. L'e-mail par SMTP reste possible en secours.

## Règles d'alerte
- Vol **direct Star Alliance** (Lufthansa, ANA…) ≤ `direct_star_alliance_max_price` (1300 €)
- Toute autre offre (≤ 1 escale) ≤ `default_max_price` (900 €)
- **ou** baisse ≥ `price_drop_percent` (15 %) par rapport au plus bas déjà relevé pour les mêmes dates
- Narita n'est signalé que s'il coûte au moins `narita_min_saving` (100 €) de moins que la meilleure offre Haneda
- Une même offre n'est pas renvoyée tant que son prix ne baisse pas

Pour un aller-retour, Google Flights détaille seulement l'aller. Le critère « direct Star Alliance » porte donc sur l'aller.

## Mise en place
1. Créez une clé API sur https://www.searchapi.io (ou sur https://serpapi.com, en mettant `provider: serpapi` dans `config.yaml`).
2. Telegram : écrivez à **@BotFather**, envoyez `/newbot` et choisissez un nom. Il vous donne un **token** (`123456:ABC…`).
3. Ouvrez votre nouveau bot dans Telegram et envoyez-lui `/start`. Lancez ensuite la commande suivante pour obtenir votre `TELEGRAM_CHAT_ID` :
   ```sh
   TELEGRAM_BOT_TOKEN=... go run ./cmd/watch -telegram-chat-id
   ```
4. Créez un dépôt GitHub **privé** et poussez ce dossier dans ce dépôt.
5. Dans *Settings → Secrets and variables → Actions*, ajoutez `SEARCHAPI_KEY`, `TELEGRAM_BOT_TOKEN` et `TELEGRAM_CHAT_ID`.
6. Onglet *Actions* → « Tokyo flight watch » → **Run workflow** pour un premier essai.

Le workflow tourne ensuite chaque jour à 6 h UTC. Il commit `state/history.json`, l'historique des prix.

Pour recevoir les alertes par e-mail plutôt que par Telegram, ne définissez pas `TELEGRAM_BOT_TOKEN`. Définissez à la place `SMTP_HOST` (`smtp.gmail.com` par défaut), `SMTP_PORT` (587), `SMTP_USER`, `SMTP_APP_PASSWORD`, `SMTP_FROM` et `ALERT_TO`.

## En local
```sh
export SEARCHAPI_KEY=... TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
go test ./...
go run ./cmd/watch -dry-run -limit 2       # 2 vraies recherches, message affiché, rien n'est sauvegardé
go run ./cmd/watch -limit 1 -force-notify  # envoie un message même sans offre, pour tester
```

## Quota
`searches_per_day: 8` correspond à environ 240 recherches par mois. Ajustez cette valeur selon votre offre SearchApi/SerpApi. Les séjours de 10 à 14 jours sont vérifiés deux fois plus souvent que les autres. Les dates qui ont déjà donné une bonne offre sont revérifiées trois fois plus souvent.
