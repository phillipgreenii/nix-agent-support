#!/usr/bin/env bash
# capture-prefix-snapshots.sh — reproducibly capture the PRE-FIX git snapshot of
# each behavior-docs set into a temp directory, for use as real-world INTRA and
# INTER FAIL fixtures (bead pg2-hvlyj.14 / .15, plan items 5.2 / 5.3).
#
# The POST-FIX (current) sets are the PASS fixtures; the sets as they were
# immediately BEFORE the pg2-hvlyj review-resolution edits are the FAIL fixtures
# — they still carry the review findings the INTRA evaluator catches (e.g. #15
# inline-status "unmet by the current implementation", #1 owner->consumer
# `pr-pool-components` pointer, #6-A stranded past-framing note), and the seam
# defects the INTER evaluator catches (an imports table that predates the
# owner-UUID rows, so cited elements are undeclared).
#
# A capture is a READ of git history into a fresh directory: it never mutates a
# repo and never touches a real set in place. `test-behavior-docs-intra-conformance`
# exercises it against a synthetic throwaway git repo, so this mechanism cannot
# rot unnoticed while the rest of the gate stays green.
#
# Usage:
#   capture-prefix-snapshots.sh <out-dir>
# Env overrides (defaults are the pg2-hvlyj pre-fix revs):
#   AGENT_SUPPORT_REPO   path to the phillipgreenii-nix-agent-support clone
#   ZR_REPO              path to the phillipg-nix-ziprecruiter clone
#   AGENT_SUPPORT_REV    pre-fix rev for the method + pr-pool sets (default d5395cf9)
#   ZR_REV               pre-fix rev for the ZR set (default 3dc2e47)
#
# The pre-fix revs are the PARENTS of this session's first edit commit per repo:
#   agent-support: d5395cf9  (parent of f20cc87a "method behavior-docs: Cluster 1")
#   ZR:            3dc2e47   (parent of a92dc64  "ZR pr-pool-components: Cluster 4")
set -euo pipefail

out="${1:?usage: capture-prefix-snapshots.sh <out-dir>}"
AGENT_SUPPORT_REPO="${AGENT_SUPPORT_REPO:-.}"
ZR_REPO="${ZR_REPO:-}"
AGENT_SUPPORT_REV="${AGENT_SUPPORT_REV:-d5395cf9}"
ZR_REV="${ZR_REV:-3dc2e47}"

mkdir -p "$out"

# capture <repo> <rev> <set-subpath> <label> — extract one set at a rev into out/<label>.
capture() {
  local repo="$1" rev="$2" sub="$3" label="$4"
  [ -d "$repo/.git" ] || {
    echo "skip $label: $repo is not a git repo" >&2
    return 0
  }
  local dest="$out/$label"
  mkdir -p "$dest"
  if git -C "$repo" cat-file -e "$rev^{commit}" 2>/dev/null; then
    git -C "$repo" archive "$rev" "$sub" | tar -x -C "$dest" --strip-components="$(awk -F/ '{print NF}' <<<"$sub")" 2>/dev/null ||
      git -C "$repo" archive "$rev" "$sub" | tar -x -C "$dest"
    echo "captured $label from $repo@$rev:$sub"
  else
    echo "skip $label: rev $rev not found in $repo" >&2
  fi
}

capture "$AGENT_SUPPORT_REPO" "$AGENT_SUPPORT_REV" "behavior-docs/docs/behavior" "method-prefix"
capture "$AGENT_SUPPORT_REPO" "$AGENT_SUPPORT_REV" "packages/pr-pool/docs/behavior" "pr-pool-prefix"
if [ -n "$ZR_REPO" ]; then
  # The ZR set path is resolved in the ZR repo; adjust ZR_SET if it differs.
  capture "$ZR_REPO" "$ZR_REV" "${ZR_SET:-docs/behavior}" "zr-prefix"
fi

echo "pre-fix snapshots under: $out"
