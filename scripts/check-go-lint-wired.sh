#!/usr/bin/env bash
# Guard: every Go module under packages/ (a directory holding a go.mod) MUST have
# a `<module>-golangci` flake check, unless scripts/go-lint-exempt.txt names it.
# The flake passes the names of the checks it actually defines, so a new module
# that nobody wired into the lint lists turns `checks.<system>.go-deps-wired` red.
#
# Usage: check-go-lint-wired.sh <repo-root> <linted-checks-file> <exempt-file>
#   <linted-checks-file>  one check name per line (e.g. `pg-desk-golangci`)
#   <exempt-file>         one module name per line; `#` comments and blanks ignored
#
# Exits 0 when covered; exits 1 after listing EVERY problem (an uncovered module,
# or an exemption that names a module that is now linted or no longer exists).
set -euo pipefail

root="${1:?usage: check-go-lint-wired.sh <repo-root> <linted-checks-file> <exempt-file>}"
linted="${2:?missing <linted-checks-file>}"
exempt="${3:?missing <exempt-file>}"
problems=()

mapfile -t exempt_mods < <(sed -e 's/#.*//' -e 's/[[:space:]]*$//' "${exempt}" | grep -v '^$' || true)

modules=()
for gomod in "${root}"/packages/*/go.mod; do
  [ -e "${gomod}" ] || continue
  modules+=("$(basename "$(dirname "${gomod}")")")
done
if [ "${#modules[@]}" -eq 0 ]; then
  echo "FAIL: found no packages/*/go.mod under ${root} (wrong root?)" >&2
  exit 1
fi

has() {
  local needle="$1"
  shift
  [ "$#" -gt 0 ] && printf '%s\n' "$@" | grep -Fxq -- "${needle}"
}

mapfile -t linted_names <"${linted}"
for mod in "${modules[@]}"; do
  if has "${mod}-golangci" "${linted_names[@]}"; then
    if has "${mod}" "${exempt_mods[@]}"; then
      problems+=("${mod}: has a ${mod}-golangci check but is still listed as exempt; remove it from the exemption file")
    fi
  elif ! has "${mod}" "${exempt_mods[@]}"; then
    problems+=("${mod}: packages/${mod}/go.mod has no ${mod}-golangci flake check and is not in the exemption file")
  fi
done
for mod in "${exempt_mods[@]}"; do
  [ -e "${root}/packages/${mod}/go.mod" ] || problems+=("${mod}: exempt but packages/${mod}/go.mod does not exist")
done

if [ "${#problems[@]}" -gt 0 ]; then
  echo "FAIL: Go lint coverage is incomplete (${#problems[@]} problem(s)):" >&2
  printf '  - %s\n' "${problems[@]}" >&2
  exit 1
fi
echo "OK: ${#modules[@]} Go modules each have a golangci check or a named exemption."
