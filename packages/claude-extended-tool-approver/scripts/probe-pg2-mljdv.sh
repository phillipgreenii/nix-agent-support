#!/usr/bin/env bash
# probe-pg2-mljdv.sh — POST-DEPLOY verification probe for bead pg2-mljdv: confirm
# pg2-cl0v2's `gh api` HTTP-method gate (merge PUT -> deny, other mutations -> ask,
# reads -> allow) is live in the INSTALLED/deployed ceta binary, not merely in a
# from-source build. pg2-cl0v2 itself was proven against a source build; until the
# home-manager generation that ships this fix is actually applied, the machine's
# real hook can still be running the old blanket-approve behavior.
#
# UNLIKE the sibling probe-pg2-*.sh scripts in this directory, this one does NOT
# build from source by default. The whole point is to exercise whatever binary is
# CURRENTLY WIRED UP as the live PreToolUse hook. Override with CETA_PROBE_BIN to
# point at a different binary (e.g. for a source-vs-deployed comparison).
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows
# land in a scratch asks.db and never reach the real corpus (pg2-cbihz).
set -euo pipefail

# Resolve the deployed hook binary the same way a real PreToolUse invocation would:
# via the user's PATH, falling through to the conventional home-manager profile
# location. CETA_PROBE_BIN overrides for deliberate comparisons.
default_bin="/etc/profiles/per-user/${USER:-$(id -un)}/bin/claude-extended-tool-approver"
bin="${CETA_PROBE_BIN:-}"
if [[ -z $bin ]]; then
  if command -v claude-extended-tool-approver >/dev/null 2>&1; then
    bin="$(command -v claude-extended-tool-approver)"
  elif [[ -x $default_bin ]]; then
    bin="$default_bin"
  else
    echo "ERROR: could not locate the installed claude-extended-tool-approver binary" >&2
    exit 1
  fi
fi

resolved="$(readlink -f "$bin" 2>/dev/null || echo "$bin")"
echo "=== BINARY UNDER TEST ==="
echo "invoked as : $bin"
echo "resolved to: $resolved"
echo -n "version    : "
"$bin" version 2>&1 || true
echo

work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-mljdv-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

decide() {
  jq -cn --arg c "$1" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-mljdv-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"'
}

pass=0
fail=0
divergent=0

# probe CMD EXPECTED [NOTE]
# EXPECTED: allow | ask | deny | abstain | "DIVERGENT:<expected>" (known-stale
# expectation superseded by a LATER landed bead; recorded, not scored as a failure).
probe() {
  local cmd="$1" expected="$2" note="${3:-}" actual
  actual="$(decide "$cmd")"
  if [[ $expected == DIVERGENT:* ]]; then
    local orig="${expected#DIVERGENT:}"
    printf 'DIVERGENT   expected(bead)=%-8s actual=%-8s %s%s\n' "$orig" "$actual" "$cmd" "${note:+  # $note}"
    divergent=$((divergent + 1))
  elif [[ $actual == "$expected" ]]; then
    printf 'PASS        expected=%-8s actual=%-8s %s\n' "$expected" "$actual" "$cmd"
    pass=$((pass + 1))
  else
    printf 'FAIL        expected=%-8s actual=%-8s %s%s\n' "$expected" "$actual" "$cmd" "${note:+  # $note}"
    fail=$((fail + 1))
  fi
}

echo "=== MUST DENY (was allow) ==="
probe 'gh api --method PUT repos/o/r/pulls/5/merge' deny
probe 'gh api -XPUT repos/o/r/pulls/5/merge' deny
probe 'gh api --method=PUT repos/o/r/pulls/5/merge' deny
probe 'gh api repos/o/r/pulls/5/merge --method PUT' deny 'flag after endpoint'
probe 'gh api repos/o/r/pulls/5/merge -X PUT' deny

echo
echo "=== MUST ASK (was allow) ==="
probe 'gh api -X PATCH repos/o/r/pulls/5 -f draft=false' ask
# pg2-h8h3f (landed AFTER this bead was filed, main ff7194ed..9e3bb00f) made the raw-API
# pull-request CREATE mirror the porcelain draft-first gate: draft absent/false is now
# Reject/deny, not Ask. These two rows are this bead's spec, verbatim -- recorded as
# DIVERGENT rather than FAIL, and filed as a follow-up bead per pg2-mljdv step 5.
probe 'gh api -X POST repos/o/r/pulls -f title=x' 'DIVERGENT:ask' 'superseded by pg2-h8h3f: draft absent -> deny'
probe 'gh api repos/o/r/pulls -f title=x' 'DIVERGENT:ask' 'superseded by pg2-h8h3f: draft absent -> deny (POST default, no -X)'
probe 'gh api graphql -f query=mutation{}' ask
probe 'gh api repos/o/r/issues --field body=x' ask
probe 'gh api repos/o/r/issues -F body=@file' ask
probe 'gh api repos/o/r/issues --raw-field body=x' ask
probe 'gh api repos/o/r/issues --input payload.json' ask

echo
echo "=== MUST STILL ALLOW (regression guards) ==="
probe 'gh api repos/o/r/pulls/5' allow
probe 'gh api -X GET repos/o/r/pulls' allow
probe 'gh api --method GET repos/o/r/pulls' allow
probe 'gh api -XGET repos/o/r/pulls' allow
probe 'gh api repos/o/r/pulls --jq .[].number' allow
probe 'gh api "repos/o/r/pulls?state=open&per_page=100"' allow
probe 'gh api --paginate repos/o/r/pulls' allow
probe 'gh api /repos' allow

echo
echo "=== TEXT, NOT AN OPERATION (the pg2-5b901 trap class) ==="
# A gh-flavored string inside an UNRELATED command's argument must not reach the gh
# rule at all -- it never sees isGhExecutable(pc.Executable)=="gh". Confirmed via
# CLAUDE_TOOL_APPROVER_TRACE=1: the gh module must not appear in the trace.
trap_probe() {
  local cmd="$1" out trace
  out="$(decide "$cmd")"
  trace="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-mljdv-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    CLAUDE_TOOL_APPROVER_TRACE=1 "$bin" 2>&1 1>/dev/null || true)"
  if printf '%s' "$trace" | grep -q '^TRACE gh '; then
    printf 'FAIL        gh rule fired on TEXT decision=%-8s %s\n' "$out" "$cmd"
    fail=$((fail + 1))
  else
    printf 'PASS        gh rule did not fire   decision=%-8s %s\n' "$out" "$cmd"
    pass=$((pass + 1))
  fi
}
trap_probe 'git commit -m "mentions gh api --method PUT repos/o/r/pulls/5/merge in prose"'
trap_probe 'bd comment pg2-xxxxx --text "would have been gh api --method PUT repos/o/r/pulls/5/merge"'

echo
echo "=== CONTROLS THAT MUST BE UNCHANGED ==="
probe 'gh pr merge' deny
probe 'gh pr merge --auto' abstain
# pg2-25oru (landed before this bead was filed, but not reflected in its text) made the
# draft-first PR gate mechanical on the porcelain: bare `gh pr create` (no --draft) is now
# Reject, not Ask. Recorded as DIVERGENT, not FAIL -- same stale-expectation class as the
# two pg2-h8h3f rows above.
probe 'gh pr create' 'DIVERGENT:ask' 'superseded by pg2-25oru: non-draft create -> deny'

echo
echo "=== SUMMARY ==="
echo "pass=$pass fail=$fail divergent=$divergent"
if ((fail > 0)); then
  exit 1
fi
