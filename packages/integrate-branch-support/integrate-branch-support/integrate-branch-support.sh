# shellcheck shell=bash
# nix build already sources integrate-branch-support.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash integrate-branch-support.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F resolve_primary_branch >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integrate-branch-support.bash"
fi

# This tool takes no positional arguments; --facts is its only recognized
# flag (besides the framework-injected --help/--version, handled above this
# script). Anything else -- an unknown flag or a stray positional -- is a
# generic usage error (exit 1, the conventional catch-all; this tool has no
# branchable exit codes).
facts_mode=0
while [[ $# -gt 0 ]]; do
  case "$1" in
  --facts)
    facts_mode=1
    shift
    ;;
  *)
    echo "integrate-branch-support: unexpected argument: $1" >&2
    exit 1
    ;;
  esac
done

# Fail-safe (spec §4.3): this tool has nothing meaningful to report outside
# a git repository, and every helper below assumes one exists -- guard
# up front and exit nonzero rather than let some later `git` call fail
# confusingly deep in the pipeline.
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "integrate-branch-support: not inside a git repository" >&2
  exit 1
fi

if [ "$facts_mode" -eq 1 ]; then
  # --facts: a stable, parseable KEY=value block (one fact per line) for an
  # agent to eval/parse directly, replacing the hand-authored WT/FB/CC/PRIMARY
  # preamble + worktree-orientation one-liners the ff-merge-to-main and
  # integrate-branch skill texts used to prescribe. Deliberately NOT the JSON
  # object below -- that shape stays reserved for the strategy-advisory
  # report; this is a separate, simpler contract callers can `while IFS='='
  # read -r key value` over without a jq dependency. Order is fixed and
  # documented (integrate-branch-support.md); a related tool (wtnew) prints
  # this same block after creating a fresh worktree, so the format must stay
  # stable across both callers.
  wt_val="$(current_worktree_root)"
  fb_val="$(current_branch)"
  cc_val="$(canonical_root)"
  primary_val="$(resolve_primary_branch)"
  dirty_val="$(current_dirty_yesno)"
  precommit_val="$(precommit_state "$wt_val")"

  ahead_val=""
  behind_val=""
  {
    IFS= read -r ahead_val
    IFS= read -r behind_val
  } < <(ahead_behind_primary "$primary_val")

  printf 'WT=%s\n' "$wt_val"
  printf 'FB=%s\n' "$fb_val"
  printf 'CC=%s\n' "$cc_val"
  printf 'PRIMARY=%s\n' "$primary_val"
  printf 'DIRTY=%s\n' "$dirty_val"
  printf 'AHEAD=%s\n' "$ahead_val"
  printf 'BEHIND=%s\n' "$behind_val"
  printf 'PRECOMMIT=%s\n' "$precommit_val"
  exit 0
fi

primary_branch="$(resolve_primary_branch)"
canonical_branch_val="$(canonical_branch)"
canonical_dirty_val="$(canonical_dirty)"

# detect_remote prints two lines: the remote (possibly empty) and an
# ambiguity reason (possibly empty) — see its definition for why two
# lines instead of a second global/return channel. Read line-by-line
# (not `mapfile`, which needs bash >=4 and so would break under whatever
# `bash` a caller's scrubbed PATH happens to resolve, e.g. macOS's
# ancient system /bin/bash) via process substitution, which — unlike
# `$(...)` command substitution — does not collapse trailing empty lines.
remote_val=""
remote_reason=""
{
  IFS= read -r remote_val
  IFS= read -r remote_reason
} < <(detect_remote)

open_pr_json="$(detect_open_pr)"
[ -n "$open_pr_json" ] || open_pr_json="null"

mr_bead_val="$(detect_mr_bead)"

declared_strategy="$(git config --get pgii-integrate-branch.strategy 2>/dev/null || true)"

strategy_val=""
reason_val=""
{
  IFS= read -r strategy_val
  IFS= read -r reason_val
} < <(resolve_strategy "$declared_strategy" "$remote_val" "$remote_reason" "$open_pr_json" "$mr_bead_val")

jq -n --arg primary_branch "$primary_branch" \
  --arg canonical_branch "$canonical_branch_val" \
  --argjson canonical_dirty "$canonical_dirty_val" \
  --arg strategy "$strategy_val" \
  --arg reason "$reason_val" \
  --arg remote "$remote_val" \
  --argjson open_pr "$open_pr_json" \
  --arg mr_bead "$mr_bead_val" \
  '{strategy: (if $strategy == "" then null else $strategy end), reason: $reason, primary_branch: $primary_branch,
    canonical: {branch: $canonical_branch, dirty: $canonical_dirty},
    remote: (if $remote == "" then null else $remote end),
    open_pr: $open_pr,
    mr_bead: (if $mr_bead == "" then null else $mr_bead end)}'
