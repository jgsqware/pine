// Spike: observed-state file + drift-driven reconcile loop — does the
// pillar-1/pillar-3 combination survive contact with Ansible's semantics?
//
// Feeds pillars 1 and 3 of docs/design/state-machine-counter-analysis.md.
//
// No ansible and no target hosts in this environment, so the spike runs
// the loop against a deterministic simulated world in which task
// properties (idempotence, check-mode behaviour) and out-of-band drift
// are injected on a script. That is the point: the loop's guardrails must
// be judged against *adversarial* semantics, which real repos only
// exhibit occasionally and unreproducibly.
//
// Prototyped pieces:
//   - state file: host × task observed record (status, observed_at tick,
//     run id) — explicitly "observed", never "desired achieved"
//   - certification: a playbook becomes reconcile-eligible after 2
//     consecutive real applies with zero changed tasks
//   - drift trigger: --check counting only check-honest tasks; playbooks
//     containing check_mode:false tasks are refused (a check would mutate)
//   - oscillation breaker: same task drifting 3 consecutive cycles stops
//     the loop and pages a human
//   - staleness: blind tasks never refresh their observation — age is the
//     only honest signal
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type checkBehaviour int

const (
	honest checkBehaviour = iota // predicts changes truthfully under --check
	blind                        // skipped under --check (command/shell)
	liar                         // always reports changed under --check
	forced                       // check_mode:false — RUNS during a check
)

type task struct {
	name       string
	check      checkBehaviour
	idempotent bool // real apply: only changes when world != desired
	desired    int
}

type playbook struct {
	name  string
	tasks []task
}

// world: host -> task name -> actual value. Out-of-band drift mutates it.
type world map[string]map[string]int

// observation mirrors what a real state file could hold per host × task.
type observation struct {
	Status     string `json:"status"` // ok | changed | unknown
	ObservedAt int    `json:"observed_at_tick"`
	RunID      string `json:"run_id"`
}

type stateFile struct {
	Semantics string                            `json:"semantics"` // guard against misuse
	Hosts     map[string]map[string]observation `json:"hosts"`
}

func newState() *stateFile {
	return &stateFile{
		Semantics: "observed from runs — NOT authoritative desired state",
		Hosts:     map[string]map[string]observation{},
	}
}

func (s *stateFile) record(host, task, status string, tick int, run string) {
	if s.Hosts[host] == nil {
		s.Hosts[host] = map[string]observation{}
	}
	s.Hosts[host][task] = observation{Status: status, ObservedAt: tick, RunID: run}
}

// apply executes the playbook for real; returns changed count per task.
func apply(pb playbook, w world, hosts []string, st *stateFile, tick int, run string) map[string]int {
	changed := map[string]int{}
	for _, h := range hosts {
		for _, t := range pb.tasks {
			if !t.idempotent || w[h][t.name] != t.desired {
				w[h][t.name] = t.desired
				changed[t.name]++
				st.record(h, t.name, "changed", tick, run)
			} else {
				st.record(h, t.name, "ok", tick, run)
			}
		}
	}
	return changed
}

// check simulates --check; honest tasks report truth, blind are skipped,
// liars always report changed, forced tasks MUTATE the world.
func check(pb playbook, w world, hosts []string, st *stateFile, tick int, run string) (reported map[string]int, mutations int) {
	reported = map[string]int{}
	for _, h := range hosts {
		for _, t := range pb.tasks {
			switch t.check {
			case honest:
				if w[h][t.name] != t.desired {
					reported[t.name]++
					st.record(h, t.name, "changed", tick, run)
				} else {
					st.record(h, t.name, "ok", tick, run)
				}
			case blind: // skipped: no observation refresh — goes stale
			case liar:
				reported[t.name]++
				st.record(h, t.name, "changed", tick, run)
			case forced:
				if w[h][t.name] != t.desired {
					w[h][t.name] = t.desired // side effect during a "check"!
					mutations++
				}
			}
		}
	}
	return reported, mutations
}

type loop struct {
	certified    map[string]int // consecutive zero-changed applies
	driftStreak  map[string]int // task -> consecutive drifting cycles
	frozen       map[string]string
	runSeq       int
}

func (l *loop) runID() string { l.runSeq++; return fmt.Sprintf("j%d", l.runSeq) }

func hasForced(pb playbook) bool {
	for _, t := range pb.tasks {
		if t.check == forced {
			return true
		}
	}
	return false
}

func honestTasks(pb playbook) (n int) {
	for _, t := range pb.tasks {
		if t.check == honest {
			n++
		}
	}
	return
}

func main() {
	hosts := []string{"web01", "web02"}
	scenarios := []struct {
		pb          playbook
		driftAt     map[int]string // tick -> task to drift out-of-band
		description string
	}{
		{playbook{"S1-idempotent", []task{{"pkg", honest, true, 1}, {"conf", honest, true, 1}}},
			map[int]string{3: "conf", 7: "conf"},
			"clean playbook + injected drift: loop should certify, detect, converge"},
		{playbook{"S2-nonidempotent", []task{{"restart", honest, false, 1}}},
			nil,
			"non-idempotent task: must never certify, loop stays display-only"},
		{playbook{"S3-check-liar", []task{{"pkg", honest, true, 1}, {"probe", liar, true, 1}}},
			nil,
			"liar task: without reliability filter the loop applies forever"},
		{playbook{"S4-blind-drift", []task{{"pkg", honest, true, 1}, {"script", blind, true, 1}}},
			map[int]string{4: "script"},
			"drift on a blind task: invisible to checks, only staleness remains"},
		{playbook{"S5-forced", []task{{"pkg", honest, true, 1}, {"probe", forced, true, 1}}},
			nil,
			"check_mode:false task: a drift scan would mutate hosts — refuse"},
	}

	st := newState()
	for _, sc := range scenarios {
		fmt.Printf("\n=== %s — %s\n", sc.pb.name, sc.description)
		w := world{}
		for _, h := range hosts {
			w[h] = map[string]int{}
		}
		l := &loop{certified: map[string]int{}, driftStreak: map[string]int{}, frozen: map[string]string{}}

		if hasForced(sc.pb) {
			fmt.Println("  tick 0: REFUSED — playbook contains check_mode:false tasks; a drift check would mutate hosts")
			continue
		}

		for tick := 1; tick <= 8; tick++ {
			if t, ok := sc.driftAt[tick]; ok {
				w[hosts[0]][t] = 99 // out-of-band change on web01
				fmt.Printf("  tick %d: [world] out-of-band drift injected on %s/%s\n", tick, hosts[0], t)
			}
			if len(l.frozen) > 0 {
				continue
			}

			certified := l.certified[sc.pb.name] >= 2
			if !certified {
				// bootstrap: real apply, count toward certification
				changed := apply(sc.pb, w, hosts, st, tick, l.runID())
				if len(changed) == 0 {
					l.certified[sc.pb.name]++
				} else {
					l.certified[sc.pb.name] = 0
				}
				fmt.Printf("  tick %d: apply (certification %d/2) changed=%v\n", tick, l.certified[sc.pb.name], changed)
				continue
			}

			// certified: drift-driven — check first, apply only on trusted drift
			rep, mut := check(sc.pb, w, hosts, st, tick, l.runID())
			if mut > 0 {
				fmt.Printf("  tick %d: check MUTATED the world %d times (bug: forced task slipped through)\n", tick, mut)
			}
			trusted := map[string]int{}
			for _, t := range sc.pb.tasks {
				if t.check == honest && rep[t.name] > 0 {
					trusted[t.name] = rep[t.name]
				}
			}
			untrusted := len(rep) - len(trusted)
			if len(trusted) == 0 {
				if untrusted > 0 {
					fmt.Printf("  tick %d: check reported %d change(s) but none from check-honest tasks — no reconcile (filtered)\n", tick, untrusted)
				} else {
					fmt.Printf("  tick %d: check clean — converged (honest coverage %d/%d tasks)\n", tick, honestTasks(sc.pb), len(sc.pb.tasks))
				}
				for t := range l.driftStreak {
					l.driftStreak[t] = 0
				}
				continue
			}
			for t := range trusted {
				l.driftStreak[t]++
				if l.driftStreak[t] >= 3 {
					l.frozen[t] = "oscillation: drifted 3 consecutive cycles"
				}
			}
			if len(l.frozen) > 0 {
				fmt.Printf("  tick %d: FROZEN, paging human: %v\n", tick, l.frozen)
				continue
			}
			changed := apply(sc.pb, w, hosts, st, tick, l.runID())
			fmt.Printf("  tick %d: drift %v -> reconcile apply, changed=%v\n", tick, trusted, changed)
		}

		// staleness audit: any observation older than 3 ticks
		for h, tasks := range st.Hosts {
			for tn, ob := range tasks {
				if 8-ob.ObservedAt > 3 {
					fmt.Printf("  audit: %s/%s STALE (last observed tick %d) — blind spot, age is the only signal\n", h, tn, ob.ObservedAt)
				}
			}
		}
		// truth audit: does the world actually match desired?
		for _, t := range sc.pb.tasks {
			for _, h := range hosts {
				if w[h][t.name] != t.desired {
					fmt.Printf("  audit: %s/%s world=%d desired=%d — UNRECONCILED drift survives\n", h, t.name, w[h][t.name], t.desired)
				}
			}
		}
		st = newState() // isolate scenarios
	}

	// dump a sample state file so the structure is concrete
	sample := newState()
	sample.record("web01", "nginx : install", "ok", 8, "j42")
	sample.record("web01", "app : deploy script", "unknown", 2, "j17")
	out, _ := json.MarshalIndent(sample, "", "  ")
	os.WriteFile("spikes/state-file/state.sample.json", append(out, '\n'), 0o644)
	fmt.Println("\nsample observed-state file written to spikes/state-file/state.sample.json")
}
