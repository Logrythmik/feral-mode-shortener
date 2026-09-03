package main

import (
	"net/url"
	"testing"
)

func TestValidateCode(t *testing.T) {
	valid := []string{"buy", "b", "spring-sale", "drop_2", "abc123", "2025"}
	for _, c := range valid {
		if err := validateCode(c); err != nil {
			t.Errorf("validateCode(%q) = %v, want nil", c, err)
		}
	}
	invalid := []string{"", "Buy", "-lead", "has space", "a/b", "admin", "api", "healthz", "robots.txt", "way.too.dotted", string(make([]byte, 70))}
	for _, c := range invalid {
		if err := validateCode(c); err == nil {
			t.Errorf("validateCode(%q) = nil, want error", c)
		}
	}
}

func TestNormalizeCode(t *testing.T) {
	if got := normalizeCode("  BuY "); got != "buy" {
		t.Errorf("normalizeCode = %q, want %q", got, "buy")
	}
}

func TestGenerateCode(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		c := generateCode()
		if err := validateCode(c); err != nil {
			t.Fatalf("generated invalid code %q: %v", c, err)
		}
		seen[c] = true
	}
	if len(seen) < 99 {
		t.Errorf("generated codes are not diverse enough: %d unique of 100", len(seen))
	}
}

func TestValidateTargetURL(t *testing.T) {
	if _, err := validateTargetURL("https://getferalmode.com/shop"); err != nil {
		t.Errorf("valid URL rejected: %v", err)
	}
	for _, bad := range []string{"", "ftp://x.com", "javascript:alert(1)", "/relative", "getferalmode.com"} {
		if _, err := validateTargetURL(bad); err == nil {
			t.Errorf("validateTargetURL(%q) = nil, want error", bad)
		}
	}
}

func TestBuildRedirectURL(t *testing.T) {
	q := url.Values{"utm_source": {"ig"}}
	got := buildRedirectURL("https://getferalmode.com/shop?ref=short", q)
	want := "https://getferalmode.com/shop?ref=short&utm_source=ig"
	if got != want {
		t.Errorf("buildRedirectURL = %q, want %q", got, want)
	}
	if got := buildRedirectURL("https://x.com/a", nil); got != "https://x.com/a" {
		t.Errorf("no-query case mangled: %q", got)
	}
}

func TestFallbackURL(t *testing.T) {
	got := fallbackURL("https://getferalmode.com", "whoops/deep", url.Values{"a": {"1"}})
	want := "https://getferalmode.com/whoops/deep?a=1"
	if got != want {
		t.Errorf("fallbackURL = %q, want %q", got, want)
	}
}
