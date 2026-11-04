# shellcheck shell=bash
# wtaudit core logic as testable functions (split out of wtaudit.sh): primary
# branch resolution, candidate enumeration, the landed test, the bead lookup,
# the decision table (a closed verdict enum), set classification and the
# guarded remove. The lsof allow-list lives in wt-liveness-lib. No top-level
# code beyond constants -- mkBashScript sources this file before the .sh body.
#
# The scan half is a read-only Query; remove is a Command that re-runs the same
# classification as a Guard and deletes the branch by compare-and-swap.

# The closed reason enum (the verdict `reason` strings), in decision-table
# order, then the set-only and remove-only reasons.
# shellcheck disable=SC2034  # read by wtaudit.sh (help text)
WTAUDIT_SCAN_REASONS="repo-unreadable canonical-checkout anchored-process bead-lookup-failed claimed worktree-unreadable dirty detached-worktree landed-ancestor bead-match-weak landed-patch-equivalent patch-equivalent-no-bead patch-equivalent-bead-open idle-clean-unworked isolated-not-worked unlanded-work"
# shellcheck disable=SC2034  # read by wtaudit.sh (help text)
WTAUDIT_SET_REASONS="pnwf-disagrees pnwf-unavailable"
# shellcheck disable=SC2034  # read by wtaudit.sh (help text)
WTAUDIT_REMOVE_REASONS="branch-refused tip-moved-late teardown-refused tip-moved verdict-changed set-member usage"

# Field separator for the entry records (a non-whitespace separator, so empty
# fields survive `read`).
WTAUDIT_US=$'\x1f'

# wtaudit_repo_readable <dir>: succeeds iff <dir> is inside a git work tree.
wtaudit_repo_readable() {
  [[ -d $1 ]] && git -C "$1" rev-parse --is-inside-work-tree >/dev/null 2>&1
}

# wtaudit_repo_toplevel <dir>: the physical top-level of the work tree.
wtaudit_repo_toplevel() {
  git -C "$1" rev-parse --show-toplevel
}

# wtaudit_primary <repo>: pgii-integrate-branch.primaryBranch (git config), else
# origin/HEAD, else main.
wtaudit_primary() {
  local repo="$1" primary
  primary="$(git -C "$repo" config pgii-integrate-branch.primaryBranch 2>/dev/null || true)"
  if [[ -z $primary ]]; then
    primary="$(git -C "$repo" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)"
    primary="${primary#origin/}"
  fi
  printf '%s\n' "${primary:-main}"
}

# wtaudit_branch_exists <repo> <branch>
wtaudit_branch_exists() {
  git -C "$1" rev-parse --verify -q "refs/heads/$2" >/dev/null 2>&1
}

# wtaudit_entries <repo> <primary>: print one record per candidate, fields
# joined by WTAUDIT_US: kind, set, branch, worktree, wtstate, detached, head.
#   kind     worktree | set-member | dangling-branch
#   wtstate  ok | missing | locked | canonical | none
# The first `git worktree list` entry is the canonical clone: it is emitted
# only when it has a non-primary branch checked out (row 1a, wtstate
# canonical); a detached canonical clone is never emitted. A dangling branch
# is a local branch that is neither the primary nor checked out anywhere.
wtaudit_entries() {
  local repo="$1" primary="$2"
  local line key val path="" head="" branch="" detached=0 locked=0 first=1
  local kind set wtstate b
  local -a checked_out=()
  while IFS= read -r line; do
    if [[ -n $line ]]; then
      key="${line%% *}"
      val=""
      [[ $line == *" "* ]] && val="${line#* }"
      case "$key" in
      worktree) path="$val" ;;
      HEAD) head="$val" ;;
      branch) branch="${val#refs/heads/}" ;;
      detached) detached=1 ;;
      locked) locked=1 ;;
      *) ;;
      esac
      continue
    fi
    # blank line: one worktree record is complete
    if [[ -n $path ]]; then
      [[ -n $branch ]] && checked_out+=("$branch")
      if ((first)); then
        if [[ -n $branch && $branch != "$primary" ]]; then
          printf '%s\n' "worktree${WTAUDIT_US}${WTAUDIT_US}${branch}${WTAUDIT_US}${path}${WTAUDIT_US}canonical${WTAUDIT_US}0${WTAUDIT_US}${head}"
        fi
      else
        kind="worktree"
        set=""
        if [[ $path =~ /\.workforests/([^/]+)/ ]]; then
          kind="set-member"
          set="${BASH_REMATCH[1]}"
        fi
        if [[ ! -d $path ]]; then
          wtstate="missing"
        elif ((locked)); then
          wtstate="locked"
        else
          wtstate="ok"
        fi
        printf '%s\n' "${kind}${WTAUDIT_US}${set}${WTAUDIT_US}${branch}${WTAUDIT_US}${path}${WTAUDIT_US}${wtstate}${WTAUDIT_US}${detached}${WTAUDIT_US}${head}"
      fi
      first=0
    fi
    path="" head="" branch="" detached=0 locked=0
  done < <(
    git -C "$repo" worktree list --porcelain 2>/dev/null
    printf '\n'
  )
  while IFS= read -r b; do
    [[ -n $b && $b != "$primary" ]] || continue
    if [[ " ${checked_out[*]:-} " == *" $b "* ]]; then
      continue
    fi
    printf '%s\n' "dangling-branch${WTAUDIT_US}${WTAUDIT_US}${b}${WTAUDIT_US}${WTAUDIT_US}none${WTAUDIT_US}0${WTAUDIT_US}"
  done < <(git -C "$repo" for-each-ref --format='%(refname:short)' refs/heads 2>/dev/null)
}

# wtaudit_bead_tokens <branch>: print `<match>\t<token>` lines in lookup order.
# drain/<id> yields only branch-id; any other branch yields the whole name
# (whole-name) when that itself looks like an id, then the longest suffix
# matching [a-z0-9]+-[a-z0-9.]+ (suffix).
wtaudit_bead_tokens() {
  local branch="$1" i suffix
  if [[ $branch == drain/* ]]; then
    printf 'branch-id\t%s\n' "${branch#drain/}"
    return 0
  fi
  # A name that cannot be a bead id can never be an exact `bd show` hit, so it
  # is not looked up (no bd call for names like `scratch` or `feature/x`).
  if [[ $branch =~ ^[a-z0-9]+-[a-z0-9.]+$ ]]; then
    printf 'whole-name\t%s\n' "$branch"
  fi
  for ((i = 0; i < ${#branch}; i++)); do
    suffix="${branch:i}"
    if [[ $suffix =~ ^[a-z0-9]+-[a-z0-9.]+$ ]]; then
      [[ $suffix == "$branch" ]] || printf 'suffix\t%s\n' "$suffix"
      break
    fi
  done
}

# wtaudit_bead_show <repo> <token>: run `bd -C <repo> show <token> --json` and
# print `{"lookup":"ok|none|failed","bead":{...}|null}`. ok = bd ran, printed
# JSON, and element 0 (of .data or of the bare array) is an object whose id is
# the token (an exact hit); none = parsed with no such element; failed = bd is
# not runnable, printed no JSON, or the output does not parse. Exit status is
# never used alone to decide.
wtaudit_bead_show() {
  local repo="$1" token="$2" out parsed
  if ! command -v bd >/dev/null 2>&1; then
    printf '{"lookup":"failed","bead":null}\n'
    return 0
  fi
  out="$(bd -C "$repo" show "$token" --json 2>/dev/null || true)"
  if [[ -z $out ]] || ! parsed="$(jq -c --arg token "$token" '
      [ (if type=="object" and has("data") then .data else . end)
        | .[0]? | select(type=="object") ] | .[0] as $b
      | if $b == null or ($b.id // "") != $token then {lookup:"none", bead:null}
        else {lookup:"ok", bead:{
          id: $b.id, status: ($b.status // ""), assignee: ($b.assignee // ""),
          lease_expires_at: ($b.lease_expires_at // ""),
          labels: ($b.labels // []), title: ($b.title // "")}} end
    ' <<<"$out" 2>/dev/null)" || [[ -z $parsed ]]; then
    printf '{"lookup":"failed","bead":null}\n'
    return 0
  fi
  printf '%s\n' "$parsed"
}

# wtaudit_bead_lookup <repo> <branch>: print `{"lookup","match","bead"}`. The
# whole-name token is tried first; a `none` falls through to the suffix token;
# a `failed` stops (the verdict is keep: bead-lookup-failed).
wtaudit_bead_lookup() {
  local repo="$1" branch="$2" match token res lookup
  while IFS=$'\t' read -r match token; do
    res="$(wtaudit_bead_show "$repo" "$token")"
    lookup="$(jq -r '.lookup' <<<"$res")"
    case "$lookup" in
    ok)
      jq -c --arg m "$match" '{lookup, match: $m, bead}' <<<"$res"
      return 0
      ;;
    failed)
      jq -c '{lookup, match: null, bead: null}' <<<"$res"
      return 0
      ;;
    *) ;;
    esac
  done < <(wtaudit_bead_tokens "$branch")
  printf '{"lookup":"none","match":null,"bead":null}\n'
}

# wtaudit_anchored_json <worktree>: the anchored processes as a JSON array of
# {pid, command, blocking}, via the shared wt-liveness-lib probe.
wtaudit_anchored_json() {
  local anchored
  anchored="$(wtdone_anchored_processes "$1")"
  if [[ -z $anchored ]]; then
    printf '[]\n'
    return 0
  fi
  wtdone_classify_anchored <<<"$anchored" | jq -R -s -c '
    split("\n") | map(select(length > 0) | capture("^(?<v>[a-z]+) (?<pid>[0-9]+) (?<cmd>.*)$")
      | {pid: (.pid | tonumber), command: .cmd, blocking: (.v == "block")})'
}

# The decision table as a jq filter: reads a candidate's facts, adds `action`,
# `reason` and `evidence`. First match wins; rows 0 to 4b are guards.
# shellcheck disable=SC2016  # jq program, not shell
WTAUDIT_DECIDE_JQ='
def claimed: .bead != null and .bead.status != "closed"
  and (.bead.status == "in_progress" or (.bead.assignee // "") != "" or (.bead.lease_expires_at // "") != "");
def strong: (.bead_match == "branch-id" or .bead_match == "whole-name");
def bead_open: .bead != null and .bead.status != "closed";
def bead_closed: .bead != null and .bead.status == "closed";
def blockers: [.anchored[] | select(.blocking)];
def verdict($a; $r; $e): . + {action: $a, reason: $r, evidence: $e};
if .repo_unreadable then
  verdict("agent"; "repo-unreadable"; "the repository path is missing, not a git repository, or its primary branch does not resolve")
elif .canonical_checkout then
  verdict("keep"; "canonical-checkout"; "branch \(.branch) is checked out in the canonical clone \(.repo_path)")
elif .worktree != null and (blockers | length) > 0 then
  verdict("keep"; "anchored-process"; "process \(blockers[0].command) (pid \(blockers[0].pid)) is anchored in \(.worktree)")
elif .bead_lookup == "failed" then
  verdict("keep"; "bead-lookup-failed"; "bd could not be read for branch \(.branch)")
elif claimed then
  verdict("keep"; "claimed"; "bead \(.bead.id) is \(.bead.status) with assignee \"\(.bead.assignee)\" and lease \"\(.bead.lease_expires_at)\"")
elif .worktree_state == "missing" or .worktree_state == "locked" or .worktree_state == "unreadable" then
  verdict("keep"; "worktree-unreadable"; "worktree \(.worktree) is \(.worktree_state)")
elif .dirty == true then
  verdict("keep"; "dirty"; "worktree \(.worktree) has tracked changes or untracked files")
elif .branch == null then
  verdict("agent"; "detached-worktree"; "worktree \(.worktree) is a detached HEAD with no branch to compare")
elif .ancestor == true and (.bead == null or (bead_closed and strong)) then
  verdict("remove"; "landed-ancestor"; "branch \(.branch) is an ancestor of \(.primary)" + (if .bead == null then " and no bead matched" else " and bead \(.bead.id) is closed" end))
elif .ancestor == true and bead_closed then
  verdict("agent"; "bead-match-weak"; "branch \(.branch) is an ancestor of \(.primary) but bead \(.bead.id) matched only by name suffix")
elif .patch_equivalent == true and .ancestor != true and bead_closed and strong then
  verdict("remove"; "landed-patch-equivalent"; "branch \(.branch) is patch-equivalent to \(.primary) (git cherry shows no + line, no merge commit) and bead \(.bead.id) is closed")
elif .patch_equivalent == true and .ancestor != true and bead_closed then
  verdict("agent"; "bead-match-weak"; "branch \(.branch) is patch-equivalent to \(.primary) but bead \(.bead.id) matched only by name suffix")
elif .patch_equivalent == true and .ancestor != true and .bead == null then
  verdict("keep"; "patch-equivalent-no-bead"; "branch \(.branch) is patch-equivalent to \(.primary) but no bead was found to confirm it is done")
elif .patch_equivalent == true and .ancestor != true and bead_open then
  verdict("agent"; "patch-equivalent-bead-open"; "branch \(.branch) is patch-equivalent to \(.primary) but bead \(.bead.id) is still \(.bead.status)")
elif .ancestor == true and bead_open and strong and .worktree != null then
  verdict("reclaim"; "idle-clean-unworked"; "branch \(.branch) has no commits beyond \(.primary), the worktree is clean and idle, bead \(.bead.id) is open and unclaimed")
elif .ancestor == true and bead_open and strong then
  verdict("keep"; "isolated-not-worked"; "branch \(.branch) has no worktree and no commits beyond \(.primary); bead \(.bead.id) is open and unclaimed")
elif .ancestor == true and bead_open then
  verdict("agent"; "bead-match-weak"; "branch \(.branch) has no commits beyond \(.primary) but bead \(.bead.id) matched only by name suffix")
else
  verdict("agent"; "unlanded-work"; "branch \(.branch) has \(.plus_commits) commit(s) not on \(.primary) and \(.merge_commits) merge commit(s)")
end
'

# wtaudit_build_candidate <repo_name> <repo_path> <primary> <kind> <set>
#   <branch> <worktree> <wtstate> <detached> <head>: print one classified
#   candidate as a single compact JSON line.
wtaudit_build_candidate() {
  local repo_name="$1" repo_path="$2" primary="$3" kind="$4" set="$5"
  local branch="$6" worktree="$7" wtstate="$8" detached="$9" head="${10}"
  local tip="" ahead="" behind="" ancestor="" plus="" merges="" patch_eq=""
  local last_date="" subjects='[]' dirty="null" anchored='[]' canonical=false
  local porcelain bead_json='{"lookup":"none","match":null,"bead":null}' id

  if [[ $wtstate == canonical ]]; then
    canonical=true
  fi

  if [[ -n $branch && $detached != 1 ]]; then
    tip="$(git -C "$repo_path" rev-parse "refs/heads/$branch")"
    ahead="$(git -C "$repo_path" rev-list --count "$primary..$branch")"
    behind="$(git -C "$repo_path" rev-list --count "$branch..$primary")"
    if git -C "$repo_path" merge-base --is-ancestor "$branch" "$primary" 2>/dev/null; then
      ancestor=true
    else
      ancestor=false
    fi
    plus="$(git -C "$repo_path" cherry "$primary" "$branch" 2>/dev/null | awk '/^\+/ { n++ } END { print n + 0 }')"
    merges="$(git -C "$repo_path" rev-list --merges --count "$primary..$branch")"
    if [[ $plus -eq 0 && $merges -eq 0 ]]; then patch_eq=true; else patch_eq=false; fi
    last_date="$(git -C "$repo_path" log -1 --format=%cs "$branch" 2>/dev/null || true)"
    subjects="$(git -C "$repo_path" log -5 --format=%s "$primary..$branch" 2>/dev/null | jq -R -s -c 'split("\n") | map(select(length > 0))')"
  fi

  if [[ $wtstate != canonical && $wtstate != none && -n $worktree && -d $worktree ]]; then
    if porcelain="$(git -C "$worktree" status --porcelain 2>/dev/null)"; then
      if [[ -n $porcelain ]]; then dirty=true; else dirty=false; fi
    else
      wtstate="unreadable"
    fi
    anchored="$(wtaudit_anchored_json "$worktree")"
  fi

  if [[ -n $branch && $detached != 1 ]]; then
    bead_json="$(wtaudit_bead_lookup "$repo_path" "$branch")"
  fi

  if [[ $detached == 1 || -z $branch ]]; then
    id="${repo_name}:${worktree}"
  else
    id="${repo_name}:${branch}"
  fi

  jq -c -n \
    --arg id "$id" --arg repo "$repo_name" --arg repo_path "$repo_path" \
    --arg kind "$kind" --arg set "$set" --arg branch "$branch" \
    --arg worktree "$worktree" --arg wtstate "$wtstate" --arg tip "$tip" \
    --arg head "$head" --arg primary "$primary" --arg ahead "$ahead" \
    --arg behind "$behind" --arg ancestor "$ancestor" --arg plus "$plus" \
    --arg merges "$merges" --arg patch_eq "$patch_eq" --arg last_date "$last_date" \
    --argjson subjects "$subjects" --argjson dirty "$dirty" \
    --argjson anchored "$anchored" --argjson bead "$bead_json" \
    --argjson canonical "$canonical" --argjson detached "$([[ $detached == 1 ]] && echo true || echo false)" '
    def nn: if . == "" then null else . end;
    def num: if . == "" then null else tonumber end;
    def bool: if . == "" then null else (. == "true") end;
    {
      id: $id, repo: $repo, repo_path: $repo_path, kind: $kind,
      set: ($set | nn), branch: ($branch | nn), worktree: ($worktree | nn),
      tip: (if $tip != "" then $tip elif $detached then ($head | nn) else null end),
      primary: $primary,
      ahead: ($ahead | num), behind: ($behind | num), ancestor: ($ancestor | bool),
      plus_commits: ($plus | num), merge_commits: ($merges | num),
      patch_equivalent: ($patch_eq | bool),
      dirty: $dirty, anchored: $anchored,
      bead: $bead.bead, bead_lookup: $bead.lookup, bead_match: $bead.match,
      last_commit_date: ($last_date | nn), subjects: $subjects,
      canonical_checkout: $canonical,
      worktree_state: $wtstate,
      repo_unreadable: false
    }' | jq -c "$WTAUDIT_DECIDE_JQ"
}

# wtaudit_unreadable_candidate <repo_name> <repo_path>: the repo-unreadable row.
wtaudit_unreadable_candidate() {
  jq -c -n --arg repo "$1" --arg path "$2" '
    {id: "\($repo):", repo: $repo, repo_path: $path, kind: "repo", set: null,
     branch: null, worktree: null, tip: null, primary: null, ahead: null,
     behind: null, ancestor: null, plus_commits: null, merge_commits: null,
     patch_equivalent: null, dirty: null, anchored: [], bead: null,
     bead_lookup: "none", bead_match: null, last_commit_date: null,
     subjects: [], canonical_checkout: false, worktree_state: "none",
     repo_unreadable: true}' | jq -c "$WTAUDIT_DECIDE_JQ"
}

# wtaudit_scan_repo <repo_name> <repo_path>: print one JSON line per candidate
# of the repo (a repo-unreadable row when it cannot be read).
wtaudit_scan_repo() {
  local repo_name="$1" repo_path="$2" primary top
  local kind set branch worktree wtstate detached head
  if ! wtaudit_repo_readable "$repo_path"; then
    wtaudit_unreadable_candidate "$repo_name" "$repo_path"
    return 0
  fi
  top="$(wtaudit_repo_toplevel "$repo_path")"
  primary="$(wtaudit_primary "$top")"
  if ! wtaudit_branch_exists "$top" "$primary"; then
    wtaudit_unreadable_candidate "$repo_name" "$repo_path"
    return 0
  fi
  while IFS="$WTAUDIT_US" read -r kind set branch worktree wtstate detached head; do
    [[ -n $kind ]] || continue
    wtaudit_build_candidate "$repo_name" "$top" "$primary" "$kind" "$set" "$branch" "$worktree" "$wtstate" "$detached" "$head"
  done < <(wtaudit_entries "$top" "$primary")
}

# wtaudit_set_land_plan <workspace_root> <set>: run the ambient `pnwf land-plan
# <set>` from <workspace_root>. Prints pnwf's stdout; returns 3 when pnwf is
# missing or exits non-zero (treated as pnwf-unavailable).
wtaudit_set_land_plan() {
  local root="$1" set="$2" out
  command -v pnwf >/dev/null 2>&1 || return 3
  out="$(cd "$root" 2>/dev/null && pnwf land-plan "$set" 2>/dev/null)" || return 3
  printf '%s' "$out"
}

# wtaudit_classify_sets: read the candidates JSON array on stdin, print the
# `sets` JSON array. A set is keep if any member is keep, else agent if any
# member is agent or reclaim, else (all remove) it is checked against the
# ambient `pnwf land-plan`.
wtaudit_classify_sets() {
  local candidates set members blocking root plan rc first_wt
  candidates="$(cat)"
  local -a sets
  mapfile -t sets < <(jq -r '[.[] | select(.kind == "set-member") | .set] | unique | .[]' <<<"$candidates")
  local out='[]' entry
  for set in "${sets[@]}"; do
    members="$(jq -c --arg s "$set" '[.[] | select(.kind == "set-member" and .set == $s)]' <<<"$candidates")"
    blocking="$(jq -c '(map(select(.action == "keep")) | .[0]) // (map(select(.action == "agent" or .action == "reclaim")) | .[0])' <<<"$members")"
    if [[ $blocking != null ]]; then
      entry="$(jq -c -n --arg set "$set" --argjson m "$members" --argjson b "$blocking" '
        {set: $set, members: ($m | map(.id)),
         action: (if $b.action == "keep" then "keep" else "agent" end),
         reason: $b.reason, evidence: "member \($b.id): \($b.reason): \($b.evidence)"}')"
    else
      first_wt="$(jq -r '.[0].worktree' <<<"$members")"
      root="${first_wt%%/.workforests/*}"
      rc=0
      plan="$(wtaudit_set_land_plan "$root" "$set")" || rc=$?
      if [[ $rc -ne 0 ]]; then
        entry="$(jq -c -n --arg set "$set" --argjson m "$members" '
          {set: $set, members: ($m | map(.id)), action: "agent", reason: "pnwf-unavailable",
           evidence: "pnwf land-plan could not be run (missing or failed), so the set cannot be confirmed fully landed"}')"
      elif [[ -n $plan ]]; then
        entry="$(jq -c -n --arg set "$set" --argjson m "$members" --arg plan "$plan" '
          {set: $set, members: ($m | map(.id)), action: "agent", reason: "pnwf-disagrees",
           evidence: ("pnwf land-plan still lists members to land: " + ($plan | gsub("\n"; ", ")))}')"
      else
        entry="$(jq -c -n --arg set "$set" --argjson m "$members" '
          {set: $set, members: ($m | map(.id)), action: "remove-via-pnwf",
           reason: (if all($m[]; .reason == "landed-ancestor") then "landed-ancestor" else "landed-patch-equivalent" end),
           evidence: "every member is landed and pnwf land-plan lists nothing to land; remove with pnwf cleanup"}')"
      fi
    fi
    out="$(jq -c --argjson e "$entry" '. + [$e]' <<<"$out")"
  done
  printf '%s\n' "$out"
}

# wtaudit_scan_document <candidate-lines-file-or-stdin>: wrap candidate JSON
# lines (stdin) into the schema_version 1 document, classifying sets.
wtaudit_scan_document() {
  local candidates sets
  candidates="$(jq -s -c '.')"
  sets="$(wtaudit_classify_sets <<<"$candidates")"
  jq -n -c --argjson c "$candidates" --argjson s "$sets" --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
    {schema_version: 1, generated_at: $ts,
     candidates: ($c | map(del(.canonical_checkout, .worktree_state, .repo_unreadable))), sets: $s}'
}

# wtaudit_render_table: read the scan document on stdin, print a human table
# (one row per candidate and one per set; nothing hidden).
wtaudit_render_table() {
  jq -r '
    (.candidates[] | ["candidate", .action, .reason, .id, .evidence] | @tsv),
    (.sets[] | ["set", .action, .reason, .set, .evidence] | @tsv)' |
    awk -F'\t' 'BEGIN { printf "%-9s %-16s %-28s %s\n", "TYPE", "ACTION", "REASON", "ID / EVIDENCE" }
      { printf "%-9s %-16s %-28s %s -- %s\n", $1, $2, $3, $4, $5 }'
}

# wtaudit_remove_json <id> <removed_worktree> <removed_branch> <exit> <reason> <message>
wtaudit_remove_json() {
  jq -c -n --arg id "$1" --argjson rw "$2" --argjson rb "$3" --argjson ex "$4" \
    --arg reason "$5" --arg message "$6" \
    '{id: $id, removed_worktree: $rw, removed_branch: $rb, exit: $ex, reason: $reason, message: $message}'
}

# wtaudit_holder_of_branch <repo> <branch>: print the worktree path holding
# <branch> (the canonical clone included), or nothing.
wtaudit_holder_of_branch() {
  git -C "$1" worktree list --porcelain | awk -v branch="$2" '
    /^worktree / { sub(/^worktree /, ""); path=$0 }
    $0 == "branch refs/heads/"branch { print path; exit }
  '
}

# wtaudit_remove <repo_path> <branch> <expect_tip> <wtdone_bin> [<keep_branch>]: the guarded
# remove. Prints one JSON object on stdout and returns the documented exit code
# (10 refused, 11 branch remains, 12 set member, 13 teardown refused, 0 done).
# With keep_branch=true (the abandoned-agent-row mode) the candidate must
# instead classify as `agent` with a worktree; only the worktree is torn down
# and the unlanded branch is left, its tip re-verified, so exit 0 means the
# worktree is gone and the branch is intact (removed_branch false).
wtaudit_remove() {
  local repo_path="$1" branch="$2" expect_tip="$3" wtdone_bin="${4:-}" keep_branch="${5:-false}"
  local repo_name primary top cand="" line id
  local kind set bname worktree wtstate detached head
  top="$(wtaudit_repo_toplevel "$repo_path")"
  repo_name="$(basename "$top")"
  primary="$(wtaudit_primary "$top")"
  id="${repo_name}:${branch}"

  if wtaudit_branch_exists "$top" "$primary"; then
    while IFS="$WTAUDIT_US" read -r kind set bname worktree wtstate detached head; do
      [[ -n $kind && $bname == "$branch" ]] || continue
      cand="$(wtaudit_build_candidate "$repo_name" "$top" "$primary" "$kind" "$set" "$bname" "$worktree" "$wtstate" "$detached" "$head")"
      break
    done < <(wtaudit_entries "$top" "$primary")
  fi

  if [[ -z $cand ]]; then
    wtaudit_remove_json "$id" false false 10 verdict-changed "branch $branch is not a removal candidate (unknown, already gone, or the primary)"
    return 10
  fi
  if [[ $(jq -r '.kind' <<<"$cand") == set-member ]]; then
    wtaudit_remove_json "$id" false false 12 set-member "branch $branch is a member of set $(jq -r '.set' <<<"$cand"); remove a set with pnwf cleanup"
    return 12
  fi
  local action reason
  action="$(jq -r '.action' <<<"$cand")"
  reason="$(jq -r '.reason' <<<"$cand")"
  if [[ $keep_branch == true ]]; then
    if [[ $action != agent || -z $(jq -r '.worktree // ""' <<<"$cand") ]]; then
      wtaudit_remove_json "$id" false false 10 verdict-changed "verdict is $action: $reason, not an agent row with a worktree to reclaim: $(jq -r '.evidence' <<<"$cand")"
      return 10
    fi
  elif [[ $action != remove && $action != reclaim ]]; then
    wtaudit_remove_json "$id" false false 10 verdict-changed "verdict is now $action: $reason: $(jq -r '.evidence' <<<"$cand")"
    return 10
  fi
  if [[ $(jq -r '.tip' <<<"$cand") != "$expect_tip" ]]; then
    wtaudit_remove_json "$id" false false 10 tip-moved "branch tip is $(jq -r '.tip' <<<"$cand"), expected $expect_tip"
    return 10
  fi

  local wt had_wt=false removed_wt=false removed_br=false wt_err=""
  wt="$(jq -r '.worktree // ""' <<<"$cand")"
  if [[ -n $wt ]]; then
    had_wt=true
    if [[ -z $wtdone_bin || ! -x $wtdone_bin ]]; then
      wtaudit_remove_json "$id" false false 13 teardown-refused "WTDONE_BIN is not an executable file: ${wtdone_bin:-<unset>}"
      return 13
    fi
    # wtdone exits 1 for every failure, so its status is not interpreted: the
    # state is re-read below.
    wt_err="$("$wtdone_bin" "$branch" --cc "$top" 2>&1 >/dev/null || true)"
    if [[ -e $wt ]]; then
      wtaudit_remove_json "$id" false false 13 teardown-refused "$wt_err"
      return 13
    fi
    removed_wt=true
  fi

  if [[ $keep_branch == true ]]; then
    if [[ $(git -C "$top" rev-parse --verify -q "refs/heads/$branch" 2>/dev/null || true) != "$expect_tip" ]]; then
      wtaudit_remove_json "$id" "$removed_wt" false 11 tip-moved-late "branch $branch is missing or its tip moved after the worktree was removed"
      return 11
    fi
    wtaudit_remove_json "$id" "$removed_wt" false 0 "$reason" "worktree removed; unlanded branch kept"
    return 0
  fi

  if wtaudit_branch_exists "$top" "$branch"; then
    if [[ $action == remove && -z $(wtaudit_holder_of_branch "$top" "$branch") ]]; then
      if git -C "$top" update-ref -d "refs/heads/$branch" "$expect_tip" 2>/dev/null; then
        removed_br=true
      else
        wtaudit_remove_json "$id" "$removed_wt" false 11 tip-moved-late "the branch tip moved after the worktree was removed; branch $branch left in place"
        return 11
      fi
    else
      wtaudit_remove_json "$id" "$removed_wt" false 11 branch-refused "branch $branch remains: git branch -d refused and the verdict ($action) does not allow the guarded delete"
      return 11
    fi
  else
    removed_br=true
  fi
  local msg="removed"
  [[ $had_wt == true ]] || msg="removed (no worktree)"
  wtaudit_remove_json "$id" "$removed_wt" "$removed_br" 0 "$reason" "$msg"
  return 0
}
