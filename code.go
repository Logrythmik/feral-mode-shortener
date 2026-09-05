package main

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Short codes are stored and matched lowercase so printed/spoken links are
// case-insensitive. Slashes are excluded: codes are a single path segment.
var codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Paths that can never be short codes because the app owns them.
var reservedCodes = map[string]bool{
	"admin":       true,
	"api":         true,
	"auth":        true,
	"health":      true,
	"healthz":     true,
	"favicon.ico": true,
	"robots.txt":  true,
}

func normalizeCode(code string) string {
	return strings.ToLower(strings.TrimSpace(code))
}

func validateCode(code string) error {
	if !codePattern.MatchString(code) {
		return fmt.Errorf("code must be 1-64 chars: a-z, 0-9, '-' or '_', starting with a letter or digit")
	}
	if reservedCodes[code] {
		return fmt.Errorf("code %q is reserved", code)
	}
	return nil
}

// Ambiguity-free alphabet (no 0/o, 1/l/i) for generated codes.
const genAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

func generateCode() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails per crypto/rand docs
	for i := range b {
		b[i] = genAlphabet[int(b[i])%len(genAlphabet)]
	}
	return string(b)
}

func validateTargetURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("targetUrl must be an absolute http(s) URL")
	}
	return u.String(), nil
}

// buildRedirectURL appends the incoming query string to the target so UTM
// params on the short link survive the redirect. Target's own params come
// first; incoming ones are appended.
func buildRedirectURL(target string, incomingQuery url.Values) string {
	if len(incomingQuery) == 0 {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	q := u.Query()
	for k, vs := range incomingQuery {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// fallbackURL maps an unknown path onto the fallback site, preserving the
// path and query (feralmo.de/whoops -> getferalmode.com/whoops).
func fallbackURL(base, path string, incomingQuery url.Values) string {
	target := base + "/" + strings.TrimPrefix(path, "/")
	return buildRedirectURL(target, incomingQuery)
}
