package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/runner"
	"github.com/jgsqware/pine/internal/store"
)

// demoApp loads the bundled demo repo into a fresh app, or skips if absent.
func demoApp(t *testing.T) *app {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := runner.New(st)
	abs, _ := filepath.Abs("../../examples/demo-infra")
	if _, err := os.Stat(abs); err != nil {
		t.Skip("demo repo not found")
	}
	repo := model.Repo{ID: store.NewID("r"), Name: "demo-infra", Path: abs, Status: model.RepoNew}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncRepo(repo.ID); err != nil {
		t.Fatal(err)
	}
	a := newApp(mgr)
	a.reload()
	return a
}

// TestViewLayout checks that every tab and mode renders to exactly the
// terminal height with no panics — guarding the bordered two-pane layout.
func TestViewLayout(t *testing.T) {
	lipgloss.SetColorProfile(0) // disable color so width math is unaffected
	a := demoApp(t)

	for _, w := range []int{120, 80, 50} {
		a.width, a.height = w, 28
		for tab := 0; tab < tabCount; tab++ {
			a.tab, a.mode = tab, "list"
			out := a.View()
			if got := lipgloss.Height(out); got != a.height {
				t.Errorf("w=%d tab=%d: height=%d want %d", w, tab, got, a.height)
			}
			if lipgloss.Width(out) > w {
				t.Errorf("w=%d tab=%d: rendered wider than terminal", w, tab)
			}
		}
	}

	a.width = 120
	a.tab, a.mode, a.runTarget = tabPlaybooks, "confirm-run", "site.yml"
	if !strings.Contains(a.View(), "Run playbook") {
		t.Error("confirm-run modal missing title")
	}
	a.mode, a.detail = "detail", renderPlaybook(a.scan.Playbooks[0])
	if lipgloss.Height(a.View()) != a.height {
		t.Error("detail overlay wrong height")
	}

	// playbook flow view, with the detail pane both closed and open
	a.tab, a.cursor[tabPlaybooks] = tabPlaybooks, 0
	a.flowPlaybook = a.scan.Playbooks[0]
	for _, open := range []bool{false, true} {
		a.mode, a.flowOpen, a.flowCur = "flow", open, 0
		if lipgloss.Height(a.View()) != a.height {
			t.Errorf("flow view (open=%v) wrong height", open)
		}
	}

	// playbook tree filter narrows the list and keeps layout intact
	a.mode, a.pbFiltering, a.pbFilter = "list", true, "site"
	a.clampCursor()
	out := a.View()
	if lipgloss.Height(out) != a.height || lipgloss.Width(out) > a.width {
		t.Error("filtered playbook tree broke layout")
	}
}
