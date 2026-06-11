// Package tui implements Pine's terminal user interface (bubbletea).
package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
)

// Run starts the TUI on top of an opened manager.
func Run(mgr *runner.Manager) error {
	m := newApp(mgr)
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
	cAccent  = lipgloss.AdaptiveColor{Light: "#3f8e3f", Dark: "#4ade80"} // green
	cCyan    = lipgloss.AdaptiveColor{Light: "#0184bc", Dark: "#22d3ee"} // cyan / blue accent
	cMuted   = lipgloss.AdaptiveColor{Light: "#696c77", Dark: "#8aa396"} // secondary text
	cDanger  = lipgloss.AdaptiveColor{Light: "#d52a1f", Dark: "#f87171"} // red
	cWarn    = lipgloss.AdaptiveColor{Light: "#986801", Dark: "#fbbf24"} // amber / orange
	cTabFg   = lipgloss.AdaptiveColor{Light: "#fafafa", Dark: "#0b0f0e"} // text on the active tab
	cBorder  = lipgloss.AdaptiveColor{Light: "#c2c4c9", Dark: "#1f2b25"} // box / divider
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sTabOn   = lipgloss.NewStyle().Bold(true).Foreground(cTabFg).Background(cAccent).Padding(0, 1)
	sTabOff  = lipgloss.NewStyle().Foreground(cMuted).Padding(0, 1)
	sSel     = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sDim     = lipgloss.NewStyle().Foreground(cMuted)
	sCyan    = lipgloss.NewStyle().Foreground(cCyan)
	sErr     = lipgloss.NewStyle().Foreground(cDanger)
	sWarn    = lipgloss.NewStyle().Foreground(cWarn)
	sBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).Padding(0, 1)
	sLogPlay = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sLogTask = lipgloss.NewStyle().Foreground(cCyan)
)

type app struct {
	mgr    *runner.Manager
	width  int
	height int

	tab    int
	cursor map[int]int
	repoIX int // selected repo index (drives playbooks/roles/inventory)

	repos []model.Repo
	scan  *model.ScanResult
	jobs  []model.Job

	mode      string // list | detail | log | confirm-run
	detail    string
	scroll    int
	status    string
	runCheck  bool
	runTarget string // playbook path pending confirmation

	// inventory tab: expandable group tree with host leaves
	collapsed    map[string]bool // node key -> collapsed (default expanded)
	invFilter    string          // active substring filter
	invFiltering bool            // true while editing the filter string

	// live job log
	logJob   string
	logLines []string
	logCh    chan string
}

func newApp(mgr *runner.Manager) *app {
	return &app{mgr: mgr, cursor: map[int]int{}, mode: "list", collapsed: map[string]bool{}}
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
	return tick()
}

func (a *app) reload() {
	a.repos = a.mgr.Store.ListRepos()
	a.jobs = a.mgr.Store.ListJobs()
	if a.repoIX >= len(a.repos) {
		a.repoIX = 0
	}
	a.scan = nil
	if len(a.repos) > 0 {
		if res, err := a.mgr.Scan(a.repos[a.repoIX].ID); err == nil {
			a.scan = res
		}
	}
}

func (a *app) listLen() int {
	switch a.tab {
	case tabRepos:
		return len(a.repos)
	case tabPlaybooks:
		if a.scan != nil {
			return len(a.scan.Playbooks)
		}
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

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case tickMsg:
		if a.mode != "log" {
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

	// While editing the inventory filter, capture printable keys instead of
	// treating them as navigation/quit shortcuts.
	if a.invFiltering && a.mode == "list" {
		switch k {
		case "esc":
			a.invFiltering, a.invFilter = false, ""
		case "enter":
			a.invFiltering = false
		case "backspace":
			if n := len(a.invFilter); n > 0 {
				a.invFilter = a.invFilter[:n-1]
			}
		case "space":
			a.invFilter += " "
		default:
			if len(k) == 1 && k[0] >= 0x20 && k[0] < 0x7f {
				a.invFilter += k
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
				_, _ = a.mgr.SyncRepo(a.repos[a.cursor[a.tab]].ID)
				a.status = "sync started"
			}
		case tabInventory:
			return a.sshSelected()
		}
	case "/":
		if a.tab == tabInventory {
			a.invFiltering = true
		}
	case "r":
		if a.tab == tabPlaybooks && a.scan != nil && a.cursor[a.tab] < len(a.scan.Playbooks) {
			a.runTarget = a.scan.Playbooks[a.cursor[a.tab]].Path
			a.mode = "confirm-run"
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
	job, err := a.mgr.StartJob(model.Job{
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
	ch, live := a.mgr.Subscribe(id)
	if live {
		a.logCh = ch
		return a, waitLine(ch)
	}
	// finished job: read the stored log
	if data, err := readFile(a.mgr.Store.JobLogPath(id)); err == nil {
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
		if a.scan != nil && a.cursor[a.tab] < len(a.scan.Playbooks) {
			a.detail = renderPlaybook(a.scan.Playbooks[a.cursor[a.tab]])
			a.mode, a.scroll = "detail", 0
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
	var b strings.Builder

	// header
	repoName := "(no repo)"
	if len(a.repos) > 0 {
		repoName = a.repos[a.repoIX].Name
	}
	header := sTitle.Render(" Pine") + sDim.Render("  repo: ") + sCyan.Render(repoName)
	tabs := make([]string, tabCount)
	for i, n := range tabNames {
		if i == a.tab {
			tabs[i] = sTabOn.Render(fmt.Sprintf("%d %s", i+1, n))
		} else {
			tabs[i] = sTabOff.Render(fmt.Sprintf("%d %s", i+1, n))
		}
	}
	b.WriteString(header + "\n" + strings.Join(tabs, " ") + "\n\n")

	bodyH := a.height - 6
	switch a.mode {
	case "detail", "log":
		b.WriteString(a.viewScrollable(bodyH))
	case "confirm-run":
		b.WriteString(a.viewConfirm())
	default:
		b.WriteString(a.viewList(bodyH))
	}

	// footer
	help := "tab/1-5 switch · j/k move · enter open · q quit"
	switch a.tab {
	case tabRepos:
		help = "enter select repo · s sync · " + help
	case tabPlaybooks:
		help = "r run playbook · " + help
	case tabInventory:
		help = "enter expand/open · s ssh · / filter · " + help
	}
	if a.tab == tabInventory && a.invFiltering {
		help = "type to filter · enter apply · esc clear"
	}
	if a.mode == "detail" || a.mode == "log" {
		help = "j/k scroll · G end · esc back"
	}
	if a.mode == "confirm-run" {
		help = "y/enter launch · c toggle check · n cancel"
	}
	b.WriteString("\n" + sDim.Render(" "+help))
	if a.status != "" {
		b.WriteString("  " + sWarn.Render(a.status))
	}
	return b.String()
}

func (a *app) viewConfirm() string {
	check := "no"
	if a.runCheck {
		check = sWarn.Render("yes (--check)")
	}
	box := fmt.Sprintf("%s\n\n  playbook : %s\n  repo     : %s\n  check    : %s\n\n  launch? (y/n)",
		sTitle.Render("Run playbook"), sCyan.Render(a.runTarget), a.repos[a.repoIX].Name, check)
	return sBox.Render(box)
}

func (a *app) viewList(h int) string {
	rows := a.rows()
	cur := a.cursor[a.tab]
	start := 0
	if cur >= h {
		start = cur - h + 1
	}
	var b strings.Builder
	if a.tab == tabInventory && (a.invFiltering || a.invFilter != "") {
		caret := ""
		if a.invFiltering {
			caret = "_"
		}
		b.WriteString(sDim.Render("  filter: ") + sCyan.Render(a.invFilter+caret) + "\n")
		h--
	}
	if len(rows) == 0 {
		if a.tab == tabInventory && a.invFilter != "" {
			b.WriteString(sDim.Render("  no hosts match"))
			return b.String()
		}
		b.WriteString(sDim.Render("  nothing here yet"))
		if a.tab == tabRepos {
			b.WriteString(sDim.Render(" - start the server and add a repo, or run with --demo"))
		}
		return b.String()
	}
	for i := start; i < len(rows) && i-start < h; i++ {
		prefix := "  "
		line := rows[i]
		if i == cur {
			prefix = sSel.Render("> ")
			line = sSel.Render(stripANSI(line))
		}
		b.WriteString(prefix + line + "\n")
	}
	return b.String()
}

func stripANSI(s string) string {
	// selection re-colors the row; cheap approach: drop existing escapes
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

func (a *app) rows() []string {
	var rows []string
	switch a.tab {
	case tabRepos:
		for _, r := range a.repos {
			src := r.URL
			if src == "" {
				src = r.Path
			}
			status := r.Status
			switch r.Status {
			case model.RepoReady:
				status = sLogPlay.Render("ready")
			case model.RepoError:
				status = sErr.Render("error")
			case model.RepoSyncing:
				status = sWarn.Render("syncing")
			}
			rows = append(rows, fmt.Sprintf("%-22s %-9s %s  %s", r.Name, status,
				sDim.Render(fmt.Sprintf("pb:%d roles:%d hosts:%d", r.Summary.Playbooks, r.Summary.Roles, r.Summary.Hosts)), sDim.Render(src)))
		}
	case tabPlaybooks:
		if a.scan != nil {
			for _, p := range a.scan.Playbooks {
				hosts := []string{}
				for _, pl := range p.Plays {
					if pl.Import == "" && pl.Hosts != "" {
						hosts = append(hosts, pl.Hosts)
					}
				}
				rows = append(rows, fmt.Sprintf("%-28s %s  %s", p.Path,
					sDim.Render(fmt.Sprintf("%d plays", len(p.Plays))), sCyan.Render(strings.Join(uniq(hosts), ", "))))
			}
		}
	case tabRoles:
		if a.scan != nil {
			for _, r := range a.scan.Roles {
				rows = append(rows, fmt.Sprintf("%-22s %s  %s", r.Name,
					sDim.Render(fmt.Sprintf("%d tasks", r.TasksCount)), sDim.Render(r.Description)))
			}
		}
	case tabInventory:
		for _, r := range a.invTree() {
			rows = append(rows, a.invRowString(r))
		}
	case tabJobs:
		for _, j := range a.jobs {
			status := j.Status
			switch j.Status {
			case model.JobSuccess:
				status = sLogPlay.Render("success")
			case model.JobFailed:
				status = sErr.Render("failed ")
			case model.JobRunning:
				status = sWarn.Render("running")
			}
			sim := ""
			if j.Simulated {
				sim = sDim.Render(" (sim)")
			}
			rows = append(rows, fmt.Sprintf("%-9s %-26s %s %s%s", status, j.Playbook,
				sDim.Render(j.RepoName), sDim.Render(j.Created), sim))
		}
	}
	return rows
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

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
