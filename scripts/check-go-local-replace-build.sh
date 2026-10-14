#!/usr/bin/env bash
# Cross-module build guard for Go modules joined by a local `replace => ../<module>`
# (bead pg2-cahkj). Body of the `go-local-replace-build` pre-commit / pre-land hook.
#
# Why: pg-desk and work-report consume pg-connector through a local
# `replace => ../pg-connector`. A dependency bump synced into pg-connector's
# go.mod/go.sum alone leaves those dependents needing a go.mod update, so their
# `go build ./...` fails -- but the hooks only ran on the pg-connector diff and
# nothing caught it until the next dependent branch tried to land (pg2-wyttf).
#
# Usage: check-go-local-replace-build.sh [--root <repo-root>] [<changed-file>...]
#   Changed files are paths relative to the repo root (what prek hands a hook).
#   A file triggers the check only when it is `<dir>/go.mod` or `<dir>/go.sum`
#   of a module under <root>/packages/. For each triggering module M, every OTHER
#   module under <root>/packages/ whose go.mod has a `=> ../M` replace gets
#   `go build ./...`. Any other file, or a module nobody replaces, costs nothing:
#   no `go` process is started.
#
# Exits 0 when every dependent builds (or nothing triggered); exits 1 after
# building EVERY dependent and listing each failure; exits 2 on a usage error.
# A missing `go` is a LOUD failure (exit 1) only when a build is actually needed.
set -euo pipefail

root=""
files=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  --)
    shift
    files+=("$@")
    break
    ;;
  -*)
    echo "check-go-local-replace-build: unknown option: $1" >&2
    exit 2
    ;;
  *)
    files+=("$1")
    shift
    ;;
  esac
done

if [ -z "${root}" ]; then
  root="$(git rev-parse --show-toplevel)"
fi
packages="${root}/packages"

# Modules whose go.mod/go.sum changed (just the module directory names).
changed=()
for f in "${files[@]+"${files[@]}"}"; do
  if [[ ${f} =~ ^packages/([^/]+)/go\.(mod|sum)$ ]]; then
    changed+=("${BASH_REMATCH[1]}")
  fi
done
if [ "${#changed[@]}" -eq 0 ]; then
  exit 0
fi

# Every module dir that has a local `=> ../<changed>` replace (comment lines are
# prose, not replaces). Single-line and `replace ( ... )` block forms both end in
# `=> ../<dir>` once the leading directive is stripped.
dependents=()
for gomod in "${packages}"/*/go.mod; do
  [ -e "${gomod}" ] || continue
  dep="$(basename "$(dirname "${gomod}")")"
  targets="$(
    sed -E '/^[[:space:]]*\/\//d' "${gomod}" |
      sed -nE 's|.*=>[[:space:]]*\.\./([^/[:space:]]+)/?[[:space:]]*(//.*)?$|\1|p'
  )"
  for mod in $(printf '%s\n' "${changed[@]}" | sort -u); do
    [ "${mod}" = "${dep}" ] && continue
    if printf '%s\n' "${targets}" | grep -Fxq -- "${mod}"; then
      dependents+=("${dep}")
      break
    fi
  done
done
if [ "${#dependents[@]}" -eq 0 ]; then
  exit 0
fi

if ! command -v go >/dev/null 2>&1; then
  echo "check-go-local-replace-build: go not found on PATH -- enter the devShell or install go before committing. Do NOT bypass with --no-verify." >&2
  exit 1
fi

# A workspace file would change module resolution; the build must see only each
# module's own go.mod, exactly as `nix build` does.
export GOWORK=off

failed=()
for dep in "${dependents[@]}"; do
  echo "check-go-local-replace-build: go build ./... in packages/${dep}" >&2
  if ! out="$(cd "${packages}/${dep}" && nice -n 10 go build ./... 2>&1)"; then
    printf '%s\n' "${out}" >&2
    failed+=("${dep}")
  fi
done

if [ "${#failed[@]}" -gt 0 ]; then
  echo "FAIL: ${#failed[@]} module(s) joined by a local replace no longer build (a go.mod/go.sum change was not synced into them):" >&2
  printf '  - packages/%s\n' "${failed[@]}" >&2
  echo "Fix: sync the dependency change into each listed module (go mod tidy there, then its update-deps.sh)." >&2
  exit 1
fi
echo "OK: ${#dependents[@]} dependent module(s) of the changed go.mod/go.sum still build." >&2
