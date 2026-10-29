# pg-decider

Decider that plans and applies work-item changes from the pg-desk composite view. Product-level
behavior lives in `docs/behavior/pg-decider/`; this page covers building and testing the package.

## Tests

- Whole module: `nix build .#checks.<system>.pg-decider-go-tests` (or `go test ./...` in this
  directory).
- Property tests (`internal/workitem/property_test.go`, `internal/rules/property_test.go`): the dedup
  key round trips, key comparison is symmetric and never matches across entities (`#1`, `#10`,
  `#100`), adoption by title shape never adopts into another entity, and the plan after an apply is
  empty over generated views (with a node id, the one remaining step is the deliberate rewrite of the
  keys to the node_id form). They use `pgregory.net/rapid`, as `pg-task-focus` does, vendored through
  `go.mod` and `gomod2nix.toml`, with the seed pinned to 1 in code (`RAPID_SEED` or `-rapid.seed`
  overrides it, e.g. to look for new failures).
- Parity gate (the decider against its frozen golden): `nix build .#checks.<system>.pg-decider-parity-gate -L`.
- `pg-pr-change` real-tool gate (the real `pg-desk`, `pg-decider` and `pg-pr-change` against a
  fixture-driven connector): `nix build .#checks.<system>.pg-pr-change-real-tools -L`. The tests that
  need a real `bd` (the not-found escalation's lookup across closed beads) carry the `contract` build
  tag and run from the `pg-pr-change-contract-test` hook, or by hand with
  `go test -tags contract -run RealBD ./internal/prchange/`.

- End-to-end suite of the PR lane (the real `pg-desk`, `pg-decider`, `pg-pr-change`, `pg-pr-backfill`,
  `pg-connector` and beads backend over a tracker, so the tracker's own listing is the ground truth). One
  set of scenarios (`internal/prchange/lane_world_test.go` describes them) runs two ways (bead
  pg2-s4xeju; operator ruling 2026-10-28: a test must not run for 1.5 hours):
  - The fast run, `TestLaneFake` (`internal/prchange/lane_fake_test.go`), over the fake `bd` of
    `internal/fakebd` (a JSON-file store answering bd's own command line, built by the test), the
    independent scenarios in parallel. It takes seconds to a couple of minutes and is the suite's
    automatic run: `nix build .#checks.<system>.pg-decider-lane-fake -L`, which runs
    `scripts/run-pg-decider-lane-e2e.sh --suite fake` and asserts a PASS line for every scenario named in
    `internal/prchange/testdata/lane-fake-scenarios.txt`, no SKIP line and a minimum scenario count.
  - The soak, `TestLaneE2E` (`internal/prchange/lane_e2e_test.go`, build tag `laneE2E`), over a real
    `bd` in a throwaway embedded store, one scenario after another, plus the one scenario that tests bd's
    own `bd list` query of a PR's review items in every state. Every `bd` call starts an embedded dolt
    engine, so it takes tens of minutes (84 on a loaded host): it is NOT a flake check and NOT a commit
    or pre-land hook (operator standard V-4, bead pg2-by94zu). Run it by hand when a change to `bd`, the
    beads backend or the lane's tracker calls warrants it: `nix run .#pg-decider-lane-e2e-soak` from the
    repository root, or `bash scripts/run-pg-decider-lane-e2e.sh` (it builds the six binaries; `bd`, `go`,
    `git` and `sqlite3` must be on `PATH`, and a missing one is a loud failure). It asserts a PASS line
    for every scenario named in `internal/prchange/testdata/lane-e2e-scenarios.txt`.

  Both skip in a plain `go test ./...` (they need the binaries). The scenarios: N keyless anchors adopted
  with no creates, the second apply writing nothing, plan and apply agreeing per kind and operation, an
  unhydrated view refused, a lookup listing above the ceiling or reporting itself truncated failing the
  run closed, the review rule (a burst of pushes, claimed, closed, legacy suffix-less, deferred and
  force-reviewed items, a claim landing between the listing and the update, a reopened PR), two
  concurrent composed runs for one PR (one anchor with the lock, two with a double that skips it, the
  interleaving forced by the fake forge's `block` control and a create barrier), a duplicate delivery,
  a kill between a tracker create and its audit comment, a decider run that exits 2 through a
  passthrough shim over `bd` that fails the Nth write, the rollback drill of the first apply, and the
  drift check of pg-desk's zero-filled `pg_decider_decisions_total` rule and reason labels against the
  decider's own lists (a change to `internal/action` reasons or to a registered rule needs
  `packages/pg-desk/internal/lanestate/vocab.go` updated in the same change, or the fast run fails). The
  fake `bd` fails loudly on any verb or flag it does not know, so a lane change that starts using more
  of `bd` fails the fast run rather than passing on a guess; such a change extends `internal/fakebd` and
  is worth a soak run.

## `pg-pr-change`

`cmd/pg-pr-change` and `internal/prchange` are the composed command a `pr-change` role runs for one
PR id: a per-PR lock, `pg-desk pr refresh`, a fail-closed view check, then `pg-decider plan|apply`
(with the delivery key in apply mode), and a record of the run: one JSON line in the run record
(`$XDG_STATE_HOME/pg-pr-change/pr-change-runs.jsonl`) and the lane state through `pg-desk pr lane-record`.
It ships in this package's output beside `pg-decider`, with `pg-desk`, `pg-connector` and the sibling
`pg-decider` on its `PATH`. Exit codes, failure classes, the delivery key, the run record and the not-found escalation are in
`docs/behavior/pg-decider/pr-change.md`.

## `pg-pr-backfill`

`cmd/pg-pr-backfill` and `internal/backfill` are the PR lane's backfill driver: `enumerate` lists the
backfill set (the tracker's open anchors plus the open PRs of the deployment's queries), `mark-watched`
is the cutover's marking step (read each open-anchor PR with the watch option, refresh it hydrate-only,
check the daemon's watched count against the open-anchor count), `run --mode
plan|apply` runs `pg-pr-change` over it, paced, under a floor on the GraphQL budget, with resume and
the operator's create limits, `summary` prints the plan summary or the apply residual check from the
run record, `flip-marker` writes the flip marker, `writes` lists every write since it, and `snapshot` and
`rollback` take the tracker's before-image and revert every write since the flip. It ships in
this package's output beside `pg-pr-change`, with `pg-connector` and the sibling `pg-pr-change` on its
`PATH`. Behavior is in `docs/behavior/pg-decider/backfill.md`; the operator's page is
`docs/runbooks/pg-pr-backfill.md`.

- Real-tool gate (the driver over the real `pg-pr-change`, `pg-desk` and `pg-decider`, with the fake
  `remaining` control): `nix build .#checks.<system>.pg-pr-backfill-real-tools -L`. `enumerate` against a
  real tracker is a `contract`-tagged test (`TestPRChangeRealBD_BackfillEnumerate...`) that runs from the
  same `pg-pr-change-contract-test` hook.
- Rollback drill: the scenario `a-rollback-drill-lists-every-write-since-the-flip-marker-by-timestamp-and-reverts-each`
  of the lane suite (the real driver over the real composed command and the tracker: the fake `bd` in
  `pg-decider-lane-fake`, a real one in the by-hand soak); the
  operator's procedure is `docs/runbooks/pg-pr-first-apply.md`. The runbook's driver commands and its
  plan-age bound are checked by `cmd/pg-pr-backfill/runbook_test.go`.

## Parity gate

`internal/parity` loads every synthetic scenario under `internal/parity/testdata/` into a schema-2
store (`pg-desk migrate --cutover`, then `pg-desk pr refresh` per entity), runs the decider
(`pg-decider plan`), normalizes its plan into one vocabulary and compares it with the scenario's
FROZEN golden, `testdata/<scenario>/expected-plan.json`. It makes no live contact: all state is
temporary and the connector is a generated fake.

The gate is a regression snapshot, not an oracle. The old sync stage observed only creates for
three kinds, and the golden compares plans, never applies. The real oracle is the end-to-end test
against a real tracker.

### The golden

Each `expected-plan.json` was recorded once, before the sync stage was removed, by running the old
sync stage (`pg-desk run pr`, `sync.mode = plan`) and the decider side by side
(`pg-decider-parity freeze`). It holds:

- `frozen_from`: the agent-support `main` commit and the pg-desk build the old plans came from. Every
  file carries the same pin (a test refuses a set frozen twice).
- `old_plan`: what the old sync stage planned, normalized. Frozen; never recomputed.
- `explained`: every difference between `old_plan` and the decider, each with its exception id (`S13`
  and so on) and reason, including the hand-pinned `UNOBS` entries (the old side exposes no write
  against an existing bead, so the decider's close, reopen and update entries are pinned instead of
  observed).
- `expected_plan`: the full normalized set the decider MUST plan: `old_plan` with the old-only
  differences removed and the new-only ones added. The gate compares this, exactly and counted,
  with the decider's plan.

An entry the golden lists that the decider no longer plans is a STALE expected entry and fails the
gate; an entry the decider plans that the golden does not list fails it too. An intended behavior
change is recorded by editing `expected_plan` AND adding its `explained` entry in the same change;
`ParseGolden` refuses a file whose `expected_plan` is not `old_plan` with the explained differences
applied. The golden is edited by hand after the freeze and is never re-recorded (`freeze` refuses to
overwrite one).

Scenario `19-legacy-version-0-row` is seeded the legacy way: a store helper (`legacy_store.go`,
driving `sqlite3`) writes the entity row an old-schema store held, into a schema-1 store before the
cutover, so the cutover leaves it at version 0, never hydrated. The runner shows the decider declines
to plan it before the refresh (a non-zero exit, or an empty plan: today the decider plans no action
from a never-hydrated row; the fail-closed exit is the composed command's job) and that it plans
exactly what scenario 01 plans after `pg-desk pr refresh`.

`Diff` and the comparison have plain negative cases (`TestParityGateNegativeCases`, no binaries
needed): an input with one kind or one operation changed, a stale entry and an unplanned extra entry
each make the gate fail, and an unchanged input passes.

The gate needs three BUILT binaries, named by environment variables holding absolute paths, and
`sqlite3` for the legacy helper:

| Variable                             | Binary         | Nix package                     |
| ------------------------------------ | -------------- | ------------------------------- |
| `PG_DECIDER_PARITY_PG_DESK_BIN`      | `pg-desk`      | `.#packages.<sys>.pg-desk`      |
| `PG_DECIDER_PARITY_PG_CONNECTOR_BIN` | `pg-connector` | `.#packages.<sys>.pg-connector` |
| `PG_DECIDER_PARITY_PG_DECIDER_BIN`   | `pg-decider`   | `.#packages.<sys>.pg-decider`   |
| `PG_DECIDER_PARITY_SQLITE3_BIN`      | `sqlite3`      | `pkgs.sqlite` (PATH when unset) |

### In nix (the gate)

`checks.<system>.pg-decider-parity-gate` builds the packages, exports the variables, and runs
`TestParityGate` (one subtest per scenario) and `TestParityGateNegativeCases`. It fails when either
fails, and it also sets `PG_DECIDER_PARITY_REQUIRE_BINARIES=1`, so a missing or empty binary
variable FAILS the run instead of skipping it. The check additionally requires a `PASS` line for
both tests and for EVERY scenario directory, no `SKIP` line, and at least 19 scenarios, so a renamed
or filtered-out test, a skipped scenario, or a gate that found nothing cannot pass vacuously.

### By hand

```bash
export PG_DECIDER_PARITY_PG_DESK_BIN="$(nix build .#pg-desk --no-link --print-out-paths)/bin/pg-desk"
export PG_DECIDER_PARITY_PG_CONNECTOR_BIN="$(nix build .#pg-connector --no-link --print-out-paths)/bin/pg-connector"
export PG_DECIDER_PARITY_PG_DECIDER_BIN="$(nix build .#pg-decider --no-link --print-out-paths)/bin/pg-decider"

go test ./internal/parity/...      # includes TestParityGate
go run ./cmd/pg-decider-parity     # prints every difference from the golden; exit 0 when clean
```

Without the variables a plain `go test` SKIPS the tests that run the real binaries (so the module
stays green for developers who have not built them). Set `PG_DECIDER_PARITY_REQUIRE_BINARIES=1` to
make that skip a failure.

### Keeping the golden honest

Removing an entry from a scenario's `expected_plan` (together with its `explained` entry) makes the
gate, and so the check, fail naming the unplanned entry. Adding an entry for something the decider
does not plan fails it too.

## Running the focus contract test

`TestFocusHoldReleaseCycleRealBD` (`internal/apply/focus_realbd_test.go`) proves what a real `bd`
does with the focus hold and release, which the recorded-argv tests
(`TestFocusHoldArgvVector`, `TestFocusReleaseArgvVector`) cannot: it drives `apply.Run` against a
real `pg-connector` and `bd` in a disposable workspace (mint, hold, release, a metadata merge, and a
claimed bead). It is opt-in and is NOT part of any nix check. The workspace is a fresh temp
directory initialised with a time-based `bd init --prefix`; `bd` 1.3.1 uses an embedded Dolt engine
there, so no dolt server is started, and the real beads workspace is never named.

Name the binaries with absolute paths:

| Variable                            | Binary                                                               |
| ----------------------------------- | -------------------------------------------------------------------- |
| `PG_DECIDER_FOCUS_PG_CONNECTOR_BIN` | `pg-connector` (`.#packages.<sys>.pg-connector`)                     |
| `PG_DECIDER_FOCUS_BD_BIN`           | `bd` (`command -v bd`)                                               |
| `PG_DECIDER_FOCUS_PG_DESK_BIN`      | `pg-desk`, or a stub script that accepts `issue refresh` and exits 0 |
| `PG_DECIDER_FOCUS_REQUIRE_BINARIES` | optional; anything but empty or `0` makes a missing binary a failure |

Skip versus fail: with the three binary variables unset the test SKIPS with a message naming the
missing ones, so a plain `go test ./...` stays green. With `PG_DECIDER_FOCUS_REQUIRE_BINARIES` set,
a missing binary FAILS the run instead. A variable that names something that is not an executable
file always fails.

The pg-connector backend `pg-connector-issue-beads` is found on `PATH` (the test puts the
`pg-connector` and `bd` directories first), so put a build of the backend under test first:

```bash
cd packages/pg-decider
ib="$(nix build ../..#pg-connector-issue-beads --no-link --print-out-paths)"
PATH="$ib/bin:$PATH" \
PG_DECIDER_FOCUS_REQUIRE_BINARIES=1 \
PG_DECIDER_FOCUS_PG_CONNECTOR_BIN="$(nix build ../..#pg-connector --no-link --print-out-paths)/bin/pg-connector" \
PG_DECIDER_FOCUS_BD_BIN="$(command -v bd)" \
PG_DECIDER_FOCUS_PG_DESK_BIN="$(nix build ../..#pg-desk --no-link --print-out-paths)/bin/pg-desk" \
  nice -n 10 go test -p 4 -count=1 -v -run TestFocusHoldReleaseCycleRealBD ./internal/apply/
```

The test does not depend on `pg-desk` behavior (a failed refresh only prints a warning), so a stub
is enough for `PG_DECIDER_FOCUS_PG_DESK_BIN`.
