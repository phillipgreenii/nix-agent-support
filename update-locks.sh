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
  "update-locks: update claude-extended-tool-approver Go deps + gomod2nix.toml" \
  bash -c 'cd packages/claude-extended-tool-approver && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pg-pr" \
  "update-locks: update pg-pr Go deps + gomod2nix.toml" \
  bash -c 'cd packages/pg-pr && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pa-monitor" \
  "update-locks: update pa-monitor Go deps + gomod2nix.toml" \
  bash -c 'cd packages/pa-monitor && go get -u ./... && ./update-deps.sh'

ul_run_step "update-goccc" \
  "update-locks: bump goccc rev + src hash" \
  nix run nixpkgs#nix-update -- -F goccc

ul_run_step "update-toktrack" \
  "update-locks: bump toktrack rev + src hash + cargoHash" \
  nix run nixpkgs#nix-update -- -F toktrack

# gascity (gc) — upstream-released Go binary pinned in packages/gascity.
# nix-update bumps version + src hash + vendorHash to the latest GitHub release.
# NOTE: gascity ships breaking changes across minor versions (Pack V2 enforcement,
# orders.overrides semantics, model aliases). Review the changelog before applying
# a bump that this step surfaces; the update summary at the end flags when it moves.
#
# DISABLED 2026-06-22: gascity is not currently consumed (its modules are disabled
# downstream) AND latest gascity (1.3.1) requires Go >= 1.26.4, which nixpkgs 26.05
# (and nixpkgs-unstable) does not yet provide (both ship 1.26.3) — so the bump is
# unbuildable and hard-fails `pn workspace update`. The package stays pinned at the
# buildable 1.2.1. Re-enable this step once nixpkgs ships Go >= 1.26.4 (or gascity
# is reintroduced into a config that needs it).
# ul_run_step "update-gascity" \
#   "update-locks: bump gascity version + src hash + vendorHash" \
#   nix run nixpkgs#nix-update -- -F gascity

ul_finalize
