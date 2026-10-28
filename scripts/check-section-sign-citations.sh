#!/usr/bin/env bash
# Section-sign citation ban (bead pg2-2yayms). Body of the `section-sign-citations`
# pre-commit / pre-land hook; the single enforcement point that replaced the two Go
# tests (ccpool and pg-connector spec_citations_test.go) and the nix check
# test-ccpool-surface-spec-citations (beads pg2-oxrha, pg2-qkk8n, pg2-hidkm).
#
# Rule (CLAUDE.md "Citation conventions", rule 3): no file in the guarded scope may
# contain the section sign (U+00A7). Cite an ADR by number and PROSE heading name,
# never by a section number: section numbers renumber silently and the specs they
# pointed at live outside this repo.
#
# Usage: check-section-sign-citations.sh [--root <repo-root>] [<changed-file>...]
#   Changed files are paths relative to the repo root (what prek hands a hook).
#   With changed files: when NONE of them lies in the guarded scope the script exits 0
#   at once; otherwise the WHOLE scope is scanned (about 0.2 s), so the liveness
#   canaries below always run against the full surface. With no files: scan the scope.
#   Only git-tracked regular files are scanned (so `git add` a new file first), and
#   .db, .out and .test files are skipped (build artifacts, never sources).
#
# Exits 0 when the scope is clean; exits 1 listing every `file:line` that carries the
# sign, or a failed liveness canary; exits 2 on a usage error.
set -euo pipefail

# --- THE guarded scope: one row per surface, so adding a surface is one edit. -------
# Row format:  <path under the repo root> | <minimum files scanned> | <required file>
# The minimum and the required file are LIVENESS CANARIES: the expected violation count
# is zero, so "found nothing" must not be able to double as proof the scan ran. A
# rename, a mis-scoped path or an emptied directory FAILS loudly instead of passing
# vacuously. The scope is deliberately NOT repo-wide (the sign is legitimate in ADR
# prose and historical plans elsewhere).
SURFACES=(
  "packages/ccpool|20|packages/ccpool/default.nix"
  "home/programs/ccpool|1|home/programs/ccpool/default.nix"
  "darwin/modules/ccpool|1|darwin/modules/ccpool/default.nix"
  "nixos/modules/ccpool|1|nixos/modules/ccpool/default.nix"
  "packages/pg-connector|100|packages/pg-connector/default.nix"
)

# The sign is built from its UTF-8 bytes so THIS file never contains the character it
# forbids, and compared bytewise (LC_ALL=C), independent of the caller's locale.
sign="$(printf '\302\247')"
export LC_ALL=C

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
    echo "check-section-sign-citations: unknown option: $1" >&2
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

# Cheap path: files were handed over and none is inside the guarded scope.
if [ "${#files[@]}" -gt 0 ]; then
  in_scope=0
  for f in "${files[@]}"; do
    for row in "${SURFACES[@]}"; do
      if [[ ${f} == "${row%%|*}"/* ]]; then
        in_scope=1
        break 2
      fi
    done
  done
  if [ "${in_scope}" -eq 0 ]; then
    exit 0
  fi
fi

failures=()
scan=()
total=0
for row in "${SURFACES[@]}"; do
  IFS='|' read -r path min required <<<"${row}"
  surface_files=()
  saw_required=0
  while IFS= read -r -d '' f; do
    case "${f}" in
    *.db | *.out | *.test) continue ;;
    esac
    [ -f "${root}/${f}" ] || continue
    surface_files+=("${f}")
    [ "${f}" = "${required}" ] && saw_required=1
  done < <(git -C "${root}" ls-files -z -- "${path}")

  if [ "${saw_required}" -eq 0 ]; then
    failures+=("canary: ${path}: never scanned ${required} (renamed or moved out from under the guard; update SURFACES in scripts/check-section-sign-citations.sh)")
  fi
  if [ "${#surface_files[@]}" -lt "${min}" ]; then
    failures+=("canary: ${path}: scanned only ${#surface_files[@]} file(s), expected at least ${min} (the scan is not covering the surface)")
  fi
  total=$((total + ${#surface_files[@]}))
  scan+=("${surface_files[@]+"${surface_files[@]}"}")
done

if [ "${#scan[@]}" -gt 0 ]; then
  # `grep` exits 1 on "no match", which is the good case here. -I skips binaries
  # (a stray compiled output), -H keeps the file name even for a single file.
  hits="$(cd "${root}" && grep -nHIF -e "${sign}" -- "${scan[@]}" || [ "$?" -eq 1 ])"
  while IFS= read -r hit; do
    [ -n "${hit}" ] || continue
    loc="$(printf '%s\n' "${hit}" | sed -E 's/^([^:]+):([0-9]+):.*/\1:\2/')"
    failures+=("${loc}: forbidden section sign (U+00A7)")
  done <<<"${hits}"
fi

if [ "${#failures[@]}" -gt 0 ]; then
  for msg in "${failures[@]}"; do
    echo "check-section-sign-citations: ${msg}" >&2
  done
  echo "FAIL: ${#failures[@]} problem(s). Cite durable owners by number and PROSE heading name (e.g. \"ADR 0038's Context\"), never by a section number; see CLAUDE.md \"Citation conventions\"." >&2
  exit 1
fi
echo "OK: ${total} file(s) scanned across ${#SURFACES[@]} surface(s); no section-sign citations." >&2
