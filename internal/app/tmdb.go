package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Optional missing-cast fallback only. MDBList remains the title metadata provider.
func (a *App) tmdbCast(ctx context.Context, r Release) ([]string, error) {
	if a.cfg.TMDBAPIKey == "" || r.TMDbID == "" {
		return nil, nil
	}
	if !numericID.MatchString(r.TMDbID) {
		return nil, errors.New("invalid canonical TMDb identifier")
	}
	kind := "movie"
	if r.Type == "show" {
		kind = "tv"
	}
	u := "https://api.themoviedb.org/3/" + kind + "/" + r.TMDbID + "/credits?api_key=" + url.QueryEscape(a.cfg.TMDBAPIKey)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", u, nil)
	if e != nil {
		return nil, errors.New("TMDb credits request invalid")
	}
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") }}
	res, e := client.Do(req)
	if e != nil {
		return nil, errors.New("TMDb credits request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("TMDb credits unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return nil, errors.New("TMDb credits response invalid")
	}
	m, e := decodeObject(b)
	if e != nil {
		return nil, e
	}
	return castIDs(m), nil
}
