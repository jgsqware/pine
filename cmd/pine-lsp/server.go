package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// server is one LSP session. It owns the workspace scan (an in-memory
// model.ScanResult), the incremental parse cache reused across re-scans, and
// the set of open documents keyed by URI. All engine analysis (lineage,
// hygiene) runs against res; the raw document text drives cursor/word
// detection and diagnostic positioning.
type server struct {
	conn *conn

	mu    sync.Mutex
	root  string             // absolute workspace root
	cache *scanner.ScanCache // incremental parse cache (persists across scans)
	res   *model.ScanResult  // latest scan
	docs  map[string]string  // uri -> current text
	shut  bool               // shutdown received
}

func newServer(c *conn) *server {
	return &server{conn: c, cache: scanner.NewScanCache(), docs: map[string]string{}}
}

// serve runs the read/dispatch loop until EOF or exit. Requests get responses;
// notifications may trigger server-initiated publishDiagnostics.
func (s *server) serve() error {
	for {
		m, err := s.conn.read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if s.dispatch(m) {
			return nil // exit
		}
	}
}

// dispatch routes one message. It returns true when the server must terminate
// (the `exit` notification).
func (s *server) dispatch(m *message) (exit bool) {
	switch m.Method {
	case "initialize":
		s.handleInitialize(m)
	case "initialized":
		// notification, nothing to do
	case "shutdown":
		s.mu.Lock()
		s.shut = true
		s.mu.Unlock()
		_ = s.conn.respond(m.ID, nil)
	case "exit":
		return true
	case "textDocument/didOpen":
		s.handleDidOpen(m)
	case "textDocument/didChange":
		s.handleDidChange(m)
	case "textDocument/didSave":
		s.handleDidSave(m)
	case "textDocument/didClose":
		s.handleDidClose(m)
	case "textDocument/hover":
		s.handleHover(m)
	default:
		// Unknown request: reply with a "method not found" error so the client
		// is not left waiting; unknown notifications are ignored.
		if len(m.ID) > 0 {
			_ = s.conn.respondErr(m.ID, -32601, "method not found: "+m.Method)
		}
	}
	return false
}

func (s *server) handleInitialize(m *message) {
	var p initializeParams
	_ = json.Unmarshal(m.Params, &p)
	root := uriToPath(p.RootURI)
	if root == "" {
		root = p.RootPath
	}
	s.mu.Lock()
	s.root = root
	s.mu.Unlock()
	s.rescan() // initial workspace scan
	_ = s.conn.respond(m.ID, initializeResult{
		Capabilities: serverCapabilities{TextDocumentSync: 1, HoverProvider: true},
		ServerInfo:   serverInfo{Name: "pine-lsp", Version: "0.1.0"},
	})
}

func (s *server) handleDidOpen(m *message) {
	var p didOpenParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return
	}
	s.mu.Lock()
	s.docs[p.TextDocument.URI] = p.TextDocument.Text
	s.mu.Unlock()
	s.publishDiagnostics(p.TextDocument.URI)
}

func (s *server) handleDidChange(m *message) {
	var p didChangeParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return
	}
	if len(p.ContentChanges) == 0 {
		return
	}
	// Sync mode 1: the last change carries the full document text.
	s.mu.Lock()
	s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
	s.mu.Unlock()
	// Re-scan on save (cheaper, and the on-disk file is what the engine reads);
	// on didChange we keep the previous scan and only refresh text-based
	// diagnostics so hover/typing stay responsive.
	s.publishDiagnostics(p.TextDocument.URI)
}

func (s *server) handleDidSave(m *message) {
	var p didSaveParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return
	}
	s.rescan() // incremental: only changed files re-parse (ScanWithCache)
	s.publishDiagnostics(p.TextDocument.URI)
}

func (s *server) handleDidClose(m *message) {
	var p didCloseParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return
	}
	s.mu.Lock()
	delete(s.docs, p.TextDocument.URI)
	s.mu.Unlock()
	// Clear diagnostics for the closed file.
	_ = s.conn.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI: p.TextDocument.URI, Diagnostics: []diagnostic{},
	})
}

// rescan reparses the workspace using the persistent incremental cache: files
// whose mtime/size are unchanged reuse their cached parse result.
func (s *server) rescan() {
	s.mu.Lock()
	root := s.root
	cache := s.cache
	s.mu.Unlock()
	if root == "" {
		return
	}
	res, err := scanner.ScanWithCache(root, cache)
	if err != nil {
		log.Printf("pine-lsp: scan %s: %v", root, err)
		return
	}
	s.mu.Lock()
	s.res = res
	s.mu.Unlock()
}

// snapshot returns the current scan, root and a copy of the document text under
// one lock acquisition.
func (s *server) snapshot(uri string) (*model.ScanResult, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res, s.root, s.docs[uri]
}

// uriToPath converts a file:// URI to a local filesystem path. Non-file URIs
// and parse errors yield "".
func uriToPath(uri string) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		// Some clients send a bare path.
		if strings.HasPrefix(uri, "/") {
			return uri
		}
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.Clean(p)
}
