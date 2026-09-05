package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	_ "embed"
)

//go:embed admin.html
var adminHTML []byte

type server struct {
	cfg    config
	store  Store
	logger *slog.Logger
}

func newServer(cfg config, store Store, logger *slog.Logger) *server {
	return &server{cfg: cfg, store: store, logger: logger}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Both spellings: Google's frontend swallows /healthz on *.run.app URLs
	// (it never reaches the container), so /health is the reliable one.
	mux.HandleFunc("GET /health", s.handleHealthz)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(adminHTML)
	})

	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("GET /auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)

	mux.Handle("GET /api/links", s.requireAdmin(s.handleListLinks))
	mux.Handle("POST /api/links", s.requireAdmin(s.handleCreateLink))
	mux.Handle("PATCH /api/links/{code}", s.requireAdmin(s.handleUpdateLink))
	mux.Handle("DELETE /api/links/{code}", s.requireAdmin(s.handleDeleteLink))
	mux.Handle("GET /api/links/{code}/stats", s.requireAdmin(s.handleLinkStats))
	mux.Handle("GET /api/links/{code}/qr.png", s.requireAdmin(s.handleLinkQR))
	mux.Handle("GET /api/misses", s.requireAdmin(s.handleMisses))

	// Everything else is a candidate short code (or an unknown path that
	// falls through to the fallback site).
	mux.HandleFunc("/", s.handleRedirect)

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// requireAdmin accepts either `Authorization: Bearer <ADMIN_API_KEY>`
// (constant-time compare) or a signed Google-session cookie.
func (s *server) requireAdmin(next http.HandlerFunc) http.Handler {
	want := sha256.Sum256([]byte(s.cfg.adminAPIKey))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(want[:], got[:]) == 1 {
				next(w, r)
				return
			}
		} else if s.sessionEmail(r) != "" {
			next(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "sign in or provide a valid API key")
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	w.Write([]byte("ok"))
}

func clientIP(r *http.Request) string {
	// Cloud Run terminates TLS and sets X-Forwarded-For: <client>, <proxies...>
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// recordClick writes analytics off the request path so redirects never wait
// on the insert.
func (s *server) recordClick(r *http.Request, code string, linkID *int) {
	click := Click{
		Code:      code,
		LinkID:    linkID,
		Referrer:  r.Referer(),
		UserAgent: r.UserAgent(),
		IP:        clientIP(r),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.RecordClick(ctx, click); err != nil {
			s.logger.Error("record click", "code", code, "err", err)
		}
	}()
}

func (s *server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.Trim(r.URL.Path, "/")
	if path == "" {
		http.Redirect(w, r, s.cfg.fallbackBase, http.StatusFound)
		return
	}

	code := normalizeCode(path)
	if codePattern.MatchString(code) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		link, err := s.store.GetLinkByCode(ctx, code)
		if err == nil {
			s.recordClick(r, code, &link.ID)
			http.Redirect(w, r, buildRedirectURL(link.TargetURL, r.URL.Query()), http.StatusFound)
			return
		}
		if !errors.Is(err, errNotFound) {
			s.logger.Error("lookup", "code", code, "err", err)
			// Fall through to the fallback site rather than erroring: a
			// broken DB shouldn't strand visitors on an error page.
		}
	}

	// Unknown or malformed code: log the miss and forward to the main site.
	if codePattern.MatchString(code) {
		s.recordClick(r, code, nil)
	}
	http.Redirect(w, r, fallbackURL(s.cfg.fallbackBase, path, r.URL.Query()), http.StatusFound)
}

type linkBody struct {
	Code        *string `json:"code"`
	TargetURL   *string `json:"targetUrl"`
	Description *string `json:"description"`
}

func (s *server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.ListLinks(r.Context())
	if err != nil {
		s.logger.Error("list links", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, links)
}

func (s *server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.TargetURL == nil {
		writeError(w, http.StatusBadRequest, "targetUrl is required")
		return
	}
	target, err := validateTargetURL(*body.TargetURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	code := ""
	if body.Code != nil && strings.TrimSpace(*body.Code) != "" {
		code = normalizeCode(*body.Code)
		if err := validateCode(code); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		code = generateCode()
	}

	link, err := s.store.CreateLink(r.Context(), code, target, body.Description)
	if errors.Is(err, errConflict) {
		writeError(w, http.StatusConflict, "code already exists")
		return
	}
	if err != nil {
		s.logger.Error("create link", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	var body linkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	var target *string
	if body.TargetURL != nil {
		t, err := validateTargetURL(*body.TargetURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		target = &t
	}
	link, err := s.store.UpdateLink(r.Context(), normalizeCode(r.PathValue("code")), target, body.Description)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	if err != nil {
		s.logger.Error("update link", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, link)
}

func (s *server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteLink(r.Context(), normalizeCode(r.PathValue("code")))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	if err != nil {
		s.logger.Error("delete link", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleLinkStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context(), normalizeCode(r.PathValue("code")))
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	if err != nil {
		s.logger.Error("stats", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *server) handleMisses(w http.ResponseWriter, r *http.Request) {
	misses, err := s.store.TopMisses(r.Context(), 25)
	if err != nil {
		s.logger.Error("misses", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, misses)
}
