package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/store"
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

func TestGzipMiddleware(t *testing.T) {
	// a JSON handler wrapped by the gzip middleware
	h := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hello":"world","padding":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	}))

	// with Accept-Encoding: gzip → compressed
	r := httptest.NewRequest(http.MethodGet, "/api/scan", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	gz, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("body is not valid gzip: %v", err)
	}
	body, _ := io.ReadAll(gz)
	if !strings.Contains(string(body), `"hello":"world"`) {
		t.Errorf("decompressed body wrong: %s", body)
	}

	// without Accept-Encoding → plain
	r = httptest.NewRequest(http.MethodGet, "/api/scan", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("must not gzip when client did not ask for it")
	}

	// SSE stream is never gzipped
	sse := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "should-be-untouched")
	}))
	r = httptest.NewRequest(http.MethodGet, "/api/jobs/j1/events", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	sse.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("SSE /events must not be gzipped")
	}
}

// jobEventsFixture registers a terminal job whose log file holds body, then
// returns the SSE replay produced by the /events handler. A terminal job is
// not "live", so Subscribe returns ok=false and jobEvents replays the log and
// returns without blocking on a channel.
func jobEventsFixture(t *testing.T, body []byte) string {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := &Server{Mgr: runner.New(st)}
	job := model.Job{ID: store.NewID("j"), Status: model.JobSuccess}
	if err := st.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}
	if err := os.WriteFile(st.JobLogPath(job.ID), body, 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/events", nil)
	req.SetPathValue("id", job.ID)
	rec := httptest.NewRecorder()
	srv.jobEvents(rec, req)
	return rec.Body.String()
}

// TestJobEventsBoundedReplay proves the SSE replay never loads a whole verbose
// log into memory: a 4 MiB log is replayed down to its tail (<= the cap), the
// earliest content is dropped with a visible truncation notice, and the most
// recent content survives.
func TestJobEventsBoundedReplay(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("EARLY_MARKER this first line must be truncated away\n")
	pad := strings.Repeat("x", 100)
	for buf.Len() < maxLogReplay*16 { // ~4 MiB, 16x the replay cap
		fmt.Fprintf(&buf, "filler %s\n", pad)
	}
	buf.WriteString("RECENT_MARKER this last line must survive the tail\n")
	logSize := buf.Len()

	body := jobEventsFixture(t, buf.Bytes())

	if strings.Contains(body, "EARLY_MARKER") {
		t.Error("early log content should have been truncated out of the replay")
	}
	if !strings.Contains(body, "RECENT_MARKER") {
		t.Error("recent log content missing from the replay tail")
	}
	if !strings.Contains(body, "earlier output truncated") {
		t.Error("truncation notice missing from replay")
	}
	if !strings.Contains(body, "event: status") {
		t.Error("status event missing from stream")
	}
	// A 4 MiB log must not produce a multi-MiB replay: the body carries only the
	// bounded tail plus small SSE framing, well under the raw log size and the
	// documented cap (allow generous headroom for per-line "event: line/data:"
	// framing).
	if got := len(body); got >= logSize/2 {
		t.Errorf("replay body %d B not bounded relative to %d B log", got, logSize)
	}
	if got := len(body); got > maxLogReplay*2 {
		t.Errorf("replay body %d B exceeds bound %d B", got, maxLogReplay*2)
	}
}

// TestJobEventsSmallLogFull confirms the common case is unaffected: a log under
// the cap is replayed in full with no truncation notice.
func TestJobEventsSmallLogFull(t *testing.T) {
	body := jobEventsFixture(t, []byte("EARLY_MARKER\nmiddle line\nRECENT_MARKER\n"))
	if !strings.Contains(body, "EARLY_MARKER") || !strings.Contains(body, "RECENT_MARKER") {
		t.Error("a small log should be replayed in full")
	}
	if strings.Contains(body, "earlier output truncated") {
		t.Error("a small log must not be flagged as truncated")
	}
}
