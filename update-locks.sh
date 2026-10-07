#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

# Every Go module in this repo is identified by a committed gomod2nix.toml beside
# its go.mod. The refresh loop below iterates THIS list, so a new Go package is
# wired the moment its toml exists (the `go-deps-wired` flake check asserts it).
#
# Emitted in dependency order (a module AFTER the sibling modules it reaches via
# `replace => ../sibling`) so a dependent's `go mod tidy` sees the sibling's
# already-refreshed go.mod.
go_module_dirs() {
  local toml
  for toml in "${SCRIPT_DIR}"/packages/*/gomod2nix.toml; do
    [ -e "${toml}" ] || continue
    basename "$(dirname "${toml}")"
  done | while IFS= read -r mod; do
    printf '%s %s\n' "$(go_module_depth "${mod}")" "${mod}"
  done | sort -s -n -k1,1 | cut -d' ' -f2
}

# Longest chain of local `replace => ../sibling` edges below a module (0 = leaf).
go_module_depth() {
  local dep d max=0
  while IFS= read -r dep; do
    d=$(($(go_module_depth "${dep}") + 1))
    [ "${d}" -gt "${max}" ] && max="${d}"
  done < <(sed -n 's/^[[:space:]]*\(replace[[:space:]]\{1,\}\)\{0,1\}[^[:space:]]\{1,\}[[:space:]]\{1,\}=>[[:space:]]\{1,\}\.\.\/\([^[:space:]]\{1,\}\)[[:space:]]*$/\2/p' "${SCRIPT_DIR}/packages/$1/go.mod")
  echo "${max}"
}

# Step bodies (run by ul_run_step in a subshell from the repo root).
refresh_go_module() { (cd "packages/$1" && ./update-deps.sh); }
refresh_go_module_with_bump() { (cd "packages/$1" && go get -u ./... && ./update-deps.sh); }

# Modules whose third-party deps are bumped (`go get -u ./...`) before regenerating.
# Every OTHER module only gets `go mod tidy` + `gomod2nix generate --with-deps`: a
# blanket `go get -u` can raise the go directive above pkgs.go and upgrades the
# targets of local replaces. Do not grow this list without that review.
GO_GET_U_MODULES=(claude-extended-tool-approver pg-pr pa-monitor pg-connector)

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
--list-go-modules)
  # Read-only: print the module dirs the refresh loop covers, then exit (used by
  # the go-deps-wired flake check; touches no tree state, needs no nix).
  go_module_dirs
  exit 0
  ;;
-h | --help)
  echo "Usage: $0 [--ci | --list-go-modules]"
  echo "  --ci               Disable laptop-only checks (nix daemon health, time-based cache)"
  echo "  --list-go-modules  Print the Go module dirs the refresh loop covers, then exit"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  echo "Usage: $0 [--ci | --list-go-modules]" >&2
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

# One step per Go module (every packages/*/gomod2nix.toml). The step name keeps
# the historical `update-deps-<name>` form so existing step caches still match.
while IFS= read -r go_mod; do
  go_get_u=false
  for m in "${GO_GET_U_MODULES[@]}"; do
    [ "${m}" = "${go_mod}" ] && go_get_u=true
  done
  if [ "${go_get_u}" = true ]; then
    ul_run_step "update-deps-${go_mod}" \
      "update-locks: update ${go_mod} Go deps + gomod2nix.toml" \
      refresh_go_module_with_bump "${go_mod}"
  else
    ul_run_step "update-deps-${go_mod}" \
      "update-locks: update ${go_mod} Go deps + gomod2nix.toml" \
      refresh_go_module "${go_mod}"
  fi
done < <(go_module_dirs)

# Refresh the SHA pins of every `uses: owner/repo@<sha> # <ref>` in .github/workflows
# (ul_refresh_action_pins, shared step from nix-repo-base lib/scripts/update-action-pins-lib.bash;
# beads pg2-ehu9q, pg2-ct1mp, pg2-1dleq). Mutable refs are reported, not rewritten.
ul_run_step "github-action-pins" \
  "update-locks: refresh GitHub Action SHA pins" \
  ul_refresh_action_pins

ul_finalize
