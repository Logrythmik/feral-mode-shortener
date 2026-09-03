// feral-mode-shortener — link shortener for feralmo.de.
//
// Short codes redirect to configured URLs; unknown paths fall through to
// FALLBACK_BASE_URL (getferalmode.com) with the path preserved. Every hit is
// recorded in Postgres for later analysis. Admin API + UI live under the
// reserved /admin and /api paths, guarded by ADMIN_API_KEY.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	port         string
	databaseURL  string
	adminAPIKey  string
	fallbackBase string // no trailing slash
}

func loadConfig() (config, error) {
	cfg := config{
		port:         os.Getenv("PORT"),
		databaseURL:  os.Getenv("DATABASE_URL"),
		adminAPIKey:  os.Getenv("ADMIN_API_KEY"),
		fallbackBase: strings.TrimSuffix(os.Getenv("FALLBACK_BASE_URL"), "/"),
	}
	if cfg.port == "" {
		cfg.port = "8080"
	}
	if cfg.fallbackBase == "" {
		cfg.fallbackBase = "https://getferalmode.com"
	}
	if cfg.databaseURL == "" {
		return cfg, errors.New("DATABASE_URL must be set")
	}
	if cfg.adminAPIKey == "" {
		return cfg, errors.New("ADMIN_API_KEY must be set")
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := newPGStore(ctx, cfg.databaseURL)
	if err != nil {
		logger.Error("database connect", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newServer(cfg, store, logger).routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("listening", "port", cfg.port, "fallback", cfg.fallbackBase)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server", "err", err)
		os.Exit(1)
	}
}
