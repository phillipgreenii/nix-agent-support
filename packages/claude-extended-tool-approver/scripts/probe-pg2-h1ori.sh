#!/usr/bin/env bash
# probe-pg2-h1ori.sh — verification probe for bead pg2-h1ori: `core.askPass` joins
# gatedConfigKeys as a configSink, so the PORCELAIN write is gated like its already-
# screened `-c` and `GIT_ASKPASS` spellings.
#
# THE DEFECT THIS MEASURES. pg2-6c85x screened `GIT_ASKPASS` on marker evidence (git
# 2.54.0, 2026-08-13: `git credential fill` ran the marker as the "Username for …"
# prompt — scripts/probe-pg2-6c85x.sh), and the `-c` route screens `core.askPass`
# because it is key-BLIND. The key itself was absent from `gatedConfigKeys`, so the ONE
# spelling that OUTLIVES the command was the approved one. Measured on main
# @ a064a73e, 2026-08-14:
#
#	GIT_ASKPASS=/tmp/evil git fetch origin        -> {}
#	git -c core.askPass=/tmp/evil fetch origin    -> {}
#	git config core.askPass /tmp/evil             -> allow   <- the persistent one
#
# WHAT TO LOOK FOR. The WRITE rows must be `ask` — the same verdict every other
# configSink write gets, since configGateResult derives it from the CLASS. The READ
# rows and the NEIGHBOUR rows must stay `allow`: the gate is on writes, and the key next
# door is not this key. Without the second half a blanket `core.*` gate would look
# identical here.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows land
# in a scratch asks.db and never reach the real corpus (pg2-cbihz).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-h1ori-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-h1ori-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-64s -> %s\n' "$cmd" "$(printf '%s' "$out" | jq -c '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
}

echo '=== THE NEWLY GATED SPELLING: every core.askPass WRITE (was allow, now ask) ==='
probe 'git config core.askPass /tmp/evil'
probe 'git config --global core.askPass /tmp/evil'
probe 'git config --local core.askPass /tmp/evil'
probe 'git config --system core.askPass /tmp/evil'
probe 'git config --add core.askPass /tmp/evil'
probe 'git config --replace-all core.askPass /tmp/evil'
probe 'git config --unset core.askPass'
probe 'git config --unset-all core.askPass'
probe 'git config set core.askPass /tmp/evil'
probe 'git config unset core.askPass'
probe 'git config -f .git/config core.askPass /tmp/evil'
probe 'git config --type=path core.askPass /tmp/evil'
probe 'git config CORE.AskPass /tmp/evil'
probe 'git config core.ASKPASS /tmp/evil'
probe 'git -C /tmp/repo config core.askPass /tmp/evil'

echo
echo '=== THE OTHER TWO SPELLINGS: unchanged, already screened ==='
probe 'GIT_ASKPASS=/tmp/evil git fetch origin'
probe 'GIT_ASKPASS=/tmp/evil git credential fill'
probe 'git -c core.askPass=/tmp/evil fetch origin'

echo
echo '=== CLASS AGREEMENT: the sibling configSink writes, for comparison ==='
probe 'git config core.pager /tmp/p'
probe 'git config core.sshCommand /tmp/s'
probe 'git config credential.helper /tmp/ch'

echo
echo '=== READS ARE NOT GATED: must stay allow ==='
probe 'git config --get core.askPass'
probe 'git config --get-all core.askPass'
probe 'git config core.askPass'
probe 'git config --list'

echo
echo '=== NEIGHBOURS: keys that merely LOOK like it must stay allow ==='
probe 'git config core.askPassword /tmp/evil'
probe 'git config askpass.core /tmp/evil'
probe 'git config user.askPass /tmp/evil'
probe 'git config core.autocrlf true'
probe 'git config user.email me@example.invalid'

echo
echo '=== TEXT IS NOT AN OPERATION (pg2-5b901): must stay allow ==='
probe 'git commit -m "gate core.askPass in gatedConfigKeys (pg2-h1ori)"'
probe 'git commit -m "git config core.askPass /tmp/evil measured allow before the fix"'
