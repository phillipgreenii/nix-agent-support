#!/usr/bin/env bash
# require-agent-pr-comment-marker.sh
#
# PreToolUse hook. Defense-in-depth check that refuses a *direct* PR-comment /
# PR-review write (`gh pr comment` / `gh api ...reviews...POST`) whose body does
# not carry the pg-pr agent marker.
#
# The pg-pr CLI auto-applies the marker to every body it posts via
# internal/marker.Stamp, so `pg-pr comment add` / `pg-pr review post` /
# `pg-pr review submit` invocations are the trusted path and are allowed
# unconditionally — their typed command legitimately carries no marker (the
# marker lands on the comment body, not the command). A refusal here therefore
# means someone is bypassing the CLI (e.g. calling `gh` directly with an
# AI-authored body) — the precise case this hook exists to catch.
#
# Accepted marker == internal/marker.IsOurs: the canonical invisible HTML marker
# `<!-- pg-pr -->` OR the legacy visible glyph (🤖).
#
# LIMITATION (defense-in-depth, not a security boundary): the whole command
# string is grepped, so a marker appearing anywhere on the line (not in the gh
# body) is accepted. Closing that would need arg-level parsing; out of scope.

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

# Only intercept the *bypass* write paths — a direct `gh` PR-comment / review
# POST. The pg-pr subcommands are the trusted path (they auto-stamp the body via
# marker.Stamp) and are deliberately NOT intercepted, so they fall through to the
# unconditional `exit 0` below. Checked with a bypass pattern only.
case "$command" in
*"gh pr comment"* | *"gh api"*"reviews"*"--method POST"*)
  # Accept marker.IsOurs: the canonical invisible HTML marker (ASCII, so
  # locale-safe) OR the legacy visible glyph (🤖 = U+1F916). Match the glyph by
  # its raw UTF-8 bytes so the check is locale-independent — `$'\U…'` degrades to
  # the literal string "\U0001F916" under a non-UTF-8 (C) locale parse, e.g. the
  # nix build sandbox (see CLAUDE.md status-line note). LC_ALL=C makes grep
  # byte-oriented; the \x escapes are always these bytes. Verified:
  # `printf '\U0001F916' | xxd` -> f0 9f a4 96.
  if printf '%s' "$command" | grep -qF -- '<!-- pg-pr -->'; then
    exit 0
  fi
  if printf '%s' "$command" | LC_ALL=C grep -qF -- $'\xf0\x9f\xa4\x96'; then
    exit 0
  fi
  echo "Refusing direct PR-comment write without the pg-pr agent marker (<!-- pg-pr --> or 🤖)." >&2
  echo "The pg-pr CLI auto-applies the marker via marker.Stamp; use 'pg-pr comment add' / 'pg-pr review post' / 'pg-pr review submit' instead of invoking gh directly." >&2
  exit 2
  ;;
esac

exit 0
