// Pine - a modern, single-binary alternative to AWX / Ansible Tower.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/server"
	"github.com/jgsqware/pine/internal/store"
	"github.com/jgsqware/pine/internal/tui"
)

var version = "0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `Pine %s - the Ansible control plane that doesn't need a control plane

Usage:
  pine serve [--addr :8743] [--data DIR] [--demo]   Start the web UI + API server
  pine tui   [--data DIR] [--demo]                  Start the terminal UI
  pine scan  PATH                                   Scan an Ansible repo and print JSON
  pine version                                      Print version

Environment:
  PINE_DATA   data directory (default ~/.pine)
  PINE_DEMO   set to 1 to auto-register the bundled demo repository
`, version)
}

func defaultDataDir() string {
	if d := os.Getenv("PINE_DATA"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pine"
	}
	return filepath.Join(home, ".pine")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "tui":
		cmdTUI(os.Args[2:])
	case "scan":
		cmdScan(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("pine", version)
	default:
		usage()
		os.Exit(2)
	}
}

func openManager(dataDir string, demo bool) *runner.Manager {
	st, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("open data dir: %v", err)
	}
	mgr := runner.New(st)
	if demo || os.Getenv("PINE_DEMO") == "1" {
		registerDemo(mgr)
	}
	return mgr
}

// registerDemo connects the bundled Acme Corp demo repository when present.
func registerDemo(mgr *runner.Manager) {
	for _, r := range mgr.Store.ListRepos() {
		if r.Name == "demo-infra" {
			return
		}
	}
	for _, candidate := range []string{
		os.Getenv("PINE_DEMO_PATH"),
		"examples/demo-infra",
		"/usr/share/pine/demo-infra",
	} {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			repo := model.Repo{
				ID:     store.NewID("r"),
				Name:   "demo-infra",
				Path:   abs,
				Status: model.RepoNew,
			}
			if err := mgr.Store.AddRepo(repo); err == nil {
				_, _ = mgr.SyncRepo(repo.ID)
				log.Printf("demo repository registered from %s", abs)
			}
			return
		}
	}
	log.Printf("demo requested but examples/demo-infra not found")
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8743", "listen address")
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	_ = fs.Parse(args)

	mgr := openManager(*data, *demo)
	h := server.New(mgr)
	log.Printf("Pine %s listening on http://localhost%s (data: %s)", version, *addr, *data)
	if err := http.ListenAndServe(*addr, h); err != nil {
		log.Fatal(err)
	}
}

func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	_ = fs.Parse(args)

	mgr := openManager(*data, *demo)
	if err := tui.Run(mgr); err != nil {
		log.Fatal(err)
	}
}

func cmdScan(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine scan PATH")
		os.Exit(2)
	}
	res, err := scanner.Scan(args[0])
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}
