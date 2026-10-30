# tokyo-flight-watch

Surveille chaque jour les prix aller-retour **FRA / CGN / DUS → Tokyo (HND, NRT)** pour un départ entre le 22 mars et le 15 avril 2027 (séjour de 7 à 17 jours). Envoie un message **Telegram** quand une offre correspond aux critères de `config.yaml`, plus un résumé chaque semaine. L'e-mail par SMTP reste possible en secours.

## Fonctionnement d'un lancement
1. **Scan du calendrier** (moteur `google_flights_calendar` de SearchApi) : toutes les combinaisons de dates sont tarifées en 3 requêtes, une fois pour toutes les compagnies (≤ 1 escale), une fois pour les vols **directs Star Alliance** uniquement. Cela fait 6 requêtes.
2. **Sélection des candidats** : les dates dont le prix du calendrier déclenche une règle, puis la combinaison la moins chère du jour, pour lire l'avis de Google sur le prix.
3. **Recherche détaillée** sur au plus `max_detail_searches` candidats (2 par défaut). Elle confirme la compagnie, l'aéroport et les escales, puis applique les règles.
4. **Alerte Telegram** pour les nouvelles offres. Le **résumé hebdo** part le jour `summary_weekday`.

## Règles d'alerte (une seule suffit)
- Vol **direct Star Alliance** ≤ `direct_star_alliance_max_price` (1300 €)
- Toute autre offre (≤ 1 escale) ≤ `default_max_price` (1050 €)
- Baisse ≥ `price_drop_percent` (15 %) par rapport au plus bas déjà relevé pour les mêmes dates
- **Nouveau plus bas** toutes dates confondues, d'au moins `new_low_min_percent` (2 %)
- Google Flights juge le prix **« bas »** (`alert_on_google_low`)
- Narita n'est signalé que s'il coûte au moins `narita_min_saving` (100 €) de moins que la meilleure offre Haneda
- Une même offre n'est pas renvoyée tant que son prix ne baisse pas

## Mise en place
1. Créez une clé API sur https://www.searchapi.io. Le calendrier de prix n'existe que chez SearchApi.
2. Telegram : écrivez à **@BotFather**, envoyez `/newbot` et choisissez un nom. Il vous donne un **token** (`123456:ABC…`).
3. Ouvrez votre nouveau bot dans Telegram et envoyez-lui `/start`. Lancez ensuite la commande suivante pour obtenir votre `TELEGRAM_CHAT_ID` :
   ```sh
   TELEGRAM_BOT_TOKEN=... go run ./cmd/watch -telegram-chat-id
   ```
4. Créez un dépôt GitHub **privé** et poussez ce dossier dans ce dépôt.
5. Dans *Settings → Secrets and variables → Actions*, ajoutez `SEARCHAPI_KEY`, `TELEGRAM_BOT_TOKEN` et `TELEGRAM_CHAT_ID`.
6. Onglet *Actions* → « Tokyo flight watch » → **Run workflow** pour un premier essai.

Le workflow tourne ensuite chaque jour. L'heure demandée est 6 h UTC, mais GitHub décale souvent les tâches planifiées de plusieurs heures. Il commit `state/history.json`, l'historique des prix.

Pour recevoir les alertes par e-mail plutôt que par Telegram, ne définissez pas `TELEGRAM_BOT_TOKEN`. Définissez à la place `SMTP_HOST` (`smtp.gmail.com` par défaut), `SMTP_PORT` (587), `SMTP_USER`, `SMTP_APP_PASSWORD`, `SMTP_FROM` et `ALERT_TO`.

## En local
```sh
export SEARCHAPI_KEY=... TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
go test ./...
go run ./cmd/watch -dry-run -summary   # scan réel, messages affichés, rien n'est envoyé ni sauvegardé
go run ./cmd/watch -force-notify       # envoie un message même sans offre, pour tester
```

## Quota
Un lancement consomme au plus 6 + `max_detail_searches` requêtes, soit **8 par jour, environ 240 par mois**. L'offre gratuite de SearchApi donne 100 requêtes au total, pas par mois. Au-delà, il faut l'offre Developer (40 $/mois pour 10 000 requêtes) ou des lancements plus espacés : modifiez le `cron` dans `.github/workflows/watch.yml`, par exemple `0 6 */3 * *` pour un lancement tous les 3 jours. Le résumé hebdo indique les requêtes consommées sur 7 jours.
