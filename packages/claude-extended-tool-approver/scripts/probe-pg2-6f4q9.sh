#!/usr/bin/env bash
# probe-pg2-6f4q9.sh — verification probe for bead pg2-6f4q9: the pre-subcommand
# `-c` / `--config-env` screen becomes a FLOOR instead of a pre-classify
# short-circuit that REPLACED every verdict.
#
# Builds the hook binary from the CURRENT worktree source and prints the RAW emitted
# hook output for each probe command. The raw output is the point: a decision-only
# reading cannot tell `{}` (abstain — claude-code decides) from a missing key, and
# `{}` vs `deny` IS this bead.
#
# WHAT TO LOOK FOR. Every block prints the command WITHOUT the `-c` immediately
# before the same command WITH it, because the acceptance criterion is a RELATION and
# not a set of literals:
#
#   - DECISIVE rows: the pair MUST MATCH. Before this bead the `-c` row was `{}`
#     while the bare row was `deny` — an irrelevant config pair (`user.name=x`)
#     laundered a hard Reject into an auto-approvable non-decision.
#   - APPROVE-CLASS rows: the pair MUST DIFFER — bare `allow`, `-c` `{}`. That is the
#     RCE guard pg2-b3eow wrote and this bead does not soften.
#   - NOT-CLASSIFIED rows (`bisect start`, `clean --help`): the `-c` row MUST be `{}`.
#     This is the arm a bare Approve-only demotion would have left unscreened, and
#     `git clean --help` is the one that would have gone to `allow` — safecmds
#     approves `git <sub> --help` as a man-page read, and git spawns the caller's
#     pager to page it.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows
# land in a scratch asks.db and never reach the real corpus (pg2-cbihz).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-6f4q9-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-6f4q9-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-70s -> %s\n' "$cmd" "$(printf '%s' "$out" | jq -c '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
}

# pair <subcommand> — the bare command, then the same command with an UNCLEARED `-c`.
pair() {
  local sub="$1" key="${2:-user.name=x}"
  probe "git $sub"
  probe "git -c $key $sub"
}

echo "=== THE FOUR MEASURED SHAPES: each pair MUST now MATCH (was {} vs deny) ==="
pair 'push --force origin main'
pair 'tag v1'
pair 'remote add upstream https://example.invalid/x.git' 'x=y'
pair 'config remote.origin.url https://evil.invalid/x.git' 'x=y'

echo
echo "=== THE REST OF THE DECISIVE RANGE: pairs MUST MATCH ==="
pair 'push -f origin main'
pair 'remote set-url origin https://evil.invalid/x.git'
pair 'config url.https://evil.invalid/.insteadOf https://github.com/'
pair 'config core.hooksPath /tmp/h'
pair 'config clean.requireForce false'

echo
echo "=== APPROVE CLASS: the guard still withdraws the approval — pairs MUST DIFFER ==="
pair 'status'
pair 'log --oneline -5'
pair 'commit -m msg'
pair 'add .'
pair 'push origin main'
probe 'git -c core.pager=EVIL log'
probe 'git -c core.pager="touch /tmp/pwned" log'
probe 'git --config-env=core.pager=X log'
probe 'git -C /repo -c core.pager=EVIL log'

echo
echo '=== NOT CLASSIFIED by this rule: the -c row MUST be abstain, NOT allow ==='
pair 'bisect start'
pair 'notes list'
pair 'clean --help' # the bare row is `allow` from safecmds; the -c row must not be
pair 'stripspace'
pair 'maintenance run'

echo
echo "=== ALREADY NON-DECISIVE: unchanged, both sides abstain ==="
pair 'clean -fdx'
pair 'reset --hard HEAD~1'
pair 'branch -D feat'
pair 'rebase -i HEAD~1'

echo
echo "=== NO SUBCOMMAND AT ALL: the refusal stands (nothing for a floor to sit under) ==="
probe 'git -c x=y'
probe 'git -c core.pager=EVIL'
probe 'git --config-env=core.pager=X'

echo
echo "=== pg2-arfw6 REGRESSION: a CLEARED pair still relaxes and still cannot upgrade ==="
pair 'status' 'core.fsmonitor=false'
pair 'diff' 'core.fsmonitor=false'
pair 'push --force origin main' 'core.fsmonitor=false'
pair 'tag v1' 'core.fsmonitor=false'
probe 'git -c core.fsmonitor=false -c core.pager=EVIL log'
