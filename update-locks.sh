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
# Pass WORKSPACE_ROOT so the resolver can prefer the on-disk sibling when present.
export WORKSPACE_ROOT
UL_LIB_DIR="${UL_LIB_DIR:-$(nix run "github:phillipgreenii/nix-repo-base#determine-ul-lib-dir")}"
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-agent-support" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_run_step "update-deps-claude-extended-tool-approver" \
  "update-locks: update claude-extended-tool-approver Go deps + vendorHash" \
  bash -c 'cd packages/claude-extended-tool-approver && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pg-pr" \
  "update-locks: update pg-pr Go deps + vendorHash" \
  bash -c 'cd packages/pg-pr && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pa-monitor" \
  "update-locks: update pa-monitor Go deps + vendorHash" \
  bash -c 'cd packages/pa-monitor && go get -u ./... && ./update-deps.sh'

ul_run_step "update-goccc" \
  "update-locks: bump goccc rev + src hash" \
  nix run nixpkgs#nix-update -- -F goccc

ul_run_step "update-toktrack" \
  "update-locks: bump toktrack rev + src hash + cargoHash" \
  nix run nixpkgs#nix-update -- -F toktrack

ul_finalize
