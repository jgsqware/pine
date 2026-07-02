package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// echoOK is a trivial handler standing in for the real mux.
var echoOK = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestSecureTokenAuth(t *testing.T) {
	h := secure(Config{Token: "s3cret"}, echoOK)

	cases := []struct {
		name string
		set  func(*http.Request)
		want int
	}{
		{"no token", func(*http.Request) {}, http.StatusUnauthorized},
		{"bearer ok", func(r *http.Request) { r.Header.Set("Authorization", "Bearer s3cret") }, http.StatusOK},
		{"bearer wrong", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, http.StatusUnauthorized},
		{"x-pine-token ok", func(r *http.Request) { r.Header.Set("X-Pine-Token", "s3cret") }, http.StatusOK},
		{"query ok (SSE)", func(r *http.Request) { r.URL.RawQuery = "token=s3cret" }, http.StatusOK},
		{"cookie ok", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "pine_token", Value: "s3cret"}) }, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
			c.set(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d", w.Code, c.want)
			}
		})
	}
}

func TestSecureNoTokenAllowsAll(t *testing.T) {
	h := secure(Config{}, echoOK)
	r := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when auth disabled", w.Code)
	}
}

func TestSecureCSRF(t *testing.T) {
	h := secure(Config{}, echoOK)

	// Cross-origin state-changing request is blocked.
	r := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	r.Host = "pine.local:8743"
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST status = %d, want 403", w.Code)
	}

	// Same-origin POST passes.
	r = httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	r.Host = "pine.local:8743"
	r.Header.Set("Origin", "http://pine.local:8743")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("same-origin POST status = %d, want 200", w.Code)
	}

	// GET is never CSRF-checked (safe method).
	r = httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("cross-origin GET status = %d, want 200", w.Code)
	}

	// Non-/api/ paths are untouched (SPA shell must load).
	r = httptest.NewRequest(http.MethodPost, "/whatever", nil)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("non-api path status = %d, want 200", w.Code)
	}
}

func TestStorableVars(t *testing.T) {
	in := map[string]any{
		"app_version":   "2.4.1",
		"db_password":   "hunter2", // secret key → dropped
		"vault_token":   "abc",     // secret key → dropped
		"replica_count": 3,
	}
	out := storableVars(in)
	if _, ok := out["db_password"]; ok {
		t.Error("db_password must not be persisted")
	}
	if _, ok := out["vault_token"]; ok {
		t.Error("vault_token must not be persisted")
	}
	if out["app_version"] != "2.4.1" {
		t.Errorf("app_version should survive, got %v", out["app_version"])
	}
	if out["replica_count"] != 3 {
		t.Errorf("replica_count should survive, got %v", out["replica_count"])
	}
	// all-secret input collapses to nil (nothing to store)
	if storableVars(map[string]any{"password": "x"}) != nil {
		t.Error("all-secret vars should yield nil")
	}
	if storableVars(nil) != nil {
		t.Error("nil in → nil out")
	}
}
