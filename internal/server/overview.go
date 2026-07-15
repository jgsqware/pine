package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jgsqware/pine/internal/claudecode"
	"github.com/jgsqware/pine/internal/overview"
	"github.com/jgsqware/pine/internal/plan"
)

// errClaudeUnavailable is returned by /describe when the `claude` CLI is not
// installed — a 409 so the web UI keeps the "Generate descriptions" action hidden.
var errClaudeUnavailable = errors.New("the Claude Code CLI (`claude`) is not available on this host")

// overview returns the composed Guide view of a repo: playbook tiers with
// resolved target hosts, a role catalog with usage cross-refs, entry points,
// honest "what you can / can't do" cautions, and whether the Claude Code CLI is
// available for the "Generate descriptions" action.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	res, err := s.Mgr.Scan(repo.ID)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	root := s.Mgr.Store.RepoWorkdir(&repo)
	hy := plan.Hygiene(res, root)
	ov := overview.ComposeWith(res, root, hy, claudecode.Detect())
	writeJSON(w, http.StatusOK, ov)
}

// describe launches a Claude Code session that writes missing descriptions for
// the repo's playbooks and roles. Body: {"write": bool} — false (default) is a
// dry-run that proposes changes without touching files. Returns the streaming
// job (poll GET /api/jobs/{id}/events for live output). 409 when the CLI is
// absent, so the client can keep the button hidden.
func (s *Server) describe(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Mgr.Store.GetRepo(r.PathValue("id"))
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	if !claudecode.Available() {
		writeErr(w, http.StatusConflict, errClaudeUnavailable)
		return
	}
	var body struct {
		Write bool `json:"write"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty body → dry-run
	}
	job, err := s.Mgr.StartDescribe(repo.ID, body.Write)
	if err != nil {
		writeErr(w, errCode(err), err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
