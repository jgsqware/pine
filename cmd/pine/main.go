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
	"strconv"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/client"
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/policy"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/scanner"
	"github.com/jgsqware/pine/internal/server"
	"github.com/jgsqware/pine/internal/store"
	"github.com/jgsqware/pine/internal/tui"
)

var version = "0.1.0"

// buildTime is stamped at build time via -ldflags (RFC3339). Empty in `go run`.
var buildTime = ""

func usage() {
	fmt.Fprintf(os.Stderr, `Pine %s - the Ansible control plane that doesn't need a control plane

Usage:
  pine PATH  [--addr :8743] [--no-open] [--tui]     Run Pine locally on one repo
  pine serve [--addr :8743] [--data DIR] [--demo]   Start the web UI + API server
  pine tui   [PATH] [--data DIR] [--demo]           Start the terminal UI (PATH opens that repo)
  pine attach [--addr :8743]                        Attach the terminal UI to a running daemon
  pine service install|status|uninstall             Run Pine as a systemd (user) service
  pine scan  PATH [--paths SUBDIR]                  Scan an Ansible repo and print JSON
  pine lineage PATH -i INV (--host H|--all-hosts)   Resolved vars + provenance per host (--json, --redact)
  pine lineage PATH --playbook PB -i INV [flags]    + a playbook's effective vars (expands import_tasks/include_vars)
  pine plan  PATH PLAYBOOK [flags]                  Predict what a playbook would do
  pine impact PATH [--base REF] [--head REF]        Blast radius of a git diff
  pine policy check PATH --policies FILE [flags]    Evaluate governance policies on the plan (CI gate)
  pine worktrees PATH [--json]                      List the repo's git worktrees
  pine probe list                                   List the read-only probes
  pine probe run PROBE [--repo N] [--limit PAT]     Observe hosts without SSH (read-only)
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
  PINE_DATA      data directory (default ~/.pine, or <PATH>/.pine in local mode)
  PINE_DEMO      set to 1 to auto-register the bundled demo repository
  PINE_ADDR      daemon address for 'pine attach' / detection (default :8743)
  PINE_TOKEN     require this API token (mandatory for a non-loopback bind)
  PINE_MAX_JOBS  max concurrent playbook runs (default 4)
  PINE_TOOL_PATH extra dirs to find ansible in (colon-separated); Pine already
                 looks in mise/asdf shims and ~/.local/bin for non-login PATHs
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
	// surface the build stamp to the HTTP layer (footer + /api/version)
	server.Version = version
	server.BuildTime = buildTime
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "tui":
		cmdTUI(os.Args[2:])
	case "attach":
		cmdAttach(os.Args[2:])
	case "service":
		cmdService(os.Args[2:])
	case "scan":
		cmdScan(os.Args[2:])
	case "plan":
		cmdPlan(os.Args[2:])
	case "lineage":
		cmdLineage(os.Args[2:])
	case "impact":
		cmdImpact(os.Args[2:])
	case "policy":
		cmdPolicy(os.Args[2:])
	case "hygiene":
		cmdHygiene(os.Args[2:])
	case "worktrees":
		cmdWorktrees(os.Args[2:])
	case "probe":
		cmdProbe(os.Args[2:])
	case "version", "--version", "-v":
		if buildTime != "" {
			fmt.Printf("pine %s (built %s)\n", version, buildTime)
		} else {
			fmt.Println("pine", version)
		}
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
	if n := mgr.ReconcileInterruptedJobs(); n > 0 {
		log.Printf("reconciled %d job(s) left in flight by a previous run", n)
	}
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
	addr := fs.String("addr", "127.0.0.1:8743", "listen address")
	dataDefault := os.Getenv("PINE_DATA")
	data := fs.String("data", dataDefault, "data directory (default <PATH>/.pine)")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	useTUI := fs.Bool("tui", false, "launch the terminal UI instead of the web server")
	token := fs.String("token", os.Getenv("PINE_TOKEN"), "require this token on /api/ (or set PINE_TOKEN); mandatory for a non-loopback bind")
	insecure := fs.Bool("insecure", false, "allow a non-loopback bind without a token (not recommended)")
	label := fs.String("label", os.Getenv("PINE_LABEL"), "instance label (or set PINE_LABEL) shown in the title/PWA, e.g. iba, gaming1")
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
	if n := mgr.ReconcileInterruptedJobs(); n > 0 {
		log.Printf("reconciled %d job(s) left in flight by a previous run", n)
	}
	registerLocalRepo(mgr, abs)

	if *useTUI {
		if err := tui.Run(mgr, ""); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := guardBind(*addr, *token, *insecure); err != nil {
		log.Fatal(err)
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
	if err := http.Serve(ln, server.New(mgr, server.Config{Token: *token, Label: *label})); err != nil {
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

// isLoopbackBind reports whether a listen address binds only the loopback
// interface. An empty host (":8743"), "0.0.0.0" or "::" binds every interface
// and is therefore not loopback-only.
func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// guardBind refuses to expose Pine on a non-loopback address without a token.
// Pine's API executes ansible-playbook and clones git, so an unauthenticated
// public bind is a remote-code-execution surface. Loopback binds stay
// friction-free; exposing requires either a token or an explicit --insecure.
func guardBind(addr, token string, insecure bool) error {
	if isLoopbackBind(addr) || token != "" || insecure {
		return nil
	}
	return fmt.Errorf("refusing to bind %s without authentication: the API runs "+
		"ansible and git.\n  Set a token (--token or PINE_TOKEN) to require auth, "+
		"or pass --insecure to override (not recommended).", addr)
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
	addr := fs.String("addr", "127.0.0.1:8743", "listen address")
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	token := fs.String("token", os.Getenv("PINE_TOKEN"), "require this token on /api/ (or set PINE_TOKEN); mandatory for a non-loopback bind")
	insecure := fs.Bool("insecure", false, "allow a non-loopback bind without a token (not recommended)")
	label := fs.String("label", os.Getenv("PINE_LABEL"), "instance label (or set PINE_LABEL) shown in the title/PWA, e.g. iba, gaming1")
	_ = fs.Parse(args)

	if err := guardBind(*addr, *token, *insecure); err != nil {
		log.Fatal(err)
	}
	mgr := openManager(*data, *demo)
	go mgr.StartScheduler(context.Background())
	h := server.New(mgr, server.Config{Token: *token, Label: *label})
	auth := "disabled (loopback)"
	if *token != "" {
		auth = "token required"
	}
	log.Printf("Pine %s listening on http://localhost%s (data: %s, auth: %s)", version, *addr, *data, auth)
	if err := http.ListenAndServe(*addr, h); err != nil {
		log.Fatal(err)
	}
}

func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "data directory")
	demo := fs.Bool("demo", false, "register the bundled demo repository")
	_ = fs.Parse(args)

	// Warn if a daemon already owns this data dir: the file-backed store has no
	// cross-process lock, so a second engine racing the same files can corrupt
	// state. Attaching over HTTP is the safe way to share a running instance.
	if client.New(baseURL(defaultAddr())).Ping() == nil {
		fmt.Fprintf(os.Stderr, "warning: a Pine daemon is already running on %s.\n"+
			"Use `pine attach` to drive it instead of opening a second engine on %s.\n\n",
			defaultAddr(), *data)
	}

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

// cmdAttach connects the terminal UI to an already-running Pine daemon over
// its HTTP API instead of opening a second engine on the shared data dir
// (which the file-backed store does not lock across processes).
func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	addr := fs.String("addr", defaultAddr(), "address of the running daemon (host:port or :port)")
	_ = fs.Parse(args)

	base := baseURL(*addr)
	c := client.New(base)
	if err := c.Ping(); err != nil {
		log.Fatalf("no Pine daemon at %s: %v\nStart one with `pine serve`, or run `pine tui` for a local session.", base, err)
	}
	tui.Version = version
	if err := tui.RunEngine(c, ""); err != nil {
		log.Fatal(err)
	}
}

// cmdProbe runs read-only observability probes against hosts, so an operator
// can inspect a box without opening an SSH session. Probes are named entries
// in a server-side catalog — there is no way to pass a command string.
func cmdProbe(args []string) {
	if len(args) > 0 && args[0] == "list" {
		for _, p := range runner.Probes() {
			inv := "ansible -m " + p.Module
			if p.Args != "" {
				inv += " -a " + strconv.Quote(p.Args)
			}
			fmt.Printf("%-10s %-22s %s\n%-33s %s\n\n", p.ID, p.Title, p.Desc, "", inv)
		}
		return
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	repoRef := fs.String("repo", "", "repo name or ID (default: the only repo, if there is exactly one)")
	inv := fs.String("i", "", "inventory name or path")
	limit := fs.String("limit", "", "ansible host pattern to probe (default: all)")
	addr := fs.String("addr", defaultAddr(), "address of the running daemon")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine probe run PROBE [--repo NAME] [-i INV] [--limit PATTERN]")
		fmt.Fprintln(os.Stderr, "       pine probe list")
		os.Exit(2)
	}
	probeID := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PROBE
	if _, ok := runner.ProbeByID(probeID); !ok {
		fmt.Fprintf(os.Stderr, "unknown probe %q — see `pine probe list`\n", probeID)
		os.Exit(2)
	}

	base := baseURL(*addr)
	c := client.New(base)
	if err := c.Ping(); err != nil {
		log.Fatalf("no Pine daemon at %s: %v\nStart one with `pine serve`.", base, err)
	}
	repoID, err := resolveRepo(c, *repoRef)
	if err != nil {
		log.Fatal(err)
	}

	job, err := c.RunProbe(repoID, probeID, *inv, *limit)
	if err != nil {
		log.Fatal(err)
	}
	// stream the live log, then fall back to the stored one if the job already
	// finished before we managed to subscribe
	if ch, ok := c.Subscribe(job.ID); ok {
		for line := range ch {
			fmt.Println(line)
		}
	} else if out, err := c.JobLog(job.ID); err == nil {
		fmt.Print(out)
	}

	// the stream can end before the daemon has written the terminal state
	final := job
	for range 50 {
		if cur, err := c.GetJob(job.ID); err == nil {
			final = cur
			if cur.Terminal() {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	s := final.Summary
	fmt.Printf("\n%s: ok=%d unreachable=%d failed=%d\n", final.Status, s.OK, s.Unreachable, s.Failed)
	if final.Status != model.JobSuccess {
		os.Exit(1)
	}
}

// resolveRepo turns a repo name or ID into an ID, defaulting to the sole repo.
func resolveRepo(c *client.Client, ref string) (string, error) {
	repos := c.ListRepos()
	if ref == "" {
		if len(repos) == 1 {
			return repos[0].ID, nil
		}
		return "", fmt.Errorf("--repo is required (%d repos configured)", len(repos))
	}
	for _, r := range repos {
		if r.ID == ref || r.Name == ref {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("unknown repo %q", ref)
}

// defaultAddr is the daemon address attach/detection use, overridable via
// PINE_ADDR (falls back to the default serve port).
func defaultAddr() string {
	if a := os.Getenv("PINE_ADDR"); a != "" {
		return a
	}
	return ":8743"
}

// baseURL turns a listen address (":8743" or "host:port", with or without a
// scheme) into a browser-style base URL.
func baseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr
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

// scanPathList is a repeatable / comma-separated --paths flag. It maps to
// scanner.Scan's variadic scanPaths, scoping discovery to subdirectories of
// a monorepo (one project / env at a time).
type scanPathList []string

func (p *scanPathList) String() string { return strings.Join(*p, ",") }
func (p *scanPathList) Set(s string) error {
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*p = append(*p, part)
		}
	}
	return nil
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	var paths scanPathList
	fs.Var(&paths, "paths", "restrict scan to subdir(s) of PATH (repeatable / comma-separated)")
	_ = fs.Bool("json", true, "print scan JSON (always on)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine scan PATH [--paths SUBDIR]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PATH

	res, err := scanner.Scan(root, paths...)
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}

func cmdLineage(args []string) {
	fs := flag.NewFlagSet("lineage", flag.ExitOnError)
	inv := fs.String("i", "", "inventory name or path")
	host := fs.String("host", "", "host to resolve variables for")
	allHosts := fs.Bool("all-hosts", false, "resolve every host in the inventory (one scan; emits a JSON array)")
	playbook := fs.String("playbook", "", "resolve a playbook's effective vars (expands import_tasks + include_vars), not just inventory precedence")
	var paths scanPathList
	fs.Var(&paths, "paths", "restrict scan to subdir(s) of PATH (repeatable / comma-separated)")
	redact := fs.Bool("redact", false, "mask vault blobs and password-like values")
	asJSON := fs.Bool("json", false, "print raw lineage JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine lineage PATH -i INVENTORY (--host HOST | --all-hosts) [--playbook PB] [--paths SUBDIR] [--redact] [--json]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PATH
	if *host == "" && !*allHosts {
		fmt.Fprintln(os.Stderr, "lineage: --host or --all-hosts is required")
		os.Exit(2)
	}

	res, err := scanner.Scan(root, paths...)
	if err != nil {
		log.Fatal(err)
	}

	// playbook mode: resolve the playbook's effective vars (include_vars +
	// import_tasks expanded), emitted in the same per-host lineage shape.
	if *playbook != "" {
		emit := func(lins []*plan.LineageResult) {
			if *redact {
				for _, l := range lins {
					l.Redact()
				}
			}
			if *asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if *allHosts {
					_ = enc.Encode(lins)
				} else {
					_ = enc.Encode(lins[0])
				}
				return
			}
			for _, l := range lins {
				printLineage(l)
			}
		}
		if *allHosts {
			lins, err := plan.ResolveLineageAll(res, root, *playbook, *inv)
			if err != nil {
				log.Fatal(err)
			}
			emit(lins)
		} else {
			lin, err := plan.ResolveLineage(res, root, *playbook, *inv, *host)
			if err != nil {
				log.Fatal(err)
			}
			emit([]*plan.LineageResult{lin})
		}
		return
	}

	if *allHosts {
		lins, err := plan.LineageAll(res, *inv)
		if err != nil {
			log.Fatal(err)
		}
		if *redact {
			for _, l := range lins {
				l.Redact()
			}
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(lins)
			return
		}
		for _, l := range lins {
			printLineage(l)
		}
		return
	}

	lin, err := plan.Lineage(res, *inv, *host)
	if err != nil {
		log.Fatal(err)
	}
	if *redact {
		lin.Redact()
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(lin)
		return
	}
	printLineage(lin)
}

func printLineage(lin *plan.LineageResult) {
	fmt.Printf("%sLINEAGE%s host=%s  inventory=%s  %s%d vars%s\n",
		cBold, cOff, lin.Host, lin.Inventory, cGray, len(lin.Vars), cOff)
	for _, v := range lin.Vars {
		fmt.Printf("\n  %s%s%s = %v\n", cBold, v.Key, cOff, v.Value)
		for i, e := range v.Chain {
			eff := ""
			if i == len(v.Chain)-1 {
				eff = cGreen + "  (effective)" + cOff
			}
			fmt.Printf("      %s%-12s %-16s%s = %v%s\n", cGray, e.Scope, e.Name, cOff, e.Value, eff)
		}
	}
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
	cRed   = "\033[31m"
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

func cmdWorktrees(args []string) {
	fs := flag.NewFlagSet("worktrees", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print raw worktrees JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine worktrees PATH [--json]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PATH

	out, err := plan.Worktrees(root)
	if err != nil {
		log.Fatal(err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}
	if !out.IsGit {
		fmt.Printf("%s%s%s is not a git repository — no worktrees\n", cGray, out.Root, cOff)
		return
	}
	fmt.Printf("%sWORKTREES%s %s%s%s  %s%d tree(s)%s\n",
		cBold, cOff, cGray, out.Root, cOff, cGray, len(out.Worktrees), cOff)
	for _, w := range out.Worktrees {
		ref := w.Branch
		if ref == "" {
			ref = "(detached)"
		}
		marker, color := " ", cGreen
		if w.Main {
			marker = "★"
		}
		head := w.Head
		if len(head) > 8 {
			head = head[:8]
		}
		fmt.Printf("  %s%s%s %-24s %s%-10s %s%s", color, marker, cOff, ref, cGray, head, w.Path, cOff)
		var flags []string
		if w.Bare {
			flags = append(flags, "bare")
		}
		if w.Locked {
			f := "locked"
			if w.LockReason != "" {
				f += ": " + w.LockReason
			}
			flags = append(flags, f)
		}
		if w.Prunable {
			f := "prunable"
			if w.PrunableReason != "" {
				f += ": " + w.PrunableReason
			}
			flags = append(flags, f)
		}
		if len(flags) > 0 {
			fmt.Printf("  %s[%s]%s", cAmber, strings.Join(flags, ", "), cOff)
		}
		fmt.Println()
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

// cmdPolicy evaluates policy-as-code rules against the estimated plan of a repo
// (the OPA/Sentinel of Ansible): `pine policy check PATH --policies FILE`. It
// computes a plan per playbook (or one with --playbook), runs the rules, prints
// the violations and exits 1 when any error-severity rule fires — the CI gate.
func cmdPolicy(args []string) {
	if len(args) < 1 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, "usage: pine policy check PATH --policies FILE [-i INV] [--playbook PB] [--limit L] [--tags T] [-e k=v] [--json]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("policy check", flag.ExitOnError)
	policiesPath := fs.String("policies", "", "path to the policy YAML file (required)")
	inv := fs.String("i", "", "inventory name or path")
	playbook := fs.String("playbook", "", "restrict evaluation to one playbook (default: all)")
	limit := fs.String("limit", "", "host limit pattern")
	tags := fs.String("tags", "", "only tasks with these tags")
	asJSON := fs.Bool("json", false, "print raw violations JSON")
	vars := extraVars{}
	fs.Var(vars, "e", "extra var key=value (repeatable)")
	_ = fs.Parse(args[1:])
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine policy check PATH --policies FILE [flags]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PATH
	if *policiesPath == "" {
		fmt.Fprintln(os.Stderr, "policy check: --policies FILE is required")
		os.Exit(2)
	}

	policies, err := policy.Load(*policiesPath)
	if err != nil {
		log.Fatal(err)
	}
	res, err := scanner.Scan(root)
	if err != nil {
		log.Fatal(err)
	}
	abs, _ := filepath.Abs(root)

	var pbs []string
	if *playbook != "" {
		pbs = []string{*playbook}
	} else {
		for _, pb := range res.Playbooks {
			pbs = append(pbs, pb.Path)
		}
	}
	if len(pbs) == 0 {
		fmt.Fprintln(os.Stderr, "policy check: no playbooks found")
		os.Exit(2)
	}

	var reports []policyReport
	var all []policy.Violation
	for _, pb := range pbs {
		out, err := plan.Compute(res, abs, model.Repo{ID: "local", Name: filepath.Base(abs)}, plan.Request{
			Playbook: pb, Inventory: *inv, Limit: *limit, Tags: *tags, Vars: vars,
		})
		if err != nil {
			log.Printf("skip %s: %v", pb, err)
			continue
		}
		groups, total := hostGroupsFor(res, out.Inventory)
		vs := policy.Evaluate(policies, out, policy.Options{HostGroups: groups, TotalHosts: total})
		reports = append(reports, policyReport{Playbook: pb, Violations: vs})
		all = append(all, vs...)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(reports)
		if policy.HasError(all) {
			os.Exit(1)
		}
		return
	}
	printPolicyReports(reports, len(policies))
	if policy.HasError(all) {
		os.Exit(1)
	}
}

// hostGroupsFor builds a host→groups map and the host count for the inventory
// the plan resolved to (matched by path or name, else the first one).
func hostGroupsFor(res *model.ScanResult, invRef string) (map[string][]string, int) {
	var chosen *model.Inventory
	for i := range res.Inventories {
		in := &res.Inventories[i]
		if in.Path == invRef || in.Name == invRef {
			chosen = in
			break
		}
	}
	if chosen == nil && len(res.Inventories) > 0 {
		chosen = &res.Inventories[0]
	}
	groups := map[string][]string{}
	if chosen == nil {
		return groups, 0
	}
	for _, h := range chosen.Hosts {
		groups[h.Name] = h.Groups
	}
	return groups, len(chosen.Hosts)
}

// policyReport groups one playbook's violations for output.
type policyReport struct {
	Playbook   string             `json:"playbook"`
	Violations []policy.Violation `json:"violations"`
}

func printPolicyReports(reports []policyReport, nPolicies int) {
	total, errs, warns := 0, 0, 0
	for _, r := range reports {
		for _, v := range r.Violations {
			total++
			if v.Severity == policy.SeverityError {
				errs++
			} else {
				warns++
			}
		}
	}
	fmt.Printf("%sPOLICY%s %d rule(s) over %d playbook(s)\n", cBold, cOff, nPolicies, len(reports))
	if total == 0 {
		fmt.Printf("%s✓ no violations%s\n", cGreen, cOff)
		return
	}
	for _, r := range reports {
		if len(r.Violations) == 0 {
			continue
		}
		fmt.Printf("\n%s%s%s\n", cBold, r.Playbook, cOff)
		for _, v := range r.Violations {
			mark, color := "✗ error", cRed
			if v.Severity == policy.SeverityWarning {
				mark, color = "! warn", cAmber
			}
			fmt.Printf("  %s[%s]%s %s%s%s — %s\n", color, mark, cOff, cBold, v.Policy, cOff, v.Detail)
			loc := ""
			if v.Task != "" {
				loc = "task " + v.Task
			}
			if len(v.Hosts) > 0 {
				h := strings.Join(v.Hosts, ", ")
				if len(v.Hosts) > 6 {
					h = fmt.Sprintf("%s … (%d hosts)", strings.Join(v.Hosts[:6], ", "), len(v.Hosts))
				}
				loc = strings.TrimSpace(loc + "  hosts: " + h)
			}
			if loc != "" {
				fmt.Printf("      %s%s%s\n", cGray, loc, cOff)
			}
			if v.Description != "" {
				fmt.Printf("      %s%s%s\n", cGray, v.Description, cOff)
			}
		}
	}
	fmt.Printf("\n%sSummary:%s %s%d error(s)%s %s%d warning(s)%s\n",
		cBold, cOff, cRed, errs, cOff, cAmber, warns, cOff)
}

// cmdHygiene runs the dead-code + smell + secrets report over a repo and prints
// it (or --json). Exit code 4 when high-severity secrets are found, so CI can
// gate a merge on "no plaintext credentials".
func cmdHygiene(args []string) {
	fs := flag.NewFlagSet("hygiene", flag.ExitOnError)
	var paths scanPathList
	fs.Var(&paths, "paths", "restrict scan to subdir(s) of PATH (repeatable / comma-separated)")
	asJSON := fs.Bool("json", false, "print raw hygiene JSON")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: pine hygiene PATH [--paths SUBDIR] [--json]")
		os.Exit(2)
	}
	root := fs.Arg(0)
	_ = fs.Parse(fs.Args()[1:]) // allow flags after PATH

	res, err := scanner.Scan(root, paths...)
	if err != nil {
		log.Fatal(err)
	}
	abs, _ := filepath.Abs(root)
	out := plan.Hygiene(res, abs)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		os.Exit(hygieneExit(out))
	}

	scoreColor := cGreen
	if out.Score < 80 {
		scoreColor = cAmber
	}
	if out.Score < 50 {
		scoreColor = cRed
	}
	fmt.Printf("%sHYGIENE%s  score %s%d/100%s\n", cBold, cOff, scoreColor, out.Score, cOff)

	section := func(title string, n int, color string) {
		if n > 0 {
			fmt.Printf("\n%s%s%s %s(%d)%s\n", color, title, cOff, cGray, n, cOff)
		}
	}
	section("Secrets", len(out.SecretFindings), cRed)
	for _, f := range out.SecretFindings {
		mark := cRed + "high" + cOff
		if f.Severity != "high" {
			mark = cAmber + f.Severity + cOff
		}
		fmt.Printf("  [%s] %s%s%s in %s — %s\n", mark, cBold, f.Key, cOff, f.File, f.Reason)
	}
	section("Smells", len(out.Smells), cAmber)
	for _, sm := range out.Smells {
		loc := sm.Where
		if sm.Count > 1 {
			loc = fmt.Sprintf("%s (+%d more)", sm.Where, sm.Count-1)
		}
		fmt.Printf("  %s%s%s %s%s%s — %s\n", cAmber, sm.Rule, cOff, cGray, loc, cOff, sm.Detail)
	}
	section("Unused roles", len(out.UnusedRoles), cAmber)
	for _, r := range out.UnusedRoles {
		fmt.Printf("  %s — %s\n", r.Name, r.Reason)
	}
	section("Unnotified handlers", len(out.UnnotifiedHandlers), cAmber)
	for _, h := range out.UnnotifiedHandlers {
		who := h.Name
		if h.Role != "" {
			who = h.Role + " › " + h.Name
		}
		fmt.Printf("  %s\n", who)
	}
	section("Untargeted hosts", len(out.UntargetedHosts), cAmber)
	for _, h := range out.UntargetedHosts {
		fmt.Printf("  %s (%s)\n", h.Name, h.Inventory)
	}
	section("Unused variables", len(out.UnusedVars), cGray)
	for _, v := range out.UnusedVars {
		fmt.Printf("  %s %s(%s)%s\n", v.Key, cGray, v.DefinedIn, cOff)
	}
	if out.VaultFiles > 0 {
		fmt.Printf("\n%s%d vault-encrypted file(s)%s\n", cGreen, out.VaultFiles, cOff)
	}
	os.Exit(hygieneExit(out))
}

// hygieneExit returns 4 when any high-severity secret was found (CI gate),
// else 0.
func hygieneExit(out *plan.HygieneResult) int {
	for _, f := range out.SecretFindings {
		if f.Severity == "high" {
			return 4
		}
	}
	return 0
}
