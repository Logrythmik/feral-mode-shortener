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
	port               string
	databaseURL        string
	adminAPIKey        string
	fallbackBase       string // no trailing slash
	publicBase         string // origin encoded into QR codes, no trailing slash
	googleClientID     string
	googleClientSecret string
	allowedEmails      map[string]bool // lowercase; Google accounts allowed into the admin
}

func loadConfig() (config, error) {
	cfg := config{
		port:               os.Getenv("PORT"),
		databaseURL:        os.Getenv("DATABASE_URL"),
		adminAPIKey:        os.Getenv("ADMIN_API_KEY"),
		fallbackBase:       strings.TrimSuffix(os.Getenv("FALLBACK_BASE_URL"), "/"),
		publicBase:         strings.TrimSuffix(os.Getenv("PUBLIC_BASE_URL"), "/"),
		googleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		googleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		allowedEmails:      map[string]bool{},
	}
	for _, e := range strings.Split(os.Getenv("ALLOWED_GOOGLE_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			cfg.allowedEmails[e] = true
		}
	}
	if cfg.port == "" {
		cfg.port = "8080"
	}
	if cfg.fallbackBase == "" {
		cfg.fallbackBase = "https://getferalmode.com"
	}
	if cfg.publicBase == "" {
		cfg.publicBase = "https://feralmo.de"
	}
	if cfg.googleClientID != "" && len(cfg.allowedEmails) == 0 {
		return cfg, errors.New("ALLOWED_GOOGLE_EMAILS must be set when Google OAuth is configured")
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
