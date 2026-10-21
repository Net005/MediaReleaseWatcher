package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

// showExternalIDsError contains only a fixed local classification, never provider data.
type showExternalIDsError struct{ Code string }

func (e *showExternalIDsError) Error() string { return "TMDb external IDs: " + e.Code }
func externalIDsError(code string) error      { return &showExternalIDsError{Code: code} }

var externalPositiveID = regexp.MustCompile(`^[1-9][0-9]*$`)
var externalIMDbID = regexp.MustCompile(`^tt[0-9]{7,12}$`)

func externalLiteralID(s string) bool {
	n, e := strconv.ParseInt(s, 10, 64)
	return externalPositiveID.MatchString(s) && e == nil && n > 0
}

// resolveShowExternalIDs resolves only missing manager identities, not metadata.
// Primary provider contract: developer.themoviedb.org/reference/tv-series-external-ids.md.
func (a *App) resolveShowExternalIDs(ctx context.Context, s Settings, r Release) (result Release, resultErr error) {
	if r.Type != "show" || r.IMDbID != "" || r.TVDbID != "" || !s.TMDB.Enabled || s.TMDB.APIKey == "" {
		return r, nil
	}
	if !externalLiteralID(r.TMDbID) {
		return r, externalIDsError("invalid_identity")
	}
	config, _ := json.Marshal([]string{a.cfg.TMDBBaseURL, s.TMDB.APIKey})
	digest := sha256.Sum256(config)
	key := "tmdb_external_ids:" + r.TMDbID + ":" + hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	proof, _ := json.Marshal(map[string]any{"source_url": "https://www.themoviedb.org/tv/" + r.TMDbID, "checked_at": now, "retry_after": now.Add(24 * time.Hour), "status": "checking"})
	// Claim before HTTP, so concurrent workers also respect the negative cache.
	claim, err := a.db.Exec(ctx, `INSERT INTO app_state(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE (app_state.value->>'retry_after')::timestamptz<=now()`, key, proof)
	if err != nil {
		return r, externalIDsError("cache_unavailable")
	}
	if claim.RowsAffected() != 1 {
		return r, externalIDsError("cached_attempt")
	}
	defer func() {
		status := "resolved"
		if e, ok := resultErr.(*showExternalIDsError); ok {
			status = e.Code
		} else if resultErr != nil {
			status = "unavailable"
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		// Do not persist transport errors, bodies, endpoint configuration or credentials.
		_, _ = a.db.Exec(cleanup, `UPDATE app_state SET value=value||jsonb_build_object('status',$2::text) WHERE key=$1 AND value->>'checked_at'=$3`, key, status, now.Format(time.RFC3339Nano))
	}()
	m, err := a.tmdbGet(ctx, s, "/tv/"+r.TMDbID+"/external_ids")
	if err != nil {
		return r, externalIDsError("lookup_failed")
	}
	id, ok := m["id"].(json.Number)
	if !ok || id.String() != r.TMDbID {
		return r, externalIDsError("identity_mismatch")
	}
	imdb, tvdb := "", ""
	if value := m["imdb_id"]; value != nil {
		var ok bool
		imdb, ok = value.(string)
		if !ok || imdb != "" && !externalIMDbID.MatchString(imdb) {
			return r, externalIDsError("invalid_imdb")
		}
	}
	if value := m["tvdb_id"]; value != nil {
		number, ok := value.(json.Number)
		if !ok || !externalLiteralID(number.String()) {
			return r, externalIDsError("invalid_tvdb")
		}
		tvdb = number.String()
	}
	if imdb == "" && tvdb == "" {
		return r, externalIDsError("no_supported_identity")
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return r, externalIDsError("persistence_failed")
	}
	defer tx.Rollback(ctx)
	// Serialize the short identity check/update against ALL writers, including inserts
	// without advisory locks. No network request runs while this lock is held.
	if _, err = tx.Exec(ctx, `LOCK TABLE releases IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return r, externalIDsError("persistence_failed")
	}
	var conflict bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM releases WHERE id<>$1 AND (($2<>'' AND (imdb_id=$2 OR data->>'imdb_id'=$2)) OR ($3<>'' AND data->>'tvdb_id'=$3)))`, r.ID, imdb, tvdb).Scan(&conflict)
	if err != nil {
		return r, externalIDsError("persistence_failed")
	}
	if conflict {
		return r, externalIDsError("identity_conflict")
	}
	tag, err := tx.Exec(ctx, `UPDATE releases SET imdb_id=$2,data=data||jsonb_build_object('imdb_id',$2::text,'tvdb_id',$3::text),updated_at=now() WHERE id=$1 AND media_type=$4 AND tmdb_id=$5 AND imdb_id=$6 AND COALESCE(data->>'tvdb_id','')=$7 AND COALESCE(data->>'imdb_id','')=$6`, r.ID, imdb, tvdb, r.Type, r.TMDbID, r.IMDbID, r.TVDbID)
	if err != nil {
		return r, externalIDsError("persistence_failed")
	}
	if tag.RowsAffected() != 1 {
		return r, externalIDsError("stale_release")
	}
	// Read the exact row back inside the same transaction before exposing identities.
	result, err = scanRelease(tx.QueryRow(ctx, releaseSelect+`WHERE r.id=$1`, r.ID))
	if err != nil || result.IMDbID != imdb || result.TVDbID != tvdb || result.TMDbID != r.TMDbID || result.Type != r.Type {
		return r, externalIDsError("readback_failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return r, externalIDsError("persistence_failed")
	}
	return result, nil
}
