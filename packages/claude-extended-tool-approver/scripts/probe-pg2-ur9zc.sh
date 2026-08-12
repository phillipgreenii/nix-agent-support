#!/usr/bin/env bash
# probe-pg2-ur9zc.sh — verification probe for bead pg2-ur9zc: `git reset --hard`
# returns Abstain instead of Ask (operator ruling, pg2-4yy4r item 4, 2026-07-30).
#
# Builds the hook binary from the CURRENT worktree source and prints the RAW
# emitted hook output for each probe command, not just the decision string. The
# raw output is the point: an Abstain must serialize to the empty object `{}`,
# and the failure this probe exists to detect is the same command coming back as
# `permissionDecision: "allow"` because a later rule in the chain re-approved the
# leaf. A decision-only probe cannot tell `{}` from a missing key.
#
# WHAT `{}` MEANS, and the consequence the operator accepted: claude-code decides
# — auto-approve mode, then settings pre-authorization, then the prompt. So in
# `default` permission mode a hard reset still prompts; in `auto` mode it runs
# unprompted. See docs/adr/0043-ceta-rule-verdict-vocabulary.md's Decision.
#
# WHY NO LATER RULE APPROVES IT: `git` is in the safecmds tables only in
# hasSubcommands (which covers `git <sub> --help` and `git help <sub>`), and is
# absent from alwaysSafe, safeReadCmds and safeWriteCmds. The two corroborating
# rows below — `git bisect start` and `git notes list` — already fell through the
# git rule's terminal Abstain before this bead and are recorded emitting `{}` in
# that ADR's Consequences.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-ur9zc-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# probe prints the raw emitted output alongside the command. The output is
# single-line JSON (or `{}`), so a fixed-width column stays readable.
probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-ur9zc-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-46s -> %s\n' "$cmd" "$out"
}

# ---------------------------------------------------------------------------
# THE RULING. Every row here MUST emit exactly `{}`.
# ---------------------------------------------------------------------------
echo "=== THE RULING: an ordinary hard reset emits {} (no prompt from this rule) ==="
probe 'git reset --hard'
probe 'git reset --hard HEAD~1'
probe 'git reset --hard origin/main'
probe 'git reset --hard && echo ok'

echo
echo "=== ABBREVIATIONS (pg2-os1kq): git parses these AS --hard, so same verdict ==="
probe 'git reset --har HEAD~1'
probe 'git reset --ha HEAD~1'
probe 'git reset --h HEAD~1'

echo
echo "=== CORROBORATION: leaves that already fell through to {} before this bead ==="
probe 'git bisect start'
probe 'git notes list'

# ---------------------------------------------------------------------------
# THE INVERSION THIS BEAD ALSO FIXED. Before pg2-ur9zc the `--hard` test ran
# BEFORE the redirected-context test. Both returned Ask, so the order was
# invisible; the moment `--hard` abstains, that order would give a redirected
# HARD reset the WEAKER `{}` while a redirected SOFT reset kept its always-
# prompting `ask`. The redirect test now runs FIRST, so both rows below ask.
# ---------------------------------------------------------------------------
echo
echo "=== REDIRECTED CONTEXT: still ask, for EVERY reset spelling ==="
probe 'GIT_DIR=/other git reset --hard HEAD~1'
probe 'GIT_DIR=/other git reset --har HEAD~1'
probe 'GIT_DIR=/other git reset --soft HEAD~1'
probe 'GIT_WORK_TREE=/other git reset --hard HEAD~1'

echo
echo "=== NOT WIDENED: the non---hard reset modes keep their allow ==="
probe 'git reset HEAD~1'
probe 'git reset --soft HEAD~1'
probe 'git reset --mixed HEAD~1'
probe 'git reset --keep HEAD~1'
probe 'git reset --merge HEAD~1'
probe 'git reset --no-hard HEAD~1'
probe 'git reset -- --hard'

echo
echo "=== ADJACENT ARMS UNTOUCHED: git clean (pg2-u0e0c, not yet ruled) still asks ==="
probe 'git clean -fd'
probe 'git clean --force'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
