#!/usr/bin/env bash
# ALR-1 check: the pg-router gates are gone, so their names MUST NOT appear anywhere
# in the workspace repos except the superseded design and investigation documents,
# the history documents and the retained legacy-input fixtures listed in the allowlist
# (docs/alr1-allowlist.txt).
#
# Banned names (whole-word matches):
#   LOW_DISK_USAGE  BEAD_SERVER_DOWN  SYSTEM_PAUSE  non_blocking_gates
#   "gate list"  "gate set"  pg_router_gate_*  pg_router_gates_active
#   disk-watchdog  disk-check-tick
# The unrelated bd gate lifecycle is not matched: "gate list" and "gate set" count only
# when NOT preceded by "bd ", and delegate list does not match (whole-word).
#
# Usage: check-gate-names-gone.sh [--allowlist FILE] [NAME=]ROOT...
#   ROOT          a repository root; NAME is the repository directory name used as the
#                 first path component of an allowlist glob (default: basename of ROOT;
#                 give NAME= when the root lives under another name, as in the nix store)
#   --allowlist   default: <first ROOT>/docs/alr1-allowlist.txt
# The allowlist holds one glob per line (blank lines and lines starting with # are
# ignored), each prefixed by the repository name, e.g. nix-agent-support/docs/adr/x.md.
# In a glob `*` and `**` both match across slashes. Files are the git-tracked files of a
# root that is a git checkout, else every regular file under it (not .git).
#
# Prints each hit as NAME/path:line:token and exits 0 when clean, 1 on any hit, 2 on a
# usage error.
set -euo pipefail

allowlist=""
roots=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --allowlist)
    allowlist="${2:?--allowlist needs a file}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -/p' "$0" | sed '$d'
    exit 0
    ;;
  -*)
    echo "check-gate-names-gone: unknown option $1" >&2
    exit 2
    ;;
  *)
    roots+=("$1")
    shift
    ;;
  esac
done
[ "${#roots[@]}" -gt 0 ] || {
  echo "check-gate-names-gone: give at least one repository root" >&2
  exit 2
}

names=()
paths=()
for r in "${roots[@]}"; do
  if [[ $r == *=* ]]; then
    n="${r%%=*}"
    p="${r#*=}"
  else
    p="$r"
    n="$(basename "$(cd "$p" 2>/dev/null && pwd)")"
  fi
  [ -d "$p" ] || {
    echo "check-gate-names-gone: $p is not a directory" >&2
    exit 2
  }
  names+=("$n")
  paths+=("$p")
done

[ -n "$allowlist" ] || allowlist="${paths[0]}/docs/alr1-allowlist.txt"
[ -f "$allowlist" ] || {
  echo "check-gate-names-gone: allowlist $allowlist not found" >&2
  exit 2
}

globs=()
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%%[[:space:]]}"
  case "$line" in '' | '#'*) continue ;; esac
  globs+=("$line")
done <"$allowlist"

# One PCRE pattern. The capture group is the token printed with each hit.
pattern='(?i:\b(?<!bd )(gate list|gate set)\b)|\b(LOW_DISK_USAGE|BEAD_SERVER_DOWN|SYSTEM_PAUSE|non_blocking_gates|pg_router_gates_active|disk-watchdog|disk-check-tick)\b|\bpg_router_gate_\w*'

hits=0
for i in "${!paths[@]}"; do
  name="${names[$i]}"
  root="${paths[$i]}"
  list="$(mktemp)"
  if git -C "$root" rev-parse --show-toplevel >/dev/null 2>&1 &&
    [ "$(cd "$root" && pwd -P)" = "$(git -C "$root" rev-parse --show-toplevel)" ]; then
    git -C "$root" ls-files -z --cached --others --exclude-standard >"$list"
  else
    (cd "$root" && find . -path ./.git -prune -o -type f -print0) >"$list"
  fi
  kept="$(mktemp)"
  while IFS= read -r -d '' f; do
    f="${f#./}"
    [ -f "$root/$f" ] || continue
    skip=0
    for g in "${globs[@]}"; do
      # shellcheck disable=SC2053 # the glob is meant to be a pattern
      if [[ "$name/$f" == $g ]]; then
        skip=1
        break
      fi
    done
    [ "$skip" -eq 1 ] || printf '%s\0' "$f" >>"$kept"
  done <"$list"
  if [ -s "$kept" ]; then
    out="$(cd "$root" && xargs -0 rg -n -o -P --no-heading --with-filename --color never -- "$pattern" <"$kept" || true)"
    if [ -n "$out" ]; then
      printf '%s\n' "$out" | while IFS= read -r l; do printf '%s/%s\n' "$name" "$l"; done
      hits=$((hits + $(printf '%s\n' "$out" | wc -l)))
    fi
  fi
  rm -f "$list" "$kept"
done

if [ "$hits" -gt 0 ]; then
  echo "check-gate-names-gone: $hits hit(s) outside the allowlist ($allowlist)" >&2
  exit 1
fi
echo "check-gate-names-gone: no gate names outside the allowlist"
