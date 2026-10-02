# shellcheck shell=bash
# nix build already sources integrate-branch-support.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash integrate-branch-support.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F resolve_primary_branch >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integrate-branch-support.bash"
fi

show_help() {
  cat <<'HELP'
integrate-branch-support: Advisory: report a repo's integration facts + recommended strategy

Usage: integrate-branch-support [--facts | --prek-branch-diff | --bundle-refresh <old-sha>]

With no option, print one JSON object (strategy, reason, primary_branch,
canonical, remote, open_pr, mr_bead) describing how the current branch
should be integrated.

Options:
  --facts             Print orientation facts as a stable KEY=value block
                      (WT, FB, CC, PRIMARY, DIRTY, AHEAD, BEHIND, PRECOMMIT,
                      CC_CORE_WORKTREE -- non-empty iff the canonical clone's
                      .git/config sets core.worktree; read-only, never cleared).
                      PRECOMMIT is bundle|stale|legacy|missing|broken, from
                      `pg-hooks status --porcelain`
  --prek-branch-diff  FF-1b: run `pg-hooks run pre-land <FB>` in WT (the hooks
                      over the whole branch diff). Exit 10 (a hook failed)
                      and any other failure are passed through; exit 13 (no
                      bundle, no usable legacy config) and a missing pg-hooks
                      (127) record their notice line and exit 0. Never
                      links, copies or regenerates a hook config
  --bundle-refresh <old-sha>
                      FF-4: run from the canonical clone after an ff-merge
                      that moved the primary branch from <old-sha>. When the
                      landed diff touches a stamp input (flake.lock,
                      flake.nix, the bundle's stampPaths), start the clone's
                      pg-hooks `reinstall` command through bgrun. Prints one
                      `FF-4: bundle refresh ...` line and always exits 0
  -h, --help          Show this help message
  -v, --version       Show version information
HELP
}

# This tool takes no positional arguments; --facts, --prek-branch-diff and
# --bundle-refresh <old-sha> are its only recognized modes (mutually
# exclusive), besides --help and the framework-injected --version (handled
# above this script). Anything else -- an unknown flag or a stray positional --
# is a generic usage error (exit 1, the conventional catch-all; this tool has
# no branchable exit codes of its own -- --prek-branch-diff passes pg-hooks's
# exit status through, 10 meaning a hook failed).
mode=report
refresh_old=""
while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --facts | --prek-branch-diff)
    if [ "$mode" != report ]; then
      echo "integrate-branch-support: --facts, --prek-branch-diff and --bundle-refresh are mutually exclusive" >&2
      exit 1
    fi
    mode="${1#--}"
    shift
    ;;
  --bundle-refresh)
    if [ "$mode" != report ]; then
      echo "integrate-branch-support: --facts, --prek-branch-diff and --bundle-refresh are mutually exclusive" >&2
      exit 1
    fi
    if [[ $# -lt 2 || -z $2 ]]; then
      echo "integrate-branch-support: --bundle-refresh requires the pre-merge primary sha" >&2
      exit 1
    fi
    mode=bundle-refresh
    refresh_old="$2"
    shift 2
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

if [ "$mode" = bundle-refresh ]; then
  # --bundle-refresh <old-sha>: ff-merge-to-main's FF-4 bundle refresh (spec
  # 4.5, D5), run from the canonical clone after the ff-merge. Reports and
  # never fails: see bundle_refresh.
  bundle_refresh "$refresh_old"
  exit 0
fi

if [ "$mode" = prek-branch-diff ]; then
  # --prek-branch-diff: ff-merge-to-main's FF-1b step, made testable here
  # rather than as inline skill prose. It delegates to `pg-hooks run pre-land
  # <FB>` (per-clone hook bundle, or a legacy .pre-commit-config.yaml that
  # pg-hooks reads in place) and NEVER links, copies, or regenerates a hook
  # config (operator ruling 2026-10-01, bead pg2-pla9d.1). Exit-status mapping:
  # see pre_land_check.
  wt_val="$(current_worktree_root)"
  fb_val="$(current_branch)"
  if [ "$fb_val" = "(detached)" ]; then
    echo "integrate-branch-support: detached HEAD in $wt_val -- no branch diff to check" >&2
    exit 1
  fi
  rc=0
  pre_land_check "$wt_val" "$fb_val" || rc=$?
  exit "$rc"
fi

if [ "$mode" = facts ]; then
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
  cc_core_worktree_val="$(canonical_core_worktree)"

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
  printf 'CC_CORE_WORKTREE=%s\n' "$cc_core_worktree_val"
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
