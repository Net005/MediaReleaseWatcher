package app

import (
	"context"
	"crypto/cipher"
	"embed"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"media-release-watcher/internal/integrations"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Config struct{ DatabaseURL, Secret, DataDir, MDBListAPIKey, MDBListBaseURL, TMDBAPIKey, TMDBBaseURL, PublicURL string }
type App struct {
	db             *pgxpool.Pool
	cipher         cipher.AEAD
	cfg            Config
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	mu             sync.Mutex
	syncState      SyncState
	settingsMu     sync.Mutex
	rateMu         sync.Mutex
	loginAttempts  map[string][]time.Time
	providerMu     sync.Mutex
	nextProvider   time.Time
	clientMu       sync.Mutex
	clientConfig   string
	providerClient *integrations.Client

	manualSyncRunning bool // guarded by mu; held through syncOnce's deferred cleanup
	started           bool
	creditCatchupWake chan struct{}
}
type SyncState struct {
	Running   bool       `json:"running"`
	LastRun   *time.Time `json:"last_run"`
	LastError string     `json:"last_error"`
	Stage     string     `json:"stage"`
}

func New(ctx context.Context, cfg Config) (*App, error) {
	if e := validPublicURL(cfg.PublicURL); e != nil {
		return nil, e
	}
	c, e := newCipher(cfg.Secret)
	if e != nil {
		return nil, e
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	pc, e := pgxpool.ParseConfig(cfg.DatabaseURL)
	if e != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	pc.MaxConns = 12
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pc.ConnConfig.ConnectTimeout = 10 * time.Second
	db, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return nil, errors.New("cannot initialize database")
	}
	if e = db.Ping(ctx); e != nil {
		db.Close()
		return nil, errors.New("database unavailable")
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if e = os.MkdirAll(cfg.DataDir+"/artwork", 0700); e != nil {
		db.Close()
		return nil, e
	}
	// Validate existing encrypted settings before any destructive upgrade. A bad
	// APP_SECRET must fail without clearing the release catalogue or reset marker.
	var hasSettings bool
	if e = db.QueryRow(ctx, `SELECT to_regclass('public.settings') IS NOT NULL`).Scan(&hasSettings); e != nil {
		db.Close()
		return nil, e
	}
	if hasSettings {
		preflight := &App{db: db, cipher: c, cfg: cfg}
		if _, e = preflight.settings(ctx); e != nil {
			db.Close()
			return nil, e
		}
	}
	migrationFiles, e := migrations.ReadDir("migrations")
	if e == nil {
		tx, x := db.Begin(ctx)
		if x != nil {
			e = x
		} else {
			defer tx.Rollback(ctx)
			_, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(486722119)`)
			if e == nil {
				for _, file := range migrationFiles {
					if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") {
						continue
					}
					var migration []byte
					migration, e = migrations.ReadFile("migrations/" + file.Name())
					if e == nil {
						_, e = tx.Exec(ctx, string(migration))
					}
					if e != nil {
						break
					}
				}
			}
			if e == nil {
				e = tx.Commit(ctx)
			}
		}
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	actx, cancel := context.WithCancel(ctx)
	a := &App{db: db, cipher: c, cfg: cfg, ctx: actx, cancel: cancel, loginAttempts: map[string][]time.Time{}, syncState: SyncState{Stage: "idle"}}
	// Decrypt existing settings at startup so a changed encryption key fails safely.
	if _, e = a.settings(ctx); e != nil {
		a.Close()
		return nil, e
	}
	var state []byte
	if e = db.QueryRow(ctx, `SELECT value FROM app_state WHERE key='sync'`).Scan(&state); e == nil {
		if e = json.Unmarshal(state, &a.syncState); e != nil {
			a.Close()
			return nil, errors.New("invalid persisted sync state")
		}
		a.syncState.Running = false
		if a.syncState.Stage != "failed" {
			a.syncState.Stage = "idle"
		}
	} else if e != pgx.ErrNoRows {
		a.Close()
		return nil, e
	}
	if e = a.ensureSchedules(ctx); e != nil {
		a.Close()
		return nil, e
	}
	return a, nil
}
func (a *App) Close() { a.mu.Lock(); a.cancel(); a.mu.Unlock(); a.wg.Wait(); a.db.Close() }
func (a *App) Start() {
	a.mu.Lock()
	if a.started || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	a.started = true
	a.creditCatchupWake = make(chan struct{}, 1)
	a.wg.Add(2)
	go func() { defer a.wg.Done(); a.creditCatchupLoop() }()
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		a.runDueSchedules(a.ctx)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				a.runDueSchedules(a.ctx)
			}
		}
	}()
}
func (a *App) activity(ctx context.Context, kind, message, status string) {
	_, _ = a.db.Exec(ctx, `INSERT INTO activity(kind,message,status) VALUES($1,$2,$3)`, kind, message, status)
}
