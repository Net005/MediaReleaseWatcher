package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Credit is supplemental TVMaze cast-credit discovery, not replacement media
// metadata. URL is the TVMaze source attribution (CC BY-SA 4.0); the caller must
// preserve attribution/license obligations and enrich these IDs via MDBList.
type Credit struct {
	Title, IMDbID, URL string
	TVDbID, Year       int
}

type requestGate struct {
	token chan struct{}
	next  time.Time
}

func newRequestGate() *requestGate { return &requestGate{token: make(chan struct{}, 1)} }

var sharedTVMazeGate = newRequestGate()

// wait limits request starts to at most one per second across production
// clients. Waiting is context-cancelable, including contention on the gate.
func (g *requestGate) wait(ctx context.Context) error {
	select {
	case g.token <- struct{}{}:
	case <-ctx.Done():
		return errors.New("TVMaze request canceled or timed out")
	}
	defer func() { <-g.token }()
	if delay := time.Until(g.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return errors.New("TVMaze request canceled or timed out")
		}
	}
	if ctx.Err() != nil {
		return errors.New("TVMaze request canceled or timed out")
	}
	g.next = time.Now().Add(time.Second)
	return nil
}

func (c *Client) tvmazeGet(ctx context.Context, path string, q url.Values, out any) error {
	if ctx.Err() != nil {
		return errors.New("TVMaze request canceled or timed out")
	}
	c.mu.Lock()
	until := c.cooldown["TVMaze"]
	c.mu.Unlock()
	if remaining := time.Until(until); remaining > 0 {
		return &HTTPError{Service: "TVMaze", StatusCode: 429, RetryAfter: remaining}
	}
	target, err := endpoint(c.tvmazeBaseURL, path, q)
	if err != nil {
		return err
	}
	if err := c.tvmazeGate.wait(ctx); err != nil {
		return err
	}
	raw, err := c.request(ctx, "TVMaze", http.MethodGet, target, "", "", nil)
	if err != nil {
		return err
	}
	return decode(raw, out, "TVMaze")
}

type tvPerson struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Birthday string `json:"birthday"`
}
type tvPersonResult struct {
	Person tvPerson `json:"person"`
}

func personName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func hyphenSearch(s string) string {
	replaced := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Pd, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(replaced), " ")
}

// TVMazeCredits is a keyless, needed-only supplemental television cast-credit
// lookup. Never use the fuzzy first result: names must match ignoring case,
// whitespace and punctuation, and a supplied DOB must match a known DOB exactly.
// More than one matching person is an error. No film, guest-credit completeness,
// inferred release dates, or MDBList /people filmography capability is claimed.
// The caller owns caching and must call this only if MDBList lacks credits.
func (c *Client) TVMazeCredits(ctx context.Context, name, birthday string) ([]Credit, error) {
	empty := []Credit{}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 250 || personName(name) == "" {
		return nil, errors.New("TVMaze requires a valid person name")
	}
	if birthday != "" {
		if _, err := time.Parse("2006-01-02", birthday); err != nil {
			return nil, errors.New("TVMaze requires an ISO person birthday")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var people []tvPersonResult
	if err := c.tvmazeGet(ctx, "/search/people", url.Values{"q": {name}}, &people); err != nil {
		return nil, err
	}
	wantedName := personName(name)
	exactName := func(rows []tvPersonResult) bool {
		for _, row := range rows {
			if personName(row.Person.Name) == wantedName {
				return true
			}
		}
		return false
	}
	// Hyphenated names can be omitted by TVMaze's fuzzy search. A second query
	// improves recall, but does not weaken the exact identity check afterwards.
	alternative := hyphenSearch(name)
	if !exactName(people) && alternative != name {
		if err := c.tvmazeGet(ctx, "/search/people", url.Values{"q": {alternative}}, &people); err != nil {
			return nil, err
		}
	}
	candidates := make(map[int]tvPerson)
	for _, row := range people {
		p := row.Person
		if p.ID <= 0 || personName(p.Name) != wantedName {
			continue
		}
		if birthday != "" && p.Birthday != birthday {
			continue
		}
		candidates[p.ID] = p
	}
	if len(candidates) == 0 {
		return nil, errors.New("TVMaze found no exact person identity; supplemental credits unavailable")
	}
	if len(candidates) != 1 {
		return nil, errors.New("TVMaze person identity is ambiguous; supplemental credits unavailable")
	}
	id := 0
	for candidateID := range candidates {
		id = candidateID
	}
	var rows []struct {
		Embedded struct {
			Show struct {
				ID        int    `json:"id"`
				Name      string `json:"name"`
				URL       string `json:"url"`
				Premiered string `json:"premiered"`
				Externals struct {
					IMDb string `json:"imdb"`
					TVDb int    `json:"thetvdb"`
				} `json:"externals"`
			} `json:"show"`
		} `json:"_embedded"`
	}
	if err := c.tvmazeGet(ctx, "/people/"+strconv.Itoa(id)+"/castcredits", url.Values{"embed": {"show"}}, &rows); err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	for _, row := range rows {
		show := row.Embedded.Show
		if show.ID <= 0 || strings.TrimSpace(show.Name) == "" {
			return nil, errors.New("TVMaze returned incomplete embedded show credits")
		}
		if seen[show.ID] {
			continue
		}
		seen[show.ID] = true
		if show.Externals.IMDb != "" && !imdbID.MatchString(show.Externals.IMDb) || show.Externals.TVDb < 0 {
			return nil, errors.New("TVMaze returned an invalid external show ID")
		}
		if show.Externals.IMDb == "" && show.Externals.TVDb == 0 {
			continue
		}
		source, err := url.Parse(show.URL)
		expectedPath := "/shows/" + strconv.Itoa(show.ID)
		if err != nil || source.Scheme != "https" || (source.Host != "www.tvmaze.com" && source.Host != "tvmaze.com") || source.User != nil || source.RawQuery != "" || source.Fragment != "" || (source.Path != expectedPath && !strings.HasPrefix(source.Path, expectedPath+"/")) {
			return nil, errors.New("TVMaze returned an invalid show attribution URL")
		}
		year := 0
		if show.Premiered != "" {
			if date, err := time.Parse("2006-01-02", show.Premiered); err == nil {
				year = date.Year()
			}
		}
		empty = append(empty, Credit{Title: show.Name, IMDbID: show.Externals.IMDb, TVDbID: show.Externals.TVDb, Year: year, URL: show.URL})
	}
	return empty, nil
}
