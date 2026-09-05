package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func oauthTestServer(store Store) (*server, http.Handler) {
	cfg := config{
		adminAPIKey: testKey, fallbackBase: "https://getferalmode.com", publicBase: "https://feralmo.de",
		googleClientID: "client-id", googleClientSecret: "client-secret",
		allowedEmails: map[string]bool{"bdstark@gmail.com": true, "jason@getferalmode.com": true},
	}
	srv := newServer(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return srv, srv.routes()
}

func TestSessionSignVerify(t *testing.T) {
	srv, _ := oauthTestServer(newMemStore())

	tok := srv.signSession("bdstark@gmail.com", time.Now().Add(time.Hour))
	if got := srv.verifySession(tok); got != "bdstark@gmail.com" {
		t.Errorf("verifySession = %q, want the signed email", got)
	}
	if got := srv.verifySession(tok + "x"); got != "" {
		t.Errorf("tampered token accepted: %q", got)
	}
	expired := srv.signSession("bdstark@gmail.com", time.Now().Add(-time.Minute))
	if got := srv.verifySession(expired); got != "" {
		t.Errorf("expired token accepted: %q", got)
	}
	// A token signed under a different key must not verify.
	other := &server{cfg: config{adminAPIKey: "other-key"}}
	if got := srv.verifySession(other.signSession("bdstark@gmail.com", time.Now().Add(time.Hour))); got != "" {
		t.Errorf("cross-key token accepted: %q", got)
	}
}

func TestSessionCookieGrantsAdmin(t *testing.T) {
	srv, h := oauthTestServer(newMemStore())

	req := httptest.NewRequest("GET", "/api/links", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.signSession("jason@getferalmode.com", time.Now().Add(time.Hour))})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("session cookie: status = %d, want 200", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/links", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "garbage"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("garbage cookie: status = %d, want 401", w.Code)
	}
}

func TestLoginRedirectsToGoogle(t *testing.T) {
	_, h := oauthTestServer(newMemStore())
	w := doReq(h, "GET", "/auth/login", "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.google.com/o/oauth2/v2/auth?") ||
		!strings.Contains(loc, "client_id=client-id") ||
		!strings.Contains(loc, "redirect_uri=") {
		t.Errorf("Location = %q", loc)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), stateCookieName+"=") {
		t.Error("state cookie not set")
	}
}

func TestLoginDisabledWithoutClient(t *testing.T) {
	h := testServer(newMemStore()) // no google client configured
	if w := doReq(h, "GET", "/auth/login", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	_, h := oauthTestServer(newMemStore())
	req := httptest.NewRequest("GET", "/auth/callback?state=abc&code=x", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "different"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "error=state") {
		t.Errorf("status = %d, Location = %q", w.Code, w.Header().Get("Location"))
	}
}

func TestMeEndpoint(t *testing.T) {
	srv, h := oauthTestServer(newMemStore())

	w := doReq(h, "GET", "/api/me", "", "")
	var me map[string]any
	json.Unmarshal(w.Body.Bytes(), &me)
	if w.Code != http.StatusOK || me["oauth"] != true || me["authenticated"] != false {
		t.Errorf("anonymous /api/me = %d %v", w.Code, me)
	}

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.signSession("bdstark@gmail.com", time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	json.Unmarshal(rec.Body.Bytes(), &me)
	if me["authenticated"] != true || me["email"] != "bdstark@gmail.com" {
		t.Errorf("signed-in /api/me = %v", me)
	}
}

func TestQREndpoint(t *testing.T) {
	store := newMemStore()
	store.CreateLink(t.Context(), "buy", "https://getferalmode.com/shop", nil)
	_, h := oauthTestServer(store)

	w := doReq(h, "GET", "/api/links/buy/qr.png", "", testKey)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("\x89PNG")) {
		t.Error("body is not a PNG")
	}
	if w := doReq(h, "GET", "/api/links/nope/qr.png", "", testKey); w.Code != http.StatusNotFound {
		t.Errorf("unknown code: status = %d, want 404", w.Code)
	}
	if w := doReq(h, "GET", "/api/links/buy/qr.png", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401", w.Code)
	}
}
