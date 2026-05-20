#!/usr/bin/env bash
# require-agent-pr-comment-marker.sh
#
# PreToolUse hook. Defense-in-depth check: refuses to let a `pg-pr
# comment add` / `pg-pr review post` / `pg-pr review submit` invocation
# through without the agent marker (🤖) somewhere in the payload.
#
# The CLI itself already auto-applies the marker via
# internal/marker.Markerify, so a refusal here implies someone is
# bypassing the CLI (e.g., calling `gh pr comment` directly with an
# AI-authored body) — that's the precise case this hook exists to
# catch.

set -euo pipefail

# Claude Code PreToolUse hooks receive the tool call payload on stdin
# as JSON. We grep the raw bytes for the marker — a JSON-aware parse
# would be more precise but adds runtime deps; the simple approach is
# sufficient because the marker is unlikely to appear in unrelated
# fields.

payload="$(cat -)"

# Only inspect Bash tool calls; other tools are out of scope.
case "${CLAUDE_TOOL_NAME:-}" in
Bash) ;;
*)
  echo "$payload"
  exit 0
  ;;
esac

# Extract the command. CLAUDE_TOOL_INPUT may carry it as JSON.
command="$(printf '%s' "$payload" | jq -r '.tool_input.command // empty' 2>/dev/null || true)"

if [ -z "$command" ]; then
  echo "$payload"
  exit 0
fi

# Only intercept the write paths we care about.
case "$command" in
*"pg-pr comment add"* | *"pg-pr review post"* | *"pg-pr review submit"* | \
  *"gh pr comment"* | *"gh api"*"reviews"*"--method POST"*)
  if ! printf '%s' "$command" | grep -q $'\U0001F916'; then
    echo "Refusing PR-comment write without the agent marker (🤖)." >&2
    echo "The pg-pr CLI auto-applies the marker; reach for 'pg-pr comment add' / 'pg-pr review post' instead of invoking gh directly." >&2
    exit 2
  fi
  ;;
esac

echo "$payload"
exit 0
