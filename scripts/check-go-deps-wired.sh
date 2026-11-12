#!/usr/bin/env bash
# Flake check body for `checks.<system>.go-deps-wired`: every Go module in this
# repo MUST be refreshed by update-locks.sh, MUST have the thin update-deps.sh
# wrapper, and the shared updater MUST generate with --with-deps against the
# gomod2nix rev flake.lock locks (phillipg-nix-repo-base ADR 0031: a toml
# without `cachePackages` leaves every Go build compiling cold).
#
# Every packages/*/go.mod MUST carry the same `go` directive. With the optional
# second argument (the flake passes pkgs.go.version) that directive MUST equal
# `go <version>`, so modules are judged by the same compiler that builds the
# deployed binaries; without it the modules must only agree with each other.
#
# Usage: check-go-deps-wired.sh <repo-root> [expected-go-version]
#
# Exits 0 when everything is wired; exits 1 after listing EVERY problem.
set -euo pipefail

root="${1:?usage: check-go-deps-wired.sh <repo-root> [expected-go-version]}"
expected_go="${2:-}"
problems=()

# Module dirs that have a gomod2nix.toml (the source of truth for "is a Go module").
mapfile -t modules < <(
  for toml in "${root}"/packages/*/gomod2nix.toml; do
    [ -e "${toml}" ] || continue
    basename "$(dirname "${toml}")"
  done | sort
)
if [ "${#modules[@]}" -eq 0 ]; then
  echo "FAIL: found no packages/*/gomod2nix.toml under ${root} (wrong root?)" >&2
  exit 1
fi

# Module dirs update-locks.sh actually refreshes, as reported by the script itself.
mapfile -t wired < <(bash "${root}/update-locks.sh" --list-go-modules | sort)

for mod in "${modules[@]}"; do
  if ! printf '%s\n' "${wired[@]}" | grep -Fxq -- "${mod}"; then
    problems+=("${mod}: gomod2nix.toml present but not refreshed by update-locks.sh")
  fi
  wrapper="${root}/packages/${mod}/update-deps.sh"
  if [ ! -x "${wrapper}" ]; then
    problems+=("${mod}: packages/${mod}/update-deps.sh missing or not executable")
  elif ! grep -Fq 'update-gomod2nix-deps.sh' "${wrapper}"; then
    problems+=("${mod}: update-deps.sh does not delegate to ../update-gomod2nix-deps.sh")
  fi
done

# update-locks.sh must not list a module that has no toml (stale wiring).
for mod in "${wired[@]}"; do
  [ -n "${mod}" ] || continue
  if [ ! -e "${root}/packages/${mod}/gomod2nix.toml" ]; then
    problems+=("${mod}: wired in update-locks.sh but has no packages/${mod}/gomod2nix.toml")
  fi
done

# One go directive everywhere (and the pinned one when the caller names it).
declare -A directives=()
for gomod in "${root}"/packages/*/go.mod; do
  [ -e "${gomod}" ] || continue
  mod="$(basename "$(dirname "${gomod}")")"
  directive="$(sed -n 's/^go[[:space:]][[:space:]]*\([^[:space:]]*\)[[:space:]]*$/\1/p' "${gomod}" | head -n 1)"
  directives["${mod}"]="${directive}"
  if [ -z "${directive}" ]; then
    problems+=("${mod}: packages/${mod}/go.mod has no go directive")
  elif [ -n "${expected_go}" ] && [ "${directive}" != "${expected_go}" ]; then
    problems+=("${mod}: go.mod says go ${directive}, expected go ${expected_go} (the locked pkgs.go)")
  fi
done
if [ -z "${expected_go}" ]; then
  distinct="$(printf '%s\n' "${directives[@]}" | sort -u | grep -c . || true)"
  if [ "${distinct}" -gt 1 ]; then
    problems+=("go.mod files disagree on the go directive: $(printf '%s\n' "${directives[@]}" | sort | uniq -c | tr -s ' ' | tr '\n' ';')")
  fi
fi

# The shared updater: --with-deps and a pin equal to the locked gomod2nix rev.
updater="${root}/packages/update-gomod2nix-deps.sh"
if ! grep -Eq 'gomod2nix/\$\{GOMOD2NIX_REV\}" -- generate --with-deps' "${updater}"; then
  problems+=("update-gomod2nix-deps.sh does not run 'generate --with-deps' at the pinned GOMOD2NIX_REV")
fi
pinned="$(sed -n 's/^GOMOD2NIX_REV="\([0-9a-f]\{40\}\)"$/\1/p' "${updater}")"
locked="$(jq -r '.nodes[.nodes.root.inputs.gomod2nix].locked.rev // empty' "${root}/flake.lock")"
if [ -z "${pinned}" ] || [ "${pinned}" != "${locked}" ]; then
  problems+=("update-gomod2nix-deps.sh GOMOD2NIX_REV (${pinned:-<none>}) != flake.lock gomod2nix rev (${locked:-<none>})")
fi

if [ "${#problems[@]}" -gt 0 ]; then
  echo "FAIL: Go dependency refresh is not fully wired (${#problems[@]} problem(s)):" >&2
  printf '  - %s\n' "${problems[@]}" >&2
  exit 1
fi
echo "OK: ${#modules[@]} Go modules wired into update-locks.sh with update-deps.sh wrappers and --with-deps at the locked gomod2nix rev."
