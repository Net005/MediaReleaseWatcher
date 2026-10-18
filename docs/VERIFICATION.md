# Verification record

Local verification on 2026-10-18. This records exercised behavior, not a claim of complete provider coverage.

## Executed successfully

| Check | Result |
| --- | --- |
| Go race suite against disposable PostgreSQL 17.6 | 180 passing test/subtest events; no test failures or skipped tests |
| Go static analysis | `go vet ./...` passed |
| Go binary build | Passed; embedded frontend served by the real app |
| Frontend unit and browser-handler suite | 22 passed |
| Parent Playwright end-to-end suite | 6 passed |
| API smoke against a separate disposable database | Setup, authentication, settings redaction, catalogue APIs and actress import/detail/edit/delete passed |
| Default and source-build Compose configurations | `config --quiet` passed |
| Live read-only MDBList import | 1,933 title records; complete discovery established a silent initial baseline |
| Private grouped watch-list import and readback | 121 literal source IDs: 46 Asian actresses entries, 75 Other actresses entries |
| Live Chromium inspection | Real portraits, biographies, cover art, profile matches, desktop and mobile UI exercised; no page errors or mobile horizontal overflow |

The main command package contains no tests; this is distinct from skipped database tests. Provider tests use synthetic local HTTP servers. Frontend fixtures are explicitly synthetic test data and are never seeded into the production catalogue.

## Regression gates exercised

- Metadata refresh cannot restore stale monitoring/automation preferences or resurrect a deleted watch.
- Every known initial title is silently baselined, including unmatched and unmonitored titles.
- Complete discovery can finish its baseline despite optional per-title enrichment gaps; failed discovery cannot.
- Malformed feed envelopes, invalid pagination types and conflicting cursors/continuation signals cannot commit a baseline.
- Optional non-IMDb `tr…` source placeholders are retained in raw metadata and never rewritten into IMDb IDs. A separate valid canonical identity is required.
- Batch result identities are mapped independently of returned order; sparse updates preserve richer metadata.
- Invalid or mismatched person identities are quarantined rather than silently renamed.
- HTTPS reverse-proxy first-run setup uses the explicitly configured public origin; unrelated origins are rejected.
- Unsupported TVDB-only supplemental lookups report incomplete coverage instead of being cached as complete.
- Single and bulk acquisition require explicit confirmation, compatible managers, verified readback and honest per-item results.
- Initial sync stays silent; global acquisition, managers and Pushover were left disabled during the live import.
- Loaded artwork no longer displays an unavailable-artwork label over a valid image. Unknown negative rating sentinels are not presented as scores or age guidance.

## Deliberate limits

- No real Sonarr/Radarr additions or Pushover sends were performed. Contract tests do not establish delivery to a real library or phone.
- Supplemental TVmaze credits are television-only and incomplete. MDBList biographies are not filmographies; source-list membership is not individual cast evidence.
- Local Docker daemon execution is unavailable in this environment. GitHub Actions is configured to build an image and run a real isolated Compose/API/browser smoke stack before GHCR publication; remote execution must be checked separately.
- Personal credentials, full source watch list, account responses, test database contents and live screenshots are runtime/private data excluded from Git and the Docker build context.
