package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// tmdbUnavailable carries only a safe retry time, never an upstream URL/body.
type tmdbUnavailable struct{ RetryAt time.Time }

func (e *tmdbUnavailable) Error() string { return "TMDb response unavailable" }
func tmdbRetryAt(err error, fallback time.Time) time.Time {
	var unavailable *tmdbUnavailable
	if errors.As(err, &unavailable) && unavailable.RetryAt.After(fallback) {
		return unavailable.RetryAt
	}
	return fallback
}

// tmdbGet never returns upstream bodies, URLs or transport errors containing credentials.
func (a *App) tmdbGet(ctx context.Context, s Settings, path string) (map[string]any, error) {
	if !s.TMDB.Enabled || s.TMDB.APIKey == "" {
		return nil, errors.New("TMDb is disabled or API key is not configured")
	}
	base := strings.TrimRight(a.cfg.TMDBBaseURL, "/")
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?api_key="+url.QueryEscape(s.TMDB.APIKey), nil)
	if e != nil {
		return nil, errors.New("TMDb request invalid")
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") }}
	var res *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		res, e = client.Do(req.Clone(ctx))
		if e != nil {
			return nil, errors.New("TMDb request failed")
		}
		if res.StatusCode == http.StatusOK {
			break
		}
		status, retryAfter := res.StatusCode, res.Header.Get("Retry-After")
		res.Body.Close()
		now := time.Now()
		retryAt := now.Add(time.Duration(1<<attempt) * 250 * time.Millisecond)
		if seconds, err := strconv.ParseInt(strings.TrimSpace(retryAfter), 10, 64); err == nil && seconds >= 0 {
			// Bound invalid/hostile durations before converting to nanoseconds.
			if seconds > int64((365*24*time.Hour)/time.Second) {
				seconds = int64((365 * 24 * time.Hour) / time.Second)
			}
			provided := now.Add(time.Duration(seconds) * time.Second)
			if provided.After(retryAt) {
				retryAt = provided
			}
		} else if provided, err := http.ParseTime(retryAfter); err == nil && provided.After(retryAt) {
			retryAt = provided
		}
		unavailable := &tmdbUnavailable{RetryAt: retryAt}
		if attempt == 2 || (status != 429 && status != 502 && status != 503 && status != 504) {
			return nil, unavailable
		}
		if deadline, ok := ctx.Deadline(); ok && !retryAt.Before(deadline) {
			return nil, unavailable
		}
		timer := time.NewTimer(time.Until(retryAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, unavailable
		case <-timer.C:
		}
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if e != nil || len(b) > 8<<20 {
		return nil, errors.New("TMDb response invalid")
	}
	m, e := decodeObject(b)
	if e != nil {
		return nil, errors.New("TMDb response invalid")
	}
	if success, ok := m["success"].(bool); ok && !success {
		return nil, errors.New("TMDb response unsuccessful")
	}
	return m, nil
}
func (a *App) testTMDB(ctx context.Context, s Settings) error {
	m, e := a.tmdbGet(ctx, s, "/authentication")
	if e != nil {
		return e
	}
	if m["success"] != true {
		return errors.New("TMDb authentication unsuccessful")
	}
	return nil
}

func (a *App) tmdbPerson(ctx context.Context, s Settings, p Actress) (Actress, error) {
	if strings.HasPrefix(p.MetadataStatus, "unavailable: identity mismatch") {
		return p, errors.New("TMDb identity quarantined")
	}
	if !numericID.MatchString(p.TMDbID) {
		return p, errors.New("invalid canonical TMDb person identifier")
	}
	m, e := a.tmdbGet(ctx, s, "/person/"+p.TMDbID)
	if e != nil {
		return p, e
	}
	valid := p.Name == "" || identityName(first(m, "name")) == identityName(p.Name)
	for _, alias := range array(m["also_known_as"]) {
		if v, ok := alias.(string); ok && identityName(v) == identityName(p.Name) {
			valid = true
		}
	}
	if first(m, "id") != p.TMDbID || first(m, "name") == "" || !valid {
		return p, errors.New("TMDb person identity mismatch")
	}
	if p.Name == "" {
		p.Name = first(m, "name")
	}
	if p.Biography == "" {
		p.Biography = first(m, "biography")
	}
	if p.Birthday == "" {
		p.Birthday = dateOnly(first(m, "birthday"))
	}
	if p.PlaceOfBirth == "" {
		p.PlaceOfBirth = first(m, "place_of_birth")
	}
	if p.Profile == "" {
		if v := first(m, "profile_path"); strings.HasPrefix(v, "/") {
			p.Profile = "https://image.tmdb.org/t/p/w500" + v
		}
	}
	missing := []string{}
	for _, f := range []struct{ name, value string }{{"portrait", p.Profile}, {"biography", p.Biography}, {"birthday", p.Birthday}, {"place of birth", p.PlaceOfBirth}} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	p.MetadataStatus = "ok: MDBList primary; missing fields supplemented by TMDb; actor credits separate"
	if len(missing) > 0 {
		p.MetadataStatus = "partial: MDBList primary with TMDb fallback; missing " + strings.Join(missing, ", ") + "; actor credits separate"
	}
	return p, nil
}

type tmdbCredit struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Resolved  bool      `json:"resolved"`
	Attempts  int       `json:"attempts,omitempty"`
	Attempted time.Time `json:"attempted,omitempty"`
	NextRetry time.Time `json:"next_retry,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (a *App) tmdbPersonCredits(ctx context.Context, s Settings, id string) ([]tmdbCredit, error) {
	if !numericID.MatchString(id) {
		return nil, errors.New("invalid canonical TMDb person identifier")
	}
	m, e := a.tmdbGet(ctx, s, "/person/"+id+"/combined_credits")
	if e != nil {
		return nil, e
	}
	if first(m, "id") != id {
		return nil, errors.New("TMDb credits identity mismatch")
	}
	cast, ok := m["cast"].([]any)
	if !ok {
		return nil, errors.New("TMDb cast response invalid")
	}
	out := []tmdbCredit{}
	seen := map[string]bool{}
	for _, v := range cast {
		o := object(v)
		kind := first(o, "media_type")
		if kind == "tv" {
			kind = "show"
		}
		title := first(o, "id")
		if !oneOf(kind, "movie", "show") || !numericID.MatchString(title) {
			return nil, errors.New("TMDb cast identity invalid")
		}
		key := kind + ":" + title
		if !seen[key] {
			seen[key] = true
			out = append(out, tmdbCredit{ID: title, Kind: kind})
		}
	}
	return out, nil
}

// Optional missing-cast fallback only. MDBList remains the title metadata provider.
func (a *App) tmdbCast(ctx context.Context, r *Release) ([]string, error) {
	s, e := a.settings(ctx)
	if e != nil {
		return nil, e
	}
	if !s.TMDB.Enabled || s.TMDB.APIKey == "" || r.TMDbID == "" {
		return nil, nil
	}
	if !numericID.MatchString(r.TMDbID) {
		return nil, errors.New("invalid canonical TMDb identifier")
	}
	kind := "movie"
	if r.Type == "show" {
		kind = "tv"
	}
	m, e := a.tmdbGet(ctx, s, "/"+kind+"/"+r.TMDbID+"/credits")
	if e != nil {
		return nil, e
	}
	if first(m, "id") != r.TMDbID {
		return nil, errors.New("TMDb credits identity mismatch")
	}
	if _, ok := m["cast"].([]any); !ok {
		return nil, errors.New("TMDb cast response invalid")
	}
	r.Cast = castDetails(m)
	return castIDs(m), nil
}
