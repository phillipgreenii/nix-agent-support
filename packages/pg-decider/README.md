# pg-decider

Decider that plans and applies work-item changes from the pg-desk composite view. Product-level
behavior lives in `docs/behavior/pg-decider/`; this page covers building and testing the package.

## Tests

- Whole module: `nix build .#checks.<system>.pg-decider-go-tests` (or `go test ./...` in this
  directory).
- Old-versus-new parity gate: `nix build .#checks.<system>.pg-decider-parity-gate -L`.

## Parity gate

`internal/parity` runs every synthetic scenario under `internal/parity/testdata/` through the old
sync (`pg-desk`, `sync.mode = plan`) and the new decider (`pg-decider plan`), normalizes both plans
into one vocabulary and diffs them. `testdata/expected-diff.json` lists the differences that are
intended (exception ids such as `S26`); any other difference, and any listed exception that does
NOT occur, fails the gate. It makes no live contact: all state is temporary and the connector is a
generated fake.

The gate needs three BUILT binaries, named by environment variables holding absolute paths:

| Variable                             | Binary         | Nix package                     |
| ------------------------------------ | -------------- | ------------------------------- |
| `PG_DECIDER_PARITY_PG_DESK_BIN`      | `pg-desk`      | `.#packages.<sys>.pg-desk`      |
| `PG_DECIDER_PARITY_PG_CONNECTOR_BIN` | `pg-connector` | `.#packages.<sys>.pg-connector` |
| `PG_DECIDER_PARITY_PG_DECIDER_BIN`   | `pg-decider`   | `.#packages.<sys>.pg-decider`   |

### In nix (the gate)

`checks.<system>.pg-decider-parity-gate` builds the three packages, exports the variables, and runs
`TestParityGate`. It fails when the gate fails, and it also sets
`PG_DECIDER_PARITY_REQUIRE_BINARIES=1`, so a missing or empty binary variable FAILS the run instead
of skipping it. The check additionally requires `TestParityGate` to report `PASS`, so a renamed or
filtered-out test cannot pass vacuously.

### By hand

```bash
export PG_DECIDER_PARITY_PG_DESK_BIN="$(nix build .#pg-desk --no-link --print-out-paths)/bin/pg-desk"
export PG_DECIDER_PARITY_PG_CONNECTOR_BIN="$(nix build .#pg-connector --no-link --print-out-paths)/bin/pg-connector"
export PG_DECIDER_PARITY_PG_DECIDER_BIN="$(nix build .#pg-decider --no-link --print-out-paths)/bin/pg-decider"

go test ./internal/parity/...      # includes TestParityGate
go run ./cmd/pg-decider-parity     # prints every unexplained difference; exit 0 when clean
```

Without the variables a plain `go test` SKIPS the tests that run the real binaries (so the module
stays green for developers who have not built them). Set `PG_DECIDER_PARITY_REQUIRE_BINARIES=1` to
make that skip a failure.

### Keeping the exceptions honest

Removing an entry from `testdata/expected-diff.json` (for example `S26`) makes the gate, and so the
check, fail naming the unexplained difference. Adding an entry for a difference that does not occur
fails it too.

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
