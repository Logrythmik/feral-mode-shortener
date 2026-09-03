package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store for handler tests.
type memStore struct {
	mu     sync.Mutex
	links  map[string]*Link
	clicks []Click
	nextID int
}

func newMemStore() *memStore { return &memStore{links: map[string]*Link{}, nextID: 1} }

func (m *memStore) GetLinkByCode(_ context.Context, code string) (*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.links[code]; ok {
		cp := *l
		return &cp, nil
	}
	return nil, errNotFound
}

func (m *memStore) ListLinks(context.Context) ([]Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Link{}
	for _, l := range m.links {
		out = append(out, *l)
	}
	return out, nil
}

func (m *memStore) CreateLink(_ context.Context, code, targetURL string, description *string) (*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[code]; ok {
		return nil, errConflict
	}
	l := &Link{ID: m.nextID, Code: code, TargetURL: targetURL, Description: description, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	m.nextID++
	m.links[code] = l
	cp := *l
	return &cp, nil
}

func (m *memStore) UpdateLink(_ context.Context, code string, targetURL, description *string) (*Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[code]
	if !ok {
		return nil, errNotFound
	}
	if targetURL != nil {
		l.TargetURL = *targetURL
	}
	if description != nil {
		l.Description = description
	}
	cp := *l
	return &cp, nil
}

func (m *memStore) DeleteLink(_ context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[code]; !ok {
		return errNotFound
	}
	delete(m.links, code)
	return nil
}

func (m *memStore) RecordClick(_ context.Context, c Click) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clicks = append(m.clicks, c)
	return nil
}

func (m *memStore) Stats(ctx context.Context, code string) (*LinkStats, error) {
	l, err := m.GetLinkByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	return &LinkStats{Link: *l, ClicksByDay: []DayCount{}, TopReferrers: []LabelCount{}}, nil
}

func (m *memStore) TopMisses(context.Context, int) ([]LabelCount, error) { return []LabelCount{}, nil }
func (m *memStore) Ping(context.Context) error                          { return nil }

// waitClicks waits for async click recording to land.
func (m *memStore) waitClicks(t *testing.T, n int) []Click {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		if len(m.clicks) >= n {
			out := append([]Click{}, m.clicks...)
			m.mu.Unlock()
			return out
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d clicks", n)
	return nil
}

const testKey = "test-key"

func testServer(store Store) http.Handler {
	cfg := config{adminAPIKey: testKey, fallbackBase: "https://getferalmode.com"}
	return newServer(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil))).routes()
}

func doReq(h http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRedirectKnownCode(t *testing.T) {
	store := newMemStore()
	store.CreateLink(t.Context(), "buy", "https://getferalmode.com/shop", nil)
	h := testServer(store)

	w := doReq(h, "GET", "/BUY?utm_source=ig", "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://getferalmode.com/shop?utm_source=ig" {
		t.Errorf("Location = %q", loc)
	}
	clicks := store.waitClicks(t, 1)
	if clicks[0].Code != "buy" || clicks[0].LinkID == nil {
		t.Errorf("click not recorded against link: %+v", clicks[0])
	}
}

func TestRedirectUnknownCodeFallsThrough(t *testing.T) {
	store := newMemStore()
	h := testServer(store)

	w := doReq(h, "GET", "/nope", "", "")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://getferalmode.com/nope" {
		t.Errorf("Location = %q", loc)
	}
	clicks := store.waitClicks(t, 1)
	if clicks[0].LinkID != nil {
		t.Errorf("miss click should have nil LinkID: %+v", clicks[0])
	}
}

func TestRedirectRootAndDeepPath(t *testing.T) {
	h := testServer(newMemStore())
	if loc := doReq(h, "GET", "/", "", "").Header().Get("Location"); loc != "https://getferalmode.com" {
		t.Errorf("root Location = %q", loc)
	}
	if loc := doReq(h, "GET", "/some/deep/path", "", "").Header().Get("Location"); loc != "https://getferalmode.com/some/deep/path" {
		t.Errorf("deep-path Location = %q", loc)
	}
}

func TestAdminAuth(t *testing.T) {
	h := testServer(newMemStore())
	if w := doReq(h, "GET", "/api/links", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no key: status = %d, want 401", w.Code)
	}
	if w := doReq(h, "GET", "/api/links", "", "wrong"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", w.Code)
	}
	if w := doReq(h, "GET", "/api/links", "", testKey); w.Code != http.StatusOK {
		t.Errorf("right key: status = %d, want 200", w.Code)
	}
}

func TestCreateLinkValidation(t *testing.T) {
	h := testServer(newMemStore())

	w := doReq(h, "POST", "/api/links", `{"code":"buy","targetUrl":"https://getferalmode.com/shop"}`, testKey)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body %s", w.Code, w.Body)
	}
	var link Link
	json.Unmarshal(w.Body.Bytes(), &link)
	if link.Code != "buy" {
		t.Errorf("code = %q", link.Code)
	}

	// Duplicate
	if w := doReq(h, "POST", "/api/links", `{"code":"BUY","targetUrl":"https://x.com"}`, testKey); w.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want 409", w.Code)
	}
	// Reserved code
	if w := doReq(h, "POST", "/api/links", `{"code":"admin","targetUrl":"https://x.com"}`, testKey); w.Code != http.StatusBadRequest {
		t.Errorf("reserved: status = %d, want 400", w.Code)
	}
	// Bad URL
	if w := doReq(h, "POST", "/api/links", `{"code":"x","targetUrl":"javascript:alert(1)"}`, testKey); w.Code != http.StatusBadRequest {
		t.Errorf("bad url: status = %d, want 400", w.Code)
	}
	// Generated code
	w = doReq(h, "POST", "/api/links", `{"targetUrl":"https://getferalmode.com/a"}`, testKey)
	if w.Code != http.StatusCreated {
		t.Fatalf("generated: status = %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &link)
	if err := validateCode(link.Code); err != nil {
		t.Errorf("generated code %q invalid: %v", link.Code, err)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	store := newMemStore()
	store.CreateLink(t.Context(), "buy", "https://old.example.com", nil)
	h := testServer(store)

	w := doReq(h, "PATCH", "/api/links/buy", `{"targetUrl":"https://getferalmode.com/new"}`, testKey)
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d", w.Code)
	}
	l, _ := store.GetLinkByCode(t.Context(), "buy")
	if l.TargetURL != "https://getferalmode.com/new" {
		t.Errorf("target not updated: %q", l.TargetURL)
	}

	if w := doReq(h, "DELETE", "/api/links/buy", "", testKey); w.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d", w.Code)
	}
	if w := doReq(h, "DELETE", "/api/links/buy", "", testKey); w.Code != http.StatusNotFound {
		t.Errorf("delete missing: status = %d", w.Code)
	}
}

func TestAdminPageAndHealthz(t *testing.T) {
	h := testServer(newMemStore())
	if w := doReq(h, "GET", "/admin", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "feralmo") {
		t.Errorf("admin page: status = %d", w.Code)
	}
	for _, p := range []string{"/health", "/healthz"} {
		if w := doReq(h, "GET", p, "", ""); w.Code != http.StatusOK {
			t.Errorf("%s: status = %d", p, w.Code)
		}
	}
}
