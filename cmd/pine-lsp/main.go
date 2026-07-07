// Command pine-lsp is a Language Server that exposes Pine's Ansible variable
// engine — precedence lineage, defined-nowhere and unused-variable/role
// analysis — inside any LSP-capable editor. It speaks JSON-RPC 2.0 over stdio
// (Content-Length framing) with no external dependencies, scans the workspace
// root as an Ansible repository, and answers hover with the full precedence
// chain of the variable under the cursor plus diagnostics on open/save.
//
// Usage: point a generic LSP client at the built binary; the editor launches
// it and communicates over stdin/stdout. See the README "Editor / LSP" section.
package main

import (
	"log"
	"os"
)

func main() {
	// Diagnostics/log go to stderr so they never corrupt the stdio JSON-RPC
	// stream on stdout.
	log.SetOutput(os.Stderr)
	log.SetPrefix("pine-lsp: ")
	log.SetFlags(0)

	srv := newServer(newConn(os.Stdin, os.Stdout))
	if err := srv.serve(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}
