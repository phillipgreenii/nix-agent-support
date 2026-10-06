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
