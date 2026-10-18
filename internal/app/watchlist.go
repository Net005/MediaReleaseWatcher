package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"media-release-watcher/internal/integrations"
)

// Watchlist mutations are only exposed by the explicit authenticated HTTP route.
// They are independent of monitoring, static-list membership and acquisition.
func (a *App) registerWatchlistRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/releases/{id}/watchlist", a.releaseWatchlist)
	mux.HandleFunc("POST /api/releases/{id}/watchlist", a.releaseWatchlist)
}

type watchlistDesired struct{ InWatchlist bool }

// Parse exactly one literal desired-state field. Last-key-wins or case-folded
// JSON must not decide which account mutation the user authorized.
func (v *watchlistDesired) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	invalid := errors.New("expected only in_watchlist boolean")
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalid
	}
	key, err := d.Token()
	if err != nil || key != "in_watchlist" {
		return invalid
	}
	token, err = d.Token()
	b, ok := token.(bool)
	if err != nil || !ok || d.More() {
		return invalid
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return invalid
	}
	v.InWatchlist = b
	return nil
}

func (a *App) releaseWatchlist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var desired watchlistDesired
	if r.Method == http.MethodPost {
		if !readJSON(w, r, &desired) {
			return
		}
	}
	var media integrations.MDBWatchlistMedia
	// Never choose an account-write identity from stale/untrusted metadata JSON.
	err := a.db.QueryRow(r.Context(), `SELECT media_type,tmdb_id,imdb_id FROM releases WHERE id=$1`, id).Scan(&media.Type, &media.TMDbID, &media.IMDbID)
	if err != nil {
		resultError(w, err, "release")
		return
	}
	s, err := a.settings(r.Context())
	if err != nil {
		respond(w, 500, integrations.MDBWatchlistResult{Status: "unknown", CheckedAt: time.Now().UTC(), Error: "cannot read settings"})
		return
	}
	var result integrations.MDBWatchlistResult
	if r.Method == http.MethodPost {
		result, err = a.client(s).MDBSetWatchlist(r.Context(), media, desired.InWatchlist)
	} else {
		result, err = a.client(s).MDBWatchlistContains(r.Context(), media)
	}
	if err != nil {
		respond(w, 502, result)
		return
	}
	respond(w, 200, result)
}
