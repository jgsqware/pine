// Pine - a modern, single-binary alternative to AWX / Ansible Tower.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
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
  pine PATH  [--addr :8743] [--no-open] [--tui]     Run Pine locally on one repo
  pine serve [--addr :8743] [--data DIR] [--demo]   Start the web UI + API server
  pine tui   [PATH] [--data DIR] [--demo]           Start the terminal UI (PATH opens that repo)
  pine scan  PATH                                   Scan an Ansible repo and print JSON
  pine plan  PATH PLAYBOOK [flags]                  Predict what a playbook would do
  pine impact PATH [--base REF] [--head REF]        Blast radius of a git diff
  pine version                                      Print version

Plan flags:
  -i INVENTORY   inventory name or path
  --limit/--tags/--check    like ansible-playbook
  --profile ID   fact profile (ubuntu-24.04, debian-12, rhel-9, ...)
  -e key=value   extra var (repeatable; value parsed as JSON when possible)
  --json         print the raw plan JSON

Examples:
  pine .                 Serve the current directory and open it in your browser
  pine . --tui           Scan the current directory and open the terminal UI

Environment:
  PINE_DATA   data directory (default ~/.pine, or <PATH>/.pine in local mode)
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
	case "plan":
		cmdPlan(os.Args[2:])
	case "impact":
		cmdImpact(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("pine", version)
	case "help", "--help", "-h":
		usage()
	default:
		// `pine PATH` (e.g. `pine .`): if the first argument is a directory,
		// run Pine locally against that single repository.
		if isDir(os.Args[1]) {
			cmdLocal(os.Args[1], os.Args[2:])
			return
		}
		usage()
		os.Exit(2)
	}
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
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

// cmdLocal runs Pine against a single local repository: it registers the
// directory as a repo, then either serves the web UI + API (default) or
// launches the terminal UI (--tui).
// Data lives in <path>/.pine by default so each repo is self-contained.
func cmdLocal(path string, args []string) {
	fs := flag.NewFlagSet("local", flag.ExitOnError)
	addr := fs.String("addr", ":8743", "listen address")
	dataDefault := os.Getenv("PINE_DATA")
	data := fs.String("data", dataDefault, "data directory (default <PATH>/.pine)")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	useTUI := fs.Bool("tui", false, "launch the terminal UI instead of the web server")
	_ = fs.Parse(args)

	abs, err := filepath.Abs(path)
	if err != nil {
		log.Fatalf("resolve path: %v", err)
	}
	dataDir := *data
	if dataDir == "" {
		dataDir = filepath.Join(abs, ".pine")
	}

	st, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("open data dir: %v", err)
	}
	mgr := runner.New(st)
	registerLocalRepo(mgr, abs)

	if *useTUI {
		if err := tui.Run(mgr, ""); err != nil {
			log.Fatal(err)
		}
		return
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	url := localURL(ln.Addr())
	log.Printf("Pine %s serving %s on %s (data: %s)", version, abs, url, dataDir)
	if !*noOpen {
		openBrowser(url)
	}
	if err := http.Serve(ln, server.New(mgr)); err != nil {
		log.Fatal(err)
	}
}

// registerLocalRepo adds the directory as a repo (deduped by path) and scans it.
func registerLocalRepo(mgr *runner.Manager, abs string) {
	for _, r := range mgr.Store.ListRepos() {
		if r.Path == abs {
			_, _ = mgr.SyncRepo(r.ID)
			return
		}
	}
	repo := model.Repo{
		ID:     store.NewID("r"),
		Name:   filepath.Base(abs),
		Path:   abs,
		Status: model.RepoNew,
	}
	if err := mgr.Store.AddRepo(repo); err != nil {
		log.Fatalf("register repo: %v", err)
	}
	if _, err := mgr.SyncRepo(repo.ID); err != nil {
		log.Printf("scan %s: %v", abs, err)
	}
}

// localURL turns a listener address into a browsable http://localhost URL.
func localURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// openBrowser best-effort opens url in the user's default browser.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "cmd", []string{"/c", "start"}
	default: // linux, *bsd, wsl
		if _, err := exec.LookPath("wslview"); err == nil {
			cmd = "wslview"
		} else {
			cmd = "xdg-open"
		}
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8743", "listen address")
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	_ = fs.Parse(args)

	mgr := openManager(*data, *demo)
	go mgr.StartScheduler(context.Background())
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
	tui.Version = version

	// An optional PATH argument opens that directory as a repo and focuses it.
	focus := ""
	if path := fs.Arg(0); path != "" {
		id, err := registerPath(mgr, path)
		if err != nil {
			log.Fatalf("open %s: %v", path, err)
		}
		focus = id
	}

	if err := tui.Run(mgr, focus); err != nil {
		log.Fatal(err)
	}
}

// registerPath connects a local directory as a repo (reusing an existing
// entry with the same path) and kicks off a sync, returning the repo ID.
func registerPath(mgr *runner.Manager, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	for _, r := range mgr.Store.ListRepos() {
		if r.Path == abs {
			_, _ = mgr.SyncRepo(r.ID)
			return r.ID, nil
		}
	}
	repo := model.Repo{
		ID:     store.NewID("r"),
		Name:   filepath.Base(abs),
		Path:   abs,
		Status: model.RepoNew,
	}
	if err := mgr.Store.AddRepo(repo); err != nil {
		return "", err
	}
	if _, err := mgr.SyncRepo(repo.ID); err != nil {
		return "", err
	}
	return repo.ID, nil
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

// extraVars collects repeatable -e key=value flags.
type extraVars map[string]any

func (e extraVars) String() string { return "" }
func (e extraVars) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	var parsed any
	if err := json.Unmarshal([]byte(v), &parsed); err == nil {
		e[k] = parsed
	} else {
		e[k] = v
	}
	return nil
}

func cmdPlan(args []string) {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	inv := fs.String("i", "", "inventory name or path")
	limit := fs.String("limit", "", "host limit pattern")
	tags := fs.String("tags", "", "only tasks with these tags")
	check := fs.Bool("check", false, "plan a --check run")
	profile := fs.String("profile", "", "fact profile id")
	asJSON := fs.Bool("json", false, "print raw plan JSON")
	vars := extraVars{}
	fs.Var(vars, "e", "extra var key=value (repeatable)")
	_ = fs.Parse(args)
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: pine plan PATH PLAYBOOK [flags]")
		os.Exit(2)
	}
	rest := fs.Args()
	root, playbook := rest[0], rest[1]
	// Go's flag package stops at the first positional: re-parse what
	// follows PATH PLAYBOOK so flags can be given in any order.
	_ = fs.Parse(rest[2:])

	res, err := scanner.Scan(root)
	if err != nil {
		log.Fatal(err)
	}
	abs, _ := filepath.Abs(root)
	out, err := plan.Compute(res, abs, model.Repo{ID: "local", Name: filepath.Base(abs)}, plan.Request{
		Playbook: playbook, Inventory: *inv, Limit: *limit, Tags: *tags,
		Check: *check, Vars: vars, FactProfile: *profile,
	})
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	printPlan(out)
	if out.Summary.Unknown > 0 {
		os.Exit(3) // distinct exit code: plan incomplete
	}
}

const (
	cGreen = "\033[32m"
	cGray  = "\033[90m"
	cAmber = "\033[33m"
	cBold  = "\033[1m"
	cOff   = "\033[0m"
)

func printPlan(out *plan.Result) {
	fmt.Printf("%sPLAN%s %s  inventory=%s  mode=%s", cBold, cOff, out.Playbook, out.Inventory, out.Mode)
	if out.FactProfile != "" {
		fmt.Printf("  facts=%s", out.FactProfile)
	}
	if out.Check {
		fmt.Print("  --check")
	}
	fmt.Println()
	for _, pp := range out.Plays {
		if pp.Import != "" {
			fmt.Printf("\n%s→ imports %s%s\n", cGray, pp.Import, cOff)
			continue
		}
		fmt.Printf("\n%sPLAY [%s]%s  hosts=%s matched=%d", cBold, pp.Name, cOff, pp.Hosts, len(pp.MatchedHosts))
		if len(pp.Batches) > 1 {
			fmt.Printf("  serial: %d batches", len(pp.Batches))
		}
		fmt.Println()
		for _, tp := range pp.Tasks {
			marker, color := "✓", cGreen
			switch {
			case tp.Counts.Unknown > 0:
				marker, color = "?", cAmber
			case tp.Counts.Run == 0:
				marker, color = "-", cGray
			}
			label := tp.Name
			if tp.Role != "" {
				label = tp.Role + " : " + label
			}
			fmt.Printf("  %s%s%s %-58s %srun=%d skip=%d unknown=%d%s",
				color, marker, cOff, label, cGray, tp.Counts.Run, tp.Counts.Skip, tp.Counts.Unknown, cOff)
			if tp.LoopItems > 0 {
				fmt.Printf(" %s×%d%s", cGray, tp.LoopItems, cOff)
			} else if tp.LoopItems == -1 {
				fmt.Printf(" %sloop ?%s", cGray, cOff)
			}
			fmt.Println()
			if tp.Counts.Unknown > 0 {
				seen := map[string]bool{}
				for _, hv := range tp.Hosts {
					for _, m := range hv.Missing {
						if !seen[m] {
							seen[m] = true
							fmt.Printf("      %s? missing: %s%s\n", cAmber, m, cOff)
						}
					}
				}
			}
		}
		for _, h := range pp.Handlers {
			u := ""
			if h.Uncertain {
				u = cAmber + " (uncertain)" + cOff
			}
			fmt.Printf("  %s⚑ handler %s%s on %d host(s)%s\n", cGray, h.Name, cOff, len(h.Hosts), u)
		}
	}
	s := out.Summary
	fmt.Printf("\n%sSummary:%s %shosts=%d tasks=%d%s  %srun=%d%s %sskip=%d%s %sunknown=%d%s\n",
		cBold, cOff, cGray, s.Hosts, s.Tasks, cOff, cGreen, s.Run, cOff, cGray, s.Skip, cOff, cAmber, s.Unknown, cOff)
	if len(s.MissingVars) > 0 {
		fmt.Printf("%sProvide these vars (-e) or a fact profile (--profile) to resolve unknowns:%s\n", cAmber, cOff)
		for _, mv := range s.MissingVars {
			fmt.Printf("  %s (%d verdicts)\n", mv.Name, mv.Count)
		}
	}
}

func cmdImpact(args []string) {
	fs := flag.NewFlagSet("impact", flag.ExitOnError)
	base := fs.String("base", "", "base git ref (default: HEAD, comparing the worktree)")
	head := fs.String("head", "", "head git ref (default: worktree)")
	asJSON := fs.Bool("json", false, "print raw JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine impact PATH [--base REF] [--head REF]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:])

	res, err := scanner.Scan(root)
	if err != nil {
		log.Fatal(err)
	}
	abs, _ := filepath.Abs(root)
	out, err := plan.Impact(res, abs, *base, *head)
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	fmt.Printf("%sIMPACT%s %s..%s\n", cBold, cOff, out.Base, out.Head)
	if len(out.ChangedFiles) == 0 {
		fmt.Println("no changes detected")
		return
	}
	for _, e := range out.Entries {
		fmt.Printf("\n%s%s%s %s(%s)%s\n", cBold, e.File, cOff, cGray, e.Kind, cOff)
		if len(e.Roles) > 0 {
			fmt.Printf("  roles:     %s\n", strings.Join(e.Roles, ", "))
		}
		for _, pb := range e.Playbooks {
			fmt.Printf("  playbook:  %s %s(%s)%s\n", pb.Path, cGray, pb.Via, cOff)
		}
		if len(e.Handlers) > 0 {
			fmt.Printf("  %shandlers:  %s%s\n", cAmber, strings.Join(e.Handlers, ", "), cOff)
		}
	}
	s := out.Summary
	fmt.Printf("\n%sSummary:%s %d file(s) → %d role(s) → %d playbook(s) → %s%d host(s)%s",
		cBold, cOff, s.Files, s.Roles, s.Playbooks, cAmber, s.HostsTotal, cOff)
	for inv, n := range s.HostsByInventory {
		fmt.Printf("  %s%s: %d%s", cGray, inv, n, cOff)
	}
	fmt.Println()
	if len(s.Handlers) > 0 {
		fmt.Printf("%swould trigger: %s%s\n", cAmber, strings.Join(s.Handlers, ", "), cOff)
	}
	if s.HostsTotal > 0 {
		os.Exit(3) // distinct exit code for CI: changes have impact
	}
}
