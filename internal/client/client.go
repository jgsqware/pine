// Package client is an HTTP client for a running Pine daemon. It implements
// the surface the TUI needs (tui.Engine) so `pine attach` can drive a server
// over its REST API instead of opening a second engine on the shared store.
package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
)

// Client talks to a Pine daemon over HTTP.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for a daemon reachable at baseURL (e.g.
// "http://localhost:8743"). A trailing slash is trimmed.
func New(baseURL string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// Ping verifies a daemon is answering at the base URL, used by `pine attach`
// to decide whether to connect before starting the TUI.
func (c *Client) Ping() error {
	resp, err := c.http.Get(c.base + "/api/stats")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	return nil
}

// apiErr is the {"error": "..."} body the server returns on failure.
type apiErr struct {
	Error string `json:"error"`
}

// do executes a request and decodes a JSON body into out (may be nil).
func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e apiErr
		data, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ListRepos returns connected repos, or nil if the daemon is unreachable.
func (c *Client) ListRepos() []model.Repo {
	var repos []model.Repo
	_ = c.do(http.MethodGet, "/api/repos", nil, &repos)
	return repos
}

// ListJobs returns job history, or nil if the daemon is unreachable.
func (c *Client) ListJobs() []model.Job {
	var jobs []model.Job
	_ = c.do(http.MethodGet, "/api/jobs", nil, &jobs)
	return jobs
}

// Scan returns the scan result for a repo.
func (c *Client) Scan(repoID string) (*model.ScanResult, error) {
	var res model.ScanResult
	if err := c.do(http.MethodGet, "/api/repos/"+repoID+"/scan", nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// SyncRepo triggers a sync (clone/pull + rescan) of a repo.
func (c *Client) SyncRepo(repoID string) (model.Repo, error) {
	var repo model.Repo
	err := c.do(http.MethodPost, "/api/repos/"+repoID+"/sync", nil, &repo)
	return repo, err
}

// StartJob launches a playbook run on the daemon.
func (c *Client) StartJob(req model.Job) (model.Job, error) {
	var job model.Job
	err := c.do(http.MethodPost, "/api/jobs", req, &job)
	return job, err
}

// JobLog fetches the stored log of a finished job.
func (c *Client) JobLog(jobID string) (string, error) {
	resp, err := c.http.Get(c.base + "/api/jobs/" + jobID + "/log")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("job log: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}

// Plan computes an estimated plan for a playbook via the daemon.
func (c *Client) Plan(repo model.Repo, playbook string) (*plan.Result, error) {
	var out plan.Result
	err := c.do(http.MethodPost, "/api/plans", plan.Request{
		RepoID: repo.ID, Playbook: playbook,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Subscribe streams a job's log lines over Server-Sent Events. It always opens
// the stream (the daemon replays the stored log, then live lines) and returns
// live=true; the channel closes when the job ends or the connection drops, at
// which point the TUI stops following. On connection failure it returns
// (nil, false) so the caller falls back to JobLog.
func (c *Client) Subscribe(jobID string) (chan string, bool) {
	resp, err := c.http.Get(c.base + "/api/jobs/" + jobID + "/events")
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, false
	}
	ch := make(chan string, 4096)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var event string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data := strings.TrimPrefix(line[len("data:"):], " ")
				if event == "line" {
					select {
					case ch <- data:
					default: // reader gone (view changed) — drop and keep draining
					}
				}
			}
		}
	}()
	return ch, true
}
