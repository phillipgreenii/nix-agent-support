#!/usr/bin/env bash
# The end-to-end suite of the PR lane (docket pg2-6mo45.3, packet P4c-1; bead
# pg2-s4xeju split it into a fast run and a soak). One set of scenarios in
# packages/pg-decider/internal/prchange runs two ways:
#
#   --suite fake  TestLaneFake: over the fake bd of internal/fakebd, the
#                 independent scenarios in parallel. It takes seconds to a couple
#                 of minutes; it is the body of the nix check
#                 `checks.<system>.pg-decider-lane-fake` (with --prebuilt).
#   --suite soak  TestLaneE2E (build tag laneE2E, the default suite): over a REAL
#                 bd in a throwaway embedded store, one scenario after another,
#                 plus the scenario that tests bd's own query semantics. Every bd
#                 call is a cold start of an embedded dolt engine, so it takes tens
#                 of minutes (84 on a loaded host). It is NOT a flake check and NOT
#                 a commit or pre-land hook (operator standard V-4; operator ruling
#                 2026-10-28, bead pg2-s4xeju): run it by hand, here or with
#                 `nix run .#pg-decider-lane-e2e-soak`.
#
# What both run for real: pg-desk, pg-decider, pg-pr-change, pg-pr-backfill,
# pg-connector and pg-connector-issue-beads, with the tracker's own listing as the
# ground truth (adoption, the second apply, plan and apply agreeing, an unhydrated
# view refused, the lookup ceiling and a truncated listing failing the run closed,
# the drift check of pg-desk's decision labels against the decider's lists, the
# review rule, two concurrent composed runs with and without the lock, a duplicate
# delivery, a kill between a create and its audit comment, a decider run that exits
# 2 through a failing passthrough over bd, and the rollback drill of the first
# apply).
#
# What it asserts about the run (a real-binary gate that could pass by running
# nothing guards nothing): the test's own PASS line, NO SKIP line, a PASS line for
# every scenario NAMED in the suite's list (internal/prchange/testdata/
# lane-fake-scenarios.txt or lane-e2e-scenarios.txt), and at least the suite's
# minimum of them (a count of zero never passes).
#
# Usage: run-pg-decider-lane-e2e.sh [--suite fake|soak] [--root <repo-root>] [--module-dir <dir>] [--prebuilt]
#
#   --suite       fake (TestLaneFake) or soak (TestLaneE2E, the default)
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

# Keep equal to minLaneFakeScenarios in internal/prchange/lane_fake_test.go.
MIN_SCENARIOS_FAKE=24
# Keep equal to minLaneE2EScenarios in internal/prchange/lane_e2e_test.go.
MIN_SCENARIOS_SOAK=25

suite="soak"
root=""
module_dir=""
prebuilt=0
while [ "$#" -gt 0 ]; do
  case "$1" in
  --suite)
    suite="${2:?--suite needs fake or soak}"
    shift 2
    ;;
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

case "${suite}" in
fake)
  test_name="TestLaneFake"
  tags=()
  list="lane-fake-scenarios.txt"
  min_scenarios="${MIN_SCENARIOS_FAKE}"
  timeout="10m"
  tools=(go)
  ;;
soak)
  test_name="TestLaneE2E"
  tags=(-tags laneE2E)
  list="lane-e2e-scenarios.txt"
  min_scenarios="${MIN_SCENARIOS_SOAK}"
  timeout="90m"
  tools=(go bd git)
  ;;
*)
  echo "run-pg-decider-lane-e2e: --suite must be fake or soak, got: ${suite}" >&2
  exit 2
  ;;
esac

if [ -z "${module_dir}" ]; then
  if [ -z "${root}" ]; then
    root="$(git rev-parse --show-toplevel)"
  fi
  module_dir="${root}/packages/pg-decider"
fi

for tool in "${tools[@]}"; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "run-pg-decider-lane-e2e: ${tool} not found on PATH -- the ${suite} suite needs it. Enter the devShell or install it." >&2
    exit 2
  fi
done
if [ -z "${PG_PR_CHANGE_SQLITE3_BIN:-}" ] && ! command -v sqlite3 >/dev/null 2>&1; then
  echo "run-pg-decider-lane-e2e: sqlite3 not found on PATH and PG_PR_CHANGE_SQLITE3_BIN is unset -- the suite reads the pg-desk store with it." >&2
  exit 2
fi

scenarios_file="${module_dir}/internal/prchange/testdata/${list}"
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

log="${scratch}/lane-${suite}.log"
status=0
(cd "${module_dir}" && nice -n 10 go test -p 4 -count=1 -v ${tags[@]+"${tags[@]}"} -timeout "${timeout}" -run "^${test_name}\$" ./internal/prchange/) >"${log}" 2>&1 || status=$?
cat "${log}"
if [ "${status}" -ne 0 ]; then
  echo "run-pg-decider-lane-e2e: the ${suite} suite FAILED (go test exit ${status})" >&2
  exit "${status}"
fi

if ! grep -q -- "^--- PASS: ${test_name} " "${log}"; then
  echo "run-pg-decider-lane-e2e: ${test_name} did not report PASS (skipped or not run)" >&2
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
  if ! grep -qF -- "    --- PASS: ${test_name}/${name} " "${log}"; then
    echo "run-pg-decider-lane-e2e: scenario ${name} has no PASS line" >&2
    exit 1
  fi
  found=$((found + 1))
done <"${scenarios_file}"
if [ "${min_scenarios}" -lt 1 ] || [ "${found}" -lt "${min_scenarios}" ]; then
  echo "run-pg-decider-lane-e2e: only ${found} scenarios named and passed, at least ${min_scenarios} are required" >&2
  exit 1
fi
echo "run-pg-decider-lane-e2e: ${suite} suite: ${found} scenarios, each with a PASS line, none skipped"
