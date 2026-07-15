package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/claudecode"
	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// StartDescribe launches a Claude Code "describe" session over the repo's
// working copy and returns the job that streams its output (reusing the job log
// + SSE + cancel machinery). write=false is a dry-run (Claude proposes
// descriptions, writes nothing); write=true lets it edit meta/main.yml and the
// pine.yml sidecar. The job carries Kind=="describe" so execute() dispatches to
// runDescribe instead of ansible-playbook.
func (m *Manager) StartDescribe(repoID string, write bool) (model.Job, error) {
	repo, err := m.Store.GetRepo(repoID)
	if err != nil {
		return model.Job{}, fmt.Errorf("unknown repo: %w", err)
	}
	label := "generate descriptions (dry-run)"
	if write {
		label = "generate descriptions (write)"
	}
	job := model.Job{
		ID:       store.NewID("j"),
		RepoID:   repo.ID,
		RepoName: repo.Name,
		Kind:     "describe",
		Playbook: label,
		Status:   model.JobPending,
		Created:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := m.Store.SaveJob(job); err != nil {
		return job, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	logFile, err := os.Create(m.Store.JobLogPath(job.ID))
	if err != nil {
		cancel()
		return job, err
	}
	r := &run{subs: map[chan string]bool{}, file: logFile, cancel: cancel, describeWrite: write}
	m.mu.Lock()
	m.runs[job.ID] = r
	m.mu.Unlock()

	go m.execute(ctx, job, r)
	return job, nil
}

// runDescribe streams a headless `claude -p` session that documents the repo.
func (m *Manager) runDescribe(ctx context.Context, job *model.Job, r *run) (failed bool) {
	repo, err := m.Store.GetRepo(job.RepoID)
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	if !claudecode.Available() {
		r.publish("ERROR: the Claude Code CLI (`claude`) was not found on this host.")
		r.publish("Install it from https://code.claude.com, then retry.")
		return true
	}
	workdir := m.Store.RepoWorkdir(&repo)
	args := claudecode.Args(workdir, r.describeWrite, true)

	if r.describeWrite {
		r.publish("Generating and WRITING descriptions with Claude Code — role meta/main.yml + pine.yml.")
	} else {
		r.publish("Generating proposed descriptions with Claude Code (dry-run — no files are modified).")
	}
	r.publish("$ claude " + redactPrompt(args))

	cmd := exec.CommandContext(ctx, claudecode.Bin(), args...)
	cmd.Dir = workdir
	cmd.Env = claudecode.Env()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // stream-json lines can be large
	sawError := false
	for scan.Scan() {
		if line := renderEvent(scan.Text()); line != "" {
			if strings.HasPrefix(line, "ERROR") || strings.Contains(line, "is_error") {
				sawError = true
			}
			r.publish(line)
		}
	}
	if err := cmd.Wait(); err != nil {
		r.publish("ERROR: claude exited: " + err.Error())
		return true
	}
	if r.describeWrite {
		r.publish("Done. Re-sync the repo to pick up the new descriptions.")
	}
	return sawError
}

// redactPrompt renders the argv for the log without dumping the multi-line
// prompt (it's long and static); the prompt arg is shown as a short placeholder.
func redactPrompt(args []string) string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-p" && i+1 < len(args) {
			out = append(out, "-p <describe-prompt>")
			i++
			continue
		}
		out = append(out, args[i])
	}
	return strings.Join(out, " ")
}

// renderEvent turns one Claude Code stream-json line into a human log line.
// Unrecognised or noise lines return "" and are dropped. The event shapes are
// documented by Claude Code's --output-format stream-json.
func renderEvent(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return line // plain text (e.g. an early error before JSON starts)
	}
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"` // tool name for tool_use blocks
			} `json:"content"`
		} `json:"message"`
		Event struct {
			Type         string `json:"type"`
			Delta        struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return "" // not a shape we render
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return "· session started"
		}
	case "assistant":
		// A complete assistant turn: surface any tool calls it makes so the log
		// shows live progress (the tool name as-is — Read, Bash/grep, …).
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" && c.Name != "" {
				return "· " + c.Name
			}
		}
	case "stream_event":
		switch {
		case ev.Event.Delta.Type == "text_delta" && ev.Event.Delta.Text != "":
			return ev.Event.Delta.Text
		case ev.Event.Type == "content_block_start" && ev.Event.ContentBlock.Type == "tool_use" && ev.Event.ContentBlock.Name != "":
			return "· " + ev.Event.ContentBlock.Name
		}
	case "result":
		if ev.IsError {
			return "ERROR: " + firstNonEmpty(ev.Result, "the session ended with an error")
		}
		if ev.Result != "" {
			return "\n" + ev.Result
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
