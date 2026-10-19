package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManualAcquireFailureNeverClaimsVerifiedSuccess(t *testing.T) {
	for _, failure := range []string{"restore", "restore-readback", "command", "command-readback"} {
		t.Run(failure, func(t *testing.T) {
			a, server, cookie, arr, _ := acquisitionRestoreApp(t)
			arr.fail = failure
			release := acquisitionRestoreRelease(t, a, "movie", nil)
			ctx := context.Background()
			if _, err := a.db.Exec(ctx, `UPDATE releases SET acquisition='{"radarr_status":"absent","radarr_checked_at":"prior-check"}' WHERE id=$1`, release.ID); err != nil {
				t.Fatal(err)
			}
			status, raw := watchlistHTTP(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/releases/%d/acquire", release.ID), `{"target":"radarr","search":true}`, server.URL)
			if status != 502 || strings.Contains(string(raw), `"ok":true`) || strings.Contains(string(raw), "private synthetic failure") || strings.Contains(string(raw), "synthetic-arr-key") {
				t.Fatalf("unverified or unsafe response: %d %s", status, raw)
			}
			got, err := a.getRelease(ctx, release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Acquisition) != 2 || got.Acquisition["radarr_status"] != "absent" || got.Acquisition["radarr_checked_at"] != "prior-check" || got.Monitored {
				t.Fatalf("failure invented local evidence: %+v", got.Acquisition)
			}
			var successes int
			if err := a.db.QueryRow(ctx, `SELECT count(*) FROM activity WHERE kind='acquisition' AND status='ok'`).Scan(&successes); err != nil {
				t.Fatal(err)
			}
			if successes != 0 {
				t.Fatal("unverified acquisition logged as successful")
			}
			arr.mu.Lock()
			defer arr.mu.Unlock()
			puts, posts := 0, 0
			for _, call := range arr.calls {
				if strings.HasPrefix(call, "PUT ") {
					puts++
				}
				if strings.HasPrefix(call, "POST ") {
					posts++
				}
			}
			wantPosts := 1
			if strings.HasPrefix(failure, "restore") {
				wantPosts = 0
			}
			if puts != 1 || posts != wantPosts {
				t.Fatalf("failure retried or searched before verification: %v", arr.calls)
			}
		})
	}
}

func TestManualBulkAcquireFailureStaysPerItem(t *testing.T) {
	for _, failure := range []string{"restore", "restore-readback", "command", "command-readback"} {
		t.Run(failure, func(t *testing.T) {
			a, server, cookie, arr, settings := acquisitionRestoreApp(t)
			badArr := newAcquisitionRestoreArr()
			badArr.fail = failure
			remote := httptest.NewServer(badArr)
			t.Cleanup(remote.Close)
			settings.Sonarr.URL = remote.URL
			if err := a.saveSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			person, err := a.saveActress(context.Background(), Actress{TMDbID: "123", Name: "Synthetic cast"})
			if err != nil {
				t.Fatal(err)
			}
			movie := acquisitionRestoreRelease(t, a, "movie", []string{"123"})
			show := acquisitionRestoreRelease(t, a, "show", []string{"123"})
			status, raw := watchlistHTTP(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/actresses/%d/acquire", person.ID), `{"scope":"all"}`, server.URL)
			if status != 200 {
				t.Fatalf("%d %s", status, raw)
			}
			var response struct {
				Results []bulkResult `json:"results"`
				Added   int          `json:"added"`
				Skipped int          `json:"skipped"`
				Failed  int          `json:"failed"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 2 || response.Added != 0 || response.Skipped != 1 || response.Failed != 1 {
				t.Fatalf("incorrect per-item counts: %s", raw)
			}
			seen := map[int64]bool{}
			for _, row := range response.Results {
				if seen[row.ReleaseID] {
					t.Fatalf("duplicate row: %s", raw)
				}
				seen[row.ReleaseID] = true
				if row.ReleaseID == movie.ID {
					if !row.OK || !row.MonitoredRestored || !row.SearchStarted || row.Error != "" {
						t.Fatalf("successful movie lost: %s", raw)
					}
				} else if row.ReleaseID == show.ID {
					if row.OK || row.AlreadyExists || row.MonitoredRestored || row.SearchStarted || row.Error == "" {
						t.Fatalf("failed show claimed success: %s", raw)
					}
				} else {
					t.Fatalf("unexpected release row: %s", raw)
				}
			}
			assertAcquisitionRestoreRemote(t, arr, "movie")
			got, err := a.getRelease(context.Background(), show.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Acquisition["sonarr"] != "" || got.Acquisition["sonarr_status"] != "" || got.Monitored {
				t.Fatalf("failed show invented local state: %+v", got)
			}
		})
	}
}

func TestAutomaticAcquisitionNeverRestoresExistingArrMonitoring(t *testing.T) {
	for _, mode := range []string{"legacy-acquire", "automation-off", "integration-off", "automatic-enabled"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _, arr, settings := acquisitionRestoreApp(t)
			ctx := context.Background()
			if _, err := a.saveActress(ctx, Actress{TMDbID: "123", Name: "Synthetic cast", Monitored: true, AutoAdd: true}); err != nil {
				t.Fatal(err)
			}
			release := acquisitionRestoreRelease(t, a, "movie", []string{"123"})
			if mode == "legacy-acquire" {
				result, err := a.acquire(ctx, release, settings, "radarr", true, 0, "")
				if err != nil || !result.AlreadyExists || result.MonitoredRestored || result.SearchStarted {
					t.Fatalf("legacy acquire result: %+v %v", result, err)
				}
			} else {
				settings.Automation.Enabled = mode != "automation-off"
				settings.Radarr.Enabled = mode != "integration-off"
				settings.Radarr.Search = true
				if _, err := a.db.Exec(ctx, `UPDATE releases SET monitored=true,monitoring_override=true,data=jsonb_set(data,'{release_date}',to_jsonb($2::text)) WHERE id=$1`, release.ID, time.Now().UTC().Format("2006-01-02")); err != nil {
					t.Fatal(err)
				}
				if err := a.automate(ctx, settings, false); err != nil {
					t.Fatal(err)
				}
				var status string
				if err := a.db.QueryRow(ctx, `SELECT status FROM actions WHERE release_id=$1 AND kind='acquire'`, release.ID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				want := "skipped"
				if mode == "automatic-enabled" {
					want = "done"
				}
				if status != want {
					t.Fatalf("automation status=%s want=%s", status, want)
				}
			}
			arr.mu.Lock()
			defer arr.mu.Unlock()
			if arr.items["movie"]["monitored"] != false || len(arr.commands) != 0 {
				t.Fatalf("automatic acquisition restored monitoring or searched: %v", arr.calls)
			}
			for _, call := range arr.calls {
				if !strings.HasPrefix(call, "GET ") {
					t.Fatalf("automatic path mutated existing item: %v", arr.calls)
				}
			}
		})
	}
}

func TestArrPresenceSyncDoesNotRestoreUnmonitoredItems(t *testing.T) {
	a, _, _, arr, _ := acquisitionRestoreApp(t)
	acquisitionRestoreRelease(t, a, "movie", nil)
	acquisitionRestoreRelease(t, a, "show", nil)
	if err := a.syncArr(context.Background()); err != nil {
		t.Fatal(err)
	}
	arr.mu.Lock()
	defer arr.mu.Unlock()
	for _, call := range arr.calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("read-only sync wrote: %v", arr.calls)
		}
	}
	if arr.items["movie"]["monitored"] != false || arr.items["series"]["monitored"] != false || len(arr.commands) != 0 {
		t.Fatal("presence sync restored Arr monitoring or searched")
	}
}

func TestManualAcquirePersistsVerifiedPresenceAndActivity(t *testing.T) {
	a, server, cookie, _, _ := acquisitionRestoreApp(t)
	release := acquisitionRestoreRelease(t, a, "movie", nil)
	ctx := context.Background()
	if _, err := a.db.Exec(ctx, `UPDATE releases SET acquisition='{"radarr_status":"absent","radarr_error":"old failure","radarr_ready":"true","radarr_file_count":"1"}' WHERE id=$1`, release.ID); err != nil {
		t.Fatal(err)
	}
	status, raw := watchlistHTTP(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/releases/%d/acquire", release.ID), `{"target":"radarr"}`, server.URL)
	if status != 200 {
		t.Fatalf("%d %s", status, raw)
	}
	got, err := a.getRelease(ctx, release.ID)
	if err != nil {
		t.Fatal(err)
	}
	ac := got.Acquisition
	if ac["radarr"] != "already_exists" || ac["radarr_status"] != "present" || ac["radarr_id"] != "7" || ac["radarr_url"] == "" || ac["radarr_checked_at"] == "" || ac["radarr_error"] != "" || ac["radarr_last_check_status"] != "ok" {
		t.Fatalf("verified manager membership not persisted: %+v", ac)
	}
	// Acquisition readback proves membership, not file readiness. Preserve prior
	// file evidence until a later inventory reconciliation observes it directly.
	if ac["radarr_ready"] != "true" || ac["radarr_file_count"] != "1" {
		t.Fatalf("imagined file evidence: %+v", ac)
	}
	var message string
	if err := a.db.QueryRow(ctx, `SELECT message FROM activity WHERE kind='acquisition' AND status='ok' ORDER BY id DESC LIMIT 1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "monitoring restored") || !strings.Contains(message, "search started") {
		t.Fatalf("activity lost verified work: %q", message)
	}
}

func TestManualBulkAcquireRestoresAndSearchesEveryMatchedItem(t *testing.T) {
	a, server, cookie, arr, _ := acquisitionRestoreApp(t)
	person, err := a.saveActress(context.Background(), Actress{TMDbID: "123", Name: "Synthetic cast", Monitored: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"movie", "show"} {
		acquisitionRestoreRelease(t, a, kind, []string{"123"})
	}
	status, raw := watchlistHTTP(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/actresses/%d/acquire", person.ID), `{"scope":"all","search":false}`, server.URL)
	if status != 200 {
		t.Fatalf("%d %s", status, raw)
	}
	assertAcquisitionRestoreRemote(t, arr, "movie")
	assertAcquisitionRestoreRemote(t, arr, "show")
	var response struct {
		Results []struct {
			ReleaseID         int64 `json:"release_id"`
			OK                bool  `json:"ok"`
			AlreadyExists     bool  `json:"already_exists"`
			MonitoredRestored bool  `json:"monitored_restored"`
			SearchStarted     bool  `json:"search_started"`
		} `json:"results"`
		Added   int `json:"added"`
		Skipped int `json:"skipped"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	// Keep the existing count contract: skipped means no new manager record,
	// not no work. Per-item flags distinguish restoration/search from a no-op.
	if len(response.Results) != 2 || response.Added != 0 || response.Skipped != 2 || response.Failed != 0 {
		t.Fatalf("dishonest new-record totals: %s", raw)
	}
	for _, row := range response.Results {
		if !row.OK || !row.AlreadyExists || !row.MonitoredRestored || !row.SearchStarted {
			t.Fatalf("verified bulk work was hidden: %s", raw)
		}
		got, err := a.getRelease(context.Background(), row.ReleaseID)
		if err != nil {
			t.Fatal(err)
		}
		target := "radarr"
		if got.Type == "show" {
			target = "sonarr"
		}
		if got.Monitored || got.Acquisition[target+"_status"] != "present" {
			t.Fatalf("wrong local state: %+v", got)
		}
	}
}

// These fixtures only call synthetic Arr HTTP servers and testApp's explicit
// TEST_DATABASE_URL. Never use a configured integration or a fallback database.
type acquisitionRestoreArr struct {
	mu       sync.Mutex
	items    map[string]map[string]any
	calls    []string
	commands map[string]map[string]any
	fail     string
}

func newAcquisitionRestoreArr() *acquisitionRestoreArr {
	return &acquisitionRestoreArr{
		items: map[string]map[string]any{
			"movie":  {"id": 7, "tmdbId": 42, "title": "Synthetic movie", "titleSlug": "synthetic-movie", "monitored": false, "qualityProfileId": 1, "rootFolderPath": "/media", "path": "/media/movie", "tags": []int{}, "minimumAvailability": "released", "hasFile": false},
			"series": {"id": 8, "tvdbId": 84, "title": "Synthetic show", "titleSlug": "synthetic-show", "monitored": false, "qualityProfileId": 1, "rootFolderPath": "/media", "path": "/media/show", "tags": []int{}, "seasons": []map[string]any{{"seasonNumber": 0, "monitored": false}, {"seasonNumber": 1, "monitored": false}, {"seasonNumber": 2, "monitored": true}}},
		},
		commands: map[string]map[string]any{},
	}
}

func (arr *acquisitionRestoreArr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	arr.mu.Lock()
	defer arr.mu.Unlock()
	arr.calls = append(arr.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			panic(err)
		}
	}
	switch r.URL.Path {
	case "/api/v3/rootfolder":
		write([]map[string]any{{"id": 1, "path": "/media"}})
		return
	case "/api/v3/qualityprofile":
		write([]map[string]any{{"id": 1, "name": "Synthetic"}})
		return
	case "/api/v3/command":
		if r.Method != http.MethodPost {
			break
		}
		if arr.fail == "command" {
			http.Error(w, "private synthetic failure", 503)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad command", 400)
			return
		}
		id := 100 + len(arr.commands)
		command := map[string]any{"id": id, "name": body["name"], "status": "queued", "body": body}
		arr.commands[strconv.Itoa(id)] = command
		write(command)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v3/command/") && r.Method == http.MethodGet {
		if arr.fail == "command-readback" {
			http.Error(w, "private synthetic failure", 503)
			return
		}
		write(arr.commands[strings.TrimPrefix(r.URL.Path, "/api/v3/command/")])
		return
	}
	for resource, item := range arr.items {
		base := "/api/v3/" + resource
		if r.URL.Path == base && r.Method == http.MethodGet {
			write([]any{item})
			return
		}
		if r.URL.Path != fmt.Sprintf("%s/%v", base, item["id"]) {
			continue
		}
		switch r.Method {
		case http.MethodGet:
			write(item)
			return
		case http.MethodPut:
			if arr.fail == "restore" {
				http.Error(w, "private synthetic failure", 503)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad restore", 400)
				return
			}
			if arr.fail != "restore-readback" {
				arr.items[resource] = body
			}
			write(body)
			return
		}
	}
	http.Error(w, "unexpected synthetic route", 404)
}

func acquisitionRestoreApp(t *testing.T) (*App, *httptest.Server, *http.Cookie, *acquisitionRestoreArr, Settings) {
	t.Helper()
	a := testApp(t)
	arr := newAcquisitionRestoreArr()
	remote := httptest.NewServer(arr)
	t.Cleanup(remote.Close)
	s := defaultSettings()
	s.Radarr = ArrSettings{URL: remote.URL, APIKey: "synthetic-arr-key", QualityProfileID: 1, RootFolder: "/media", Enabled: false, Search: false}
	s.Sonarr = s.Radarr
	if err := a.saveSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("b", 64)
	if _, err := a.db.Exec(context.Background(), `INSERT INTO sessions(token_hash,expires_at) VALUES($1,now()+interval '1 hour')`, tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	t.Cleanup(server.Close)
	return a, server, &http.Cookie{Name: "mrw_session", Value: token}, arr, s
}

func acquisitionRestoreRelease(t *testing.T, a *App, kind string, cast []string) Release {
	t.Helper()
	r := Release{Title: "Synthetic " + kind, Type: kind, TMDbID: "42", Monitored: false}
	if kind == "show" {
		r.TVDbID = "84"
	}
	saved, err := a.saveRelease(context.Background(), r, cast)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(context.Background(), `UPDATE releases SET monitored=false,monitoring_override=false WHERE id=$1`, saved.ID); err != nil {
		t.Fatal(err)
	}
	return saved
}

func assertAcquisitionRestoreRemote(t *testing.T, arr *acquisitionRestoreArr, kind string) {
	t.Helper()
	arr.mu.Lock()
	defer arr.mu.Unlock()
	resource := "movie"
	if kind == "show" {
		resource = "series"
	}
	item := arr.items[resource]
	if item["monitored"] != true {
		t.Fatalf("Arr monitoring was not restored: %+v; calls=%v", item, arr.calls)
	}
	if kind == "show" {
		raw, _ := json.Marshal(item["seasons"])
		var seasons []struct {
			Monitored bool `json:"monitored"`
		}
		if err := json.Unmarshal(raw, &seasons); err != nil {
			t.Fatal(err)
		}
		for _, season := range seasons {
			if !season.Monitored {
				t.Fatalf("season monitoring not restored: %s", raw)
			}
		}
	}
	want := "MoviesSearch"
	if kind == "show" {
		want = "SeriesSearch"
	}
	found := 0
	for _, command := range arr.commands {
		if command["name"] == want {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("wanted exactly one verified %s command, got %d: %v", want, found, arr.calls)
	}
}

func TestManualAcquireRestoresMonitoringAndSearchesWithAutomationOff(t *testing.T) {
	for _, kind := range []string{"movie", "show"} {
		t.Run(kind, func(t *testing.T) {
			a, server, cookie, arr, _ := acquisitionRestoreApp(t)
			release := acquisitionRestoreRelease(t, a, kind, nil)
			target := "radarr"
			if kind == "show" {
				target = "sonarr"
			}
			status, raw := watchlistHTTP(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/releases/%d/acquire", release.ID), fmt.Sprintf(`{"target":%q,"search":false}`, target), server.URL)
			if status != 200 {
				t.Fatalf("manual acquisition: %d %s", status, raw)
			}
			assertAcquisitionRestoreRemote(t, arr, kind)
			var response struct {
				OK     bool `json:"ok"`
				Result struct {
					AlreadyExists     bool `json:"already_exists"`
					MonitoredRestored bool `json:"monitored_restored"`
					SearchStarted     bool `json:"search_started"`
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if !response.OK || !response.Result.AlreadyExists || !response.Result.MonitoredRestored || !response.Result.SearchStarted {
				t.Fatalf("missing verified outcome flags: %s", raw)
			}
			got, err := a.getRelease(context.Background(), release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Monitored {
				t.Fatal("manual Arr restoration changed the independent local monitoring override")
			}
			var override *bool
			if err := a.db.QueryRow(context.Background(), `SELECT monitoring_override FROM releases WHERE id=$1`, release.ID).Scan(&override); err != nil {
				t.Fatal(err)
			}
			if override == nil || *override {
				t.Fatal("manual Arr restoration cleared or changed the explicit local monitoring opt-out")
			}
		})
	}
}
