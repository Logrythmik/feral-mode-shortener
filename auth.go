package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Google OAuth for the admin UI. The bearer API key stays valid alongside it
// (scripts, curl); when GOOGLE_CLIENT_ID is unset — local dev — OAuth is off
// and the key is the only way in.

const (
	sessionCookieName = "feralmo_session"
	stateCookieName   = "feralmo_oauth_state"
	sessionTTL        = 7 * 24 * time.Hour
)

func (s *server) oauthEnabled() bool {
	return s.cfg.googleClientID != "" && s.cfg.googleClientSecret != ""
}

// sessionKey derives the cookie-signing key from the admin API key so no
// second secret needs provisioning; rotating the key logs everyone out.
func (s *server) sessionKey() []byte {
	sum := sha256.Sum256([]byte("feralmo-session-hmac:" + s.cfg.adminAPIKey))
	return sum[:]
}

func (s *server) signSession(email string, expires time.Time) string {
	payload := email + "|" + strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, s.sessionKey())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySession returns the signed-in email, or "" for a missing/invalid/
// expired token.
func (s *server) verifySession(token string) string {
	encPayload, encSig, ok := strings.Cut(token, ".")
	if !ok {
		return ""
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(encPayload)
	sig, err2 := base64.RawURLEncoding.DecodeString(encSig)
	if err1 != nil || err2 != nil {
		return ""
	}
	mac := hmac.New(sha256.New, s.sessionKey())
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return ""
	}
	email, expStr, ok := strings.Cut(string(payload), "|")
	if !ok {
		return ""
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	return email
}

func (s *server) sessionEmail(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return s.verifySession(c.Value)
}

// requestOrigin reconstructs the external origin. Cloud Run terminates TLS
// and sets X-Forwarded-Proto.
func requestOrigin(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return proto + "://" + r.Host
}

func isHTTPS(r *http.Request) bool {
	return strings.HasPrefix(requestOrigin(r), "https://")
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.oauthEnabled() {
		writeError(w, http.StatusNotFound, "Google sign-in is not configured")
		return
	}
	buf := make([]byte, 16)
	rand.Read(buf)
	state := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName, Value: state, Path: "/auth",
		MaxAge: 600, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	q := url.Values{
		"client_id":     {s.cfg.googleClientID},
		"redirect_uri":  {requestOrigin(r) + "/auth/callback"},
		"response_type": {"code"},
		"scope":         {"openid email"},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusFound)
}

// exchangeCode trades the authorization code for the id_token's email claim.
// The token comes straight from Google's token endpoint over TLS in exchange
// for our client secret, so the JWT payload is trusted without signature
// verification.
func (s *server) exchangeCode(r *http.Request, code string) (email string, verified bool, err error) {
	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {code},
		"client_id":     {s.cfg.googleClientID},
		"client_secret": {s.cfg.googleClientSecret},
		"redirect_uri":  {requestOrigin(r) + "/auth/callback"},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", false, errors.New("token exchange failed: " + string(body))
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", false, err
	}
	parts := strings.Split(tok.IDToken, ".")
	if len(parts) != 3 {
		return "", false, errors.New("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, err
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false, err
	}
	return claims.Email, claims.EmailVerified, nil
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oauthEnabled() {
		writeError(w, http.StatusNotFound, "Google sign-in is not configured")
		return
	}
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value || stateCookie.Value == "" {
		http.Redirect(w, r, "/admin?error=state", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Path: "/auth", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/admin?error=denied", http.StatusFound)
		return
	}
	email, verified, err := s.exchangeCode(r, code)
	if err != nil {
		s.logger.Error("oauth exchange", "err", err)
		http.Redirect(w, r, "/admin?error=exchange", http.StatusFound)
		return
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !verified || !s.cfg.allowedEmails[email] {
		s.logger.Warn("oauth rejected", "email", email)
		http.Redirect(w, r, "/admin?error=unauthorized", http.StatusFound)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: s.signSession(email, time.Now().Add(sessionTTL)),
		Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// handleMe reports auth state to the admin UI; it is the one /api route that
// does not require auth.
func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	email := s.sessionEmail(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"oauth":         s.oauthEnabled(),
		"authenticated": email != "",
		"email":         email,
	})
}
