#!/usr/bin/env bash
# probe-pg2-a66hc.sh — post-apply verification for pg2-2ke04 (the
# dynamic-expansion READ guard) against the DEPLOYED claude-extended-tool-approver
# binary, plus a live pass/fail table for every case pg2-a66hc's own AC names.
#
# UNLIKE probe-pg2-2ke04.sh (which `go build`s from worktree source), THIS
# SCRIPT EXERCISES THE INSTALLED ARTIFACT — the one Claude Code actually shells
# out to on this machine. Resolution order for CETA_BIN mirrors
# probe-pg2-95hna.sh:
#
#   1. $CETA_BIN if the caller sets it explicitly.
#   2. /etc/profiles/per-user/$USER/bin/claude-extended-tool-approver (the
#      home-manager-managed per-user profile symlink into the nix store).
#   3. `command -v claude-extended-tool-approver` as a last-resort fallback.
#
# The script REFUSES to run against anything that does not resolve under
# /nix/store, and refuses anything that resolves inside this repo/worktree (a
# dev build would defeat the entire point of testing the deployed artifact).
#
# ASKLOG ISOLATION: XDG_DATA_HOME is exported to a throwaway directory for
# every invocation, so no probe row reaches the real corpus
# (~/.local/share/claude-extended-tool-approver/asks.db).
#
# Command coverage mirrors packages/claude-extended-tool-approver/scripts/probe-pg2-2ke04.sh
# plus the additional role-split regression guards pg2-a66hc's own bead body
# calls out explicitly (action_meta=$(jq ...) && echo hi, without &&, etc).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-a66hc-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

resolve_bin() {
  if [[ -n ${CETA_BIN:-} ]]; then
    echo "$CETA_BIN"
    return 0
  fi
  local candidate="/etc/profiles/per-user/${USER}/bin/claude-extended-tool-approver"
  if [[ -x $candidate ]]; then
    echo "$candidate"
    return 0
  fi
  candidate="$(command -v claude-extended-tool-approver || true)"
  if [[ -n $candidate ]]; then
    echo "$candidate"
    return 0
  fi
  return 1
}

bin="$(resolve_bin)" || {
  echo "FATAL: could not resolve a deployed claude-extended-tool-approver binary." >&2
  echo "Refusing to fall back to a fresh 'go build' — this probe's entire point is" >&2
  echo "to exercise the DEPLOYED artifact. Set CETA_BIN explicitly if it lives" >&2
  echo 'somewhere other than /etc/profiles/per-user/$USER/bin/.' >&2
  exit 2
}

resolved="$(readlink -f "$bin" 2>/dev/null || echo "$bin")"
if [[ $resolved != /nix/store/* ]]; then
  echo "FATAL: resolved binary '$bin' -> '$resolved' does not live under" >&2
  echo "/nix/store — it does not look like a deployed (nix-built) artifact." >&2
  exit 2
fi
if [[ $resolved == "$pkg_root"/* || $resolved == "$(cd "$pkg_root/../.." && pwd)"/* ]]; then
  echo "FATAL: resolved binary '$resolved' is inside this repo/worktree — that is" >&2
  echo "a dev build, not the deployed artifact. Refusing to probe it." >&2
  exit 2
fi

ver="$("$bin" version 2>/dev/null || echo unknown)"
echo "=== DEPLOYED BINARY UNDER TEST (pg2-a66hc) ==="
echo "path:     $bin"
echo "resolved: $resolved"
echo "version:  $ver"
echo

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"
echo "asklog isolation: XDG_DATA_HOME=$XDG_DATA_HOME (throwaway, removed on exit)"
echo

pass=0
fail=0
declare -a fail_rows=()

# probe EXPECTED CMD
#   EXPECTED: allow | ask | deny | abstain
probe() {
  local expected="$1" cmd="$2"
  local out decision
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-a66hc-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null || true)"
  if [[ $out == "{}" || -z $out ]]; then
    decision="abstain"
  else
    decision="$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // "abstain"' 2>/dev/null || echo "unparseable")"
  fi

  if [[ $decision == "$expected" ]]; then
    pass=$((pass + 1))
    printf 'PASS          expected=%-8s got=%-8s %s\n' "$expected" "$decision" "$cmd"
  else
    fail=$((fail + 1))
    fail_rows+=("expected=$expected got=$decision  cmd=[$cmd]")
    printf 'FAIL          expected=%-8s got=%-8s %s\n' "$expected" "$decision" "$cmd"
  fi
}

section() {
  echo
  echo "=== $1 ==="
}

# ===========================================================================
# MUST NOT allow — the bypass itself (bead's verbatim rows). Expect abstain.
# ===========================================================================
section "MUST NOT allow: single variable hop on a READ (bead verbatim rows)"
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; cat $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; head $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; xxd $F'
probe abstain 'F=/Users/phillipg/.aws/credentials; cat $F'
probe abstain 'FOO=~/.ssh/id_rsa; cat $FOO'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; cat "$F"'
probe abstain 'D=/Users/phillipg/.ssh; cat $D/id_rsa'

section "MUST NOT allow: whole safeReadCmds list, one variable hop"
for rc in cat head tail less more wc diff sort uniq awk jq tq xxd strings; do
  probe abstain "F=/Users/phillipg/.ssh/id_rsa; $rc \$F"
done

section "MUST NOT allow: other read surfaces sharing the read zone check"
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; sed -n 1p $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; yq . $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; gofmt -l $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; grep x $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; rg x $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; jar tf $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; bash -n $F'

section "MUST NOT allow: the xargs INNER command (same choke point via readPathIssue)"
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; find /tmp -maxdepth 0 | xargs cat $F'

section "KNOWN REMAINING HOLE (out of scope, resolved won't-fix by pg2-qmtsn): unchanged"
probe allow 'F=/Users/phillipg/.ssh/id_rsa; echo $F | xargs cat'

section "MUST still deny (unchanged)"
probe deny 'cat /Users/phillipg/.ssh/id_rsa'
probe deny 'cat $(echo /Users/phillipg/.ssh/id_rsa)'
probe deny 'cat `echo /Users/phillipg/.ssh/id_rsa`'
probe deny 'cat ${HOME}/.ssh/id_rsa'
probe deny 'cat $HOME/.ssh/id_rsa'

section "MUST be unchanged — write path (non-approving; guard already fired)"
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; rm $F'
probe abstain 'F=/Users/phillipg/.ssh/id_rsa; cp $F /tmp/x'

section "MUST still allow — role split (highest-risk regression surface)"
probe allow "awk '{print \$1}' file"
probe allow "sed 's/x\$//' file"
probe allow "jq -nc --arg a b '{a:\$a}'"
probe allow "action_meta=\$(jq -nc --arg a b '{a:\$a}') && echo hi"
probe allow "action_meta=\$(jq -nc --arg a b '{a:\$a}') echo hi"
probe allow 'ls $d'
probe allow 'git commit -m "cat $F ..."'
probe allow 'echo "cat $F"'

section "role split: additional program-operand controls (from probe-pg2-2ke04.sh)"
probe allow "awk '{print \$1}' /tmp/x"
probe allow "awk -F'\\t' '{print \$2}' /tmp/x"
probe allow "awk -v n=1 '{print \$n}' /tmp/x"
probe allow "jq '.count = \$c' /tmp/x.json"
probe allow "echo '{}' | jq --arg a b '{a:\$a}'"

section "regression: text-vs-parsed (the bypass as PROSE, never an operand)"
probe allow 'git commit -m "cat $F no longer auto-approves (pg2-2ke04)"'
# NOTE: probe-pg2-2ke04.sh's own probe list carries this SAME case spelled with
# a "-m" flag (`bd comment pg2-2ke04 -m "..."`), but that spelling is not a real
# `bd` invocation at all — `bd comment`'s actual syntax is a POSITIONAL body
# (`bd comment <id> "text"` / `--file` / `--stdin`; verified via `bd comment
# --help`, no `-m` flag exists) and cmdparse.SkipMessageArgs's bd exemption
# table (argflags.go messageFlags) correspondingly has no bd "-m" entry either
# — only --reason/--notes/--append-notes/--description/--acceptance/--design/
# --context/--title, plus the dedicated positional-body carve-out
# (bdCommentBodyIndex) that this realistic spelling exercises. The "-m" form
# measures `ask` (SkipMessageArgs leaves the flag+value alone, so the secrets
# rule scans the literal "id_rsa" substring in the value) — that is not a
# defect, it is an invalid probe command inherited from probe-pg2-2ke04.sh's
# purely-observational (non-asserting) probe() output; using the SAME spelling
# here would misreport a false divergence. The correct, CLI-accurate spelling
# is the positional form below, which correctly measures `allow`.
probe allow 'bd comment pg2-2ke04 "F=/Users/phillipg/.ssh/id_rsa; cat $F measured allow"'
probe allow 'echo "cat $F"'

section "regression: static read paths keep their verdict"
probe allow 'cat /tmp/x'
probe allow 'cat ./README.md'
probe allow 'head -20 /tmp/log.txt'
probe allow 'wc -l /tmp/x'
probe allow 'jq . /tmp/x.json'
probe allow 'grep -rn foo /tmp'
probe allow 'sed -n 1p /tmp/x'
probe allow 'cat'
probe allow 'ls -la /tmp'
probe allow 'echo hi'

section "EXPECTED COST: benign dynamic reads now abstain (measured by replay)"
probe abstain 'for f in *.go; do cat "$f"; done'
probe abstain 'cat $HOME/.zshrc'
probe abstain 'wc -l $f'

section "browsing stays exempt: dynamic path is not an exfiltration primitive here"
probe allow 'ls $d'
probe allow 'find $d -name x'

echo
echo "=== SUMMARY ==="
echo "pass: $pass"
echo "fail: $fail"
if [[ $fail -gt 0 ]]; then
  echo
  echo "--- FAILURES (deviations from pg2-a66hc's expectations — file each as its own bead) ---"
  for row in "${fail_rows[@]}"; do
    echo "$row"
  done
  exit 1
fi

echo
echo "All probes matched pg2-a66hc's expectations against the deployed binary."
