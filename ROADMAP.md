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
- [x] ✅ **5. Continuous drift detection** — drift heatmap playbooks × hosts
      computed from the latest `--check` job per playbook ("changed" under
      check = reality diverges). `GET /api/repos/{id}/drift`,
      `POST …/drift/check`, "Drift" page.
- [x] ✅ **6. Plan-gated schedules** — recurring runs (interval-based) that
      refuse to execute when the current plan fingerprint differs from the
      approved one; approve to resume. `/api/schedules` CRUD + approve +
      run-now, "Schedules" page.
- [x] ✅ **7. Light pipelines** — chained playbooks with stop-on-failure
      (or continue), canary steps via --limit, and manual approval gates
      (waiting_approval → approve & continue). `/api/pipelines` +
      `/api/pipeline-runs`, "Pipelines" page.
- [x] ✅ **8. Estimated duration in plans** — record real per-task durations
      from job logs, surface `≈ Xmin` on plans and slowest-task insights.

## Fun / demo

- [x] ✅ **9. Topology time-lapse** — replay the repo's git history and
      animate the inventory topology commit by commit (deduplicated frames).
      `GET /api/repos/{id}/timelapse`, player on the Topology page.
- [ ] ⏳ **10. Web SSH console** — per-host terminal in the browser
      (the TUI already has `s`); xterm.js + websocket, vendored.
      *Deliberately last: needs real SSH targets to validate.*
- [x] ✅ **11. Secrets hygiene** — plaintext password-like values in vars,
      vault usage inventory. Part of the hygiene report.

## Earlier milestones (done)

- [x] ✅ Multi-repo + recursive scanning + per-repo scan paths
- [x] ✅ Smart inventory discovery (INI/YAML/extension-less, merged dirs)
- [x] ✅ Constructed-inventory emulation with badges
- [x] ✅ Plan mode phases 1–2 (tri-state eval, missing vars, fact profiles,
      what-if topology, web/TUI/CLI surfaces)
- [x] ✅ Plan mode phase 3 — fact harvesting ("[gather facts]" jobs via
      `ansible -m setup --tree` or simulated; facts feed plans, lineage
      and schedule fingerprints)
- [x] ✅ Plan mode phase 4 — exact mode via `ansible-playbook --check`
      with the JSON callback, rendered in the plan UI as mode "exact"
- [x] ✅ `pine attach` — terminal UI over the daemon's HTTP API, so a
      systemd/Docker instance can be driven from a shell without opening a
      second engine on the single-writer store (`pine tui` warns on conflict)
- [x] ✅ `pine service install|status|uninstall` — manage a systemd user unit
      for the daemon (auto-restart, optional linger) straight from the CLI
- [x] ✅ TUI auto-refresh — re-syncs connected repos on load and periodically,
      announcing what changed in the status bar
- [x] ✅ Service status — hosts × services heatmap from the `services:` var,
      real running/stopped state via ansible `service_facts` (tri-state, honest
      `unknown`/`estimated`), plus status pills on inventory hosts
