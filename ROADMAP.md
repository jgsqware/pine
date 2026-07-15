# Pine roadmap

Feature checklist from the "what AWX is missing" brainstorm.
Status: ✅ done · 🚧 in progress · ⏳ planned · 🔗 blocked by another phase

## Security hardening (audit sprint 0)

- [x] ✅ **API token auth** — `--token` / `PINE_TOKEN` gate on every `/api/`
      request (Bearer / `X-Pine-Token` header, `?token=` for SSE); web UI prompts
      and remembers it.
- [x] ✅ **Secure-by-default bind** — `serve`/local bind `127.0.0.1` by default;
      a non-loopback bind refuses to start without a token (or `--insecure`).
- [x] ✅ **CSRF protection** — cross-origin state-changing requests rejected.
- [x] ✅ **Git transport allowlist** — only https/http/git/ssh URLs cloned,
      transport-helper syntax (`ext::`/`fd::`) blocked + `GIT_ALLOW_PROTOCOL`
      enforced (closes an unauthenticated RCE).
- [x] ✅ **Secret-leak fixes** — `/lineage` and `/sync` now redact like
      `/resolve`; secret-key heuristic covers `passphrase` and the `vault_`
      convention; symlinks in the raw-file endpoint are confined to the workdir;
      data dir written `0600`/`0700`.
- [ ] ⏳ **RBAC / SSO / audit log** — per-user roles and an audit trail (needed
      for multi-user/enterprise; sprint 3).

## Robustness (audit sprint 1)

- [x] ✅ **Inter-process store lock** — Open() takes an exclusive flock on the
      data dir, so a second Pine on the same `--data` fails fast instead of
      corrupting the JSON store (points to `pine attach`).
- [x] ✅ **Bounded job concurrency** — a worker pool caps concurrent
      ansible-playbook runs (default 4, `PINE_MAX_JOBS`); a burst of due
      schedules queues instead of spawning unbounded processes.
- [x] ✅ **Boot reconciliation** — jobs left running/pending by a crashed or
      restarted process are marked failed at startup (their in-memory run is
      gone), so they don't linger "running" forever.
- [x] ✅ **Carry vars through Re-run** — jobs persist their non-secret extra
      vars so a Re-run prefills them (secrets dropped, vault never stored).
- [ ] ⏳ **Ansible-precedence fixes** — vars_files vs play vars order, role
      `vars/main.yml` in the plan engine, exact `RoleRef` in hygiene/impact,
      and `!`/`&` host patterns (from the audit).

## Quick wins (engine already exists)

- [x] ✅ **1. Variable lineage** — "where does this value come from?":
      full precedence chain per host × variable (role default → group_vars
      parents-first → host_vars → magic), shown in the inventory host panel.
      `GET /api/repos/{id}/lineage?inventory=…&host=…`
- [x] ✅ **2. Dead-code detection** — unused roles, never-notified handlers,
      unused vars (best effort), hosts targeted by no playbook.
      Part of `GET /api/repos/{id}/hygiene`, "Hygiene" page.
- [x] ✅ **2b. Task-level smells** — command-instead-of-module, unnamed tasks,
      `ignore_errors: true`, `shell` without `changed_when`, bare `include:`,
      Jinja-wrapped `when:`, `state: latest`; grouped by rule with a count and
      folded into the score. In `GET …/hygiene`, the "Hygiene" page, and the new
      `pine hygiene` CLI (exit 4 on plaintext creds). Validated on messy
      real-world repos (streisand: 110 unnamed, 104 no-changed_when, …).
- [x] ✅ **3. Run diff** — compare two jobs of the same playbook: per
      task × host status transitions (ok→changed, ok→failed, new/removed
      tasks). `GET /api/jobs/{id}/diff?with=…`, view in job detail.
- [x] ✅ **Guide (repo explainer)** — a per-repo onboarding page composed from
      the scan: playbook **tiers** (grouped by directory) with each playbook's
      resolved target hosts / roles / flags, a role catalog with "used-by"
      cross-refs, detected **entry points** (`run.sh`, `site.yml`, `ansible.cfg`,
      docs), a hygiene-derived **"what you can / can't do"** list, and the repo's
      own README rendered inline. Pure projection of the scan (honest — no guessed
      prose). `GET /api/repos/{id}/overview`, "Guide" page (top of Overview) +
      `pine overview PATH`.
- [x] ✅ **Auto-describe (Claude Code)** — when the `claude` CLI is on the host,
      the Guide's **Generate descriptions** action runs a Claude Code session that
      writes a one-line description for every playbook (committed `pine.yml`
      sidecar, read back by the scanner) and role (`meta/main.yml`
      `galaxy_info.description`). Dry-run by default; Apply writes for git review.
      Honours the user's existing `claude` login; hidden when the CLI is absent.
      `POST /api/repos/{id}/describe`, `pine describe PATH [--write]`.

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
      Partly superseded for read-only use by probes (below): most "let me
      just check something" needs never justified a shell.
- [x] ✅ **12. Read-only probes** — observe a host without SSH, from a fixed
      catalog of vetted module+argv pairs. `GET /api/probes`,
      `POST /api/repos/{id}/probes`, `pine probe list|run`. Safety is
      structural: no command string is accepted, and `become` is forced off
      with `-e ansible_become=false` so a repo's `ansible.cfg` cannot
      silently escalate a probe to root.
      *Remaining: web UI, TUI, website, screenshots.*
- [x] ✅ **11. Secrets hygiene** — plaintext password-like values in vars,
      vault usage inventory. Part of the hygiene report.
- [ ] ⏳ **13. Honor `ansible.cfg` `inventory =`** — parse the repo-local
      `[defaults] inventory =` directive to locate inventories by their
      declared path. Would additionally catch arbitrarily-named *extension-less
      INI* inventories, which content-sniffing cannot (INI never parses as a
      YAML map). Extension-less *YAML* inventories are already discovered by
      content.

## State machine / GitOps — counter-analysis outcomes

From `docs/design/state-machine-counter-analysis.md` (theory + 3 spikes,
2026-07-12). Terraform-style *authoritative* state, destroy and import are
**abandoned permanently** — structurally impossible on Ansible; the doc is
the record of why. What survives, in dependency order:

- [ ] ⏳ **Fingerprint v2** — hash resolved module-args digest + per-host
      connection identity, drop the raw task name. Closes the two
      demonstrated fail-open gaps (version bump, host repoint) and the
      rename false block (`spikes/fingerprint-stability`). Referenced
      file/template *content* stays a documented blind spot unless content
      digests are added (decide then).
- [ ] ⏳ **Check-reliability badges** — classify every task
      honest/blind/forced/overridden (KB from `spikes/checkmode-liars`),
      badge plan & drift views, compute a per-playbook **drift-trust
      score**, and refuse drift scans on playbooks containing
      `check_mode: false` tasks (a scan must never mutate hosts).
- [ ] ⏳ **Observed-state file** — persist host × task observations
      (status, observed-at, run id) from existing job logs; staleness
      alarms for check-blind tasks ("last observed N days ago" is the only
      honest signal). Labeled *observed*, never authoritative.
- [ ] 🔗 **Drift-driven schedules** — reconcile on trusted drift instead of
      a timer, gated on: idempotence certification (2 consecutive
      zero-changed *user-initiated* runs) ∧ zero forced tasks ∧ trigger
      counted on check-honest tasks only ∧ oscillation breaker (3 cycles →
      freeze + notify). Blocked by the three items above.

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
- [x] ✅ Version-manager-aware ansible — Pine finds `ansible`/`ansible-playbook`/
      `ansible-vault` installed via mise/asdf/pipx even under a minimal
      (systemd/cron) PATH, by augmenting PATH with the common shim/bin dirs
      (mise & asdf shims, `~/.local/bin`, …) and running the tools with it;
      `PINE_TOOL_PATH` adds extra dirs. No more false "simulation mode".
- [x] ✅ Service status — hosts × services heatmap from the `services:` var,
      real running/stopped state via ansible `service_facts` (tri-state, honest
      `unknown`/`estimated`), plus status pills on inventory hosts
- [x] ✅ Git worktrees — list the working trees of a connected repo (main +
      linked, branch/HEAD, locked/prunable flags) under each repo on the
      Repositories page, with one-click **switch** to open a worktree's branch
      as the active repo; also the CLI (`pine worktrees`) and the REST API
      (`GET /api/repos/{id}/worktrees`)
- [x] ✅ Grouped playbook browser — playbooks listed as compact rows grouped
      by project (directory), with a live filter over name/path/host/tag and
      click-to-filter host & tag chips (replaces the old tile grid)
- [x] ✅ Inline `import_tasks` — static `import_tasks` are resolved at scan time
      (recursively, with cycle protection) and pulled into the playbook task-flow
      visualization in place; dynamic `include_tasks` stay a clickable reference
- [x] ✅ `pine lineage --playbook` — resolve a playbook's effective variables per
      host (expanding `import_tasks`/`import_playbook` and applying `include_vars`
      in Ansible order), in the same JSON/lineage shape with `include_vars`
      provenance; surfaces per-service config (e.g. `dedicated.yaml`) that plain
      inventory lineage misses. The web resolver picks up `include_vars` too.
- [x] ✅ Inline variable resolution — `{{ vars }}` in task names/args are resolved
      in the task-flow (and Plan args), host-agnostically by default with a
      "resolve as" host picker; each variable links to its lineage, unresolved
      vars stay raw, secrets are redacted (`GET /api/repos/{id}/resolve`).
      Covers role `vars/main.yml`, `include_role`/`import_role` roles (not just
      role defaults + the `roles:` list), `vars_prompt` defaults, and
      `{{ playbook_dir }}`-templated `vars_files`; nested values are expanded
      (`{{ monitoring_dir }}/alloy` → the composed path, facts left intact);
      `{{ item }}` in a loop shows the
      possible items instead of the placeholder. Unresolved vars are triaged
      runtime / defined-elsewhere / **defined-nowhere** (red); each play box has a
      **Variables panel docked beside its tasks** listing every variable it uses
      (resolved + unresolved, names coloured by state, with a colour legend, a
      "defined nowhere" count and an **All / Used-here filter**) with lineage; the
      panel is **resizable** (drag handle) and docked beside the tasks; the
      "resolve as" host picker (hosts the playbook **targets** via its `hosts:`
      pattern are grouped first; hosts with variable variation flagged); notify
      chips are **click-to-scroll** to their handler
      **highlights hosts that override a variable the playbook uses**
- [x] ✅ Syntax-highlighted source preview — the raw-file / "View YAML" pane
      highlights YAML and INI (keys, strings, numbers, booleans, comments,
      `{{ jinja }}`) with a tiny dependency-free tokenizer (no build, no CDN)
- [x] ✅ Plan variable resolution — the estimated plan resolves `vars_prompt`
      variables (their default, or a value provided in the run/plan modal),
      expands nested `{{ var }}` references, and follows `include_vars` — so task
      names/args/conditions template as they would at run time (e.g. a prompted
      `docker_path` no longer shows raw). vars_prompt fields are surfaced in the
      run/plan modal.
- [x] ✅ Vault-aware plans & runs — vault-encrypted variables the playbook uses
      are detected and listed (in the modal and the plan view); providing the
      ansible-vault password decrypts them for that plan via the ansible-vault
      CLI (transient, never stored). Undecrypted vault values are masked
      (`***vault***`) so raw blobs never leak into resolved names/args.
      `GET /resolve` and the plan report expose `vault_vars`. **Apply (run)**
      carries the password through to `ansible-playbook --vault-password-file`
      (and the resolved vars_prompt/extra vars via `-e @file`), all written to
      temp files removed after the run and never persisted to the job. A repo can
      also **store a default vault password** (Repositories → Settings) that
      plans and runs fall back to — persisted server-side, redacted out of every
      API read (`has_vault_password` marker only).
- [x] ✅ Per-repo SSH host-key checking — a repo setting ("" respect ansible.cfg
      / accept-new / disabled) applied to runs and exact plans via
      `ANSIBLE_HOST_KEY_CHECKING` / `ANSIBLE_SSH_EXTRA_ARGS`, so SSH password auth
      against hosts not yet in known_hosts can be unblocked explicitly.
- [x] ✅ Job output polish — task `msg` outputs (debug, failures) are extracted
      from the run log into a **Messages panel** above it (host + task context,
      coloured by ok/changed/failed, multi-line preserved); the PLAY RECAP host
      line is **colour-coded** per counter (ok green, changed amber, failed/
      unreachable red, zeros dimmed). Job detail also has a **Re-run** button.
- [x] ✅ Click-to-open source files — a task that reads a local file/template
      (`template`/`copy`/`unarchive`/`assemble`/`script`) gets an **open** link
      on its `src:`; a `{{ templated }}` src is resolved against the current vars
      first, so it points at the real file (tried in templates/ or files/), shown
      in the highlighted preview. `import_tasks`/`include_*` paths were already
      clickable
