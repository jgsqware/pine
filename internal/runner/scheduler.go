package runner

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/plan"
	"github.com/jgsqware/pine/internal/store"
)

// scheduleFingerprint computes the current plan fingerprint for a schedule.
func (m *Manager) scheduleFingerprint(sc *model.Schedule) (string, error) {
	repo, err := m.Store.GetRepo(sc.RepoID)
	if err != nil {
		return "", err
	}
	res, err := m.Scan(sc.RepoID)
	if err != nil {
		return "", err
	}
	out, err := plan.Compute(res, m.Store.RepoWorkdir(&repo), repo, plan.Request{
		Playbook: sc.Playbook, Inventory: sc.Inventory,
		Limit: sc.Limit, Tags: sc.Tags, Check: sc.Check,
		HostFacts: m.HostFactsFor(sc.RepoID),
	})
	if err != nil {
		return "", err
	}
	return plan.Fingerprint(out), nil
}

// HostFactsFor loads every stored fact set for a repo.
func (m *Manager) HostFactsFor(repoID string) map[string]map[string]any {
	metas := m.Store.ListFacts(repoID)
	if len(metas) == 0 {
		return nil
	}
	out := map[string]map[string]any{}
	for host := range metas {
		if f := m.Store.HostFacts(repoID, host); f != nil {
			out[host] = f
		}
	}
	return out
}

// CreateSchedule validates and stores a new schedule.
func (m *Manager) CreateSchedule(sc model.Schedule) (model.Schedule, error) {
	repo, err := m.Store.GetRepo(sc.RepoID)
	if err != nil {
		return sc, fmt.Errorf("unknown repo: %w", err)
	}
	if sc.Playbook == "" {
		return sc, fmt.Errorf("playbook is required")
	}
	iv, err := time.ParseDuration(sc.Interval)
	if err != nil || iv < time.Minute {
		return sc, fmt.Errorf("interval must be a duration of at least 1m (e.g. 15m, 1h, 24h)")
	}
	sc.ID = store.NewID("s")
	sc.RepoName = repo.Name
	sc.Status = scheduleStatus(&sc)
	sc.NextRunAt = time.Now().Add(iv).UTC().Format(time.RFC3339)
	if sc.Gate {
		// a fresh schedule approves the current plan implicitly
		if fp, err := m.scheduleFingerprint(&sc); err == nil {
			sc.ApprovedFingerprint = fp
			sc.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	return sc, m.Store.SaveSchedule(sc)
}

func scheduleStatus(sc *model.Schedule) string {
	if !sc.Enabled {
		return "disabled"
	}
	if sc.BlockedReason != "" {
		return "blocked"
	}
	return "ok"
}

// ApproveSchedule accepts the current plan fingerprint and unblocks.
func (m *Manager) ApproveSchedule(id string) (model.Schedule, error) {
	sc, err := m.getSchedule(id)
	if err != nil {
		return sc, err
	}
	fp, err := m.scheduleFingerprint(&sc)
	if err != nil {
		return sc, err
	}
	sc.ApprovedFingerprint = fp
	sc.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
	sc.BlockedReason = ""
	sc.Status = scheduleStatus(&sc)
	return sc, m.Store.SaveSchedule(sc)
}

func (m *Manager) getSchedule(id string) (model.Schedule, error) {
	for _, sc := range m.Store.ListSchedules() {
		if sc.ID == id {
			return sc, nil
		}
	}
	return model.Schedule{}, store.ErrNotFound
}

// RunScheduleNow executes a schedule immediately (bypasses the gate).
func (m *Manager) RunScheduleNow(id string) (model.Job, error) {
	sc, err := m.getSchedule(id)
	if err != nil {
		return model.Job{}, err
	}
	job, err := m.StartJob(model.Job{
		RepoID: sc.RepoID, Playbook: sc.Playbook, Inventory: sc.Inventory,
		Limit: sc.Limit, Tags: sc.Tags, Check: sc.Check,
	})
	if err == nil {
		sc.LastRunID = job.ID
		sc.LastRunAt = time.Now().UTC().Format(time.RFC3339)
		_ = m.Store.SaveSchedule(sc)
	}
	return job, err
}

// StartScheduler runs the schedule loop until ctx is canceled.
func (m *Manager) StartScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tickSchedules(time.Now())
		}
	}
}

// tickSchedules fires every due schedule, applying the plan gate.
func (m *Manager) tickSchedules(now time.Time) {
	for _, sc := range m.Store.ListSchedules() {
		if !sc.Enabled {
			continue
		}
		iv, err := time.ParseDuration(sc.Interval)
		if err != nil {
			continue
		}
		if sc.NextRunAt == "" {
			sc.NextRunAt = now.Add(iv).UTC().Format(time.RFC3339)
			_ = m.Store.SaveSchedule(sc)
			continue
		}
		next, err := time.Parse(time.RFC3339, sc.NextRunAt)
		if err != nil || now.Before(next) {
			continue
		}
		// due: re-arm first so failures don't tight-loop
		sc.NextRunAt = now.Add(iv).UTC().Format(time.RFC3339)

		if sc.Gate {
			fp, err := m.scheduleFingerprint(&sc)
			if err != nil {
				sc.BlockedReason = "plan failed: " + err.Error()
				sc.Status = scheduleStatus(&sc)
				_ = m.Store.SaveSchedule(sc)
				continue
			}
			if fp != sc.ApprovedFingerprint {
				sc.BlockedReason = "plan fingerprint changed since approval"
				sc.Status = scheduleStatus(&sc)
				_ = m.Store.SaveSchedule(sc)
				log.Printf("schedule %s blocked: plan changed (%s)", sc.ID, sc.Playbook)
				continue
			}
		}

		job, err := m.StartJob(model.Job{
			RepoID: sc.RepoID, Playbook: sc.Playbook, Inventory: sc.Inventory,
			Limit: sc.Limit, Tags: sc.Tags, Check: sc.Check,
		})
		if err != nil {
			sc.BlockedReason = "launch failed: " + err.Error()
		} else {
			sc.LastRunID = job.ID
			sc.LastRunAt = now.UTC().Format(time.RFC3339)
			sc.BlockedReason = ""
			log.Printf("schedule %s launched job %s (%s)", sc.ID, job.ID, sc.Playbook)
		}
		sc.Status = scheduleStatus(&sc)
		_ = m.Store.SaveSchedule(sc)
	}
}
