# Pine roadmap

Feature checklist from the "what AWX is missing" brainstorm.
Status: ✅ done · 🚧 in progress · ⏳ planned · 🔗 blocked by another phase

## Quick wins (engine already exists)

- [x] ✅ **1. Variable lineage** — "where does this value come from?":
      full precedence chain per host × variable (role default → group_vars
      parents-first → host_vars → magic), shown in the inventory host panel.
      `GET /api/repos/{id}/lineage?inventory=…&host=…`
- [x] ✅ **2. Dead-code detection** — unused roles, never-notified handlers,
      unused vars (best effort), hosts targeted by no playbook.
      Part of `GET /api/repos/{id}/hygiene`, "Hygiene" page.
- [x] ✅ **3. Run diff** — compare two jobs of the same playbook: per
      task × host status transitions (ok→changed, ok→failed, new/removed
      tasks). `GET /api/jobs/{id}/diff?with=…`, view in job detail.

## Strong differentiators

- [x] ✅ **4. Blast radius on git diff** — changed files → impacted roles →
      playbooks → hosts → handlers. `GET /api/repos/{id}/impact?base=…&head=…`,
      "Impact" page + `pine impact` CLI for CI.
- [ ] ⏳🔗 **5. Continuous drift detection** — scheduled check-mode /
      harvested-facts diff, drift heatmap per host × role.
      *Blocked by plan-mode phase 3 (fact harvesting).*
- [ ] ⏳ **6. Plan-gated schedules** — scheduled runs that refuse to execute
      when the current plan differs from the last approved plan.
- [ ] ⏳ **7. Light pipelines** — chained playbooks with recap conditions,
      canary batch + health gate, manual approval gates.
- [x] ✅ **8. Estimated duration in plans** — record real per-task durations
      from job logs, surface `≈ Xmin` on plans and slowest-task insights.

## Fun / demo

- [ ] ⏳ **9. Topology time-lapse** — replay the repo's git history and
      animate the inventory topology commit by commit.
- [ ] ⏳ **10. Web SSH console** — per-host terminal in the browser
      (the TUI already has `s`); xterm.js + websocket, vendored.
- [x] ✅ **11. Secrets hygiene** — plaintext password-like values in vars,
      vault usage inventory. Part of the hygiene report.

## Earlier milestones (done)

- [x] ✅ Multi-repo + recursive scanning + per-repo scan paths
- [x] ✅ Smart inventory discovery (INI/YAML/extension-less, merged dirs)
- [x] ✅ Constructed-inventory emulation with badges
- [x] ✅ Plan mode phases 1–2 (tri-state eval, missing vars, fact profiles,
      what-if topology, web/TUI/CLI surfaces)
- [ ] ⏳ Plan mode phase 3 — fact harvesting from real runs
- [ ] ⏳ Plan mode phase 4 — exact mode via `ansible-playbook --check`
