#!/usr/bin/env bash
# probe-pg2-h8h3f.sh — verification probe for bead pg2-h8h3f: the raw-API pull-request
# create now MIRRORS the porcelain's draft-first verdict, because the `draft` body parameter
# is read as a VALUE instead of a presence boolean.
#
# WHAT THE HOLE WAS. pg2-25oru made the porcelain `gh pr create` draft-aware — Approve with
# `--draft`, Reject without — but deliberately did NOT follow it on the raw API, because
# parseGhAPICall could not tell `-f draft=true` from `-f draft=false` and a Reject on that
# reading would have refused the BLESSED create with no in-session override. So
# `gh api -X POST repos/o/r/pulls -f draft=false` held an Ask, and Ask is auto-accepted in an
# auto-approving session: draft-first was mechanical on the porcelain and only advisory here.
#
# HOW TO READ IT. The point is that each api row lands on the SAME decision as the porcelain
# row printed beside it — the movement is `ask -> allow` for a draft create and `ask -> deny`
# for a non-draft one, and BOTH directions matter. `{}` (abstain) must appear nowhere.
#
#   draft=true                base `ask`  -> patched `allow`  == `gh pr create --draft`
#   draft absent / =false     base `ask`  -> patched `deny`   == `gh pr create`
#   draft UNREADABLE          base `ask`  -> patched `ask`    (the named residual, see below)
#   graphql createPullRequest base `ask`  -> patched `ask`    (PINNED, not incidental)
#
# THE UNREADABLE ROWS ARE THE DELIBERATE RESIDUAL. `--input payload.json` and
# `-F draft=@file` put the value outside argv — measured — and `--input` also DEMOTES an argv
# `-f draft=true` to a query-string parameter while the body still comes wholly from the
# file, so an argv `-f` there describes something other than what is sent. With no readable
# value the choice is Reject (which would refuse a legitimate draft create) or the pg2-cl0v2
# Ask floor. It is the floor.
#
# ASKLOG ISOLATION: XDG_DATA_HOME is pointed at a throwaway directory so probe rows land in
# a scratch asks.db and never reach the real corpus (pg2-cbihz).
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(git -C "$pkg_root" rev-parse --show-toplevel 2>/dev/null || true)"
base_ref="${CETA_PROBE_BASE:-a064a73e}"

work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-h8h3f-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# See probe-pg2-44dsd.sh for why pre-built binaries may be supplied: these beads are worked
# in batches in one shared checkout, where a sibling change in flight can break `go build`
# for reasons unrelated to the rows below.
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
    '{hook_event_name:"PreToolUse",session_id:"pg2-h8h3f-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$1" 2>/dev/null |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "{} (abstain)"'
}

probe() {
  local cmd="$1" b="n/a" p
  [[ -n $base ]] && b="$(decide "$base" "$cmd")"
  p="$(decide "$patched" "$cmd")"
  printf 'base=%-13s patched=%-13s %s\n' "$b" "$p" "$cmd"
}

echo "=== THE PORCELAIN, for comparison (UNCHANGED by this bead) ==="
probe 'gh pr create --draft'
probe 'gh pr create --title x'
probe 'gh pr create --draft=false --title x'

echo
echo "=== draft=true -> allow, mirroring the porcelain draft create ==="
probe 'gh api -X POST repos/o/r/pulls -f draft=true -f title=x'
probe 'gh api -X POST repos/o/r/pulls -F draft=true'
probe 'gh api -X POST repos/o/r/pulls --field draft=true'
probe 'gh api -X POST repos/o/r/pulls --raw-field draft=true'
probe 'gh api -X POST repos/o/r/pulls --field=draft=true'
probe 'gh api -X POST repos/o/r/pulls -fdraft=true'
probe "gh api -X POST repos/o/r/pulls -f draft='true'"
probe 'gh api -X POST repos/o/r/pulls -f draft=1'

echo
echo "=== POSITION INDEPENDENCE: same verdict wherever the parameter sits ==="
probe 'gh api -X POST -f draft=true repos/o/r/pulls'
probe 'gh api -f draft=true -X POST repos/o/r/pulls'
probe 'gh api repos/o/r/pulls -f title=x -X POST -f draft=true'
probe "gh api -X POST repos/o/r/pulls -H 'Accept: application/json' -f draft=true"
probe 'gh api repos/o/r/pulls -f draft=true'

echo
echo "=== draft=false -> deny (THE HOLE THIS BEAD CLOSES) ==="
probe 'gh api -X POST repos/o/r/pulls -f draft=false'
probe 'gh api -X POST repos/o/r/pulls -F draft=false'
probe 'gh api -X POST repos/o/r/pulls --raw-field draft=0'
probe 'gh api -X POST repos/o/r/pulls -f draft='
probe 'gh api -X POST repos/o/r/pulls -f draft=garbage'

echo
echo "=== draft ABSENT -> deny, mirroring the bare porcelain create ==="
probe 'gh api -X POST repos/o/r/pulls -f title=x'
probe 'gh api -X POST repos/o/r/pulls'
probe 'gh api -X post repos/o/r/pulls -f title=x'
probe 'gh api -X POST /repos/o/r/pulls -f title=x'
probe 'gh api -X POST repos/{owner}/{repo}/pulls -f title=x'
probe 'gh api repos/o/r/pulls -f title=x'

echo
echo "=== draft UNREADABLE -> ask, the pg2-cl0v2 floor (the named residual) ==="
probe 'gh api -X POST repos/o/r/pulls --input payload.json'
probe 'gh api -X POST repos/o/r/pulls --input=payload.json'
probe 'gh api -X POST repos/o/r/pulls --input -'
probe 'gh api -X POST repos/o/r/pulls --input payload.json -f draft=true'
probe 'gh api -X POST repos/o/r/pulls -F draft=@draft.txt -f title=x'

echo
echo "=== THE GRAPHQL CASE: createPullRequest is PINNED at ask, never allow, never {} ==="
probe "gh api graphql -f 'query=mutation { createPullRequest(input: {repositoryId: \"x\", title: \"t\"}) { pullRequest { number } } }'"
probe "gh api graphql -f 'query=mutation { createPullRequest(input: {repositoryId: \"x\", draft: true}) { pullRequest { number } } }'"
probe "gh api graphql -f 'query=mutation New(\$in: CreatePullRequestInput!) { createPullRequest(input: \$in) { pullRequest { number } } }' -f in={}"
probe "gh api graphql -f 'query={ createPullRequest { number } }'"

echo
echo "=== NOT THIS GATE: a different endpoint or verb keeps its own verdict ==="
probe 'gh api -X PATCH repos/o/r/pulls/5 -f draft=false'
probe 'gh api -X POST repos/o/r/pulls/5/reviews -f body=x'
probe 'gh api -X POST repos/o/r/issues -f title=x'
probe 'gh api --method PUT repos/o/r/pulls/5/merge'
probe 'gh api repos/o/r/pulls'
probe 'gh api repos/o/r/pulls/5'
