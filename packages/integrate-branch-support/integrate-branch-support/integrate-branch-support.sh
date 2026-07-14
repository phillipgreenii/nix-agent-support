# shellcheck shell=bash
# nix build already sources integrate-branch-support.bash ahead of this body
# (mkBashScript's hasSupportBash injection); this guard only fires for a raw
# `bash integrate-branch-support.sh` run (e.g. local bats), where nothing has
# sourced it yet.
if ! declare -F resolve_primary_branch >/dev/null 2>&1; then
  source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integrate-branch-support.bash"
fi

# Stub: remote/PR/MR signal detection + strategy selection land in later tasks.
primary_branch="$(resolve_primary_branch)"
canonical_branch_val="$(canonical_branch)"
canonical_dirty_val="$(canonical_dirty)"
jq -n --arg primary_branch "$primary_branch" \
  --arg canonical_branch "$canonical_branch_val" \
  --argjson canonical_dirty "$canonical_dirty_val" \
  '{strategy: null, reason: "stub", primary_branch: $primary_branch,
    canonical: {branch: $canonical_branch, dirty: $canonical_dirty},
    remote: null, open_pr: null, mr_bead: null}'
