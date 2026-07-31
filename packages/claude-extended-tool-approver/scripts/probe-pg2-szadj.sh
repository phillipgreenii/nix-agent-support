#!/usr/bin/env bash
# probe-pg2-szadj.sh — reproduction / verification probe for bead pg2-szadj
# (`git config` writes to safety-interlock and execution-sink keys).
#
# Builds the hook binary from the CURRENT worktree source and asks it for a
# verdict on each probe command, printing `<decision>  <command>`.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe
# rows land in a scratch asks.db and never reach the real corpus.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-szadj-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-szadj-probe",cwd:"/tmp",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  decision="$(printf '%s' "$out" |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
  printf '%-7s %s\n' "$decision" "$cmd"
}

echo "=== DEFECT: safety-interlock / execution-sink config WRITES ==="
probe 'git config clean.requireForce false'
probe 'git config --global clean.requireForce false'
probe 'git config --type=bool clean.requireForce false'
probe 'git config core.hooksPath /tmp/h'

echo
echo "=== FLAG-POSITION INDEPENDENCE (all must be gated after the fix) ==="
probe 'git config --local clean.requireForce false'
probe 'git config --system clean.requireForce false'
probe 'git config --unset clean.requireForce'
probe 'git config --global --type=bool clean.requireForce false'
probe 'git config --global core.hooksPath /tmp/h'
probe 'git config --replace-all core.hooksPath /tmp/h'
probe 'git config -f .git/config core.hooksPath /tmp/h'

echo
echo "=== git 2.54 SUBCOMMAND form (key at the SECOND operand) ==="
probe 'git config set core.hooksPath /tmp/h'
probe 'git config set --global clean.requireForce false'
probe 'git config unset clean.requireForce'
probe 'git config get core.hooksPath'
probe 'git config list'

echo
echo "=== ANTI-BYPASS SIBLINGS ==="
probe 'git config pager.log /tmp/p'
probe 'git config core.editor /tmp/e'
probe 'git config filter.x.clean /tmp/c'
probe 'git config include.path /tmp/evil.cfg'
probe 'git config credential.helper /tmp/ch'
probe 'git config remote.origin.url https://evil.invalid/x.git'
probe 'git config url.https://evil.invalid/.pushInsteadOf https://github.com/'
probe 'git config CORE.HooksPath /tmp/h'

echo
echo "=== OTHER SURVEYED KEYS ==="
probe 'git config core.pager /tmp/p'
probe 'git config core.fsmonitor /tmp/m'
probe 'git config core.sshCommand /tmp/s'
probe 'git config diff.mine.textconv /tmp/t'
probe 'git config diff.external /tmp/d'
probe 'git config receive.denyCurrentBranch false'
probe 'git config http.sslVerify false'
probe 'git config url.https://evil.invalid/.insteadOf https://github.com/'

echo
echo "=== READS must stay allow ==="
probe 'git config --get user.email'
probe 'git config --list'
probe 'git config core.hooksPath'
probe 'git config clean.requireForce'
probe 'git config --get-regexp ^user'
probe 'git config --get core.hooksPath'

echo
echo "=== REGRESSION: ordinary config WRITES must stay allow ==="
probe 'git config user.email a@b.c'
probe 'git config commit.gpgsign true'
probe 'git config branch.main.remote origin'
probe 'git config x y'

echo
echo "=== REGRESSION: -c injection route (expect abstain) ==="
probe 'git -c clean.requireForce=false clean'

echo
echo "=== EXPECTED ask, unchanged by this bead ==="
probe 'git clean'
probe 'git clean -fdx'
probe 'git config clean.requireForce false && git clean'

echo
echo "=== REGRESSION: text-vs-parsed (config spelling as an ARGUMENT) ==="
probe 'bd comment pg2-szadj -m "git config clean.requireForce false measured allow"'
probe 'git commit -m "git config core.hooksPath is now gated (pg2-szadj)"'

echo
echo "=== REGRESSION: sibling-bead verdicts ==="
probe 'git push --force origin main'
probe 'git push origin :main'
probe 'git push --mirror origin'
probe 'git push --force-with-lease origin main:other'
probe 'git push --force-with-lease origin main'
probe 'git push https://example.invalid/x.git main'
probe 'git push /tmp/dst.git main'
probe 'git remote -v add upstream https://example.invalid/x.git'
probe 'git remote -v'
probe 'git branch -D feat'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
