# Pine — project conventions

## Definition of done for a feature

A feature is not done until ALL of these are updated in the same change:

1. Engine + tests (`go test ./...` green, `go vet` clean)
2. REST API endpoint(s) documented in README.md's API table
3. Web UI (`web/`) and, where it makes sense, TUI (`internal/tui`) + CLI
4. **The presentation website (`website/`)** — every user-facing feature
   gets a section or an example on the site, in the same visual language
   (dark #0b0f0e, pine-green #4ade80, cyan #22d3ee, no build step, no CDN)
5. README.md feature list + ROADMAP.md status
6. Screenshots in `docs/screenshots/`. **Prefer real** captures from the
   running app (playwright is available). When the app genuinely cannot be
   launched (e.g. the sandbox blocks the network bind), a **faithful mock is
   allowed as a fallback**: render the *real* UI markup/CSS (same components,
   the actual dark #0b0f0e / pine-green #4ade80 / cyan #22d3ee visual language)
   populated with representative data (demo repo values or a documented
   example), rendered to an image via the headless Chrome in the puppeteer
   cache. Name mock files `*-mock.png` and note "rendered mock (app bind
   blocked in sandbox)" in the commit/PR so a reviewer can tell it apart and
   re-capture a live shot later. Never fabricate a screenshot that
   misrepresents how the app looks or invents behaviour it does not have.

## Auth / bind model

Pine's API executes `ansible-playbook` and `git`, so an unauthenticated
public bind is a remote-code-execution surface. `pine serve` binds
**loopback** (`127.0.0.1:8743`) by default — friction-free, no token. A
non-loopback bind (`:8743`, `0.0.0.0`, `::`, a LAN IP) is **refused**
unless a token is set (`--token` / `PINE_TOKEN`) or `--insecure` is passed
(`guardBind` in `cmd/pine/main.go`).

Any change to the bind/auth surface must keep these in sync:

- `pine serve` — the `guardBind` gate + `server.Config{Token}`.
- `pine service install` — bakes `Environment=PINE_TOKEN` into the systemd
  unit for a non-loopback bind (never on the ExecStart command line).
- Web UI (`web/`) — token in `localStorage["pine_token"]`, sent as the
  `X-Pine-Token` header and `?token=` on the EventSource stream.
- README auth/serve docs.

## Quality bar

- Honest engines: static analysis must never present a guess as a
  certainty (tri-state unknown + missing variables, "estimated" vs
  "exact" labels).
- Validate against the demo repo (`examples/demo-infra`) and, for scanner
  changes, against real-world repos (ansible-nas, ansible-for-devops,
  debops).
- The demo repo must keep exercising every feature the site showcases.

## Build & test

```bash
go build -o pine ./cmd/pine && go vet ./... && go test ./...
node --check web/app.js
./pine serve --demo --data .pine   # loopback 127.0.0.1:8743, no token needed
                                   # a non-loopback bind needs --token/PINE_TOKEN
```
