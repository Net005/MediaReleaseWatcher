# Media Release Watcher

An MDBList-first, self-hosted release desk for the people and titles you follow.

**Go · PostgreSQL · Docker Compose · Sonarr · Radarr · Silo · Pushover**

Browse past, current and upcoming movies and series in a cinematic, cover-first web interface. Follow actresses, inspect their biographies and genuinely matched releases, and send individual titles—or a confirmed batch—to your media managers.

## Features

- Responsive dark catalogue, real cover art, featured releases, date windows, search, groups and release calendar.
- Actress portrait cards, clickable source-linked cast/person details, upcoming-only filtering and a detail view of genuinely matched titles.
- Bulk actress import with the same group, monitor, automatic-add and notification controls as single-add, with exact-ID deduplication and per-entry overrides.
- Per-personal-MDBList filters and independent monitoring/automatic-add policies. General discovery is unmonitored by default; explicit manual opt-out always wins.
- Infinite release scrolling with deduplication, stale-request cancellation, retry and an accessible load-more fallback.
- Explicit MDBList watchlist add/remove on release details, with remote-state readback rather than optimistic success.
- Read-only Sonarr/Radarr library reconciliation and simple exact-ID In Silo badges with item links.
- Persistent once/interval/daily/weekly schedules, IANA time zones, run-now, enable/disable and next/last-run results.
- MDBList catalogue/list discovery, biographies, ratings, certifications, age guidance, parental-content indicators, release dates, providers and artwork.
- Movie quick-add to Radarr and series quick-add to Sonarr, with profile/root-folder selection, existing-item checks and exact readback verification.
- Per-actress bulk add with item-by-item success, existing, skipped and failure results.
- Opt-in automatic acquisition and Pushover alerts with a cover attachment and release information. Background actions are disabled initially; baseline imports do not send historic notification floods.
- Persistent PostgreSQL state, encrypted integration secrets, single-user password authentication and a cached artwork proxy.
- Activity/progress reporting and configurable background synchronization, without an LLM in the data pipeline.

## Upgrading from the original discovery release

**This upgrade clears the old release catalogue once, using guarded migration 3.** Administrator login, sessions, encrypted integration settings/API keys and actress watches are preserved. Later restarts and upgrades preserve newly collected releases; this is not a recurring reset. Keep the original `.env` and `APP_SECRET`. An invalid encryption key now stops startup before the catalogue reset can run.

Back up the database and `.env`, then update only the app:

```sh
docker compose pull app
docker compose up -d app
```

Run **Sync now** to repopulate the catalogue and inspect Activity. Review the independent per-list opt-ins before enabling automation. Existing library recognition never sends an add or search command. Do not restore the old catalogue over the upgraded policy tables without an intentional rollback plan, and do not delete the database volume.

## Quick start: published container

```sh
git clone https://github.com/Net005/MediaReleaseWatcher.git media-release-watcher
cd media-release-watcher
python3 scripts/configure.py
docker compose up -d
```

Open **http://localhost:8317**, create your administrator password, and configure MDBList in **Settings**. `scripts/configure.py` generates a private `.env` containing unique PostgreSQL and encryption secrets; it never prints their values. Do not overwrite this file after an installation has been configured.

The default application image is:

```text
ghcr.io/net005/mediareleasewatcher:latest
```

The publishing workflow also produces commit tags (`sha-…`) and version tags for `v*` releases. Set `MRW_IMAGE_TAG` in `.env` to select a published tag. If the package is private, authenticate Docker to GHCR with a credential authorized to read that package before starting.

### Local source build

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

Only this explicit override builds local source. Ordinary `docker compose up -d` uses GHCR.

### Network exposure

Compose binds the web UI to **127.0.0.1** by default and does not publish PostgreSQL. For trusted-LAN access set `APP_BIND=0.0.0.0` in `.env`, or put a TLS reverse proxy in front. Set `PUBLIC_URL=https://your-exact-host` in `.env` before first setup through an HTTPS reverse proxy. The same **Public URL** setting is available in the web UI for notification links and strict reverse-proxy origin checks. Do not expose an unfinished first-run setup screen to the public Internet.

## Configure your sources

1. Save your MDBList API key in Settings (or supply `MDBLIST_API_KEY` through `.env`). Existing blank secret inputs mean **keep the saved value**, not erase it.
2. Select personal MDBList sources or enter numeric list IDs. Each has independent Enabled, Monitor and Auto-add controls; Monitor and Auto-add start off. Movie and show lists are separate sources; all pages are collected. No personal lists or private roster are shipped with the repository.
3. Add actresses using a literal TMDb person ID and known source name, or import a JSON watch list. Group names are user-defined and not inferred from ethnicity.
4. Run synchronization and inspect Activity for coverage and any provider limitations.

Import shape:

```json
{
  "group": "Default group",
  "monitored": true,
  "auto_add": false,
  "notify": false,
  "entries": [
    {"name": "Example performer", "actor_id": "123456789", "group": "My group"}
  ]
}
```

This is a **synthetic format example**, not a real performer record. Top-level fields are defaults for new entries; explicit entry flags and source groups take precedence. Ordinary defaults do not replace existing watch preferences. Explicit per-entry preference updates can apply to an existing exact ID without creating another watch. Source IDs are preserved rather than silently repaired. Missing provider records remain explicit warnings.

### MDBList first; supplements only when needed

MDBList is the primary backend for title metadata, images and biographies. Its person-detail endpoint does **not** provide complete filmographies, and membership in a multi-actress list does **not** establish that every actress appears in every title.

Only explicit source-linked identities produce actress/title matches. When missing television credits need a supplement, the app can resolve an exact name/date-of-birth identity using TVmaze's public cast-credit API, then obtain the title metadata from MDBList. This is television-only, source-attributed and cached—not a claim of complete movie or television coverage. An optional `TMDB_API_KEY` environment variable supports additional missing-credit lookups where configured. Adding a local watcher never silently changes your MDBList favorites.

**Missing and ambiguous information stays missing or ambiguous.** A low database score is not an age rating; a keyword is not proof of a nude scene; a provider name is not proof of playback availability in your country. Official/aggregate certification, suggested age and review ratings are displayed as different measures.

## Sonarr and Radarr

Configure each service's base URL, API key, quality profile and root folder in Settings. URLs may include the service's installation prefix; do not append `/api/v3`. If the manager runs on another machine, use an address reachable from the application container. `localhost` inside the app container refers to the app container, not your host or another service.

- **Movies → Radarr. Series → Sonarr.** The app rejects incompatible targets.
- Quick-add and bulk-add are explicit web actions. Search-on-add is an explicit option; an add is not proof that a download completed.
- Automatic acquisition additionally requires a monitored title, an explicit per-list or per-actress automatic-add rule, the global switch, and a configured/enabled destination.
- Existing items are checked before adding. A new add is read back from the exact remote ID before success is recorded. Uncertain or partial results remain visible.

## Local availability and schedules

Configure Silo in Settings and use **Test** / **Sync Silo**. A unique, type-consistent TMDb, IMDb or TVDb ID match shows **In Silo** with a link to that item. This checks library membership, not playback health; no file probes or playback sessions are started. Ambiguous matches and failed/incomplete lookups remain unknown/error, never a false absence. Sonarr/Radarr membership is displayed separately.

The Tasks section supports catalogue refresh, Silo availability, Arr library reconciliation and explicitly permitted missing-release searches. Schedules persist across restarts, including disabled jobs and edits. IANA zones and local dates/times are validated; nonexistent DST times are not silently shifted. Download searches require the task's explicit permission, global automation, enabled compatible integration and that integration's search opt-in. Searches target only exact manager IDs corresponding to app-monitored releases, never an entire manager library.

Watchlist controls are independent of these tasks: background discovery never adds to or removes from your MDBList watchlist.

## Pushover

Configure your Pushover application token and user/group key, save, then use the explicit test action. Background alerts require opt-in settings. Where available, new-match alerts attach a validated cover image and include release information and a deep link to the app. Set **Public URL** to an address your phone can reach.

The app checks both HTTP status and Pushover's JSON status. Ambiguous deliveries are not blindly resent because Pushover does not supply an idempotency key for ordinary messages. Provider acceptance and actual display on your device are different verification levels.

## Persistence and security

- `postgres-data`: application database, encrypted settings and action history.
- `artwork-data`: verified poster/profile cache.
- `.env`: database password and `APP_SECRET`, required to decrypt stored credentials. Back it up securely alongside the database.
- The application runs as a non-root container user with a read-only root filesystem, a writable data volume, dropped capabilities and no-new-privileges.
- Integration secrets are never returned by the settings-read API. Sessions use HttpOnly cookies; modifying browser requests are origin-checked.
- Artwork requests are restricted to supported public image hosts with size, MIME and decoded-image checks.

Do not run `docker compose down -v` on an installation whose data you want to keep.

## Development and verification

Go 1.25 or later and PostgreSQL 17 are required. The frontend is embedded static HTML/CSS/ES-module JavaScript; it requires no production Node service or asset CDN.

```sh
go test -race ./...
go vet ./...
npm ci --no-audit --no-fund
npm run test:ui
# With a disposable app serving the frontend on port 8318:
npx playwright install chromium
npm run test:e2e
go build ./cmd/media-release-watcher
```

Database tests use **`TEST_DATABASE_URL` and truncate their disposable database**. Never point them at your real installation. Without that variable, database integration tests are explicitly skipped. Provider contract tests use synthetic local HTTP servers; they never add media to real managers or send live notifications.

`scripts/smoke.py --url http://127.0.0.1:8317` exercises setup/authentication, settings, catalogue APIs and actress CRUD against an **empty, disposable installation**. It refuses to modify an existing setup.

GitHub Actions runs Go tests/vet, frontend unit and browser-workflow tests, builds an image, starts a disposable Compose stack and runs the API smoke checks before publishing the GHCR image. No MDBList key or personal library data is supplied to CI.

## Sources and attribution

- Primary metadata: [MDBList](https://mdblist.com/), [API documentation](https://api.mdblist.com/docs/).
- Supplemental TV cast credits: [TVmaze](https://www.tvmaze.com/), [API and licensing](https://www.tvmaze.com/api), [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/). Source links are retained with matched records.
- [Sonarr API](https://sonarr.tv/docs/api/), [Radarr API](https://radarr.video/docs/api/), [Pushover API](https://pushover.net/api).

Artwork remains owned by its respective rights holders. Personal credentials, account responses, watch lists and deployment state are runtime data—not repository fixtures.
