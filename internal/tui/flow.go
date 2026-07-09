// Playbook flow view: a navigable vertical chain of connected blocks
// (play → roles → tasks, with nested block/rescue/always) and a toggle-able
// detail pane that expands full information for the selected step.
package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jgsqware/pine/internal/model"
)

const flowIndent = 2 // columns of indent per nesting level

type flowKind int

const (
	fnPlay    flowKind = iota // a play header card
	fnImport                  // an import_playbook reference
	fnSection                 // a non-selectable separator (roles, pre_tasks, …)
	fnRole                    // a role applied by the play
	fnTask                    // a task (possibly nested in block/rescue/always)
)

// flowNode is one entry in the flattened, ordered playbook flow.
type flowNode struct {
	kind    flowKind
	depth   int
	title   string
	section string      // fnSection label
	play    *model.Play // fnPlay / fnImport
	task    *model.Task // fnTask
}

func selectableKind(k flowKind) bool { return k != fnSection }

// flowNodes flattens the selected playbook into the ordered list of blocks.
func (a *app) flowNodes() []flowNode {
	var out []flowNode
	pb := &a.flowPlaybook
	for pi := range pb.Plays {
		play := &pb.Plays[pi]
		if play.Import != "" {
			out = append(out, flowNode{kind: fnImport, title: play.Import, play: play})
			continue
		}
		out = append(out, flowNode{kind: fnPlay, title: playTitle(play), play: play})
		if len(play.Roles) > 0 {
			out = append(out, flowNode{kind: fnSection, depth: 1, section: "roles"})
			for _, r := range play.Roles {
				out = append(out, flowNode{kind: fnRole, depth: 1, title: r})
			}
		}
		for _, sec := range []struct {
			label string
			tasks []model.Task
		}{
			{"pre_tasks", play.PreTasks}, {"tasks", play.Tasks},
			{"post_tasks", play.PostTasks}, {"handlers", play.Handlers},
		} {
			if len(sec.tasks) == 0 {
				continue
			}
			out = append(out, flowNode{kind: fnSection, depth: 1, section: sec.label})
			out = append(out, flowTaskNodes(sec.tasks, 1)...)
		}
	}
	return out
}

func flowTaskNodes(tasks []model.Task, depth int) []flowNode {
	var out []flowNode
	for i := range tasks {
		t := &tasks[i]
		out = append(out, flowNode{kind: fnTask, depth: depth, title: taskTitle(t), task: t})
		for _, sub := range []struct {
			label string
			tasks []model.Task
		}{{"block", t.Block}, {"rescue", t.Rescue}, {"always", t.Always}} {
			if len(sub.tasks) == 0 {
				continue
			}
			out = append(out, flowNode{kind: fnSection, depth: depth + 1, section: sub.label})
			out = append(out, flowTaskNodes(sub.tasks, depth+1)...)
		}
	}
	return out
}

// flowMove advances the selection to the next selectable node in a direction.
func (a *app) flowMove(delta int) {
	nodes := a.flowNodes()
	for i := a.flowCur + delta; i >= 0 && i < len(nodes); i += delta {
		if selectableKind(nodes[i].kind) {
			a.flowCur = i
			return
		}
	}
}

// flowSnap clamps the cursor into range and onto a selectable node, searching
// in the given direction first (1 forward, -1 backward) then the other way.
func (a *app) flowSnap(dir int) {
	nodes := a.flowNodes()
	if len(nodes) == 0 {
		a.flowCur = 0
		return
	}
	a.flowCur = clampInt(a.flowCur, 0, len(nodes)-1)
	if selectableKind(nodes[a.flowCur].kind) {
		return
	}
	for _, step := range []int{dir, -dir} {
		i := a.flowCur
		for i >= 0 && i < len(nodes) {
			if selectableKind(nodes[i].kind) {
				a.flowCur = i
				return
			}
			i += step
		}
	}
}

func (a *app) viewFlow(h int) string {
	a.flowSnap(1)
	pb := a.flowPlaybook
	tasks := 0
	for i := range pb.Plays {
		p := &pb.Plays[i]
		tasks += len(p.PreTasks) + len(p.Tasks) + len(p.PostTasks) + len(p.Handlers)
	}
	title := sTitle.Render("⛓ "+filepath.Base(pb.Path)) +
		sFaint.Render(fmt.Sprintf("   %d plays · %d tasks", len(pb.Plays), tasks))

	ch := h - 2 // title line + blank line

	// Master/detail by default: the block chain on the left, a live preview of
	// the highlighted step on the right. Collapses to a full-width chain when the
	// terminal is narrow or the preview is toggled off (h / ←).
	chainW := a.width * 52 / 100
	if chainW < 30 {
		chainW = 30
	}
	detW := a.width - chainW - 1
	var content string
	if a.flowOpen && detW >= 30 {
		left := a.flowChain(chainW, ch)
		right := a.box(a.flowDetailTitle(), a.flowDetailBody(detW-4), detW, ch, false)
		content = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	} else {
		content = a.flowChain(a.width, ch)
	}
	return title + "\n\n" + content
}

// flowDetailTitle labels the right-hand preview box by the selected node kind.
func (a *app) flowDetailTitle() string {
	nodes := a.flowNodes()
	if a.flowCur >= 0 && a.flowCur < len(nodes) {
		switch nodes[a.flowCur].kind {
		case fnPlay:
			return "play"
		case fnImport:
			return "import"
		case fnRole:
			return "role"
		case fnTask:
			return "task"
		}
	}
	return "detail"
}

// flowChain renders the block chain into a totalW×h cell, scrolled to keep the
// selected node visible.
func (a *app) flowChain(totalW, h int) string {
	nodes := a.flowNodes()
	if len(nodes) == 0 {
		return lipgloss.Place(totalW, h, lipgloss.Center, lipgloss.Center, sFaint.Render("empty playbook"))
	}
	cardW := totalW - 1
	if cardW > 72 {
		cardW = 72
	}

	var lines []string
	starts := make([]int, len(nodes))
	for i := range nodes {
		indent := nodes[i].depth * flowIndent
		if i > 0 {
			lines = append(lines, padTo(strings.Repeat(" ", indent)+sDim.Render("│"), totalW))
		}
		starts[i] = len(lines)
		for _, l := range a.flowNodeLines(nodes[i], indent, cardW-indent, i == a.flowCur) {
			lines = append(lines, padTo(l, totalW))
		}
	}

	start := 0
	if len(lines) > h {
		start = clampInt(starts[clampInt(a.flowCur, 0, len(nodes)-1)]-h/2, 0, len(lines)-h)
	}
	end := min(len(lines), start+h)
	out := lines[start:end]
	for len(out) < h {
		out = append(out, strings.Repeat(" ", totalW))
	}
	return strings.Join(out, "\n")
}

func (a *app) flowNodeLines(n flowNode, indent, w int, sel bool) []string {
	switch n.kind {
	case fnSection:
		return []string{strings.Repeat(" ", indent) + sFaint.Render("── "+n.section+" ──")}
	case fnPlay:
		return flowCard("PLAY  "+n.title, playMeta(n.play), indent, w, sel, cCyan, sCyan)
	case fnImport:
		return flowCard("import_playbook", sCyan.Render(n.title), indent, w, sel, cCyan, sCyan)
	case fnRole:
		return flowCard("role  "+n.title, sFaint.Render("applied as a role"), indent, w, sel, cBorder, sDim)
	case fnTask:
		// Inverted card: the ansible module labels the frame; the box body says
		// *what it will do* (the task's descriptive name). Truncate the name to
		// the content width (module titles are short and truncated by flowCard).
		what := truncTo(taskWhat(n.task), w-4)
		return flowCard(shortModule(n.task.Module), what, indent, w, sel, cBorder, sCyan)
	}
	return nil
}

// taskWhat is the human "what will be done" line for a task's card body: its
// descriptive name, or a faint placeholder when the task is unnamed.
func taskWhat(t *model.Task) string {
	if t.Name != "" {
		return t.Name
	}
	return "(unnamed)"
}

// flowCard renders a 3-line rounded block with the title on the top border and
// a single content line. baseBorder/baseTitle style an unselected card; the
// selected step is always promoted to the green accent.
func flowCard(title, sub string, indent, w int, sel bool, baseBorder lipgloss.TerminalColor, baseTitle lipgloss.Style) []string {
	if w < 8 {
		w = 8
	}
	bc, ts := baseBorder, baseTitle
	marker := "  "
	if sel {
		bc, ts, marker = cAccent, sTitle, "▸ "
	}
	bs := lipgloss.NewStyle().Foreground(bc)
	innerW := w - 2
	contentW := innerW - 2
	lbl := ts.Render(truncTo(marker+title, innerW-2))
	dashes := innerW - 1 - lipgloss.Width(lbl)
	if dashes < 0 {
		dashes = 0
	}
	pad := strings.Repeat(" ", indent)
	return []string{
		pad + bs.Render("╭─") + lbl + bs.Render(strings.Repeat("─", dashes)+"╮"),
		pad + bs.Render("│") + " " + padTo(sub, contentW) + " " + bs.Render("│"),
		pad + bs.Render("╰"+strings.Repeat("─", innerW)+"╯"),
	}
}

// --- detail pane (right, toggled) ---

func (a *app) flowDetailBody(w int) string {
	nodes := a.flowNodes()
	if a.flowCur < 0 || a.flowCur >= len(nodes) {
		return sFaint.Render("nothing selected")
	}
	switch n := nodes[a.flowCur]; n.kind {
	case fnPlay:
		return playDetailBody(n.play, w)
	case fnImport:
		return sCyan.Render("import_playbook") + "\n\n" + n.title
	case fnRole:
		return sTitle.Render("role "+n.title) + "\n\n" + sFaint.Render("full tasks, defaults and handlers\nare on the Roles tab")
	case fnTask:
		return taskDetailBody(n.task, w)
	}
	return ""
}

func taskDetailBody(t *model.Task, w int) string {
	var b strings.Builder

	// Headline: the module (accent) — the *kind* of action — with its full FQCN
	// faint alongside; then the descriptive name — *what it will do* — below.
	short := shortModule(t.Module)
	b.WriteString(sTitle.Render(short))
	if t.Module != short {
		b.WriteString(sFaint.Render("  " + t.Module))
	}
	b.WriteString("\n")
	if t.Name != "" {
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(truncTo(t.Name, w)) + "\n")
	} else {
		b.WriteString(sFaint.Render("(unnamed task)") + "\n")
	}

	// Badge row: structural markers in cyan, conditional in amber.
	var badges []string
	if t.Loop {
		badges = append(badges, sCyan.Render("loop"))
	}
	if t.When != "" {
		badges = append(badges, sWarn.Render("conditional"))
	}
	if t.IncludePath != "" {
		badges = append(badges, sCyan.Render("include"))
	}
	for _, tg := range t.Tags {
		badges = append(badges, sCyan.Render("#"+tg))
	}
	if len(badges) > 0 {
		b.WriteString("\n" + strings.Join(badges, sFaint.Render("  ")) + "\n")
	}

	if len(t.Notify) > 0 {
		b.WriteString("\n" + sCyan.Render("notifies") + "\n" +
			quoteBlock(sWarn.Render("→ "+strings.Join(t.Notify, ", "))))
	}
	if t.IncludePath != "" {
		b.WriteString("\n" + sCyan.Render("includes") + "\n" + quoteBlock(sCyan.Render(t.IncludePath)))
	}
	if t.When != "" {
		b.WriteString("\n" + sCyan.Render("when") + "\n" + quoteBlock(wrap(t.When, w-4)))
	}
	if t.Args != "" {
		b.WriteString("\n" + sCyan.Render("args") + "\n" + quoteBlock(wrap(t.Args, w-4)))
	}

	for _, sec := range []struct {
		label string
		tasks []model.Task
	}{{"block", t.Block}, {"rescue", t.Rescue}, {"always", t.Always}} {
		if len(sec.tasks) == 0 {
			continue
		}
		b.WriteString("\n" + sTitle.Render(fmt.Sprintf("%s (%d)", sec.label, len(sec.tasks))) + "\n")
		for i := range sec.tasks {
			b.WriteString(sFaint.Render("  • ") + taskTitle(&sec.tasks[i]) + "\n")
		}
	}
	return b.String()
}

// quoteBlock renders a value block with a faint left rule, for when/args/notify
// sections in the detail pane.
func quoteBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = sFaint.Render("│ ") + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func playDetailBody(p *model.Play, w int) string {
	_ = w
	var b strings.Builder
	b.WriteString(sTitle.Render(playTitle(p)) + "\n\n")
	b.WriteString(kv("hosts", sCyan.Render(p.Hosts)))
	if p.Become {
		b.WriteString(kv("become", "yes"))
	}
	if p.Serial != "" {
		b.WriteString(kv("serial", p.Serial))
	}
	if p.Strategy != "" {
		b.WriteString(kv("strategy", p.Strategy))
	}
	if len(p.Tags) > 0 {
		b.WriteString(kv("tags", strings.Join(p.Tags, ", ")))
	}
	if len(p.VarsFiles) > 0 {
		b.WriteString(kv("vars_files", strings.Join(p.VarsFiles, ", ")))
	}
	if len(p.VarsPrompt) > 0 {
		names := make([]string, len(p.VarsPrompt))
		for i, vp := range p.VarsPrompt {
			names[i] = vp.Name
		}
		b.WriteString(kv("prompts", strings.Join(names, ", ")))
	}
	if len(p.Roles) > 0 {
		b.WriteString(kv("roles", sCyan.Render(strings.Join(p.Roles, ", "))))
	}
	var counts []string
	for _, c := range []struct {
		label string
		n     int
	}{{"pre", len(p.PreTasks)}, {"tasks", len(p.Tasks)}, {"post", len(p.PostTasks)}, {"handlers", len(p.Handlers)}} {
		if c.n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", c.label, c.n))
		}
	}
	if len(counts) > 0 {
		b.WriteString("\n" + sFaint.Render(strings.Join(counts, " · ")) + "\n")
	}
	return b.String()
}

// renderPlaybookSummary is the compact list-preview for a playbook (the full
// step-by-step flow lives behind enter → flow view).
func renderPlaybookSummary(p model.Playbook) string {
	tasks := 0
	for i := range p.Plays {
		pl := &p.Plays[i]
		tasks += len(pl.PreTasks) + len(pl.Tasks) + len(pl.PostTasks) + len(pl.Handlers)
	}
	var b strings.Builder
	b.WriteString(sFaint.Render(fmt.Sprintf("%d plays · %d tasks", len(p.Plays), tasks)) + "\n\n")
	for i := range p.Plays {
		pl := &p.Plays[i]
		if pl.Import != "" {
			b.WriteString(sDim.Render("import ") + sCyan.Render(pl.Import) + "\n\n")
			continue
		}
		b.WriteString(sTitle.Render("▸ "+playTitle(pl)) + "\n")
		b.WriteString("  " + sDim.Render("hosts ") + sCyan.Render(pl.Hosts))
		if pl.Serial != "" {
			b.WriteString(sFaint.Render("  serial " + pl.Serial))
		}
		if pl.Become {
			b.WriteString(sFaint.Render("  become"))
		}
		b.WriteString("\n")
		if len(pl.Roles) > 0 {
			b.WriteString("  " + sDim.Render("roles ") + sCyan.Render(strings.Join(pl.Roles, ", ")) + "\n")
		}
		var counts []string
		for _, c := range []struct {
			label string
			n     int
		}{{"pre", len(pl.PreTasks)}, {"tasks", len(pl.Tasks)}, {"post", len(pl.PostTasks)}, {"handlers", len(pl.Handlers)}} {
			if c.n > 0 {
				counts = append(counts, fmt.Sprintf("%s %d", c.label, c.n))
			}
		}
		if len(counts) > 0 {
			b.WriteString("  " + sFaint.Render(strings.Join(counts, " · ")) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(sKey.Render("enter") + sDim.Render(" open flow view"))
	return b.String()
}

// --- small helpers ---

func playTitle(p *model.Play) string {
	if p.Name != "" {
		return p.Name
	}
	return "(unnamed play)"
}

func taskTitle(t *model.Task) string {
	if t.Name != "" {
		return t.Name
	}
	return shortModule(t.Module)
}

func shortModule(m string) string {
	if i := strings.LastIndex(m, "."); i >= 0 && i < len(m)-1 {
		return m[i+1:]
	}
	return m
}

func playMeta(p *model.Play) string {
	parts := []string{sCyan.Render(p.Hosts)}
	if p.Serial != "" {
		parts = append(parts, sFaint.Render("serial "+p.Serial))
	}
	if p.Strategy != "" {
		parts = append(parts, sFaint.Render(p.Strategy))
	}
	if p.Become {
		parts = append(parts, sFaint.Render("become"))
	}
	return strings.Join(parts, sDim.Render(" · "))
}

func kv(label, val string) string {
	return sCyan.Render(fmt.Sprintf("%-10s", label)) + val + "\n"
}

func wrap(s string, w int) string {
	if w < 4 {
		w = 4
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
