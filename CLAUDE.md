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
6. Real screenshots in `docs/screenshots/` taken from the running app
   (playwright is available; never fake screenshots)

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
./pine serve --demo --data .pine   # manual check on :8743
```
