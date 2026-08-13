#!/usr/bin/env bash
# probe-pg2-arfw6.sh — verification probe for bead pg2-arfw6: the narrow
# pre-subcommand `-c` relaxation (operator spec S-1..S-8, 2026-07-28).
#
# Builds the hook binary from the CURRENT worktree source and prints the RAW
# emitted hook output for each probe command, not just a decision string. The raw
# output is the point on both sides of this bead:
#
#   - the RELAXED rows must emit `permissionDecision: "allow"` — the whole defect
#     was `git -c core.fsmonitor=false diff` emitting `{}` while the bare
#     `git diff` emitted `allow`;
#   - the GUARD rows must emit exactly `{}` — `-c core.pager=EVIL` is the RCE
#     class this guard exists for, and a decision-only probe cannot tell `{}`
#     from a missing key.
#
# WHY PAIRS AND NOT KEYS. `core.fsmonitor` is NOT an inert key: `git help config`
# says the variable "contains the pathname of the 'fsmonitor' hook command"
# whenever it is not set to a boolean, and `git -c core.fsmonitor=<script> status`
# was measured EXECUTING the script on git 2.54.0. So the allowlist is keyed on
# (key -> value predicate) pairs, and the `=/tmp/evil.sh` rows below are what a
# key-only allowlist would have cleared.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-arfw6-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-arfw6-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-62s -> %s\n' "$cmd" "$out"
}

echo "=== BASELINE: the bare read-only commands this bead must match ==="
for sub in status diff log show merge-base; do probe "git $sub"; done

echo
echo "=== S-2 RELAXED: the ONE allowlisted pair, boolean value -> must be allow ==="
for sub in status diff log show merge-base; do probe "git -c core.fsmonitor=false $sub"; done
probe 'git -c core.fsmonitor=true status'
probe 'git -c core.fsmonitor=0 status'
probe 'git -c core.fsmonitor=off status'

echo
echo "=== S-3 KEY CASE: git config keys are case-insensitive -> must be allow ==="
probe 'git -c core.FSMonitor=false status'
probe 'git -c CORE.FSMONITOR=false status'
probe 'git -c core.fsmonitor=FALSE status'

echo
echo "=== S-4 BARE KEY (no =): git reads it as boolean true -> must be allow ==="
probe 'git -c core.fsmonitor status'
probe 'git -c core.FSMonitor diff'

echo
echo "=== S-2 GUARD: a NON-boolean value is a pathname git EXECUTES -> must be {} ==="
probe 'git -c core.fsmonitor=/tmp/evil.sh status'
probe 'git -c core.fsmonitor= status'
probe 'git -c core.fsmonitor=truex status'
probe 'git -c core.fsmonitor=2 status'

echo
echo "=== S-1 CLOSEDNESS: an unlisted key is not cleared, boolean value or not -> {} ==="
probe 'git -c core.pager=EVIL log'
probe 'git -c core.pager=false log'
probe 'git -c core.editor=false log'
probe 'git -c core.hooksPath=false log'
probe 'git -c include.path=false log'
probe 'git -c user.name=false log'
probe 'git -c core.sneaky.fsmonitor=false log'

echo
echo "=== S-6 ALL-OR-NOTHING: one non-cleared -c abstains the whole command -> {} ==="
probe 'git -c core.fsmonitor=false -c core.pager=EVIL log'
probe 'git -c core.pager=EVIL -c core.fsmonitor=false log'
probe 'git -c core.fsmonitor=false -c core.fsmonitor=true log'

echo
echo "=== S-5 --config-env STAYS UNCONDITIONAL (value comes from the env) -> {} ==="
probe 'git --config-env=core.fsmonitor=SOMEVAR log'
probe 'git --config-env core.fsmonitor=SOMEVAR log'
probe 'git --config-env=core.pager=X log'

echo
echo "=== RCE REGRESSION GUARDS (git_test.go / git_chdir_test.go) -> {} ==="
probe 'git -c core.pager="touch /tmp/pwned" log'
probe 'git -C /repo -c core.pager=EVIL log'

echo
echo "=== S-7 RELAXATION ONLY: a cleared -c never upgrades a verdict ==="
echo "    (each row must match the SAME command without the -c)"
probe 'git push --force'
probe 'git -c core.fsmonitor=false push --force'
probe 'git tag v1'
probe 'git -c core.fsmonitor=false tag v1'
probe 'git reset --hard HEAD~1'
probe 'git -c core.fsmonitor=false reset --hard HEAD~1'
probe 'git clean -fdx'
probe 'git -c core.fsmonitor=false clean -fdx'
probe 'git commit -m msg'
probe 'git -c core.fsmonitor=false commit -m msg'
