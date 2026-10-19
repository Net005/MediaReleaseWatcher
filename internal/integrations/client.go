// Package integrations implements bounded, credential-safe provider adapters.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config contains server-side credentials. Never serialize it into API responses.
type Config struct{ MDBListAPIKey, MDBListBaseURL, SonarrURL, SonarrAPIKey, RadarrURL, RadarrAPIKey, PushoverToken, PushoverUser, PushoverURL, SiloURL, SiloAPIKey string }
type Options struct {
	RootFolders     []RootFolder     `json:"root_folders"`
	QualityProfiles []QualityProfile `json:"quality_profiles"`
}
type RootFolder struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}
type QualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type Media struct {
	Title, Type, IMDbID, PosterURL string
	TMDbID, TVDbID, Year           int
}
type AddOptions struct {
	QualityProfileID  int
	RootFolderPath    string
	Monitored, Search bool
	// RestoreMonitoring is explicit manual consent for an existing exact item.
	// Leave false for automatic acquisition and read-only reconciliation.
	RestoreMonitoring   bool
	Tags                []int
	MinimumAvailability string
}
type AddResult struct {
	ID                int    `json:"id"`
	AlreadyExists     bool   `json:"already_exists"`
	URL               string `json:"url"`
	MonitoredRestored bool   `json:"monitored_restored"`
	SearchStarted     bool   `json:"search_started"`
}
type Notification struct {
	Title, Message, URL, URLTitle, Device, AttachmentName string
	Attachment                                            []byte
}

const (
	requestTimeout     = 20 * time.Second
	maxResponseBytes   = 8 << 20
	maxRequestBytes    = 6 << 20
	maxAttachmentBytes = 5_242_880
)

// HTTPError deliberately excludes URLs, remote bodies and credentials. A 429
// records a per-provider cooldown; callers may schedule a later attempt, but the
// adapter never automatically retries requests (particularly writes).
type HTTPError struct {
	Service    string
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e.StatusCode == 429 {
		return fmt.Sprintf("%s rate limited; retry after %s", e.Service, e.RetryAfter.Round(time.Second))
	}
	return fmt.Sprintf("%s returned HTTP %d", e.Service, e.StatusCode)
}

type Client struct {
	cfg           Config
	http          *http.Client
	mu            sync.Mutex
	cooldown      map[string]time.Time
	addMu         sync.Mutex
	siloMu        sync.Mutex
	siloCache     *siloSnapshot
	tvmazeBaseURL string
	tvmazeGate    *requestGate
}

func NewClient(cfg Config) *Client {
	if cfg.MDBListBaseURL == "" {
		cfg.MDBListBaseURL = "https://api.mdblist.com"
	}
	if cfg.PushoverURL == "" {
		cfg.PushoverURL = "https://api.pushover.net/1/messages.json"
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: requestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") }}, cooldown: make(map[string]time.Time), tvmazeBaseURL: "https://api.tvmaze.com", tvmazeGate: sharedTVMazeGate}
}

func endpoint(base, path string, q url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\\r\n") {
		return "", errors.New("invalid integration endpoint configuration")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("invalid integration endpoint configuration")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) request(ctx context.Context, service, method, target, key, contentType string, body []byte) (json.RawMessage, error) {
	return c.requestHeaders(ctx, service, method, target, key, contentType, body, nil)
}

func (c *Client) requestHeaders(ctx context.Context, service, method, target, key, contentType string, body []byte, headers http.Header) (json.RawMessage, error) {
	if len(body) > maxRequestBytes {
		return nil, errors.New("integration request exceeds size limit")
	}
	c.mu.Lock()
	until := c.cooldown[service]
	c.mu.Unlock()
	if remaining := time.Until(until); remaining > 0 {
		return nil, &HTTPError{Service: service, StatusCode: 429, RetryAfter: remaining}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s request configuration invalid", service)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MediaReleaseWatcher")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	for name, values := range headers {
		req.Header[name] = values
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s request canceled or timed out", service)
		}
		return nil, fmt.Errorf("%s request failed", service)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		e := &HTTPError{Service: service, StatusCode: res.StatusCode}
		if res.StatusCode == 429 {
			e.RetryAfter = retryAfter(res.Header.Get("Retry-After"))
			c.mu.Lock()
			c.cooldown[service] = time.Now().Add(e.RetryAfter)
			c.mu.Unlock()
		}
		return nil, e
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s response read failed", service)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%s response exceeds size limit", service)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%s returned invalid JSON", service)
	}
	return json.RawMessage(raw), nil
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return time.Minute
}

func decode(raw json.RawMessage, target any, service string) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
		return fmt.Errorf("%s returned an unexpected response", service)
	}
	return nil
}
