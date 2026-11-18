#!/usr/bin/env bash
# go-work-vet: type-check across Go modules with the root go.work active.
#
# Each module builds with GOWORK=off against its own go.mod, so a change in one
# module that breaks a module consuming it through `replace => ../<module>` is
# invisible to that module's own checks. This gate runs `go vet ./...` with the
# work file active over (a) every module with a changed non-test Go file and
# (b) every module that consumes such a module by replace.
#
# Usage: go-work-vet.sh [--root DIR] [FILE...]
#   FILE...   changed paths (relative to the root). The set of changed modules is
#             the modules owning a non-test .go file among them. With NO files
#             the gate runs over every module ("passes on the whole workspace").
# Not a unit test: it type-checks across modules, so it runs under
# `pg-hooks run pre-land` (the hook wrapper skips it at commit time), never at commit.
set -euo pipefail

root=""
if [ "${1:-}" = "--root" ]; then
  root="${2:?--root needs a directory}"
  shift 2
fi
if [ -z "${root}" ]; then
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi
if [ ! -f "${root}/go.work" ]; then
  echo "FAIL: ${root}/go.work is missing (scripts/gen-go-work.sh --write)" >&2
  exit 1
fi

all=()
for gomod in "${root}"/packages/*/go.mod; do
  [ -e "${gomod}" ] || continue
  all+=("$(basename "$(dirname "${gomod}")")")
done

declare -A pick=()
if [ "$#" -eq 0 ]; then
  for m in "${all[@]}"; do pick["${m}"]=1; done
else
  declare -A changed=()
  for f in "$@"; do
    case "${f}" in
    packages/*/*.go)
      case "${f}" in *_test.go) continue ;; esac
      m="${f#packages/}"
      m="${m%%/*}"
      [ -f "${root}/packages/${m}/go.mod" ] && changed["${m}"]=1
      ;;
    esac
  done
  for m in "${!changed[@]}"; do
    pick["${m}"]=1
    for c in "${all[@]}"; do
      if grep -Eq "=>[[:space:]]+\.\./${m}[[:space:]]*\$" "${root}/packages/${c}/go.mod"; then
        pick["${c}"]=1
      fi
    done
  done
fi

if [ "${#pick[@]}" -eq 0 ]; then
  echo "go-work-vet: no module has a changed non-test Go file; nothing to vet."
  exit 0
fi

# Workspace mode accepts only -mod=readonly|vendor: drop an inherited -mod=...
flags="${GOFLAGS:-}"
flags="$(printf '%s' "${flags}" | sed -E 's/(^| )-mod=[^ ]*//g')"
export GOFLAGS="${flags}"
export GOWORK="${root}/go.work"

# go.work.sum snapshot: go (and gopls) append missing sums to it when the workspace
# is active, which dirties the tracked file. Detect that here instead of letting a
# later editor run rewrite it in the canonical clone.
sumfile="${root}/go.work.sum"
sum_before="$(mktemp)"
trap 'rm -f "${sum_before}"' EXIT
sum_existed=0
if [ -f "${sumfile}" ]; then
  sum_existed=1
  cp -f "${sumfile}" "${sum_before}"
fi

failed=()
for m in $(printf '%s\n' "${!pick[@]}" | LC_ALL=C sort); do
  echo "go-work-vet: go vet ./... in packages/${m}" >&2
  if ! (cd "${root}/packages/${m}" && go vet ./...); then
    failed+=("${m}")
  fi
done
if [ "${#failed[@]}" -gt 0 ]; then
  echo "FAIL: go-work-vet found type errors in: ${failed[*]}" >&2
  exit 1
fi
if [ "${sum_existed}" -eq 1 ] && ! cmp -s "${sum_before}" "${sumfile}"; then
  cp -f "${sum_before}" "${sumfile}"
  echo "FAIL: go.work.sum is incomplete: go wanted new sums. Run 'go work sync' (or this vet with GOFLAGS=-mod=mod) in a worktree and commit go.work.sum." >&2
  exit 1
fi
if [ "${sum_existed}" -eq 0 ] && [ -f "${sumfile}" ]; then
  rm -f "${sumfile}"
  echo "FAIL: go.work.sum did not exist and go created it: run 'go work sync' and commit go.work.sum." >&2
  exit 1
fi
echo "OK: go-work-vet passed over ${#pick[@]} module(s)."
