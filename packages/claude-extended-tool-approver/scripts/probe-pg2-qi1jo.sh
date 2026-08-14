#!/usr/bin/env bash
# probe-pg2-qi1jo.sh — verification probe for bead pg2-qi1jo: the ALTERNATE-TRANSPORT
# family, settled under ONE ruling.
#
# TWO INSTRUMENTS, BOTH RUN HERE, and the split matters:
#
#   PART A — REAL GIT. Points each key at a marker script and reports whether git
#     EXECUTED it. This is the evidence the ruling rests on, and it is what refutes
#     pg2-szadj's "server-side path this workflow does not use" reading for two of the
#     keys: `remote.<n>.uploadpack` and `remote.<n>.receivepack` are run by the side
#     that FETCHES and the side that PUSHES — this machine — whenever the remote is a
#     local path, which is the everyday shape in a `pn` workforest.
#
#   PART B — THE HOOK. Prints the emitted verdict for every spelling, so the gating and
#     its boundary are visible. `{}` (abstain) is NOT `allow`, and a decision-only
#     reading cannot tell `{}` from a missing key.
#
# ONE READING NEEDED A SECOND INSTRUMENT AND IS WORTH READING TWICE.
# `uploadpack.packObjectsHook` set in the SERVED REPO's own config does NOT run — git
# documents ignoring it at repository level "as a safety measure against fetching from
# untrusted repositories" — so a first pass reads as "not a sink". Set in GLOBAL config
# it RUNS, and GLOBAL is exactly what `git config --global` writes. A no-sink reading at
# one config LEVEL is not evidence about the KEY.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows land
# in a scratch asks.db and never reach the real corpus (pg2-cbihz). Part A runs entirely
# inside throwaway repos with GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM neutralised, so it
# cannot read or write this machine's git configuration.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-qi1jo-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

############################  PART A — REAL GIT  ##############################
git --version

marker="$work/marker.sh"
cat >"$marker" <<'EOF'
#!/bin/sh
printf 'MARKER RAN: %s\n' "$*" >> "$MARKER_LOG"
exit 1
EOF
chmod +x "$marker"
MARKER_LOG="$work/log.txt"
export MARKER_LOG
: >"$MARKER_LOG"

export GIT_CONFIG_GLOBAL="$work/gitconfig" GIT_CONFIG_SYSTEM=/dev/null
: >"$GIT_CONFIG_GLOBAL"
repo="$work/r"
mkdir -p "$repo"
git -C "$repo" init -q .
echo hi >"$repo/f"
git -C "$repo" add f
git -C "$repo" -c user.email=p@i -c user.name=p commit -q -m init
bare="$work/bare.git"
git init -q --bare "$bare"

sink() {
  local label="$1"
  shift
  local before after
  before="$(wc -l <"$MARKER_LOG")"
  "$@" >/dev/null 2>&1
  after="$(wc -l <"$MARKER_LOG")"
  if [ "$after" -gt "$before" ]; then
    printf '  %-40s SINK: YES  ' "$label"
    tail -n "$((after - before))" "$MARKER_LOG" | head -1
  else
    printf '  %-40s SINK: no\n' "$label"
  fi
}

echo
echo '=== A1. core.gitProxy — the unauthenticated git:// transport ==='
sink 'core.gitProxy / ls-remote git://' git -C "$repo" -c core.gitProxy="$marker" ls-remote git://invalid.invalid/x.git

echo '=== A2. remote.<n>.uploadpack — the FETCHING side (this machine) runs it ==='
sink 'remote.loc.uploadpack / ls-remote' git -C "$repo" -c remote.loc.url="$repo" -c remote.loc.uploadpack="$marker" ls-remote loc
sink 'remote.loc.uploadpack / fetch' git -C "$repo" -c remote.loc.url="$repo" -c remote.loc.uploadpack="$marker" fetch loc

echo '=== A3. remote.<n>.receivepack — the PUSHING side runs it ==='
sink 'remote.b.receivepack / push' git -C "$repo" -c remote.b.url="$bare" -c remote.b.receivepack="$marker" push b HEAD:refs/heads/x

echo '=== A4. uploadpack.packObjectsHook — repo level IGNORED, GLOBAL level RUNS ==='
git -C "$repo" config uploadpack.packObjectsHook "$marker"
sink 'packObjectsHook, repository level' git -c protocol.file.allow=always clone -q --no-local "$repo" "$work/c1"
git -C "$repo" config --unset uploadpack.packObjectsHook
git config --global uploadpack.packObjectsHook "$marker"
sink 'packObjectsHook, GLOBAL level' git -c protocol.file.allow=always clone -q --no-local "$repo" "$work/c2"
git config --global --unset uploadpack.packObjectsHook

echo '=== A5. protocol.<n>.allow — the loosening IS the difference ==='
sink 'ext:: WITHOUT protocol.ext.allow' git -C "$repo" ls-remote "ext::$marker %S"
sink 'ext:: WITH protocol.ext.allow=always' git -C "$repo" -c protocol.ext.allow=always ls-remote "ext::$marker %S"
sink 'ext:: WITH GIT_ALLOW_PROTOCOL=ext' env GIT_ALLOW_PROTOCOL=ext git -C "$repo" ls-remote "ext::$marker %S"

############################  PART B — THE HOOK  ##############################
set -eo pipefail
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1"
  local out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-qi1jo-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-70s -> %s\n' "$cmd" "$(printf '%s' "$out" | jq -c '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"')"
}

echo
echo '=== B1. THE NEWLY GATED WRITES: the four sinks (were allow, now ask) ==='
probe 'git config core.gitProxy /tmp/evil'
probe 'git config --global core.gitProxy /tmp/evil'
probe 'git config --unset core.gitProxy'
probe 'git config remote.origin.uploadpack /tmp/evil'
probe 'git config remote.origin.receivepack /tmp/evil'
probe 'git config uploadpack.packObjectsHook /tmp/evil'
probe 'git config --global uploadpack.packObjectsHook /tmp/evil'

echo
echo '=== B2. THE NEWLY GATED WRITE: the interlock (was allow, now ask) ==='
probe 'git config protocol.ext.allow always'
probe 'git config protocol.allow always'
probe 'git config --global protocol.ext.allow always'
probe 'git config protocol.file.allow always'

echo
echo '=== B3. THE ENV HALF: GIT_PROXY_COMMAND moves from allow to abstain ==='
probe 'GIT_PROXY_COMMAND=/tmp/evil git fetch origin'
probe 'GIT_PROXY_COMMAND=/tmp/evil git ls-remote git://invalid.invalid/x.git'
probe 'git -c core.gitProxy=/tmp/evil fetch origin'

echo
echo '=== B4. STILL DECLINED: the interlock env twin has no screen to join ==='
probe 'GIT_ALLOW_PROTOCOL=ext git ls-remote origin'
probe 'GIT_SSL_NO_VERIFY=1 git fetch origin'

echo
echo '=== B5. READS ARE NOT GATED: must stay allow ==='
probe 'git config --get core.gitProxy'
probe 'git config --get remote.origin.uploadpack'
probe 'git config --list'

echo
echo '=== B6. BOUNDARY: neighbours in the same sections must stay allow ==='
probe 'git config remote.origin.fetch +refs/heads/*:refs/remotes/origin/*'
probe 'git config remote.origin.prune true'
probe 'git config uploadpack.allowFilter true'
probe 'git config protocol.version 2'
probe 'git config core.gitProxyish /tmp/evil'
probe 'git fetch origin'
probe 'git ls-remote origin'
probe 'git commit -m "settle core.gitProxy + GIT_PROXY_COMMAND under one ruling (pg2-qi1jo)"'
