# tokyo-flight-watch

Surveille les prix aller-retour **FRA / CGN / DUS → Tokyo (HND, NRT)** pour un départ entre le 22 mars et le 15 avril 2027 (séjour de 7 à 17 jours), avec les données Google Flights de [SearchApi](https://www.searchapi.io). Envoie les bonnes affaires sur **Telegram**, avec un résumé périodique. L'e-mail par SMTP reste possible en secours.

## Fonctionnement d'un lancement
1. **Crédits** : vérifie le solde SearchApi. S'il est insuffisant, le suivi se met en pause et prévient sur Telegram (une fois par jour).
2. **Scan du calendrier** (`google_flights_calendar`) : toutes les combinaisons de dates sont tarifées en 3 requêtes par catégorie :
   - **toutes compagnies**, au plus `max_stops` escale ;
   - **vols directs** (aller *et* retour) des compagnies `direct_airlines` (Star Alliance), si `watch_direct`.
3. **Signal** : pour chaque catégorie, le prix le plus bas du jour déclenche une alerte s'il est
   - sous la **cible** (`target_price`, `target_price_direct`), **ou**
   - au moins `sensitivity_percent` % sous le **meilleur prix habituel**, c'est-à-dire la médiane des meilleurs prix quotidiens des `typical_window_days` derniers jours (il faut au moins 3 jours d'historique),
   - **et** s'il bat la dernière alerte de la catégorie d'au moins max(`min_improvement_eur`, `min_improvement_percent` %). Une alerte est oubliée quand son tarif a disparu depuis `alert_expiry_runs` lancements.
4. **Vérification** : une recherche détaillée par alerte, avec **les mêmes filtres** que le calendrier, donne la compagnie et l'aéroport. Haneda est retenu, sauf si un autre aéroport est au moins `narita_min_saving` € moins cher. Si elle échoue, l'alerte part quand même avec le prix du calendrier.
5. **Messages** : un message Telegram par lancement (alertes + avertissements). Les échecs de lancement et les scans incomplets sont signalés. Un résumé part tous les `summary_every_days` jours.

## Mise en place
1. Créez une clé API sur https://www.searchapi.io.
2. Telegram : écrivez à **@BotFather**, envoyez `/newbot` et choisissez un nom. Il vous donne un **token**.
3. Envoyez `/start` à votre bot. Lancez ensuite la commande suivante pour obtenir votre `TELEGRAM_CHAT_ID` :
   ```sh
   TELEGRAM_BOT_TOKEN=... go run ./cmd/watch -telegram-chat-id
   ```
4. Dans *Settings → Secrets and variables → Actions* du dépôt GitHub, ajoutez `SEARCHAPI_KEY`, `TELEGRAM_BOT_TOKEN` et `TELEGRAM_CHAT_ID`.
5. Onglet *Actions* → « Tokyo flight watch » → **Run workflow**. Le champ « args » accepte par exemple `-dry-run -summary`.

Le workflow tourne ensuite chaque jour. GitHub décale souvent les tâches planifiées de plusieurs heures. Il commit `state/history.json`, qui contient les statistiques quotidiennes et la mémoire des alertes.

## En local
```sh
export SEARCHAPI_KEY=... TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=...
go test ./...
go run ./cmd/watch -dry-run -summary   # scan réel, messages affichés, rien n'est envoyé ni sauvegardé
go run ./cmd/watch -force-notify       # envoie un message même sans offre, pour tester
```

## Quota
Un lancement coûte 3 requêtes par catégorie (6 avec les vols directs), plus 1 par alerte, dans la limite de `max_requests_per_run`. L'offre gratuite de SearchApi donne 100 requêtes **au total**. Au-delà, il faut l'offre Developer (40 $/mois) ou des lancements plus espacés : modifiez le `cron` de `.github/workflows/watch.yml`, par exemple `0 6 */3 * *` pour un lancement tous les 3 jours. Le résumé indique les crédits restants.
