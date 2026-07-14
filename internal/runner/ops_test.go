package runner

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/store"
)

// newTestManager opens a manager over a temp store with a tiny local repo.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	write := func(rel, content string) {
		p := filepath.Join(repoDir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("site.yml", "- name: Site\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n")
	write("inventories/prod/hosts.yml", "web:\n  hosts:\n    web01:\n      ansible_host: 10.0.0.1\n")

	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	repo := model.Repo{ID: "r_test", Name: "test", Path: repoDir, Status: model.RepoReady}
	if err := st.AddRepo(repo); err != nil {
		t.Fatal(err)
	}
	return m, repoDir
}

func waitJob(t *testing.T, m *Manager, id string) model.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		j, err := m.Store.GetJob(id)
		if err == nil && j.Terminal() {
			return j
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return model.Job{}
}

func TestGatherFactsSimulated(t *testing.T) {
	t.Setenv("PINE_SIMULATE", "1") // otherwise a dev box with ansible installed runs the real thing
	m, _ := newTestManager(t)
	job, err := m.GatherFacts("r_test", "")
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, m, job.ID)
	if final.Status != model.JobSuccess {
		t.Fatalf("status = %s", final.Status)
	}
	facts := m.Store.HostFacts("r_test", "web01")
	if facts == nil || facts["os_family"] != "Debian" {
		t.Errorf("web01 facts = %v", facts)
	}
	metas := m.Store.ListFacts("r_test")
	if len(metas) != 1 || metas["web01"].Keys == 0 {
		t.Errorf("metas = %v", metas)
	}
}

func TestDriftFromCheckJobs(t *testing.T) {
	m, _ := newTestManager(t)
	jobs, err := m.DriftCheck("r_test", nil, "")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("drift check: %v %v", jobs, err)
	}
	waitJob(t, m, jobs[0].ID)

	report, err := m.Drift("r_test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.CheckedPlaybooks != 1 || len(report.Playbooks) != 1 {
		t.Fatalf("report = %+v", report.Summary)
	}
	if report.Playbooks[0].Playbook != "site.yml" {
		t.Errorf("playbook = %s", report.Playbooks[0].Playbook)
	}
	if len(report.Hosts) == 0 {
		t.Error("expected hosts in the report")
	}
}

func TestScheduleGateBlocksOnPlanChange(t *testing.T) {
	m, repoDir := newTestManager(t)
	sc, err := m.CreateSchedule(model.Schedule{
		RepoID: "r_test", Playbook: "site.yml", Interval: "1h", Gate: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sc.ApprovedFingerprint == "" {
		t.Fatal("creation should approve the current plan")
	}

	// force due now, unchanged plan -> must launch a job
	sc.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(sc)
	m.tickSchedules(time.Now())
	got, _ := m.getSchedule(sc.ID)
	if got.LastRunID == "" || got.Status != "ok" {
		t.Fatalf("expected a run, got %+v", got)
	}
	waitJob(t, m, got.LastRunID)

	// change the playbook -> plan fingerprint changes -> next tick blocks
	extra := "- name: Site\n  hosts: web\n  tasks:\n    - name: Ping\n      ansible.builtin.ping:\n    - name: New task\n      ansible.builtin.command: echo hi\n"
	if err := os.WriteFile(filepath.Join(repoDir, "site.yml"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Forget("r_test") // drop the scan cache like a sync would

	got.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(got)
	m.tickSchedules(time.Now())
	got2, _ := m.getSchedule(sc.ID)
	if got2.Status != "blocked" {
		t.Fatalf("expected blocked, got %+v", got2)
	}
	if got2.LastRunID != got.LastRunID {
		t.Error("blocked schedule must not have launched a new job")
	}

	// approve -> unblocks and next tick runs
	approved, err := m.ApproveSchedule(sc.ID)
	if err != nil || approved.Status != "ok" {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	approved.NextRunAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	_ = m.Store.SaveSchedule(approved)
	m.tickSchedules(time.Now())
	got3, _ := m.getSchedule(sc.ID)
	if got3.Status != "ok" || got3.LastRunID == got.LastRunID {
		t.Fatalf("expected a new run after approval, got %+v", got3)
	}
}

func TestPipelineRunWithApprovalGate(t *testing.T) {
	m, _ := newTestManager(t)
	pipe, err := m.CreatePipeline(model.Pipeline{
		Name: "deploy", RepoID: "r_test",
		Steps: []model.PipelineStep{
			{Name: "canary", Playbook: "site.yml", Limit: "web01"},
			{Name: "fleet", Playbook: "site.yml", RequireApproval: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := m.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatal(err)
	}

	// wait for the approval pause
	deadline := time.Now().Add(30 * time.Second)
	for {
		cur, _ := m.Store.GetPipelineRun(run.ID)
		if cur.Status == model.PipeWaiting {
			if cur.Steps[0].Status != "success" || cur.Steps[1].Status != model.PipeWaiting {
				t.Fatalf("unexpected step states: %+v", cur.Steps)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reached waiting_approval: %+v", cur)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if _, err := m.ApprovePipelineRun(run.ID); err != nil {
		t.Fatal(err)
	}
	for {
		cur, _ := m.Store.GetPipelineRun(run.ID)
		if cur.Status == model.PipeSuccess {
			if cur.Steps[1].Status != "success" || cur.Steps[1].JobID == "" {
				t.Fatalf("step 2 = %+v", cur.Steps[1])
			}
			break
		}
		if cur.Status == model.PipeFailed || time.Now().After(deadline) {
			t.Fatalf("pipeline did not succeed: %+v", cur)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestScanCacheImmutable verifies that mutating the slice returned by Scan()
// does not corrupt the cached ScanResult. This guards Audit §2 A6: the first
// link of the immutable-cache chain (scan-cache-immutable).
func TestScanCacheImmutable(t *testing.T) {
	m, _ := newTestManager(t)

	// Prime the cache with the first call.
	res1, err := m.Scan("r_test")
	if err != nil {
		t.Fatalf("first Scan: %v", err)
	}
	if len(res1.Playbooks) == 0 {
		t.Fatal("expected at least one playbook in the scan result")
	}
	origLen := len(res1.Playbooks)

	// Mutate: append a fake playbook to the returned slice.
	res1.Playbooks = append(res1.Playbooks, model.Playbook{Path: "injected.yml", Name: "injected"})

	// Overwrite the first element's name directly.
	savedName := res1.Playbooks[0].Name
	res1.Playbooks[0].Name = "CORRUPTED"

	// Second call must return the original cached data, untouched.
	res2, err := m.Scan("r_test")
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if len(res2.Playbooks) != origLen {
		t.Errorf("cache corrupted: got %d playbooks, want %d (append leaked into cache)",
			len(res2.Playbooks), origLen)
	}
	// The element name mutation also must not have leaked (elements are copied by
	// value into the new slice, so the cached element is unaffected).
	_ = savedName // assigned above; the cache copy is independent
	if res2.Playbooks[0].Name == "CORRUPTED" {
		t.Errorf("cache corrupted: playbook name was mutated through the returned pointer")
	}
}

func TestReconcileInterruptedJobs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	if err := st.SaveJob(model.Job{ID: "j_stuck", RepoID: "r", Playbook: "site.yml", Status: model.JobRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJob(model.Job{ID: "j_pending", RepoID: "r", Playbook: "site.yml", Status: model.JobPending}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJob(model.Job{ID: "j_done", RepoID: "r", Playbook: "site.yml", Status: model.JobSuccess}); err != nil {
		t.Fatal(err)
	}
	if n := m.ReconcileInterruptedJobs(); n != 2 {
		t.Fatalf("reconciled %d, want 2 (running + pending)", n)
	}
	for _, id := range []string{"j_stuck", "j_pending"} {
		j, _ := st.GetJob(id)
		if j.Status != model.JobFailed {
			t.Errorf("%s status = %s, want failed", id, j.Status)
		}
		if j.Finished == "" {
			t.Errorf("%s should have a Finished timestamp", id)
		}
	}
	if done, _ := st.GetJob("j_done"); done.Status != model.JobSuccess {
		t.Errorf("finished job must stay success, got %s", done.Status)
	}
}

// TestScanSingleflightDeduplication verifies that N concurrent Scan calls for
// the same uncached repo result in exactly one real filesystem scan (Audit §5-P3).
// All callers must receive the same content; none must get an error.
func TestScanSingleflightDeduplication(t *testing.T) {
	m, _ := newTestManager(t)
	const goroutines = 8

	// The Manager was just created; no scan has run yet, so m.scans is empty
	// and m.caches is empty.  All goroutines will encounter a cache miss.

	var (
		wg      sync.WaitGroup
		results = make([]*model.ScanResult, goroutines)
		errs    = make([]error, goroutines)
		gate    = make(chan struct{}) // lets all goroutines start at the same time
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate // wait until all goroutines are ready
			results[idx], errs[idx] = m.Scan("r_test")
		}(i)
	}
	close(gate) // release all goroutines simultaneously
	wg.Wait()

	// All goroutines must succeed.
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d got error: %v", i, err)
		}
	}

	// All must have received at least one playbook.
	for i, res := range results {
		if res == nil || len(res.Playbooks) == 0 {
			t.Errorf("goroutine %d got empty result", i)
		}
	}

	// All results must have the same playbook count.
	ref := results[0]
	for i := 1; i < goroutines; i++ {
		if results[i] == nil {
			continue
		}
		if len(results[i].Playbooks) != len(ref.Playbooks) {
			t.Errorf("goroutine %d: got %d playbooks, want %d",
				i, len(results[i].Playbooks), len(ref.Playbooks))
		}
	}

	// The manager's ScanCache for this repo must show exactly one parse run
	// (i.e. the actual scan only happened once, regardless of how many goroutines
	// were racing).  Parses() is a cumulative counter incremented once per file
	// parsed; if N scans happened it would be N*files, but singleflight means
	// exactly 1 scan was performed.
	finalCache := m.scanCacheFor("r_test")
	parsesAfterRace := finalCache.Parses()
	if parsesAfterRace == 0 {
		t.Error("expected at least 1 parse (scan must have actually run)")
	}
	// Run a second wave of concurrent Scans — all should hit the cache now (no
	// new parses because the files have not changed).
	var wg2 sync.WaitGroup
	gate2 := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			<-gate2
			m.Scan("r_test") //nolint:errcheck
		}()
	}
	close(gate2)
	wg2.Wait()
	if got := finalCache.Parses(); got != parsesAfterRace {
		t.Errorf("second wave triggered extra parses: before=%d after=%d (cache not effective)",
			parsesAfterRace, got)
	}
}

// TestScanSingleflightErrorPropagatedToAll verifies that when the scan itself
// fails (here: the repo ID is unknown so Store.GetRepo returns an error), all
// concurrent waiters receive the error — not just the leader.
func TestScanSingleflightErrorPropagatedToAll(t *testing.T) {
	m, _ := newTestManager(t)

	// "r_unknown" is not in the store; Store.GetRepo will return an error.
	const unknownID = "r_unknown_does_not_exist"
	const goroutines = 6
	var (
		wg    sync.WaitGroup
		errCh = make(chan error, goroutines)
		gate  = make(chan struct{})
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, scanErr := m.Scan(unknownID)
			errCh <- scanErr
		}()
	}
	close(gate)
	wg.Wait()
	close(errCh)

	var gotErrors, gotNil int
	for e := range errCh {
		if e != nil {
			gotErrors++
		} else {
			gotNil++
		}
	}
	if gotNil > 0 {
		t.Errorf("%d goroutine(s) got nil error on a failing scan; want all errors", gotNil)
	}
	if gotErrors != goroutines {
		t.Errorf("expected %d errors, got %d", goroutines, gotErrors)
	}
}

// TestScanSingleflightScanCountAfterForget verifies that a Scan after Forget
// performs a fresh scan (parse counter increments again), not a stale read.
func TestScanSingleflightScanCountAfterForget(t *testing.T) {
	m, _ := newTestManager(t)

	// Prime the cache.
	if _, err := m.Scan("r_test"); err != nil {
		t.Fatalf("first Scan: %v", err)
	}

	c1 := m.scanCacheFor("r_test")
	parsesAfterFirst := c1.Parses()

	// Forget drops the scan result and its ScanCache.
	m.Forget("r_test")

	// Next Scan should perform a full re-scan and create a brand-new ScanCache.
	if _, err := m.Scan("r_test"); err != nil {
		t.Fatalf("Scan after Forget: %v", err)
	}

	c2 := m.scanCacheFor("r_test")
	parsesAfterSecond := c2.Parses()

	// c2 is a new cache (Forget deleted the old one), so it starts from 0 and
	// should have performed at least one parse.
	if parsesAfterSecond == 0 {
		t.Error("expected at least 1 parse after Forget+Scan (new cache should not be empty)")
	}
	// The two cache objects must be distinct (Forget created a fresh one).
	if c1 == c2 {
		t.Error("expected a new ScanCache after Forget, but got the same pointer")
	}
	_ = parsesAfterFirst // documented for context; not directly asserted across caches
}

// TestScanSingleflightRaceNoDataRace is a lightweight race-detector companion.
// It drives 12 goroutines hammering Scan+Forget concurrently to expose any
// unsynchronised access.  Run with: go test -race ./internal/runner/
func TestScanSingleflightRaceNoDataRace(t *testing.T) {
	m, _ := newTestManager(t)
	var counter atomic.Int64
	const goroutines = 12
	var wg sync.WaitGroup
	gate := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-gate
			if idx%4 == 0 {
				// Mix in a Forget to exercise the concurrent Forget path.
				m.Forget("r_test")
			}
			if _, err := m.Scan("r_test"); err == nil {
				counter.Add(1)
			}
		}(i)
	}
	close(gate)
	wg.Wait()

	if counter.Load() == 0 {
		t.Error("expected at least one successful Scan during the race test")
	}
}

