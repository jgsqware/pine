package runner

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// run is the in-memory side of a live job: log fan-out + cancellation.
type run struct {
	mu     sync.Mutex
	subs   map[chan string]bool
	file   *os.File
	cancel context.CancelFunc
	done   bool

	// per-task wall-time, measured from the TASK banners as they stream
	curTask  string
	curStart time.Time
	timings  []model.TaskDuration
}

func (r *run) publish(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.track(line)
	if r.file != nil {
		_, _ = r.file.WriteString(line + "\n")
	}
	for ch := range r.subs {
		select {
		case ch <- line:
		default: // slow subscriber: drop rather than block the job
		}
	}
}

// track records task durations from the streamed output (caller holds mu).
func (r *run) track(line string) {
	banner := taskBannerRe.FindStringSubmatch(line)
	if banner == nil && !strings.HasPrefix(line, "PLAY ") {
		return
	}
	now := time.Now()
	if r.curTask != "" {
		r.timings = append(r.timings, model.TaskDuration{
			Task: r.curTask, MS: now.Sub(r.curStart).Milliseconds(),
		})
	}
	r.curTask, r.curStart = "", now
	if banner != nil {
		r.curTask = banner[1]
	}
}

// takeTimings finalizes and returns the collected durations, averaging
// repeats of the same task (serial batches).
func (r *run) takeTimings() []model.TaskDuration {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.track("PLAY RECAP") // close the last open task
	sum := map[string]int64{}
	count := map[string]int64{}
	var order []string
	for _, t := range r.timings {
		if count[t.Task] == 0 {
			order = append(order, t.Task)
		}
		sum[t.Task] += t.MS
		count[t.Task]++
	}
	out := make([]model.TaskDuration, 0, len(order))
	for _, task := range order {
		out = append(out, model.TaskDuration{Task: task, MS: sum[task] / count[task]})
	}
	return out
}

func (r *run) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	for ch := range r.subs {
		close(ch)
	}
	r.subs = map[chan string]bool{}
}

// StartJob creates a job and launches it in the background.
func (m *Manager) StartJob(req model.Job) (model.Job, error) {
	repo, err := m.Store.GetRepo(req.RepoID)
	if err != nil {
		return model.Job{}, fmt.Errorf("unknown repo: %w", err)
	}
	job := model.Job{
		ID:        store.NewID("j"),
		RepoID:    repo.ID,
		RepoName:  repo.Name,
		Playbook:  req.Playbook,
		Inventory: req.Inventory,
		Limit:     req.Limit,
		Tags:      req.Tags,
		Check:     req.Check,
		Status:    model.JobPending,
		Created:   time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		job.Simulated = true
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
	r := &run{subs: map[chan string]bool{}, file: logFile, cancel: cancel}
	m.mu.Lock()
	m.runs[job.ID] = r
	m.mu.Unlock()

	go m.execute(ctx, job, r)
	return job, nil
}

// Subscribe returns buffered log lines already written plus a channel of
// future lines. The channel is closed when the job finishes. ok=false means
// the job is not running (read the log file instead).
func (m *Manager) Subscribe(jobID string) (ch chan string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, exists := m.runs[jobID]
	if !exists {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil, false
	}
	ch = make(chan string, 4096)
	r.subs[ch] = true
	return ch, true
}

// Unsubscribe detaches a log listener.
func (m *Manager) Unsubscribe(jobID string, ch chan string) {
	m.mu.Lock()
	r, exists := m.runs[jobID]
	m.mu.Unlock()
	if !exists {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.subs[ch] {
		delete(r.subs, ch)
		close(ch)
	}
}

// Cancel stops a running job.
func (m *Manager) Cancel(jobID string) (model.Job, error) {
	job, err := m.Store.GetJob(jobID)
	if err != nil {
		return job, err
	}
	m.mu.Lock()
	r, exists := m.runs[jobID]
	m.mu.Unlock()
	if exists {
		r.cancel()
	}
	if !job.Terminal() {
		job.Status = model.JobCanceled
		job.Finished = time.Now().UTC().Format(time.RFC3339)
		_ = m.Store.SaveJob(job)
	}
	return job, nil
}

func (m *Manager) execute(ctx context.Context, job model.Job, r *run) {
	start := time.Now()
	job.Status = model.JobRunning
	job.Started = start.UTC().Format(time.RFC3339)
	_ = m.Store.SaveJob(job)

	var failed bool
	if job.Simulated {
		failed = m.simulate(ctx, &job, r)
	} else {
		failed = m.runAnsible(ctx, &job, r)
	}

	// reload in case Cancel already wrote a terminal state
	if cur, err := m.Store.GetJob(job.ID); err == nil && cur.Status == model.JobCanceled {
		job.Status = model.JobCanceled
	} else if ctx.Err() != nil {
		job.Status = model.JobCanceled
	} else if failed {
		job.Status = model.JobFailed
	} else {
		job.Status = model.JobSuccess
	}
	job.Finished = time.Now().UTC().Format(time.RFC3339)
	job.DurationMS = time.Since(start).Milliseconds()
	job.TaskDurations = r.takeTimings()
	_ = m.Store.SaveJob(job)

	r.close()
	m.mu.Lock()
	delete(m.runs, job.ID)
	m.mu.Unlock()
}

// recapRe matches PLAY RECAP lines: "web01 : ok=3 changed=1 unreachable=0 failed=0 skipped=2 ..."
var recapRe = regexp.MustCompile(`^\s*(\S+)\s*:\s*ok=(\d+)\s+changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)(?:\s+skipped=(\d+))?`)

func parseRecapLine(line string, sum *model.JobSummary) bool {
	mm := recapRe.FindStringSubmatch(line)
	if mm == nil {
		return false
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	sum.OK += atoi(mm[2])
	sum.Changed += atoi(mm[3])
	sum.Unreachable += atoi(mm[4])
	sum.Failed += atoi(mm[5])
	if mm[6] != "" {
		sum.Skipped += atoi(mm[6])
	}
	return true
}

// runAnsible executes the real ansible-playbook command, streaming output.
func (m *Manager) runAnsible(ctx context.Context, job *model.Job, r *run) (failed bool) {
	repo, err := m.Store.GetRepo(job.RepoID)
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	workdir := m.Store.RepoWorkdir(&repo)

	args := []string{job.Playbook}
	if job.Inventory != "" {
		args = append(args, "-i", job.Inventory)
	}
	if job.Limit != "" {
		args = append(args, "--limit", job.Limit)
	}
	if job.Tags != "" {
		args = append(args, "--tags", job.Tags)
	}
	if job.Check {
		args = append(args, "--check")
	}

	cmd := exec.CommandContext(ctx, "ansible-playbook", args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "ANSIBLE_FORCE_COLOR=0", "ANSIBLE_NOCOLOR=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	cmd.Stderr = cmd.Stdout
	r.publish(fmt.Sprintf("$ ansible-playbook %s", strings.Join(args, " ")))
	if err := cmd.Start(); err != nil {
		r.publish("ERROR: " + err.Error())
		return true
	}
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inRecap := false
	for scan.Scan() {
		line := scan.Text()
		r.publish(line)
		if strings.HasPrefix(line, "PLAY RECAP") {
			inRecap = true
			continue
		}
		if inRecap {
			parseRecapLine(line, &job.Summary)
		}
	}
	if err := cmd.Wait(); err != nil {
		return true
	}
	return job.Summary.Failed > 0 || job.Summary.Unreachable > 0
}
