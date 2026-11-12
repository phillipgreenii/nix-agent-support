#!/usr/bin/env bash
# Public-repo check: this repository is public, so it MUST contain no employer-specific
# name, host or tracker. The forbidden tokens are NEVER committed to any repository; the
# check reads them from a machine-local plain-text file, one token per line (blank lines
# and lines starting with # ignored), at $PG_PUBLIC_REPO_DENYLIST when that variable is
# set, else $XDG_CONFIG_HOME/pg-public-repo/denylist.txt (XDG_CONFIG_HOME defaulting to
# ~/.config). The file is managed by the deployment repository as a home-manager option.
# It is NOT the identifier-allowlist hook, which is an allowlist of safe handles and
# holds no employer names.
#
# Matching is case-insensitive and literal over the git-tracked files of the root (or
# every regular file under it when it is not a git checkout).
#
# When the file is absent or holds no token the check prints
#   skipped: no denylist on this machine
# and exits 0. A skipped run is NEVER a pass: the output says "skipped", never "passed".
#
# Usage: check-public-repo-denylist.sh [--root DIR]     (default: the current directory)
#
# Exits 0 when no token is found (prints "passed: ...") or when skipped; 1 listing each
# hit as path:line (the token itself is not printed); 2 on a usage error.
set -euo pipefail

root="$PWD"
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -/p' "$0" | sed '$d'
    exit 0
    ;;
  *)
    echo "check-public-repo-denylist: unknown argument $1" >&2
    exit 2
    ;;
  esac
done
[ -d "$root" ] || {
  echo "check-public-repo-denylist: --root $root is not a directory" >&2
  exit 2
}

denylist="${PG_PUBLIC_REPO_DENYLIST:-${XDG_CONFIG_HOME:-$HOME/.config}/pg-public-repo/denylist.txt}"
if [ ! -f "$denylist" ]; then
  echo "skipped: no denylist on this machine"
  exit 0
fi

tokens="$(mktemp)"
files="$(mktemp)"
trap 'rm -f "$tokens" "$files"' EXIT
while IFS= read -r line || [ -n "$line" ]; do
  line="${line%$'\r'}"
  case "$line" in '' | '#'*) continue ;; esac
  printf '%s\n' "$line" >>"$tokens"
done <"$denylist"
if [ ! -s "$tokens" ]; then
  echo "skipped: no denylist on this machine"
  exit 0
fi

if [ "$(cd "$root" && pwd -P)" = "$(git -C "$root" rev-parse --show-toplevel 2>/dev/null || true)" ]; then
  git -C "$root" ls-files -z --cached --others --exclude-standard >"$files"
else
  (cd "$root" && find . -path ./.git -prune -o -type f -print0) |
    while IFS= read -r -d '' f; do printf '%s\0' "${f#./}"; done >"$files"
fi

out="$(cd "$root" && xargs -0 rg -n -i -F -f "$tokens" --no-heading --with-filename --color never -- <"$files" || true)"
if [ -n "$out" ]; then
  printf '%s\n' "$out" | cut -d: -f1,2
  echo "check-public-repo-denylist: $(printf '%s\n' "$out" | wc -l | tr -d ' ') hit(s); the denylist file is $denylist" >&2
  exit 1
fi
echo "passed: no denylisted token in $(tr -cd '\0' <"$files" | wc -c | tr -d ' ') files (denylist file $denylist)"
