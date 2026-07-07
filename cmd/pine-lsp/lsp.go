package main

// This file holds the subset of the Language Server Protocol structures Pine's
// server exchanges. Field names/tags follow the LSP spec so a generic client
// (VS Code, Neovim, the Go test client) interoperates without a schema.

// position is a zero-based (line, character) coordinate. Per the LSP spec
// character is a UTF-16 offset; Pine treats it as a byte offset within the
// line, which is exact for ASCII/Latin content and off only inside multi-byte
// runes (a documented v1 limitation).
type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// rng is an inclusive-start, exclusive-end span of a document.
type rng struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type textDocumentItem struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// initializeParams carries the workspace root; Pine scans rootUri (falling back
// to the deprecated rootPath) as the Ansible repository.
type initializeParams struct {
	RootURI  string `json:"rootUri"`
	RootPath string `json:"rootPath"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   serverInfo         `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// serverCapabilities advertises what Pine supports: full-document text sync and
// hover. textDocumentSync=1 means the client sends the whole document text on
// each change (simplest, and cheap given the incremental scan cache).
type serverCapabilities struct {
	TextDocumentSync int  `json:"textDocumentSync"`
	HoverProvider    bool `json:"hoverProvider"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

// contentChange is a full-document replacement (no Range) under sync mode 1.
type contentChange struct {
	Text string `json:"text"`
}

type didSaveParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type hoverParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
}

// hoverResult renders Markdown content in an editor tooltip.
type hoverResult struct {
	Contents markupContent `json:"contents"`
	Range    *rng          `json:"range,omitempty"`
}

type markupContent struct {
	Kind  string `json:"kind"` // "markdown"
	Value string `json:"value"`
}

// Diagnostic severities per the LSP spec.
const (
	severityError   = 1
	severityWarning = 2
	severityInfo    = 3
	severityHint    = 4
)

type diagnostic struct {
	Range    rng    `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []diagnostic `json:"diagnostics"`
}
