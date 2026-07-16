#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
-h | --help)
  echo "Usage: $0 [--ci]"
  echo "  --ci  Disable laptop-only checks (nix daemon health, time-based cache)"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  echo "Usage: $0 [--ci]" >&2
  exit 1
  ;;
esac

# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
# Pin nix-repo-base to the locked rev (closes the unpinned-HEAD code-execution
# hole that GH_TOKEN-bearing CI would otherwise expose). Fall back to unpinned
# HEAD when the lock itself is the broken artifact, preserving the self-repair
# property (see update-locks-lib.bash ANCHOR ul_reexec-self-repair-nrb-rev-fallback).
NRB_REV=$(nix flake metadata --json 2>/dev/null |
  jq -r '.locks.nodes."phillipgreenii-nix-base".locked.rev // empty')
if [ -n "$NRB_REV" ]; then
  NRB_REF="github:phillipgreenii/nix-repo-base/${NRB_REV}"
else
  echo "WARN: could not resolve nix-repo-base from flake.lock; using unpinned HEAD" >&2
  NRB_REF="github:phillipgreenii/nix-repo-base"
fi
# Pass WORKSPACE_ROOT so the resolver can prefer the on-disk sibling when present.
export WORKSPACE_ROOT
UL_LIB_DIR="${UL_LIB_DIR:-$(nix run "${NRB_REF}#determine-ul-lib-dir")}"
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-agent-support" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_run_step "update-deps-claude-extended-tool-approver" \
  "update-locks: update claude-extended-tool-approver Go deps + gomod2nix.toml" \
  bash -c 'cd packages/claude-extended-tool-approver && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pg-pr" \
  "update-locks: update pg-pr Go deps + gomod2nix.toml" \
  bash -c 'cd packages/pg-pr && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pa-monitor" \
  "update-locks: update pa-monitor Go deps + gomod2nix.toml" \
  bash -c 'cd packages/pa-monitor && go get -u ./... && ./update-deps.sh'

ul_finalize
