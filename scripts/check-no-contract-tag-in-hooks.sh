#!/usr/bin/env bash
# Guard: no git hook may run a `-tags contract` Go test (HK-2 in
# phillipg-nix-repo-base flake-modules/pre-commit.nix: a git hook MUST NOT run
# any non-unit test kind). The contract-tagged real-bd suites run from
# `checks.<system>.*-bd-contract-tests`, which build the tests against the
# pinned bd.
#
# Usage: check-no-contract-tag-in-hooks.sh <file>...
#
# Fails (exit 1) naming file:line for every non-comment line that carries
# `-tags contract` (also `-tags=contract`, `-tags "contract"`, and a tag list
# that includes contract). Exits 2 on a usage error.
#
# flake.nix is scanned only above its `perSystem =` line (the flake-level
# pre-commit hook definitions): the checks below it legitimately run
# contract-tagged suites. Every other file is scanned whole.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "check-no-contract-tag-in-hooks: usage: $0 <file>..." >&2
  exit 2
fi

status=0
for f in "$@"; do
  scan="$f"
  case "$f" in
  */flake.nix | flake.nix)
    scan="$(mktemp)"
    trap 'rm -f "$scan"' EXIT
    awk '/^      perSystem =$/ { exit } { print }' "$f" >"$scan"
    ;;
  esac
  hits="$(grep -nE -- '-tags[ =]+["'"'"']?([a-z_]+,)*contract([,"'"'"' ]|$)' "$scan" | grep -vE '^[0-9]+:[[:space:]]*#' || true)"
  if [ -n "$hits" ]; then
    while IFS= read -r line; do
      echo "check-no-contract-tag-in-hooks: $f:$line" >&2
    done <<<"$hits"
    status=1
  fi
done
if [ "$status" -ne 0 ]; then
  echo "check-no-contract-tag-in-hooks: a hook runs a contract-tagged test; move it to a checks.<system>.*-bd-contract-tests derivation" >&2
fi
exit "$status"
