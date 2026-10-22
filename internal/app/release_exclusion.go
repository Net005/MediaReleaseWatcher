package app

import (
	"context"
	"fmt"
	"net/http"
)

// The key is the literal provider identity, never a title or secondary-ID guess.
// Keep the archived row and cast/source provenance; exclusion is not a merge.
func (a *App) releaseExclusion(ctx context.Context, kind, tmdb string) (string, error) {
	var excluded bool
	var canonical string
	err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app_state WHERE key=$1),COALESCE((SELECT value->>'canonical_tmdb_id' FROM app_state WHERE key=$1),'')`, "release_exclusion:"+kind+":"+tmdb).Scan(&excluded, &canonical)
	if err != nil {
		return "", err
	}
	if excluded {
		return fmt.Sprintf("release excluded: verified duplicate/special; canonical TMDb %s", canonical), nil
	}
	return "", nil
}

func (a *App) rejectExcludedRelease(w http.ResponseWriter, r *http.Request, id int64) bool {
	var kind, tmdb string
	if err := a.db.QueryRow(r.Context(), `SELECT media_type,tmdb_id FROM releases WHERE id=$1`, id).Scan(&kind, &tmdb); err != nil {
		resultError(w, err, "release")
		return true
	}
	reason, err := a.releaseExclusion(r.Context(), kind, tmdb)
	if err != nil {
		fail(w, 500, "cannot read release exclusion")
		return true
	}
	if reason != "" {
		fail(w, 409, reason)
		return true
	}
	return false
}
