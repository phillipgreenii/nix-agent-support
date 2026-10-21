#!/usr/bin/env bash
# Non-sandboxed gate for the pg-disk-reclaimer tmp-sweep bats suite (bead pg2-sbvin).
# Body of the `pg-disk-reclaimer-tmp-sweep-test` pre-commit / pre-land hook.
#
# Why: the 46 cases in tests/test-pg-disk-reclaimer-tmp-sweep.bats drive the REAL
# sweep library (pgdr-tmp-sweep.bash) with real find/rm/mv/ps/lsof against mktemp
# fixtures. The darwin nix sandbox denies ps, so inside the nix checks those cases
# skip and the check passes: no automated gate failed when a sweep guard regressed.
# This script runs the suite OUTSIDE any sandbox, with PGDR_REQUIRE_SWEEP_TOOLS=1
# (tests/sweep-tools.bash) so an unusable ps or lsof FAILS the cases instead of
# skipping them, and then refuses a green result that still contains an unexpected
# skip. A skip is allowed only for the two environmental reasons that cannot apply
# everywhere: no BSD userland (Linux) and root ignoring file modes.
#
# Usage: run-tmp-sweep-bats.sh [--root <repo-root>] [--suite <bats-file>]
#   --suite defaults to the package's tmp-sweep suite under <repo-root>.
#
# Exits 0 when every case ran and passed; 1 on a failing case or an unexpected
# skip; 2 on a usage error or a missing tool. Never skips quietly.
set -euo pipefail

root=""
suite=""
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  --suite)
    suite="${2:?--suite needs a bats file}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "run-tmp-sweep-bats: unknown argument: $1" >&2
    exit 2
    ;;
  esac
done

if [ -z "${root}" ]; then
  root="$(git rev-parse --show-toplevel)"
fi
if [ -z "${suite}" ]; then
  suite="${root}/packages/pg-disk-reclaimer/pg-disk-reclaimer/tests/test-pg-disk-reclaimer-tmp-sweep.bats"
fi
if [ ! -f "${suite}" ]; then
  echo "run-tmp-sweep-bats: suite not found: ${suite}" >&2
  exit 2
fi
for tool in bats ps lsof; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "run-tmp-sweep-bats: ${tool} not found on PATH -- the sweep cases need it. Do NOT bypass with --no-verify." >&2
    exit 2
  fi
done

tap="$(mktemp "${TMPDIR:-/tmp}/tmp-sweep-bats.XXXXXX")"
trap 'rm -f "${tap}"' EXIT

rc=0
PGDR_REQUIRE_SWEEP_TOOLS=1 bats --tap "${suite}" >"${tap}" 2>&1 || rc=$?
cat "${tap}"
if [ "${rc}" -ne 0 ]; then
  echo "run-tmp-sweep-bats: the tmp-sweep suite failed (bats exit ${rc})" >&2
  exit 1
fi

# `ok N name # skip reason` lines that are not one of the two allowed reasons.
bad="$(grep -E '^ok [0-9]+ .*# skip' "${tap}" | grep -vE 'no BSD userland here|root ignores file modes' || true)"
if [ -n "${bad}" ]; then
  echo "run-tmp-sweep-bats: unexpected skipped cases (the gate must execute them):" >&2
  echo "${bad}" >&2
  exit 1
fi
ran="$(grep -cE '^ok [0-9]+ ' "${tap}" || true)"
echo "run-tmp-sweep-bats: ${ran} cases ran and passed"
