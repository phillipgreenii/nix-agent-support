#!/usr/bin/env bash
# Non-sandboxed gate for pg-pr-change's real-tracker tests (docket pg2-6mo45.3,
# packet P3b1b). Body of the `pg-pr-change-contract-test` pre-commit / pre-land hook.
#
# Why: the not-found escalation of pg-pr-change looks for an earlier escalation in
# every status, closed included, with `pg-connector search` against the beads
# tracker. Whether that lookup finds a CLOSED bead is a property of a REAL tool (bd's
# search), so its regression guard runs against a real bd in a throwaway embedded
# store (global rule V-3): the `contract`-tagged TestPRChangeRealBD_* tests in
# packages/pg-decider/internal/prchange, which build the real pg-desk, pg-decider,
# pg-pr-change, pg-connector and pg-connector-issue-beads from source. The nix build
# sandbox has no bd, so checks.<system>.pg-pr-change-real-tools covers everything
# else and this gate covers the one thing it cannot.
#
# The files the hook fires on are in scripts/pg-pr-change-contract-files.regex (the
# one place the flake and tests/pg-pr-change-contract-hook.bats both read).
#
# Usage: run-pg-pr-change-contract.sh [--root <repo-root>]
#
# Exits 0 when the tests pass; the test run's own code when they fail; 2 on a usage
# error or a missing tool. A missing tool is a LOUD failure, never a skip.
set -euo pipefail

root=""
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
  *)
    echo "run-pg-pr-change-contract: unknown argument: $1" >&2
    exit 2
    ;;
  esac
done

if [ -z "${root}" ]; then
  root="$(git rev-parse --show-toplevel)"
fi
for tool in go bd; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "run-pg-pr-change-contract: ${tool} not found on PATH -- the real-tracker tests need it. Enter the devShell or install it before committing. Do NOT bypass with --no-verify." >&2
    exit 2
  fi
done

cd "${root}/packages/pg-decider"
exec nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run '^TestPRChangeRealBD_' ./internal/prchange/
