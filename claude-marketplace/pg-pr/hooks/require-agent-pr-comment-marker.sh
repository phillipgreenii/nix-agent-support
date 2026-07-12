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

# Only inspect Bash tool calls; other tools are out of scope. Claude Code
# delivers the tool name in the stdin JSON as `.tool_name` (there is no
# CLAUDE_TOOL_NAME env var), so read it from the same payload the command is
# read from below.
tool_name="$(printf '%s' "$payload" | jq -r '.tool_name // empty' 2>/dev/null || true)"
case "$tool_name" in
Bash) ;;
*)
  exit 0
  ;;
esac

# Extract the command from the stdin payload.
command="$(printf '%s' "$payload" | jq -r '.tool_input.command // empty' 2>/dev/null || true)"

if [ -z "$command" ]; then
  exit 0
fi

# Only intercept the write paths we care about.
case "$command" in
*"pg-pr comment add"* | *"pg-pr review post"* | *"pg-pr review submit"* | \
  *"gh pr comment"* | *"gh api"*"reviews"*"--method POST"*)
  # Match the agent marker (🤖 = U+1F916) by its raw UTF-8 bytes so the check is
  # locale-independent — `$'\U…'` degrades to the literal string "\U0001F916"
  # under a non-UTF-8 (C) locale parse, e.g. the nix build sandbox (see CLAUDE.md
  # status-line note). LC_ALL=C makes grep byte-oriented; the \x escapes are
  # always these bytes. Verified: `printf '\U0001F916' | xxd` -> f0 9f a4 96.
  if ! printf '%s' "$command" | LC_ALL=C grep -qF -- $'\xf0\x9f\xa4\x96'; then
    echo "Refusing PR-comment write without the agent marker (🤖)." >&2
    echo "The pg-pr CLI auto-applies the marker; reach for 'pg-pr comment add' / 'pg-pr review post' instead of invoking gh directly." >&2
    exit 2
  fi
  ;;
esac

exit 0
