#!/usr/bin/env bash
# Auto-rebuild the running Pine docker-compose stack when a build-relevant file
# changes. Wired as a PostToolUse hook (Edit|Write|MultiEdit); the hook payload
# JSON arrives on stdin. No-ops unless the `pine` service is already running, so
# it keeps a live stack in sync without ever spinning one up unasked.
set -euo pipefail

root="${CLAUDE_PROJECT_DIR:-$(pwd)}"
payload="$(cat)"
file="$(printf '%s' "$payload" | jq -r '.tool_input.file_path // empty')"
[ -n "$file" ] || exit 0

# Only react to files that are baked into the image.
rel="${file#"$root"/}"
case "$rel" in
  *.go | go.mod | go.sum | Dockerfile | docker-compose.yml | web/*) ;;
  *) exit 0 ;;
esac

cd "$root" || exit 0

# Only rebuild a stack that is already up.
if [ -z "$(docker compose ps -q pine 2>/dev/null)" ]; then
  exit 0
fi

# Serialize rebuilds so rapid successive edits don't launch concurrent builds.
exec 9>/tmp/pine-rebuild.lock
flock -n 9 || exit 0

if out="$(docker compose up -d --build pine 2>&1)"; then
  echo "pine container rebuilt and restarted ($rel)"
  exit 0
fi

echo "pine rebuild failed after editing $rel:" >&2
printf '%s\n' "$out" | tail -n 25 >&2
exit 2
