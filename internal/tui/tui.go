// Package tui implements Pine's terminal user interface (bubbletea).
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/runner"
)

// Version is shown in the TUI brand row; the CLI sets it at startup.
var Version = "dev"

// gitSHA is the short commit hash the Go toolchain embeds at build time
// (a "*" suffix marks a dirty working tree). Empty when built without VCS info.
var gitSHA = readGitSHA()

func readGitSHA() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "*"
			}
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		return ""
	}
	return rev + dirty
}

// Run starts the TUI on top of an in-process manager (pine tui). focusRepoID,
// when set, selects that repo once repos load (e.g. a path passed on the CLI).
func Run(mgr *runner.Manager, focusRepoID string) error {
	return RunEngine(NewLocalEngine(mgr), focusRepoID)
}

// RunEngine starts the TUI on top of any Engine — an in-process manager or an
// HTTP client attached to a running daemon (pine attach).
func RunEngine(eng Engine, focusRepoID string) error {
	m := newApp(eng)
	m.wantRepoID = focusRepoID
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

const (
	tabRepos = iota
	tabPlaybooks
	tabRoles
	tabInventory
	tabJobs
	tabCount
)

var tabNames = []string{"Repos", "Playbooks", "Roles", "Inventory", "Jobs"}

// Palette adapts to the terminal background: light terminals get an
// Atom One Light variant (tuned for contrast on #fafafa), dark terminals
// keep the original bright-on-dark scheme.
var (
	cAccent = lipgloss.AdaptiveColor{Light: "#3f8e3f", Dark: "#4ade80"} // green
	cCyan   = lipgloss.AdaptiveColor{Light: "#0184bc", Dark: "#22d3ee"} // cyan / blue accent
	cMuted  = lipgloss.AdaptiveColor{Light: "#696c77", Dark: "#8aa396"} // secondary text
	cDanger = lipgloss.AdaptiveColor{Light: "#d52a1f", Dark: "#f87171"} // red
	cWarn   = lipgloss.AdaptiveColor{Light: "#986801", Dark: "#fbbf24"} // amber / orange
	cText   = lipgloss.AdaptiveColor{Light: "#2b2f2c", Dark: "#e7efe9"} // primary text
	cTabFg  = lipgloss.AdaptiveColor{Light: "#fafafa", Dark: "#08130c"} // text on the active tab / selection
	cBorder = lipgloss.AdaptiveColor{Light: "#c2c4c9", Dark: "#2c3f35"} // inactive pane border
	cFaint  = lipgloss.AdaptiveColor{Light: "#9aa0a6", Dark: "#5c7064"} // very dim hint text

	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sBrand   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTabOn   = lipgloss.NewStyle().Bold(true).Foreground(cTabFg).Background(cAccent).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(cMuted).Padding(0, 1)
	sSel     = lipgloss.NewStyle().Bold(true).Foreground(cTabFg).Background(cAccent)
	sDim     = lipgloss.NewStyle().Foreground(cMuted)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sCyan    = lipgloss.NewStyle().Foreground(cCyan)
	sErr     = lipgloss.NewStyle().Foreground(cDanger)
	sWarn    = lipgloss.NewStyle().Foreground(cWarn)
	sKey     = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).Padding(0, 1)
	sLogPlay = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sLogTask = lipgloss.NewStyle().Foreground(cCyan)
)

type app struct {
	eng    Engine
	width  int
	height int

	tab        int
	cursor     map[int]int
	repoIX     int    // selected repo index (drives playbooks/roles/inventory)
	wantRepoID string // repo to focus on first load (from a CLI path arg)

	repos []model.Repo
	scan  *model.ScanResult
	jobs  []model.Job

	mode      string // list | detail | log | confirm-run | flow
	detail    string
	scroll    int
	status    string
	runCheck  bool
	runTarget string // playbook path pending confirmation

	// flow: navigable block-chain view of a playbook
	flowPlaybook model.Playbook
	flowCur      int  // index into flowNodes() of the selected step
	flowOpen     bool // detail pane toggled open

	// inventory tab: expandable group tree with host leaves
	collapsed    map[string]bool // node key -> collapsed (default expanded)
	invFilter    string          // active substring filter
	invFiltering bool            // true while editing the filter string

	// playbooks tab: directory treeview with its own substring filter
	pbFilter    string
	pbFiltering bool

	// live job log
	logJob   string
	logLines []string
	logCh    chan string

	// auto-refresh: re-sync connected repos on load and periodically so
	// external edits surface without a manual sync, and announce completions.
	lastSynced map[string]string // repoID -> last seen LastSynced (change detector)
	syncTicks  int               // ticks since the last periodic auto-sync
}

func newApp(eng Engine) *app {
	return &app{eng: eng, cursor: map[int]int{}, mode: "list", collapsed: map[string]bool{}}
}

type tickMsg time.Time
type logLineMsg struct {
	line string
	ok   bool
}
type sshDoneMsg struct {
	host string
	err  error
}

func tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func waitLine(ch chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		return logLineMsg{line, ok}
	}
}

func (a *app) Init() tea.Cmd {
	a.reload()
	a.triggerSync(true) // auto-refresh on load so we show the latest, and say so
	return tick()
}

func (a *app) reload() {
	prev := a.lastSynced
	a.repos = a.eng.ListRepos()
	a.jobs = a.eng.ListJobs()
	if a.wantRepoID != "" {
		for i, r := range a.repos {
			if r.ID == a.wantRepoID {
				a.repoIX = i
				break
			}
		}
		a.wantRepoID = ""
	}
	if a.repoIX >= len(a.repos) {
		a.repoIX = 0
	}

	// Detect repos whose sync finished since the last reload (LastSynced moved
	// and they're back to ready) so we can announce the refresh. On the first
	// reload prev is nil, so we only record a baseline — no spurious notice.
	next := make(map[string]string, len(a.repos))
	var refreshed []model.Repo
	for _, r := range a.repos {
		next[r.ID] = r.LastSynced
		if old, seen := prev[r.ID]; seen && r.Status == model.RepoReady && r.LastSynced != "" && r.LastSynced != old {
			refreshed = append(refreshed, r)
		}
	}
	a.lastSynced = next

	a.scan = nil
	if len(a.repos) > 0 {
		if res, err := a.eng.Scan(a.repos[a.repoIX].ID); err == nil {
			a.scan = res
		}
	}
	a.clampCursor()
	if len(refreshed) > 0 {
		a.notifyRefreshed(refreshed)
	}
}

// triggerSync asks the engine to re-sync (and thus rescan) every connected
// repo. When announce is set it shows a "refreshing…" notice; completions are
// announced later by reload via notifyRefreshed.
func (a *app) triggerSync(announce bool) {
	started := 0
	for _, r := range a.repos {
		if _, err := a.eng.SyncRepo(r.ID); err == nil {
			started++
		}
	}
	if !announce || started == 0 {
		return
	}
	if len(a.repos) == 1 {
		a.status = "↻ refreshing " + a.repos[0].Name + "…"
	} else {
		a.status = fmt.Sprintf("↻ refreshing %d repos…", started)
	}
}

// notifyRefreshed posts a status notice summarizing what a finished sync found.
func (a *app) notifyRefreshed(repos []model.Repo) {
	if len(repos) == 1 {
		s := repos[0].Summary
		a.status = fmt.Sprintf("✓ %s refreshed · %d playbooks · %d roles · %d hosts",
			repos[0].Name, s.Playbooks, s.Roles, s.Hosts)
		return
	}
	a.status = fmt.Sprintf("✓ %d repos refreshed", len(repos))
}

func (a *app) listLen() int {
	switch a.tab {
	case tabRepos:
		return len(a.repos)
	case tabPlaybooks:
		return len(a.pbTree())
	case tabRoles:
		if a.scan != nil {
			return len(a.scan.Roles)
		}
	case tabInventory:
		return len(a.invTree())
	case tabJobs:
		return len(a.jobs)
	}
	return 0
}

// activeFilter returns the filter text/editing flags for the current tab, or
// (nil, nil) when the tab has no substring filter.
func (a *app) activeFilter() (*string, *bool) {
	switch a.tab {
	case tabInventory:
		return &a.invFilter, &a.invFiltering
	case tabPlaybooks:
		return &a.pbFilter, &a.pbFiltering
	}
	return nil, nil
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case tickMsg:
		if a.mode != "log" {
			// Periodically re-sync (~every 60s) so external repo edits keep
			// surfacing while the UI is open; reload announces what changed.
			a.syncTicks++
			if a.syncTicks >= 30 {
				a.syncTicks = 0
				a.triggerSync(false)
			}
			a.reload()
		}
		return a, tick()

	case logLineMsg:
		if !msg.ok {
			a.logCh = nil
			a.status = "job finished - press esc to go back"
			a.reload()
			return a, nil
		}
		a.logLines = append(a.logLines, msg.line)
		return a, waitLine(a.logCh)

	case sshDoneMsg:
		if msg.err != nil {
			a.status = "ssh " + msg.host + " failed: " + msg.err.Error()
		} else {
			a.status = "ssh session to " + msg.host + " ended"
		}
		return a, nil

	case tea.KeyMsg:
		return a.key(msg)
	}
	return a, nil
}

func (a *app) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return a, tea.Quit
	}

	// While editing a list filter, capture printable keys instead of
	// treating them as navigation/quit shortcuts.
	if fp, ffp := a.activeFilter(); ffp != nil && *ffp && a.mode == "list" {
		switch k {
		case "esc":
			*ffp, *fp = false, ""
		case "enter":
			*ffp = false
		case "backspace":
			if n := len(*fp); n > 0 {
				*fp = (*fp)[:n-1]
			}
		case "space":
			*fp += " "
		default:
			if len(k) == 1 && k[0] >= 0x20 && k[0] < 0x7f {
				*fp += k
			}
		}
		a.clampCursor()
		return a, nil
	}

	if k == "q" && a.mode == "list" {
		return a, tea.Quit
	}

	if a.mode == "confirm-run" {
		switch k {
		case "y", "enter":
			return a.launchJob()
		case "c":
			a.runCheck = !a.runCheck
		case "n", "esc":
			a.mode = "list"
			a.status = ""
		}
		return a, nil
	}

	if a.mode == "detail" || a.mode == "log" {
		switch k {
		case "esc", "q":
			a.mode = "list"
			a.scroll = 0
			a.status = ""
		case "up", "k":
			a.scroll = max(0, a.scroll-1)
		case "down", "j":
			a.scroll++
		case "pgup":
			a.scroll = max(0, a.scroll-10)
		case "pgdown":
			a.scroll += 10
		case "G":
			a.scroll = 1 << 30
		}
		return a, nil
	}

	if a.mode == "flow" {
		switch k {
		case "esc", "q":
			a.mode = "list"
			a.status = ""
		case "up", "k":
			a.flowMove(-1)
		case "down", "j":
			a.flowMove(1)
		case "enter", "tab", "i":
			a.flowOpen = !a.flowOpen
		case "right", "l":
			a.flowOpen = true
		case "left", "h":
			a.flowOpen = false
		case "g", "home":
			a.flowCur = 0
			a.flowSnap(1)
		case "G", "end":
			a.flowCur = 1 << 30
			a.flowSnap(-1)
		case "r":
			a.runTarget = a.flowPlaybook.Path
			a.mode = "confirm-run"
		}
		return a, nil
	}

	switch k {
	case "tab", "right", "l":
		a.tab = (a.tab + 1) % tabCount
	case "shift+tab", "left", "h":
		a.tab = (a.tab + tabCount - 1) % tabCount
	case "1", "2", "3", "4", "5":
		a.tab = int(k[0] - '1')
	case "up", "k":
		a.cursor[a.tab] = max(0, a.cursor[a.tab]-1)
	case "down", "j":
		a.cursor[a.tab] = min(a.listLen()-1, a.cursor[a.tab]+1)
	case "enter":
		if cmd := a.open(); cmd != nil {
			return a, cmd
		}
	case "s":
		switch a.tab {
		case tabRepos:
			if a.cursor[a.tab] < len(a.repos) {
				_, _ = a.eng.SyncRepo(a.repos[a.cursor[a.tab]].ID)
				a.status = "sync started"
			}
		case tabInventory:
			return a.sshSelected()
		}
	case "/":
		if _, ffp := a.activeFilter(); ffp != nil {
			*ffp = true
		}
	case "r":
		if a.tab == tabPlaybooks {
			if row, ok := a.pbSelected(); ok && row.kind == pbLeaf {
				a.runTarget = a.scan.Playbooks[row.pbIX].Path
				a.mode = "confirm-run"
			}
		}
	case "p":
		if a.tab == tabPlaybooks && a.scan != nil && a.cursor[a.tab] < len(a.scan.Playbooks) {
			a.showPlan(a.scan.Playbooks[a.cursor[a.tab]].Path)
		}
	}
	if a.cursor[a.tab] < 0 {
		a.cursor[a.tab] = 0
	}
	return a, nil
}

func (a *app) launchJob() (tea.Model, tea.Cmd) {
	if len(a.repos) == 0 {
		a.mode = "list"
		return a, nil
	}
	repo := a.repos[a.repoIX]
	inv := ""
	if a.scan != nil && len(a.scan.Inventories) > 0 {
		inv = a.scan.Inventories[0].Path
	}
	job, err := a.eng.StartJob(model.Job{
		RepoID: repo.ID, Playbook: a.runTarget, Inventory: inv, Check: a.runCheck,
	})
	if err != nil {
		a.status = "error: " + err.Error()
		a.mode = "list"
		return a, nil
	}
	return a.followJob(job.ID)
}

func (a *app) followJob(id string) (tea.Model, tea.Cmd) {
	a.mode = "log"
	a.logJob = id
	a.logLines = nil
	a.scroll = 1 << 30
	ch, live := a.eng.Subscribe(id)
	if live {
		a.logCh = ch
		return a, waitLine(ch)
	}
	// finished job: read the stored log
	if data, err := a.eng.JobLog(id); err == nil {
		a.logLines = strings.Split(strings.TrimRight(data, "\n"), "\n")
	}
	return a, nil
}

func (a *app) open() tea.Cmd {
	switch a.tab {
	case tabRepos:
		if i := a.cursor[a.tab]; i < len(a.repos) {
			a.repoIX = i
			a.reload()
			a.tab = tabPlaybooks
		}
	case tabPlaybooks:
		tree := a.pbTree()
		if i := a.cursor[a.tab]; i >= 0 && i < len(tree) {
			row := tree[i]
			if row.kind == pbDir {
				a.collapsed[row.key] = !a.collapsed[row.key]
				a.clampCursor()
			} else {
				a.flowPlaybook = a.scan.Playbooks[row.pbIX]
				a.flowCur, a.flowOpen, a.mode = 0, true, "flow" // open the detail preview by default
				a.flowSnap(1)
			}
		}
	case tabRoles:
		if a.scan != nil && a.cursor[a.tab] < len(a.scan.Roles) {
			a.detail = renderRole(a.scan.Roles[a.cursor[a.tab]])
			a.mode, a.scroll = "detail", 0
		}
	case tabInventory:
		tree := a.invTree()
		if i := a.cursor[a.tab]; i >= 0 && i < len(tree) {
			r := tree[i]
			switch r.kind {
			case rowInv, rowGroup:
				a.collapsed[r.key] = !a.collapsed[r.key]
				a.clampCursor()
			case rowHost:
				a.detail = renderHostDetail(r.inv, r.host)
				a.mode, a.scroll = "detail", 0
			}
		}
	case tabJobs:
		if i := a.cursor[a.tab]; i < len(a.jobs) {
			_, cmd := a.followJob(a.jobs[i].ID)
			return cmd
		}
	}
	return nil
}

// --- view ---

func (a *app) View() string {
	if a.width == 0 {
		return "loading..."
	}
	header := a.header()
	footer := a.footer()
	bodyH := a.height - 4 // header + blank + body + blank + footer
	if bodyH < 3 {
		bodyH = 3
	}

	var body string
	switch a.mode {
	case "detail", "log":
		body = a.viewOverlay(bodyH)
	case "flow":
		body = a.viewFlow(bodyH)
	case "confirm-run":
		body = lipgloss.Place(a.width, bodyH, lipgloss.Center, lipgloss.Center, a.confirmBox())
	default:
		body = a.viewPanes(bodyH)
	}
	return header + "\n\n" + body + "\n\n" + footer
}

// header renders the brand row: 🌲 Pine vX · pill tabs · ? help / q quit.
func (a *app) header() string {
	brand := sBrand.Render("🌲 Pine") + " " + sFaint.Render("v"+Version)
	if gitSHA != "" {
		brand += sFaint.Render(" · " + gitSHA)
	}
	tabs := make([]string, tabCount)
	for i, n := range tabNames {
		if i == a.tab {
			tabs[i] = sTabOn.Render(n)
		} else {
			tabs[i] = sTabOff.Render(n)
		}
	}
	left := brand + "   " + lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	right := sFaint.Render("? help · q quit")
	return truncTo(rowBetween(left, right, a.width), a.width)
}

// footer renders the status bar: contextual keybindings · repo sync state.
func (a *app) footer() string {
	left := a.keyHelp()
	if a.status != "" {
		left = sWarn.Render(a.status)
	}
	return truncTo(rowBetween(left, a.repoStatus(), a.width), a.width)
}

func (a *app) keyHelp() string {
	if a.mode == "detail" || a.mode == "log" {
		return helpLine([2]string{"↑/↓", "scroll"}, [2]string{"G", "end"}, [2]string{"esc", "back"})
	}
	if a.mode == "flow" {
		toggle := "show detail"
		if a.flowOpen {
			toggle = "hide detail"
		}
		return helpLine([2]string{"↑/↓", "step"}, [2]string{"enter", toggle}, [2]string{"r", "run"}, [2]string{"esc", "back"})
	}
	if a.mode == "confirm-run" {
		return helpLine([2]string{"y", "launch"}, [2]string{"c", "--check"}, [2]string{"n", "cancel"})
	}
	if _, ffp := a.activeFilter(); ffp != nil && *ffp {
		return helpLine([2]string{"type", "filter"}, [2]string{"enter", "apply"}, [2]string{"esc", "clear"})
	}
	pairs := []([2]string){{"↑/↓", "move"}}
	switch a.tab {
	case tabRepos:
		pairs = append(pairs, [2]string{"enter", "select"}, [2]string{"s", "sync"})
	case tabPlaybooks:
		pairs = append(pairs, [2]string{"enter", "open"}, [2]string{"r", "run"}, [2]string{"/", "filter"})
	case tabRoles:
		pairs = append(pairs, [2]string{"enter", "open"})
	case tabInventory:
		pairs = append(pairs, [2]string{"enter", "expand"}, [2]string{"s", "ssh"}, [2]string{"/", "filter"})
	case tabJobs:
		pairs = append(pairs, [2]string{"enter", "logs"})
	}
	pairs = append(pairs, [2]string{"tab", "switch"}, [2]string{"q", "quit"})
	return helpLine(pairs...)
}

func (a *app) repoStatus() string {
	if len(a.repos) == 0 {
		return sFaint.Render("no repo")
	}
	r := a.repos[a.repoIX]
	mark := sLogPlay.Render("✓ synced")
	switch r.Status {
	case model.RepoError:
		mark = sErr.Render("✗ error")
	case model.RepoSyncing:
		mark = sWarn.Render("● syncing")
	}
	return sDim.Render("repo: ") + sCyan.Render(r.Name) + " " + mark
}

// viewPanes lays out the focused list (left) beside a live detail preview
// (right). On very narrow terminals it collapses to the list alone.
func (a *app) viewPanes(h int) string {
	leftW := a.width / 3
	if leftW < 26 {
		leftW = 26
	}
	if leftW > 42 {
		leftW = 42
	}
	rightW := a.width - leftW - 1
	if rightW < 24 {
		return a.leftPane(a.width, h)
	}
	left := a.leftPane(leftW, h)
	right := a.rightPane(rightW, h)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (a *app) leftPane(totalW, totalH int) string {
	contentW := totalW - 4
	if contentW < 4 {
		contentW = 4
	}
	innerH := totalH - 2
	labels := a.listLabels()
	cur := a.cursor[a.tab]

	var lines []string
	rows := innerH
	if fp, ffp := a.activeFilter(); fp != nil && (*ffp || *fp != "") {
		caret := ""
		if *ffp {
			caret = "▏"
		}
		lines = append(lines, sFaint.Render("filter ")+sCyan.Render(*fp+caret))
		rows--
	}
	if len(labels) == 0 {
		lines = append(lines, sFaint.Render(a.emptyHint()))
	} else {
		start := 0
		if cur >= rows {
			start = cur - rows + 1
		}
		for i := start; i < len(labels) && i-start < rows; i++ {
			if i == cur {
				bar := truncTo("▸ "+stripANSI(labels[i]), contentW)
				lines = append(lines, sSel.Width(contentW).Render(bar))
			} else {
				lines = append(lines, "  "+labels[i])
			}
		}
	}
	return a.box(a.paneTitle(), strings.Join(lines, "\n"), totalW, totalH, true)
}

func (a *app) rightPane(totalW, totalH int) string {
	title, body := a.listDetail()
	return a.box(title, body, totalW, totalH, false)
}

func (a *app) paneTitle() string {
	repo := "no repo"
	if len(a.repos) > 0 {
		repo = a.repos[a.repoIX].Name
	}
	return repo + " · " + strings.ToLower(tabNames[a.tab])
}

func (a *app) emptyHint() string {
	if fp, _ := a.activeFilter(); fp != nil && *fp != "" {
		return "no matches"
	}
	if a.tab == tabRepos {
		return "no repos — add one, or run with --demo"
	}
	return "nothing here yet"
}

func (a *app) viewOverlay(h int) string {
	title := "details"
	if a.mode == "log" {
		title = "job · " + a.logJob
	}
	return a.box(title, a.viewScrollable(h-2), a.width, h, true)
}

func (a *app) confirmBox() string {
	check := sDim.Render("off")
	if a.runCheck {
		check = sWarn.Render("on (--check)")
	}
	repo := ""
	if len(a.repos) > 0 {
		repo = a.repos[a.repoIX].Name
	}
	body := sCyan.Render("playbook") + "  " + a.runTarget + "\n" +
		sCyan.Render("repo") + "      " + repo + "\n" +
		sCyan.Render("check") + "     " + check + "\n\n" +
		sKey.Render("y") + sDim.Render(" launch  ") + sKey.Render("c") + sDim.Render(" toggle check  ") + sKey.Render("n") + sDim.Render(" cancel")
	return sBox.BorderForeground(cAccent).Render(sTitle.Render("Run playbook") + "\n\n" + body)
}

// --- compact list labels (left pane) ---

func (a *app) listLabels() []string {
	var out []string
	switch a.tab {
	case tabRepos:
		for _, r := range a.repos {
			out = append(out, repoMark(r.Status)+" "+r.Name)
		}
	case tabPlaybooks:
		for _, r := range a.pbTree() {
			out = append(out, a.pbRowString(r))
		}
	case tabRoles:
		if a.scan != nil {
			for _, r := range a.scan.Roles {
				out = append(out, r.Name)
			}
		}
	case tabInventory:
		for _, r := range a.invTree() {
			out = append(out, a.invRowString(r))
		}
	case tabJobs:
		for _, j := range a.jobs {
			out = append(out, jobMark(j.Status)+" "+filepath.Base(j.Playbook))
		}
	}
	return out
}

func repoMark(s string) string {
	switch s {
	case model.RepoReady:
		return sLogPlay.Render("✓")
	case model.RepoError:
		return sErr.Render("✗")
	case model.RepoSyncing:
		return sWarn.Render("●")
	}
	return sFaint.Render("·")
}

func jobMark(s string) string {
	switch s {
	case model.JobSuccess:
		return sLogPlay.Render("✓")
	case model.JobFailed:
		return sErr.Render("✗")
	case model.JobRunning:
		return sWarn.Render("●")
	}
	return sFaint.Render("·")
}

// --- live detail preview (right pane) ---

func (a *app) listDetail() (string, string) {
	cur := a.cursor[a.tab]
	switch a.tab {
	case tabRepos:
		if cur >= 0 && cur < len(a.repos) {
			return a.repos[cur].Name, renderRepoDetail(a.repos[cur])
		}
	case tabPlaybooks:
		tree := a.pbTree()
		if cur >= 0 && cur < len(tree) {
			if row := tree[cur]; row.kind == pbLeaf {
				p := a.scan.Playbooks[row.pbIX]
				return filepath.Base(p.Path), renderPlaybookSummary(p)
			} else {
				return row.name + "/", a.pbDirDetail(row)
			}
		}
	case tabRoles:
		if a.scan != nil && cur >= 0 && cur < len(a.scan.Roles) {
			r := a.scan.Roles[cur]
			return r.Name, renderRole(r)
		}
	case tabInventory:
		tree := a.invTree()
		if cur >= 0 && cur < len(tree) {
			return a.invDetail(tree[cur])
		}
	case tabJobs:
		if cur >= 0 && cur < len(a.jobs) {
			j := a.jobs[cur]
			return filepath.Base(j.Playbook), a.renderJobDetail(j)
		}
	}
	return "details", sFaint.Render("nothing selected")
}

func renderRepoDetail(r model.Repo) string {
	src := r.URL
	if src == "" {
		src = r.Path
	}
	status := sLogPlay.Render("ready")
	switch r.Status {
	case model.RepoError:
		status = sErr.Render("error")
	case model.RepoSyncing:
		status = sWarn.Render("syncing")
	}
	var b strings.Builder
	b.WriteString(sCyan.Render("source") + "  " + src + "\n")
	if r.Branch != "" {
		b.WriteString(sCyan.Render("branch") + "  " + r.Branch + "\n")
	}
	b.WriteString(sCyan.Render("status") + "  " + status + "\n")
	if r.LastSynced != "" {
		b.WriteString(sCyan.Render("synced") + "  " + sDim.Render(r.LastSynced) + "\n")
	}
	if r.Error != "" {
		b.WriteString("\n" + sErr.Render(r.Error) + "\n")
	}
	b.WriteString("\n" + sTitle.Render("contents") + "\n")
	b.WriteString(fmt.Sprintf("  %s playbooks\n  %s roles\n  %s inventories\n  %s hosts\n  %s groups\n",
		sLogPlay.Render(fmt.Sprintf("%3d", r.Summary.Playbooks)),
		sLogPlay.Render(fmt.Sprintf("%3d", r.Summary.Roles)),
		sLogPlay.Render(fmt.Sprintf("%3d", r.Summary.Inventories)),
		sLogPlay.Render(fmt.Sprintf("%3d", r.Summary.Hosts)),
		sLogPlay.Render(fmt.Sprintf("%3d", r.Summary.Groups))))
	return b.String()
}

func (a *app) renderJobDetail(j model.Job) string {
	status := sLogPlay.Render("✓ success")
	switch j.Status {
	case model.JobFailed:
		status = sErr.Render("✗ failed")
	case model.JobRunning:
		status = sWarn.Render("● running")
	}
	var b strings.Builder
	b.WriteString(sCyan.Render("playbook") + "  " + j.Playbook + "\n")
	b.WriteString(sCyan.Render("status") + "    " + status)
	if j.Simulated {
		b.WriteString(sFaint.Render("  (simulated)"))
	}
	b.WriteString("\n")
	b.WriteString(sCyan.Render("created") + "   " + sDim.Render(j.Created) + "\n")
	if j.DurationMS > 0 {
		b.WriteString(sCyan.Render("duration") + "  " + sDim.Render((time.Duration(j.DurationMS) * time.Millisecond).String()) + "\n")
	}
	s := j.Summary
	b.WriteString("\n" + sTitle.Render("recap") + "  " +
		sLogPlay.Render(fmt.Sprintf("ok=%d", s.OK)) + " " +
		sWarn.Render(fmt.Sprintf("changed=%d", s.Changed)) + " " +
		sErr.Render(fmt.Sprintf("failed=%d", s.Failed)) + " " +
		sDim.Render(fmt.Sprintf("skipped=%d unreachable=%d", s.Skipped, s.Unreachable)) + "\n\n")

	if data, err := a.eng.JobLog(j.ID); err == nil {
		lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
		if n := len(lines); n > 18 {
			lines = lines[n-18:]
			b.WriteString(sFaint.Render("… "+fmt.Sprint(n-18)+" earlier lines") + "\n")
		}
		for _, l := range lines {
			b.WriteString(colorLogLine(l) + "\n")
		}
	} else {
		b.WriteString(sFaint.Render("enter to stream full log"))
	}
	return b.String()
}

// invDetail describes the highlighted inventory row in the preview pane.
func (a *app) invDetail(r invRow) (string, string) {
	switch r.kind {
	case rowHost:
		return r.host.Name, renderHostDetail(r.inv, r.host)
	case rowGroup:
		var b strings.Builder
		b.WriteString(sCyan.Render("group") + "  " + r.group.Name + "\n")
		b.WriteString(sDim.Render(groupMeta(r.group)) + "\n")
		if len(r.group.Children) > 0 {
			b.WriteString("\n" + sTitle.Render("children") + "\n  " + sCyan.Render(strings.Join(r.group.Children, ", ")) + "\n")
		}
		if len(r.group.Hosts) > 0 {
			b.WriteString("\n" + sTitle.Render("hosts") + "\n  " + strings.Join(r.group.Hosts, ", ") + "\n")
		}
		if len(r.group.Vars) > 0 {
			b.WriteString("\n" + sTitle.Render("vars") + "\n")
			keys := make([]string, 0, len(r.group.Vars))
			for k := range r.group.Vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString(fmt.Sprintf("  %s: %v\n", sCyan.Render(k), r.group.Vars[k]))
			}
		}
		return r.group.Name, b.String()
	default:
		var b strings.Builder
		b.WriteString(sCyan.Render("inventory") + "  " + r.inv.Name + "\n")
		b.WriteString(sCyan.Render("format") + "     " + r.inv.Format + "\n")
		b.WriteString(fmt.Sprintf("%s     %s groups · %s hosts\n", sCyan.Render("size"),
			sLogPlay.Render(fmt.Sprint(len(r.inv.Groups))), sLogPlay.Render(fmt.Sprint(len(r.inv.Hosts)))))
		return r.inv.Name, b.String()
	}
}

// --- low-level layout helpers ---

// box draws a rounded panel of exactly totalW×totalH cells with the title
// embedded in the top border. Active panels use the accent border colour.
func (a *app) box(title, body string, totalW, totalH int, active bool) string {
	bc := cBorder
	ts := sFaint
	if active {
		bc = cAccent
		ts = sTitle
	}
	bs := lipgloss.NewStyle().Foreground(bc)
	innerW := totalW - 2
	if innerW < 4 {
		innerW = 4
	}
	innerH := totalH - 2
	if innerH < 1 {
		innerH = 1
	}
	contentW := innerW - 2 // one space padding on each side

	// Leave room for the leading "─" and the space on each side of the title.
	label := ts.Render(" " + truncTo(title, innerW-3) + " ")
	dashes := innerW - 1 - lipgloss.Width(label)
	if dashes < 0 {
		dashes = 0
	}
	top := bs.Render("╭─") + label + bs.Render(strings.Repeat("─", dashes)+"╮")

	src := strings.Split(body, "\n")
	out := []string{top}
	for i := 0; i < innerH; i++ {
		line := ""
		if i < len(src) {
			line = src[i]
		}
		out = append(out, bs.Render("│")+" "+padTo(line, contentW)+" "+bs.Render("│"))
	}
	out = append(out, bs.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(out, "\n")
}

// rowBetween places left and right text on one line, filling the gap so right
// is flush against the given width.
func rowBetween(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// helpLine renders "key label · key label" with bold key caps.
func helpLine(pairs ...[2]string) string {
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = sKey.Render(p[0]) + " " + sDim.Render(p[1])
	}
	return strings.Join(parts, sFaint.Render(" · "))
}

// truncTo cuts an (ANSI-aware) string to at most w visible cells.
func truncTo(s string, w int) string {
	if w < 0 {
		w = 0
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// padTo truncates then right-pads a string to exactly w visible cells.
func padTo(s string, w int) string {
	s = truncTo(s, w)
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func (a *app) viewScrollable(h int) string {
	var lines []string
	if a.mode == "log" {
		for _, l := range a.logLines {
			lines = append(lines, colorLogLine(l))
		}
	} else {
		lines = strings.Split(a.detail, "\n")
	}
	maxScroll := max(0, len(lines)-h)
	if a.scroll > maxScroll {
		a.scroll = maxScroll
	}
	end := min(len(lines), a.scroll+h)
	return strings.Join(lines[a.scroll:end], "\n")
}

func colorLogLine(l string) string {
	switch {
	case strings.HasPrefix(l, "PLAY RECAP"), strings.HasPrefix(l, "PLAY ["):
		return sLogPlay.Render(l)
	case strings.HasPrefix(l, "TASK ["), strings.HasPrefix(l, "RUNNING HANDLER"):
		return sLogTask.Render(l)
	case strings.HasPrefix(l, "ok:"):
		return lipgloss.NewStyle().Foreground(cAccent).Render(l)
	case strings.HasPrefix(l, "changed:"):
		return sWarn.Render(l)
	case strings.HasPrefix(l, "failed:"), strings.HasPrefix(l, "fatal:"), strings.HasPrefix(l, "ERROR"):
		return sErr.Render(l)
	case strings.HasPrefix(l, "skipping:"):
		return sDim.Render(l)
	}
	return l
}

// --- detail renderers ---

func renderTask(t model.Task, indent string) string {
	flags := []string{}
	if t.When != "" {
		flags = append(flags, "when")
	}
	if t.Loop {
		flags = append(flags, "loop")
	}
	if len(t.Notify) > 0 {
		flags = append(flags, "notify:"+strings.Join(t.Notify, ","))
	}
	if len(t.Tags) > 0 {
		flags = append(flags, "tags:"+strings.Join(t.Tags, ","))
	}
	extra := ""
	if len(flags) > 0 {
		extra = sDim.Render("  [" + strings.Join(flags, " ") + "]")
	}
	line := fmt.Sprintf("%s- %s  %s%s", indent, t.Name, sCyan.Render(t.Module), extra)
	if t.Module == "block" {
		var b strings.Builder
		b.WriteString(line)
		for _, st := range t.Block {
			b.WriteString("\n" + renderTask(st, indent+"    "))
		}
		for _, st := range t.Rescue {
			b.WriteString("\n" + indent + "  " + sErr.Render("rescue:"))
			b.WriteString("\n" + renderTask(st, indent+"    "))
		}
		return b.String()
	}
	return line
}

func renderPlaybook(p model.Playbook) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(p.Path) + "\n")
	for _, play := range p.Plays {
		if play.Import != "" {
			b.WriteString("\n" + sDim.Render("import_playbook: ") + sCyan.Render(play.Import) + "\n")
			continue
		}
		b.WriteString("\n" + sTitle.Render("PLAY "+play.Name) + sDim.Render("  hosts: ") + sCyan.Render(play.Hosts))
		if play.Serial != "" {
			b.WriteString(sWarn.Render("  serial: " + play.Serial))
		}
		if play.Become {
			b.WriteString(sDim.Render("  become"))
		}
		b.WriteString("\n")
		if len(play.Roles) > 0 {
			b.WriteString("  roles: " + sCyan.Render(strings.Join(play.Roles, ", ")) + "\n")
		}
		sections := []struct {
			label string
			list  []model.Task
		}{
			{"pre_tasks", play.PreTasks}, {"tasks", play.Tasks},
			{"post_tasks", play.PostTasks}, {"handlers", play.Handlers},
		}
		for _, sec := range sections {
			label, list := sec.label, sec.list
			if len(list) == 0 {
				continue
			}
			b.WriteString("  " + sDim.Render(label+":") + "\n")
			for _, t := range list {
				b.WriteString(renderTask(t, "    ") + "\n")
			}
		}
	}
	return b.String()
}

func renderRole(r model.Role) string {
	var b strings.Builder
	b.WriteString(sTitle.Render("role "+r.Name) + "\n")
	if r.Description != "" {
		b.WriteString(sDim.Render(r.Description) + "\n")
	}
	if len(r.Dependencies) > 0 {
		b.WriteString("dependencies: " + sCyan.Render(strings.Join(r.Dependencies, ", ")) + "\n")
	}
	b.WriteString("\n" + sDim.Render("tasks:") + "\n")
	for _, t := range r.Tasks {
		b.WriteString(renderTask(t, "  ") + "\n")
	}
	if len(r.Handlers) > 0 {
		b.WriteString("\n" + sDim.Render("handlers:") + "\n")
		for _, t := range r.Handlers {
			b.WriteString(renderTask(t, "  ") + "\n")
		}
	}
	if len(r.Defaults) > 0 {
		b.WriteString("\n" + sDim.Render("defaults:") + "\n")
		keys := make([]string, 0, len(r.Defaults))
		for k := range r.Defaults {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("  %s: %v\n", sCyan.Render(k), r.Defaults[k]))
		}
	}
	return b.String()
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

// showPlan computes an estimated plan for the selected playbook and shows
// it in the detail pane.
func (a *app) showPlan(playbook string) {
	if len(a.repos) == 0 {
		return
	}
	repo := a.repos[a.repoIX]
	out, err := a.eng.Plan(repo, playbook)
	if err != nil {
		a.status = "plan failed: " + err.Error()
		return
	}
	a.detail = renderPlan(out)
	a.mode, a.scroll = "detail", 0
}

func renderPlan(out *plan.Result) string {
	var b strings.Builder
	b.WriteString(sTitle.Render("PLAN "+out.Playbook) + sDim.Render("  inventory: "+out.Inventory+"  mode: "+out.Mode) + "\n")
	for _, pp := range out.Plays {
		if pp.Import != "" {
			b.WriteString("\n" + sDim.Render("→ imports "+pp.Import) + "\n")
			continue
		}
		b.WriteString("\n" + sTitle.Render("PLAY "+pp.Name) +
			sDim.Render(fmt.Sprintf("  hosts: %s  matched: %d", pp.Hosts, len(pp.MatchedHosts))))
		if len(pp.Batches) > 1 {
			b.WriteString(sWarn.Render(fmt.Sprintf("  serial: %d batches", len(pp.Batches))))
		}
		b.WriteString("\n")
		for _, tp := range pp.Tasks {
			marker := sLogPlay.Render("✓")
			if tp.Counts.Unknown > 0 {
				marker = sWarn.Render("?")
			} else if tp.Counts.Run == 0 {
				marker = sDim.Render("-")
			}
			label := tp.Name
			if tp.Role != "" {
				label = tp.Role + " : " + label
			}
			loop := ""
			if tp.LoopItems > 0 {
				loop = sDim.Render(fmt.Sprintf(" ×%d", tp.LoopItems))
			} else if tp.LoopItems == -1 {
				loop = sDim.Render(" loop ?")
			}
			b.WriteString(fmt.Sprintf("  %s %s  %s%s\n", marker, label,
				sDim.Render(fmt.Sprintf("run=%d skip=%d unknown=%d", tp.Counts.Run, tp.Counts.Skip, tp.Counts.Unknown)), loop))
			if tp.Counts.Unknown > 0 {
				seen := map[string]bool{}
				for _, hv := range tp.Hosts {
					for _, m := range hv.Missing {
						if !seen[m] {
							seen[m] = true
							b.WriteString("      " + sWarn.Render("? missing: "+m) + "\n")
						}
					}
				}
			}
		}
		for _, h := range pp.Handlers {
			u := ""
			if h.Uncertain {
				u = sWarn.Render(" (uncertain)")
			}
			b.WriteString(sDim.Render(fmt.Sprintf("  ⚑ handler %s on %d host(s)", h.Name, len(h.Hosts))) + u + "\n")
		}
	}
	s := out.Summary
	b.WriteString("\n" + sTitle.Render("Summary: ") +
		fmt.Sprintf("hosts=%d tasks=%d  ", s.Hosts, s.Tasks) +
		sLogPlay.Render(fmt.Sprintf("run=%d ", s.Run)) +
		sDim.Render(fmt.Sprintf("skip=%d ", s.Skip)) +
		sWarn.Render(fmt.Sprintf("unknown=%d", s.Unknown)) + "\n")
	for _, mv := range s.MissingVars {
		b.WriteString(sWarn.Render(fmt.Sprintf("  missing var: %s (%d verdicts)", mv.Name, mv.Count)) + "\n")
	}
	return b.String()
}
