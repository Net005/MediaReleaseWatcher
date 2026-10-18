package main

import (
	"context"
	"log/slog"
	"media-release-watcher/internal/app"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("application stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, e := app.New(ctx, app.Config{DatabaseURL: os.Getenv("DATABASE_URL"), Secret: os.Getenv("APP_SECRET"), DataDir: os.Getenv("DATA_DIR"), PublicURL: os.Getenv("PUBLIC_URL"), MDBListAPIKey: os.Getenv("MDBLIST_API_KEY"), TMDBAPIKey: os.Getenv("TMDB_API_KEY")})
	if e != nil {
		return e
	}
	defer a.Close()
	a.Start()
	addr := os.Getenv("APP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() { slog.Info("Media Release Watcher listening", "addr", addr); errCh <- srv.ListenAndServe() }()
	select {
	case e := <-errCh:
		if e != http.ErrServerClosed {
			return e
		}
		return nil
	case <-ctx.Done():
		shutdown, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		return srv.Shutdown(shutdown)
	}
}
