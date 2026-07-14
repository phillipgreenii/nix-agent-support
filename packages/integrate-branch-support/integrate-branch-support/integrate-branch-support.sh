# shellcheck shell=bash
# nix build already sources integrate-branch-support.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash integrate-branch-support.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F resolve_primary_branch >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integrate-branch-support.bash"
fi

# Fail-safe (spec §4.3): this tool has nothing meaningful to report outside
# a git repository, and every helper below assumes one exists -- guard
# up front and exit nonzero rather than let some later `git` call fail
# confusingly deep in the pipeline.
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "integrate-branch-support: not inside a git repository" >&2
  exit 1
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
