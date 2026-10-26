#!/usr/bin/env bash
# The real-tracker end-to-end gate of the PR lane (docket pg2-6mo45.3, packet
# P4c-1). Body of the nix check `checks.<system>.pg-decider-lane-e2e` (with
# --prebuilt: nix built the binaries) and of the by-hand run (no flag: the
# binaries are built from source here). It is NOT a commit or pre-land hook: the
# suite spans processes and takes tens of minutes, so operator standard V-4 keeps
# it out of every commit and land gate (bead pg2-by94zu).
#
# What it runs: TestLaneE2E in packages/pg-decider/internal/prchange: the real
# pg-desk, pg-decider, pg-pr-change, pg-connector and pg-connector-issue-beads
# over a real bd in a throwaway embedded store, with the tracker's own listing as
# the ground truth (adoption, the second apply, plan and apply agreeing, an
# unhydrated view refused, the lookup ceiling and a truncated listing failing the
# run closed, the review-item query, and the drift check of pg-desk's decision
# labels against the decider's lists, then the review rule against the real
# tracker, two concurrent composed runs with and without the lock, a duplicate
# delivery, a kill between a create and its audit comment, and a decider run that
# exits 2 through a failing passthrough over bd), and the rollback drill of the first
# apply (the real backfill driver lists every write since the flip marker by
# timestamp, from the audit comments in the real tracker, and reverts each).
#
# What it asserts about the run (a real-binary gate that could pass by running
# nothing guards nothing): the test's own PASS line, NO SKIP line, a PASS line for
# every scenario NAMED in internal/prchange/testdata/lane-e2e-scenarios.txt, and
# at least MIN_SCENARIOS of them (a count of zero never passes).
#
# Usage: run-pg-decider-lane-e2e.sh [--root <repo-root>] [--module-dir <dir>] [--prebuilt]
#
#   --root        the repository root (default: git toplevel)
#   --module-dir  the pg-decider module directory (default: <root>/packages/pg-decider)
#   --prebuilt    use the binaries named by the environment instead of building them:
#                 PG_PR_CHANGE_PG_DESK_BIN, PG_PR_CHANGE_PG_DECIDER_BIN,
#                 PG_PR_CHANGE_BIN, PG_LANE_E2E_PG_PR_BACKFILL_BIN,
#                 PG_LANE_E2E_PG_CONNECTOR_BIN,
#                 PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN (and
#                 PG_PR_CHANGE_SQLITE3_BIN)
#
# Exits 0 when the gate passes; the test run's own code when the tests fail; 1 when
# the run's output fails an assertion above; 2 on a usage error or a missing tool. A
# missing tool is a LOUD failure, never a skip.
set -euo pipefail

# Keep equal to minLaneE2EScenarios in internal/prchange/lane_e2e_test.go and to
# minScenarios of the nix check.
MIN_SCENARIOS=25

root=""
module_dir=""
prebuilt=0
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  --module-dir)
    module_dir="${2:?--module-dir needs a directory}"
    shift 2
    ;;
  --prebuilt)
    prebuilt=1
    shift
    ;;
  -h | --help)
    sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "run-pg-decider-lane-e2e: unknown argument: $1" >&2
    exit 2
    ;;
  esac
done

if [ -z "${module_dir}" ]; then
  if [ -z "${root}" ]; then
    root="$(git rev-parse --show-toplevel)"
  fi
  module_dir="${root}/packages/pg-decider"
fi

tools=(go bd git)
for tool in "${tools[@]}"; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "run-pg-decider-lane-e2e: ${tool} not found on PATH -- the real-tracker gate needs it. Enter the devShell or install it." >&2
    exit 2
  fi
done
if [ -z "${PG_PR_CHANGE_SQLITE3_BIN:-}" ] && ! command -v sqlite3 >/dev/null 2>&1; then
  echo "run-pg-decider-lane-e2e: sqlite3 not found on PATH and PG_PR_CHANGE_SQLITE3_BIN is unset -- the gate reads the pg-desk store with it." >&2
  exit 2
fi

scenarios_file="${module_dir}/internal/prchange/testdata/lane-e2e-scenarios.txt"
if [ ! -f "${scenarios_file}" ]; then
  echo "run-pg-decider-lane-e2e: ${scenarios_file} not found" >&2
  exit 2
fi

scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT

if [ "${prebuilt}" -eq 0 ]; then
  pkgs_dir="$(cd "${module_dir}/.." && pwd -P)"
  build() { # build <module> <out> <package>
    (cd "${pkgs_dir}/$1" && go build -o "${scratch}/bin/$2" "$3")
  }
  mkdir -p "${scratch}/bin"
  build pg-desk pg-desk ./cmd/pg-desk
  build pg-decider pg-decider ./cmd/pg-decider
  build pg-decider pg-pr-change ./cmd/pg-pr-change
  build pg-decider pg-pr-backfill ./cmd/pg-pr-backfill
  build pg-connector pg-connector ./cmd/pg-connector
  build pg-connector pg-connector-issue-beads ./cmd/pg-connector-issue-beads
  export PG_PR_CHANGE_PG_DESK_BIN="${scratch}/bin/pg-desk"
  export PG_PR_CHANGE_PG_DECIDER_BIN="${scratch}/bin/pg-decider"
  export PG_PR_CHANGE_BIN="${scratch}/bin/pg-pr-change"
  export PG_LANE_E2E_PG_PR_BACKFILL_BIN="${scratch}/bin/pg-pr-backfill"
  export PG_LANE_E2E_PG_CONNECTOR_BIN="${scratch}/bin/pg-connector"
  export PG_LANE_E2E_PG_CONNECTOR_ISSUE_BEADS_BIN="${scratch}/bin/pg-connector-issue-beads"
fi
# An unset binary FAILS the run instead of skipping it.
export PG_PR_CHANGE_REQUIRE_BINARIES=1

log="${scratch}/lane-e2e.log"
status=0
(cd "${module_dir}" && nice -n 10 go test -p 4 -count=1 -v -timeout 90m -run '^TestLaneE2E$' ./internal/prchange/) >"${log}" 2>&1 || status=$?
cat "${log}"
if [ "${status}" -ne 0 ]; then
  echo "run-pg-decider-lane-e2e: the end-to-end gate FAILED (go test exit ${status})" >&2
  exit "${status}"
fi

if ! grep -q -- '^--- PASS: TestLaneE2E ' "${log}"; then
  echo "run-pg-decider-lane-e2e: TestLaneE2E did not report PASS (skipped or not run)" >&2
  exit 1
fi
if grep -q -- '--- SKIP' "${log}"; then
  echo "run-pg-decider-lane-e2e: a test or scenario was SKIPPED" >&2
  grep -- '--- SKIP' "${log}" >&2
  exit 1
fi
found=0
while IFS= read -r name; do
  case "${name}" in '' | '#'*) continue ;; esac
  if ! grep -qF -- "    --- PASS: TestLaneE2E/${name} " "${log}"; then
    echo "run-pg-decider-lane-e2e: scenario ${name} has no PASS line" >&2
    exit 1
  fi
  found=$((found + 1))
done <"${scenarios_file}"
if [ "${MIN_SCENARIOS}" -lt 1 ] || [ "${found}" -lt "${MIN_SCENARIOS}" ]; then
  echo "run-pg-decider-lane-e2e: only ${found} scenarios named and passed, at least ${MIN_SCENARIOS} are required" >&2
  exit 1
fi
echo "run-pg-decider-lane-e2e: ${found} scenarios, each with a PASS line, none skipped"
