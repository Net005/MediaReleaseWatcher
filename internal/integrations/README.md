# HTTP integration adapters

Implements the exact `docs/CONTRACT.md` package interface using the Go standard library, plus the requested supplemental `TVMazeCredits(ctx, name, birthday) ([]Credit, error)` method. Do not serialize `Config`: it contains credentials. Reuse a client while its configuration remains unchanged to preserve its provider cooldowns and per-client acquisition serialization.

## Provider contracts and safety

- **MDBList:** default `https://api.mdblist.com`; configured API key goes in the documented `apikey` query parameter. `MDBGet` only accepts the contract's read-only discovery paths. `MDBBatch` accepts 1–100 canonical positive numeric TMDb IDs for `movie` or `show`, sends numeric `ids` plus `append_to_response: ["keyword"]`, and checks every returned object's `ids.tmdb` and `type` against the requested set. Responses are not ordered to match inputs; absent titles may be omitted. IMDb batches are deliberately rejected because the live schema's integer-only request definition does not establish string-ID support. Individual IMDb GET lookups are supported. There are no remote favorites, lists, account, watchlist, or other mutations.
- **Sonarr/Radarr:** base URLs may intentionally be LAN HTTP/HTTPS endpoints, optionally with an installation prefix (not `/api/v3`). Credentials use `X-Api-Key`, never query strings. `Add` validates media type and IDs, validates an explicit root folder/profile against current options, deduplicates existing IDs, obtains authoritative lookup metadata, posts explicit monitoring/search settings, and GETs the exact returned internal ID. Verification checks native identity, profile, root/path, monitoring, tags, Radarr minimum availability, and Sonarr season monitoring. Existing items are identity-readback-verified without changing their settings. Empty Radarr minimum availability defaults explicitly to `released`. Add/readback uncertainty returns an error and is never automatically retried. Serialization prevents duplicate writes on the same client; separate processes can still race, and remote uniqueness enforcement remains necessary. Successful verification proves stored configuration, not eventual download/search completion.
- **Pushover:** multipart fields are `token`, `user`, `title`, `message`, `url`, `url_title`, `device`, and optional binary `attachment` with image MIME metadata. The caller supplies content information and a click-through URL; the adapter neither infers content ratings nor fetches arbitrary image URLs. PNG/JPEG/GIF images must decode, fit within 5,242,880 bytes and conservative dimension limits, and are not base64 encoded. A successful HTTP response must contain JSON `status: 1`; HTTP 200 with `status: 0` fails. No automatic send retries.
- **TVMaze supplemental television cast credits:** keyless public GET `/search/people?q=...`, then `/people/{id}/castcredits?embed=show`. Names must match exactly ignoring case, whitespace, and punctuation. A supplied DOB must match a known provider DOB exactly; ambiguous people fail. One alternate hyphen-to-space query is attempted only if the first query contains no exact name. Never select the first fuzzy match. The adapter returns only actual embedded show credits with usable IMDb/TVDB IDs and TVMaze source URLs, deduplicates show IDs, and preserves unknown premiere years as zero. Production clients share a context-cancelable one-request-per-second gate. The **caller owns caching**, needed-only fallback selection, MDBList re-enrichment, attribution, and applicable CC BY-SA 4.0 obligations. This is not filmography completeness, a movie-credit source, or a substitute for MDBList metadata.

All HTTP requests have a 20-second timeout and honor caller cancellation. TVMaze's entire lookup also has a 20-second deadline including rate waits. Responses are capped at 8 MiB, request bodies at 6 MiB, MDBList query strings at 16 KiB. Redirects are blocked for every provider, including same-origin redirects, to avoid credential forwarding or replaying writes. Endpoint userinfo, query parameters, fragments, and path traversal are rejected. Errors never include remote response bodies, credential values, or request URLs. Typed `*HTTPError` exposes `StatusCode` and `RetryAfter`: HTTP 429 records a per-client/provider cooldown, honors valid delta-seconds/HTTP-date `Retry-After`, and defaults invalid/missing values to one minute. Calls during cooldown fail locally; there are no sleep/retry loops or automatic POST retries.

## Authoritative sources

- MDBList official OpenAPI schema at `https://api.mdblist.com/schema/` (authenticated first-party access); read-only batch semantics, top-level array response, numeric TMDb IDs and the `keyword` append field were verified against the API. No private account responses are included in fixtures.
- Sonarr API docs: `https://sonarr.tv/docs/api/`; official `Sonarr/Sonarr` develop `src/Sonarr.Api.V3/openapi.json`.
- Radarr API docs: `https://radarr.video/docs/api/`; official `Radarr/Radarr` develop `src/Radarr.Api.V3/openapi.json`.
- Pushover Message API: `https://pushover.net/api`.
- TVMaze public API, cast-credit embedding, rate-limit and license documentation: `https://www.tvmaze.com/api`.

## Verification

From the repository root with Go 1.25+:

```sh
go test -race -count=2 -coverprofile=/path/outside/repo/integration-coverage.out ./internal/integrations
go vet ./internal/integrations
go tool cover -func=/path/outside/repo/integration-coverage.out
```

Use an installed Go 1.25+ toolchain. Set `GOTOOLCHAIN=local` when toolchain auto-download is intentionally disabled.

Tests exclusively use synthetic fixtures and `httptest` loopback servers. They exercise success, deduplication, exact readback mismatch, false/true monitoring and search options, lookup identity, configuration validation, auth placement, multipart binary attachments, status-zero rejection, redirects, payload bounds, cancellation, sanitized errors, cooldowns, exact person matching/ambiguity, source URLs, and request pacing. No live acquisition writes or notification sends are performed. Live deployment connectivity, account entitlements, and real download or device delivery remain unverified by these offline tests.
