package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClient drives the server over an in-process pipe pair, exactly like a
// real editor would over stdio. A background pump continuously reads from the
// server so its request responses and server-initiated notifications
// (publishDiagnostics) are demultiplexed without blocking the synchronous
// io.Pipe — mirroring how a real LSP client is always listening.
type testClient struct {
	t        *testing.T
	toServer *conn // client writes requests here
	nextID   int

	mu      sync.Mutex
	pending map[string]chan *message // id -> response channel
	diags   map[string][]diagnostic  // uri -> latest diagnostics
	diagCh  chan string              // uri notifications, for waiters
}

// startServer wires client <-> server through two io.Pipes, runs the server
// loop in a goroutine and starts the client read pump.
func startServer(t *testing.T) *testClient {
	t.Helper()
	cliToSrvR, cliToSrvW := io.Pipe()
	srvToCliR, srvToCliW := io.Pipe()

	srv := newServer(newConn(cliToSrvR, srvToCliW))
	go func() {
		_ = srv.serve()
		_ = srvToCliW.Close()
	}()

	c := &testClient{
		t:        t,
		toServer: newConn(nil, cliToSrvW),
		pending:  map[string]chan *message{},
		diags:    map[string][]diagnostic{},
		diagCh:   make(chan string, 64),
	}
	go c.pump(newConn(srvToCliR, nil))
	return c
}

// pump reads every server message and dispatches it: responses wake the
// waiting request; publishDiagnostics update the diags map.
func (c *testClient) pump(from *conn) {
	for {
		m, err := from.read()
		if err != nil {
			return // server closed
		}
		if len(m.ID) > 0 && m.Method == "" {
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method == "textDocument/publishDiagnostics" {
			var p publishDiagnosticsParams
			_ = json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			c.diags[p.URI] = p.Diagnostics
			c.mu.Unlock()
			select {
			case c.diagCh <- p.URI:
			default:
			}
		}
	}
}

// request sends a request and blocks until the matching response arrives.
func (c *testClient) request(method string, params any) *message {
	c.t.Helper()
	c.nextID++
	id, _ := json.Marshal(c.nextID)
	ch := make(chan *message, 1)
	c.mu.Lock()
	c.pending[string(id)] = ch
	c.mu.Unlock()

	raw, _ := json.Marshal(params)
	if err := c.toServer.write(&message{ID: id, Method: method, Params: raw}); err != nil {
		c.t.Fatalf("write %s: %v", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			c.t.Fatalf("%s error: %s", method, m.Error.Message)
		}
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatalf("timed out waiting for %s response", method)
		return nil
	}
}

// notify sends a notification (no response expected).
func (c *testClient) notify(method string, params any) {
	c.t.Helper()
	raw, _ := json.Marshal(params)
	if err := c.toServer.write(&message{Method: method, Params: raw}); err != nil {
		c.t.Fatalf("notify %s: %v", method, err)
	}
}

// waitDiagnostics blocks until a publishDiagnostics for uri has been received.
func (c *testClient) waitDiagnostics(uri string) []diagnostic {
	c.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		d, ok := c.diags[uri]
		c.mu.Unlock()
		if ok {
			return d
		}
		select {
		case <-c.diagCh:
		case <-deadline:
			c.t.Fatalf("no diagnostics for %s", uri)
		}
	}
}

func fileURI(path string) string { return "file://" + path }

// framingCases exercises the Content-Length framing round-trip in isolation
// (table-driven), independent of the LSP semantics.
func TestJSONRPCFraming(t *testing.T) {
	cases := []struct {
		name   string
		method string
		params any
	}{
		{"empty-params", "initialized", map[string]any{}},
		{"nested", "textDocument/hover", hoverParams{
			TextDocument: textDocumentIdentifier{URI: "file:///a.yml"},
			Position:     position{Line: 3, Character: 7},
		}},
		{"unicode", "custom", map[string]any{"s": "café — pine 🌲"}},
		{"big", "custom", map[string]any{"s": strings.Repeat("x", 5000)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			w := newConn(nil, pw)
			r := newConn(pr, nil)
			go func() {
				raw, _ := json.Marshal(tc.params)
				_ = w.write(&message{Method: tc.method, Params: raw})
			}()
			got, err := r.read()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got.Method != tc.method {
				t.Fatalf("method = %q, want %q", got.Method, tc.method)
			}
			want, _ := json.Marshal(tc.params)
			if string(got.Params) != string(want) {
				t.Fatalf("params = %s, want %s", got.Params, want)
			}
		})
	}
}

// TestHoverAndDiagnostics is the end-to-end check: initialize against the demo
// repo, open real files, and assert a non-empty hover on a known variable plus
// the expected diagnostics the engine produces.
func TestHoverAndDiagnostics(t *testing.T) {
	root, err := filepath.Abs("../../examples/demo-infra")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("demo-infra not found: %v", err)
	}

	c := startServer(t)

	// initialize / initialized
	c.request("initialize", initializeParams{RootURI: fileURI(root)})
	c.notify("initialized", map[string]any{})

	// --- hover on a known play variable in webservers.yml ---
	pbPath := filepath.Join(root, "webservers.yml")
	pbText := readFile(t, pbPath)
	pbURI := fileURI(pbPath)
	c.notify("textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{URI: pbURI, Version: 1, Text: pbText},
	})

	line, ch := findToken(t, pbText, "healthcheck_path")
	hov := c.request("textDocument/hover", hoverParams{
		TextDocument: textDocumentIdentifier{URI: pbURI},
		Position:     position{Line: line, Character: ch},
	})
	var hr hoverResult
	if err := json.Unmarshal(hov.Result, &hr); err != nil {
		t.Fatalf("hover result: %v", err)
	}
	if hr.Contents.Value == "" {
		t.Fatal("hover on healthcheck_path returned empty content")
	}
	if !strings.Contains(hr.Contents.Value, "healthcheck_path") {
		t.Fatalf("hover missing var name:\n%s", hr.Contents.Value)
	}
	if !strings.Contains(strings.ToLower(hr.Contents.Value), "effective") {
		t.Fatalf("hover missing lineage (no 'effective' marker):\n%s", hr.Contents.Value)
	}
	t.Logf("HOVER healthcheck_path:\n%s", hr.Contents.Value)

	// --- diagnostics: unused host var in host_vars/lb01.yml ---
	hvPath := filepath.Join(root, "inventories/production/host_vars/lb01.yml")
	hvText := readFile(t, hvPath)
	hvURI := fileURI(hvPath)
	c.notify("textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{URI: hvURI, Version: 1, Text: hvText},
	})
	diags := c.waitDiagnostics(hvURI)
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "haproxy_node_weight") && d.Severity == severityHint {
			found = true
			t.Logf("DIAG %s:%d %s", hvURI, d.Range.Start.Line, d.Message)
		}
	}
	if !found {
		t.Fatalf("expected unused-var hint for haproxy_node_weight, got %d diagnostics: %+v", len(diags), diags)
	}

	// --- graceful shutdown/exit ---
	c.request("shutdown", nil)
	c.notify("exit", nil)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// findToken returns the (line, character) of the first occurrence of tok.
func findToken(t *testing.T, text, tok string) (int, int) {
	t.Helper()
	for i, line := range strings.Split(text, "\n") {
		if c := strings.Index(line, tok); c >= 0 {
			return i, c
		}
	}
	t.Fatalf("token %q not found", tok)
	return 0, 0
}
