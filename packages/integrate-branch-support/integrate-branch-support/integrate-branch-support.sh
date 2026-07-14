# shellcheck shell=bash
# nix build already sources integrate-branch-support.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash integrate-branch-support.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F resolve_primary_branch >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integrate-branch-support.bash"
fi

# Stub: strategy selection lands in a later task.
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
# reason/strategy assembly proper is a later task; "stub" is still the
# default, but an ambiguous remote is worth surfacing now.
reason_val="stub"
[ -n "$remote_reason" ] && reason_val="$remote_reason"

open_pr_json="$(detect_open_pr)"
[ -n "$open_pr_json" ] || open_pr_json="null"

mr_bead_val="$(detect_mr_bead)"

jq -n --arg primary_branch "$primary_branch" \
  --arg canonical_branch "$canonical_branch_val" \
  --argjson canonical_dirty "$canonical_dirty_val" \
  --arg reason "$reason_val" \
  --arg remote "$remote_val" \
  --argjson open_pr "$open_pr_json" \
  --arg mr_bead "$mr_bead_val" \
  '{strategy: null, reason: $reason, primary_branch: $primary_branch,
    canonical: {branch: $canonical_branch, dirty: $canonical_dirty},
    remote: (if $remote == "" then null else $remote end),
    open_pr: $open_pr,
    mr_bead: (if $mr_bead == "" then null else $mr_bead end)}'
