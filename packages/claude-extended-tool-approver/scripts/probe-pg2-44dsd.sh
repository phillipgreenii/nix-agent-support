#!/usr/bin/env bash
# probe-pg2-44dsd.sh — verification probe for bead pg2-44dsd: a read-only
# `gh api graphql` document is APPROVED instead of paying pg2-cl0v2's mutation Ask.
#
# WHY A BEFORE/AFTER PROBE. Every GraphQL call is an HTTP POST (measured — see api.go's
# spelling table), so pg2-cl0v2's method-keyed verdict Asked on a read-only query as
# readily as on a mutation. The whole claim of this bead is MOVEMENT on the read rows and
# NO MOVEMENT anywhere else, and a single-sided reading cannot show either. So each row is
# probed against BOTH a binary built from the pinned base commit and one built from the
# current worktree, and both decisions are printed.
#
# HOW TO READ IT:
#
#   READ rows      base `ask`    -> patched `allow`   (the fix)
#   WRITE rows     base `ask`    -> patched `ask`     (unchanged; a mutation must not slip)
#   OPAQUE rows    base `ask`    -> patched `ask`     (fail-safe: the document is not in argv)
#   TRAP rows      base `ask`    -> patched `allow`   (`mutation` as TEXT is not an operation)
#
# A `{}` output means ABSTAIN — claude-code decides — and is NOT the same as `allow`. No row
# here may print `{}`: that is the state an auto-approving session accepts silently.
#
# THE GLUED-QUOTE SECTION IS THE ONE THAT MATTERS MOST IN PRACTICE. Of the 576 measured
# `gh api graphql` rows in the asklog, 567 write the document as `-f query='…'` — a quoted
# segment glued to an unquoted `query=` prefix, which cmdparse does not unquote. If those
# rows do not move, the fix is inert on real traffic whatever the hand-written fixtures say.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows land in
# a scratch asks.db and never reach the real corpus (pg2-cbihz).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(git -C "$pkg_root" rev-parse --show-toplevel 2>/dev/null || true)"
# The commit the reported movement was measured against: wave 1's tip, the fork point of
# this bead's branch. Pinned rather than `main`, so the comparison keeps its meaning after
# this change lands on main.
base_ref="${CETA_PROBE_BASE:-a064a73e}"

work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-44dsd-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# CETA_PROBE_PATCHED_BIN / CETA_PROBE_BASE_BIN let a caller supply already-built binaries.
# That is not a convenience: this repo's beads are worked in BATCHES in one shared checkout,
# so `go build` over the whole package can fail on a SIBLING change still in flight, which
# would make this probe unrunnable for reasons that have nothing to do with the rows below.
patched="${CETA_PROBE_PATCHED_BIN:-}"
if [[ -z $patched ]]; then
  patched="$work/ceta-patched"
  (cd "$pkg_root" && go build -o "$patched" ./cmd/claude-extended-tool-approver)
fi

base="${CETA_PROBE_BASE_BIN:-}"
if [[ -z $base ]]; then
  if [[ -n $repo_root ]] && git -C "$repo_root" cat-file -e "${base_ref}^{commit}" 2>/dev/null; then
    mkdir -p "$work/base"
    git -C "$repo_root" archive "$base_ref" packages/claude-extended-tool-approver | tar -x -C "$work/base"
    base="$work/ceta-base"
    (cd "$work/base/packages/claude-extended-tool-approver" && go build -o "$base" ./cmd/claude-extended-tool-approver)
  else
    echo "NOTE: base ref '$base_ref' is unavailable here; printing the patched column only." >&2
  fi
fi

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

decide() {
  jq -cn --arg c "$2" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-44dsd-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$1" 2>/dev/null |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "{} (abstain)"'
}

probe() {
  local cmd="$1" b="n/a" p
  [[ -n $base ]] && b="$(decide "$base" "$cmd")"
  p="$(decide "$patched" "$cmd")"
  printf 'base=%-13s patched=%-13s %s\n' "$b" "$p" "$cmd"
}

echo "=== READ, GLUED-QUOTE SPELLING (567 of the 576 measured rows) -> ask BECOMES allow ==="
probe "gh api graphql -f query='{ viewer { login } }'"
probe "gh api graphql -f query='{ rateLimit { cost remaining resetAt } }'"
probe 'gh api graphql -f query='"'"'{ repository(owner:"cli",name:"cli"){ pullRequests(first:1){ nodes{ number } } } }'"'"''
probe 'gh api graphql -f query="{ viewer { login } }"'

echo
echo "=== READ, BARE SHORTHAND — the operation keyword is OPTIONAL -> allow ==="
probe 'gh api graphql -f query={viewer{login}}'
probe "gh api graphql -f 'query={ viewer { login } }'"

echo
echo "=== READ, EXPLICIT KEYWORD / NAMED / VARIABLES / DIRECTIVES -> allow ==="
probe "gh api graphql -f 'query=query { viewer { login } }'"
probe "gh api graphql -f 'query=query Me { viewer { login } }'"
probe "gh api graphql -f 'query=query(\$search: String!) { search(query: \$search, type: ISSUE, first: 50) { issueCount } }' -f search=foo"
probe "gh api graphql -f 'query=query @skip(if: false) { viewer { login } }'"
probe "gh api graphql -f 'query=query { a } fragment F on T { b }'"
probe "gh api graphql -f 'query={ viewer { login } }' -F variables=@vars.json"

echo
echo "=== THE pg2-5b901 TRAP: 'mutation' as TEXT is not an operation -> allow ==="
probe 'gh api graphql -f '"'"'query={ repository(owner:"o",name:"r") { mutation } }'"'"''
probe "gh api graphql -f 'query=query(\$mutation: String) { search(query: \$mutation, type: ISSUE) { issueCount } }'"
probe "gh api graphql -f 'query=# mutation
{ viewer { login } }'"
probe 'gh api graphql -f '"'"'query={ search(query: "mutation { x }", type: ISSUE) { issueCount } }'"'"''
probe "gh api graphql -f 'query=query mutation { viewer { login } }'"

echo
echo "=== GENUINE MUTATIONS -> ask, UNCHANGED (this is the half that must not move) ==="
probe 'gh api graphql -f query=mutation{}'
probe "gh api graphql -f 'query=mutation { addStar(input: {starrableId: \"x\"}) { clientMutationId } }'"
probe "gh api graphql -f 'query=query A { a } mutation B { b }'"
probe "gh api graphql -f 'query=subscription { x }'"

echo
echo "=== DOCUMENT NOT IN ARGV -> ask, the FAIL-SAFE the bead requires ==="
probe 'gh api graphql -F query=@q.graphql'
probe 'gh api graphql --field query=@q.graphql'
probe 'gh api graphql --input payload.json'
probe 'gh api graphql --input -'
probe 'gh api graphql --input payload.json -f query={viewer{login}}'
probe 'gh api graphql -f query=@q.graphql'

echo
echo "=== DOCUMENT DOES NOT SCAN -> ask ==="
probe 'gh api graphql -f foo=bar'
probe "gh api graphql -f 'query={ viewer { login }'"
probe "gh api graphql -f 'query=type Query { a: Int }'"
probe "gh api graphql -f 'query=fragment F on T { a }'"

echo
echo "=== NO MOVEMENT OUTSIDE graphql: the pg2-cl0v2 verdicts stand ==="
probe 'gh api repos/o/r/pulls/5'
probe 'gh api -X GET repos/o/r/pulls'
probe 'gh api --method PUT repos/o/r/pulls/5/merge'
probe 'gh api -X PATCH repos/o/r/pulls/5 -f draft=false'
probe 'gh api repos/o/r/issues -f title=x'
probe 'gh api -X DELETE repos/o/r/git/refs/heads/feat'
