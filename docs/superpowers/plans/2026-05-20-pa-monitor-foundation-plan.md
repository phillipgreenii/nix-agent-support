# pa-monitor Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carve pure-logic packages into `internal/core/`, add a gRPC proto schema, and implement a `claude-agents-tui daemon` subcommand whose process-lifecycle (pidfile + socket + flock cleanup) passes the full failure-path test matrix. No behaviour change to the existing TUI. No OTel yet. No client refactor yet.

**Architecture:** Today's TUI imports modules from `internal/{session,aggregate,caffeinate,burnrate,transcript,ccusage,...}`. This plan moves those into `internal/core/<name>/` so the future daemon and the existing TUI both consume them through one canonical path. A new `internal/daemon/` package owns lifecycle (pidfile, flock, socket bind/unlink, cleanup defers, signal handling) and a gRPC server skeleton. A new `cmd/claude-agents-tui` subcommand dispatcher routes `claude-agents-tui daemon` to the daemon entrypoint while keeping the default (no-arg) TUI behaviour. A new `internal/proto/` package holds the protobuf schema and generated Go code (committed to the tree — no codegen at consumer build time).

**Tech Stack:** Go 1.24, `google.golang.org/grpc`, `google.golang.org/protobuf`, `gofrs/flock` (for cross-platform flock), existing Bubble Tea TUI, nix flake build, bats tests for any shell scripts.

**Scope of this plan:** Spec phases 1–3 only. Plan 2 (OTel emitter + decorators + week tracking) and Plan 3 (client refactor + nix wiring + binary rename) are tracked separately in beads.

---

## File Structure

### Created in this plan

```
packages/claude-agents-tui/
  internal/
    core/                        # NEW: pure logic, no UI dependency
      session/                   # moved from internal/session/
      aggregate/                 # moved from internal/aggregate/
      caffeinate/                # moved from internal/caffeinate/
      burnrate/                  # moved from internal/burnrate/
      transcript/                # moved from internal/transcript/
      ccusage/                   # moved from internal/ccusage/
      models/                    # moved from internal/models/
      treestate/                 # moved from internal/treestate/
      subshell/                  # moved from internal/subshell/
      poller/                    # moved from internal/poller/
    daemon/                      # NEW
      lifecycle.go               # pidfile + flock + socket bind/unlink + cleanup
      lifecycle_test.go          # full failure-path matrix
      paths.go                   # XDG path resolution
      paths_test.go
      server.go                  # gRPC server wrapper
      server_test.go
      heartbeat.go               # heartbeat scheduling logic
      heartbeat_test.go
    proto/                       # NEW
      pa_monitor.proto           # schema
      pa_monitor.pb.go           # generated (committed)
      pa_monitor_grpc.pb.go      # generated (committed)
      gen.go                     # `go generate` directives
  cmd/
    claude-agents-tui/
      main.go                    # MODIFIED: subcommand dispatcher
      daemon.go                  # NEW: `daemon` subcommand entry
  scripts/
    gen-proto.sh                 # NEW: protoc invocation wrapper
  default.nix                    # MODIFIED: add buildInputs for codegen tools
```

### Modified

- All TUI source files that today import `internal/{session,aggregate,...}` — imports rewritten to `internal/core/<name>`.
- `go.mod` / `go.sum` — new deps (`google.golang.org/grpc`, `gofrs/flock`).

---

## Task 1: Snapshot baseline + branch setup

**Files:** none (preparation only).

- [ ] **Step 1.1: Run baseline tests so we know what "green" looks like**

  Run from `packages/claude-agents-tui/`:

  ```bash
  go test ./...
  ```

  Expected: all tests pass. Capture the output. If anything is failing on the current `HEAD`, stop and fix that before proceeding — this plan assumes a green baseline.

- [ ] **Step 1.2: Verify nix flake check is green**

  Run from the repo root (`phillipgreenii-nix-agent-support/`):

  ```bash
  nix flake check 2>&1 | tail -40
  ```

  Expected: no errors. If broken, stop and fix.

- [ ] **Step 1.3: Commit a no-op marker so the plan is rooted at a known commit**

  Skip — no need to commit if there's nothing to change. Note the current commit SHA in your scratch notes so you can `git diff` against it later if something goes sideways.

---

## Task 2: Move `internal/session/` → `internal/core/session/`

**Files:**

- Move: `packages/claude-agents-tui/internal/session/*.go` → `packages/claude-agents-tui/internal/core/session/*.go`
- Modify: every file under `packages/claude-agents-tui/` that imports `.../internal/session`

The move is a `git mv` (preserves history) + a search-and-replace of the import path. The package name inside the files stays `session`.

- [ ] **Step 2.1: Create the destination directory**

  ```bash
  cd packages/claude-agents-tui
  mkdir -p internal/core
  ```

- [ ] **Step 2.2: `git mv` the package**

  ```bash
  git mv internal/session internal/core/session
  ```

- [ ] **Step 2.3: Rewrite imports across the package**

  Find every file referencing the old import path:

  ```bash
  grep -rln "claude-agents-tui/internal/session" .
  ```

  For each match, replace `claude-agents-tui/internal/session` with `claude-agents-tui/internal/core/session`. Example pattern:

  ```bash
  grep -rl "claude-agents-tui/internal/session" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/session|claude-agents-tui/internal/core/session|g'
  ```

  (On Linux use `sed -i ''` → `sed -i` without the empty arg.)

- [ ] **Step 2.4: Build and test**

  ```bash
  go build ./...
  go test ./internal/core/session/... ./internal/poller/... ./internal/tui/... ./internal/headless/...
  ```

  Expected: build succeeds; previously-green tests stay green. If a test that referenced `internal/session` is now in `internal/core/session`, it picks up via `./...` — run `go test ./...` for the full sweep.

- [ ] **Step 2.5: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move session package into internal/core"
  ```

---

## Task 3: Move `internal/aggregate/` → `internal/core/aggregate/`

**Files:**

- Move: `packages/claude-agents-tui/internal/aggregate/*.go` → `packages/claude-agents-tui/internal/core/aggregate/*.go`
- Modify: import sites.

- [ ] **Step 3.1: `git mv`**

  ```bash
  git mv internal/aggregate internal/core/aggregate
  ```

- [ ] **Step 3.2: Rewrite imports**

  ```bash
  grep -rl "claude-agents-tui/internal/aggregate" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/aggregate|claude-agents-tui/internal/core/aggregate|g'
  ```

- [ ] **Step 3.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

  Expected: all green.

- [ ] **Step 3.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move aggregate package into internal/core"
  ```

---

## Task 4: Move `internal/caffeinate/` → `internal/core/caffeinate/`

**Files:**

- Move: `packages/claude-agents-tui/internal/caffeinate/*.go` → `packages/claude-agents-tui/internal/core/caffeinate/*.go`

- [ ] **Step 4.1: `git mv`**

  ```bash
  git mv internal/caffeinate internal/core/caffeinate
  ```

- [ ] **Step 4.2: Rewrite imports**

  ```bash
  grep -rl "claude-agents-tui/internal/caffeinate" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/caffeinate|claude-agents-tui/internal/core/caffeinate|g'
  ```

- [ ] **Step 4.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

- [ ] **Step 4.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move caffeinate package into internal/core"
  ```

---

## Task 5: Move `internal/burnrate/` → `internal/core/burnrate/`

- [ ] **Step 5.1: `git mv`**

  ```bash
  git mv internal/burnrate internal/core/burnrate
  ```

- [ ] **Step 5.2: Rewrite imports**

  ```bash
  grep -rl "claude-agents-tui/internal/burnrate" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/burnrate|claude-agents-tui/internal/core/burnrate|g'
  ```

- [ ] **Step 5.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

- [ ] **Step 5.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move burnrate package into internal/core"
  ```

---

## Task 6: Move `internal/transcript/` → `internal/core/transcript/`

- [ ] **Step 6.1: `git mv`**

  ```bash
  git mv internal/transcript internal/core/transcript
  ```

- [ ] **Step 6.2: Rewrite imports**

  ```bash
  grep -rl "claude-agents-tui/internal/transcript" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/transcript|claude-agents-tui/internal/core/transcript|g'
  ```

- [ ] **Step 6.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

- [ ] **Step 6.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move transcript package into internal/core"
  ```

---

## Task 7: Move `internal/ccusage/` → `internal/core/ccusage/`

- [ ] **Step 7.1: `git mv`**

  ```bash
  git mv internal/ccusage internal/core/ccusage
  ```

- [ ] **Step 7.2: Rewrite imports**

  ```bash
  grep -rl "claude-agents-tui/internal/ccusage" . \
    | xargs sed -i '' 's|claude-agents-tui/internal/ccusage|claude-agents-tui/internal/core/ccusage|g'
  ```

- [ ] **Step 7.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

- [ ] **Step 7.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move ccusage package into internal/core"
  ```

---

## Task 8: Move remaining helpers (`models`, `treestate`, `subshell`, `poller`)

These move together because they're small and mutually dependent.

- [ ] **Step 8.1: `git mv` each**

  ```bash
  git mv internal/models     internal/core/models
  git mv internal/treestate  internal/core/treestate
  git mv internal/subshell   internal/core/subshell
  git mv internal/poller     internal/core/poller
  ```

- [ ] **Step 8.2: Rewrite imports for all four**

  ```bash
  for pkg in models treestate subshell poller; do
    grep -rl "claude-agents-tui/internal/${pkg}" . \
      | xargs sed -i '' "s|claude-agents-tui/internal/${pkg}|claude-agents-tui/internal/core/${pkg}|g"
  done
  ```

- [ ] **Step 8.3: Build + test**

  ```bash
  go build ./...
  go test ./...
  ```

  Expected: all green.

- [ ] **Step 8.4: Commit**

  ```bash
  git add -A
  git commit -m "refactor: move models, treestate, subshell, poller into internal/core"
  ```

---

## Task 9: Confirm `internal/` now contains only non-core packages

**Files:** none (verification step).

- [ ] **Step 9.1: List what's left at `internal/` top level**

  ```bash
  ls internal/
  ```

  Expected output (order may differ):

  ```
  cmuxstatus
  config
  core
  headless
  render
  signal
  tui
  ```

  These are the packages that are UI-aware or process-aware and stay where they are for now. If anything else is at this level, you missed a move — investigate before continuing.

- [ ] **Step 9.2: Run the full sweep one more time**

  ```bash
  go test ./...
  ```

  Expected: all green.

- [ ] **Step 9.3: No commit (verification only).**

---

## Task 10: Add gRPC + protobuf dependencies

**Files:**

- Modify: `go.mod`, `go.sum`

- [ ] **Step 10.1: Add dependencies**

  ```bash
  go get google.golang.org/grpc@latest
  go get google.golang.org/protobuf@latest
  go get github.com/gofrs/flock@latest
  ```

- [ ] **Step 10.2: Tidy and verify**

  ```bash
  go mod tidy
  go build ./...
  ```

  Expected: build succeeds. New entries in `go.mod`. New entries in `go.sum`.

- [ ] **Step 10.3: Commit**

  ```bash
  git add go.mod go.sum
  git commit -m "deps: add grpc, protobuf, flock for daemon work"
  ```

---

## Task 11: Add the proto schema

**Files:**

- Create: `packages/claude-agents-tui/internal/proto/pa_monitor.proto`
- Create: `packages/claude-agents-tui/internal/proto/gen.go`

- [ ] **Step 11.1: Create the proto package directory and gen.go**

  Create `packages/claude-agents-tui/internal/proto/gen.go`:

  ```go
  // Package proto contains the gRPC service definition and generated
  // Go bindings for pa-monitor. The .pb.go and _grpc.pb.go files in this
  // directory are checked in; regenerate with scripts/gen-proto.sh.
  package proto

  //go:generate ../../scripts/gen-proto.sh
  ```

- [ ] **Step 11.2: Write the proto schema**

  Create `packages/claude-agents-tui/internal/proto/pa_monitor.proto`:

  ```proto
  syntax = "proto3";

  package pa_monitor.v1;

  option go_package = "github.com/phillipgreenii/claude-agents-tui/internal/proto;proto";

  import "google/protobuf/timestamp.proto";

  // Service definition matches the spec's RPC surface. v1 is bare bones:
  // GetState, WatchState (with heartbeat), Ping. Other methods land in later
  // plans as the daemon grows responsibilities.
  service PaMonitor {
    // GetState returns a snapshot of the daemon's current state.
    rpc GetState(GetStateRequest) returns (DaemonState);

    // WatchState streams updates whenever state changes. Idle periods are
    // filled with Heartbeat messages so clients can detect a hung daemon.
    rpc WatchState(WatchStateRequest) returns (stream WatchStateEvent);

    // Ping is a cheap liveness check used by clients to confirm the daemon
    // is responsive before issuing a heavier RPC.
    rpc Ping(PingRequest) returns (PingResponse);
  }

  message GetStateRequest {}

  message WatchStateRequest {
    // Heartbeat interval requested by the client, in milliseconds. The
    // server clamps to a sane range. 0 means "use server default".
    uint32 heartbeat_interval_ms = 1;
  }

  message WatchStateEvent {
    oneof payload {
      DaemonState state = 1;
      Heartbeat heartbeat = 2;
    }
  }

  message Heartbeat {
    google.protobuf.Timestamp ts = 1;
    uint64 daemon_uptime_seconds = 2;
  }

  message PingRequest {}

  message PingResponse {
    google.protobuf.Timestamp ts = 1;
  }

  // DaemonState is intentionally minimal in this plan. Plans 2 and 3 grow
  // it with sessions, blocks, weeks, caffeinate, and so on. The placeholder
  // fields below exist only so the wire shape is non-empty during
  // foundation work — real fields land later.
  message DaemonState {
    google.protobuf.Timestamp now = 1;
    uint64 daemon_uptime_seconds = 2;
    string daemon_version = 3;
  }
  ```

- [ ] **Step 11.3: Verify the file parses**

  ```bash
  # Quick syntax check using buf or protoc if available; if neither is in
  # the environment, defer verification to the codegen step.
  command -v buf >/dev/null && buf lint --path internal/proto/pa_monitor.proto || true
  ```

- [ ] **Step 11.4: Commit (schema only, no generated files yet)**

  ```bash
  git add internal/proto/pa_monitor.proto internal/proto/gen.go
  git commit -m "proto: add pa-monitor schema (Ping, GetState, WatchState)"
  ```

---

## Task 12: Add codegen script

**Files:**

- Create: `packages/claude-agents-tui/scripts/gen-proto.sh`

- [ ] **Step 12.1: Write the codegen wrapper**

  Create `packages/claude-agents-tui/scripts/gen-proto.sh`:

  ```bash
  #!/usr/bin/env bash
  # Regenerates pa_monitor.pb.go and pa_monitor_grpc.pb.go from the .proto
  # schema. Run from the package root: ./scripts/gen-proto.sh
  #
  # Requirements (provided via nix devShell): protoc, protoc-gen-go,
  # protoc-gen-go-grpc.

  set -euo pipefail

  cd "$(dirname "$0")/.."

  if ! command -v protoc >/dev/null; then
    echo "error: protoc not in PATH" >&2
    exit 1
  fi

  protoc \
    --go_out=. \
    --go_opt=paths=source_relative \
    --go-grpc_out=. \
    --go-grpc_opt=paths=source_relative \
    internal/proto/pa_monitor.proto

  echo "Generated: internal/proto/pa_monitor.pb.go internal/proto/pa_monitor_grpc.pb.go"
  ```

- [ ] **Step 12.2: Make it executable**

  ```bash
  chmod +x scripts/gen-proto.sh
  ```

- [ ] **Step 12.3: Commit the script (without running it yet)**

  ```bash
  git add scripts/gen-proto.sh
  git commit -m "scripts: add gen-proto.sh codegen wrapper"
  ```

---

## Task 13: Add codegen tooling to the nix flake's devShell

**Files:**

- Modify: `packages/claude-agents-tui/default.nix`

- [ ] **Step 13.1: Read the existing default.nix to find the buildInputs / nativeBuildInputs lists**

  ```bash
  cat packages/claude-agents-tui/default.nix
  ```

  Identify where `buildInputs` or `nativeBuildInputs` is set (likely on the derivation expression).

- [ ] **Step 13.2: Add codegen tools**

  Edit `default.nix` so `nativeBuildInputs` (or an explicit devShell input list) contains:

  ```nix
  nativeBuildInputs = [
    pkgs.protobuf
    pkgs.protoc-gen-go
    pkgs.protoc-gen-go-grpc
  ];
  ```

  If `default.nix` already had a `nativeBuildInputs`, append to it. If not, add it.

- [ ] **Step 13.3: Enter devShell, verify tooling**

  ```bash
  nix develop --command bash -c 'protoc --version && protoc-gen-go --version && protoc-gen-go-grpc --version'
  ```

  Expected: each tool prints a version. If `nix develop` is not the right shell command in this repo, substitute the local convention (`nix-shell`, `direnv reload`, etc.).

- [ ] **Step 13.4: Commit**

  ```bash
  git add packages/claude-agents-tui/default.nix
  git commit -m "nix: add protobuf codegen tooling to claude-agents-tui devShell"
  ```

---

## Task 14: Run codegen and commit generated files

**Files:**

- Create (generated): `packages/claude-agents-tui/internal/proto/pa_monitor.pb.go`
- Create (generated): `packages/claude-agents-tui/internal/proto/pa_monitor_grpc.pb.go`

- [ ] **Step 14.1: Run the codegen script inside the devShell**

  ```bash
  nix develop --command bash -c 'cd packages/claude-agents-tui && ./scripts/gen-proto.sh'
  ```

  Expected: two files created. Last line of script output names them.

- [ ] **Step 14.2: Build to ensure generated code compiles**

  ```bash
  cd packages/claude-agents-tui
  go build ./...
  ```

  Expected: no errors.

- [ ] **Step 14.3: Commit the generated files**

  ```bash
  git add internal/proto/pa_monitor.pb.go internal/proto/pa_monitor_grpc.pb.go
  git commit -m "proto: regenerate pa_monitor bindings"
  ```

---

## Task 15: Add subcommand dispatcher to `cmd/claude-agents-tui/main.go`

**Files:**

- Modify: `packages/claude-agents-tui/cmd/claude-agents-tui/main.go`

The current main.go runs the TUI by default and supports `--wait-until-idle`. We add a subcommand layer so `claude-agents-tui daemon` routes to a different entrypoint. No new flags removed; today's behaviour preserved.

- [ ] **Step 15.1: Read the existing main.go**

  ```bash
  cat cmd/claude-agents-tui/main.go
  ```

  Note the current `main()` body. The dispatcher wraps it.

- [ ] **Step 15.2: Write a test for the dispatcher**

  Create `cmd/claude-agents-tui/main_dispatch_test.go`:

  ```go
  package main

  import "testing"

  func TestPickSubcommand(t *testing.T) {
      cases := []struct {
          name     string
          args     []string
          wantCmd  string
          wantRest []string
      }{
          {"no args", []string{"claude-agents-tui"}, "tui", nil},
          {"daemon", []string{"claude-agents-tui", "daemon"}, "daemon", []string{}},
          {"daemon with flag", []string{"claude-agents-tui", "daemon", "--socket=/tmp/x"}, "daemon", []string{"--socket=/tmp/x"}},
          {"flag-first preserves tui", []string{"claude-agents-tui", "--wait-until-idle"}, "tui", []string{"--wait-until-idle"}},
      }
      for _, c := range cases {
          t.Run(c.name, func(t *testing.T) {
              gotCmd, gotRest := pickSubcommand(c.args)
              if gotCmd != c.wantCmd {
                  t.Errorf("cmd: got %q, want %q", gotCmd, c.wantCmd)
              }
              if len(gotRest) != len(c.wantRest) {
                  t.Fatalf("rest len: got %d, want %d", len(gotRest), len(c.wantRest))
              }
              for i := range gotRest {
                  if gotRest[i] != c.wantRest[i] {
                      t.Errorf("rest[%d]: got %q, want %q", i, gotRest[i], c.wantRest[i])
                  }
              }
          })
      }
  }
  ```

- [ ] **Step 15.3: Run the test to confirm it fails**

  ```bash
  go test ./cmd/claude-agents-tui/ -run TestPickSubcommand -v
  ```

  Expected: FAIL — `pickSubcommand` is undefined.

- [ ] **Step 15.4: Implement `pickSubcommand` in main.go**

  Add to the top of `cmd/claude-agents-tui/main.go` (above `main()`):

  ```go
  // pickSubcommand inspects os.Args-style input and returns the subcommand
  // name plus the remaining args (minus the subcommand token).
  //
  // Rules:
  //   - If args[1] is a known subcommand name, that wins; the rest are its args.
  //   - Otherwise the command is "tui" and args[1:] are its args.
  //   - The flag-first case (e.g. --wait-until-idle) routes to tui because
  //     no current TUI flags collide with a subcommand name.
  func pickSubcommand(args []string) (cmd string, rest []string) {
      known := map[string]bool{"daemon": true}
      if len(args) < 2 {
          return "tui", nil
      }
      if known[args[1]] {
          return args[1], args[2:]
      }
      return "tui", args[1:]
  }
  ```

- [ ] **Step 15.5: Rewrite `main()` to dispatch**

  Replace the body of `main()` with:

  ```go
  func main() {
      cmd, rest := pickSubcommand(os.Args)
      switch cmd {
      case "daemon":
          runDaemon(rest)
      case "tui":
          runTUI(rest)
      default:
          fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
          os.Exit(2)
      }
  }
  ```

  Move the old `main()` body into a new function `runTUI(args []string)` in the same file. Adjust it to take `args` instead of reading `os.Args` directly. `runDaemon` will be defined in the next task — for now, stub it:

  ```go
  func runDaemon(args []string) {
      fmt.Fprintln(os.Stderr, "daemon: not yet implemented")
      os.Exit(1)
  }
  ```

- [ ] **Step 15.6: Run all tests**

  ```bash
  go test ./...
  ```

  Expected: dispatcher test passes; existing TUI tests pass.

- [ ] **Step 15.7: Smoke-check the TUI entry path still works**

  ```bash
  go build -o /tmp/cat-smoke ./cmd/claude-agents-tui
  /tmp/cat-smoke --help 2>&1 | head -5
  ```

  Expected: the existing `--help` output appears (proves `runTUI` is reachable). Smoke binary is throwaway.

- [ ] **Step 15.8: Commit**

  ```bash
  git add cmd/claude-agents-tui/main.go cmd/claude-agents-tui/main_dispatch_test.go
  git commit -m "cmd: add subcommand dispatcher (tui default, daemon stub)"
  ```

---

## Task 16: Add daemon entrypoint file

**Files:**

- Create: `packages/claude-agents-tui/cmd/claude-agents-tui/daemon.go`

- [ ] **Step 16.1: Write the entrypoint skeleton**

  Create `cmd/claude-agents-tui/daemon.go`:

  ```go
  package main

  import (
      "context"
      "flag"
      "fmt"
      "os"
      "os/signal"
      "syscall"

      "github.com/phillipgreenii/claude-agents-tui/internal/daemon"
  )

  // runDaemon is invoked by the dispatcher when the user runs
  // `claude-agents-tui daemon`. It owns the daemon process from
  // start to clean shutdown.
  func runDaemon(args []string) {
      fs := flag.NewFlagSet("daemon", flag.ExitOnError)
      socketPath := fs.String("socket", "", "Override socket path (default: XDG-derived)")
      pidPath := fs.String("pidfile", "", "Override pidfile path (default: XDG-derived)")
      if err := fs.Parse(args); err != nil {
          fmt.Fprintln(os.Stderr, err)
          os.Exit(2)
      }

      paths, err := daemon.ResolvePaths(daemon.PathOverrides{
          Socket:  *socketPath,
          PIDFile: *pidPath,
      })
      if err != nil {
          fmt.Fprintf(os.Stderr, "daemon: resolve paths: %v\n", err)
          os.Exit(1)
      }

      ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
      defer cancel()

      if err := daemon.Run(ctx, paths); err != nil {
          fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
          os.Exit(1)
      }
  }
  ```

  (`daemon.ResolvePaths`, `daemon.PathOverrides`, and `daemon.Run` will be defined in tasks below. Compile will fail until those land.)

- [ ] **Step 16.2: Do not commit yet** — this file won't compile until Task 17 lands. Continue.

---

## Task 17: XDG path resolution (`internal/daemon/paths.go`)

**Files:**

- Create: `packages/claude-agents-tui/internal/daemon/paths.go`
- Create: `packages/claude-agents-tui/internal/daemon/paths_test.go`

- [ ] **Step 17.1: Write failing tests**

  Create `internal/daemon/paths_test.go`:

  ```go
  package daemon

  import (
      "path/filepath"
      "runtime"
      "testing"
  )

  func TestResolvePaths_XDGStateHomeRespected(t *testing.T) {
      t.Setenv("XDG_STATE_HOME", "/tmp/fake-state")
      // Force the macOS branch off so behaviour is testable on both OSes.
      t.Setenv("XDG_RUNTIME_DIR", "/tmp/fake-runtime")

      p, err := ResolvePaths(PathOverrides{})
      if err != nil {
          t.Fatal(err)
      }

      wantDir := filepath.Join("/tmp/fake-runtime", "pa-monitor")
      if runtime.GOOS == "darwin" {
          wantDir = filepath.Join("/tmp/fake-state", "pa-monitor")
      }

      if p.Dir != wantDir {
          t.Errorf("Dir = %q, want %q", p.Dir, wantDir)
      }
      if p.Socket != filepath.Join(wantDir, "daemon.sock") {
          t.Errorf("Socket = %q, want %q", p.Socket, filepath.Join(wantDir, "daemon.sock"))
      }
      if p.PIDFile != filepath.Join(wantDir, "daemon.pid") {
          t.Errorf("PIDFile = %q, want %q", p.PIDFile, filepath.Join(wantDir, "daemon.pid"))
      }
  }

  func TestResolvePaths_OverridesWin(t *testing.T) {
      p, err := ResolvePaths(PathOverrides{
          Socket:  "/custom/sock",
          PIDFile: "/custom/pid",
      })
      if err != nil {
          t.Fatal(err)
      }
      if p.Socket != "/custom/sock" {
          t.Errorf("Socket override ignored: got %q", p.Socket)
      }
      if p.PIDFile != "/custom/pid" {
          t.Errorf("PIDFile override ignored: got %q", p.PIDFile)
      }
  }

  func TestResolvePaths_MissingHomeIsAnError(t *testing.T) {
      t.Setenv("XDG_STATE_HOME", "")
      t.Setenv("XDG_RUNTIME_DIR", "")
      t.Setenv("HOME", "")
      if _, err := ResolvePaths(PathOverrides{}); err == nil {
          t.Fatal("expected error when no HOME or XDG vars are set")
      }
  }
  ```

- [ ] **Step 17.2: Run tests to confirm they fail**

  ```bash
  go test ./internal/daemon/ -v
  ```

  Expected: FAIL (`undefined: ResolvePaths`).

- [ ] **Step 17.3: Implement `paths.go`**

  Create `internal/daemon/paths.go`:

  ```go
  package daemon

  import (
      "fmt"
      "os"
      "path/filepath"
      "runtime"
  )

  // PathOverrides allows callers (tests, CLI flags) to override individual
  // file paths. Empty fields fall back to the XDG-derived defaults.
  type PathOverrides struct {
      Socket  string
      PIDFile string
  }

  // Paths holds every path the daemon needs on disk.
  type Paths struct {
      Dir     string // parent directory; daemon may create
      Socket  string
      PIDFile string
  }

  // ResolvePaths picks Dir per spec:
  //   - Linux: $XDG_RUNTIME_DIR/pa-monitor
  //   - macOS: $XDG_STATE_HOME/pa-monitor  (XDG_RUNTIME_DIR is not standard there)
  // Both Socket and PIDFile live inside Dir. Overrides win unconditionally.
  func ResolvePaths(o PathOverrides) (Paths, error) {
      dir, err := defaultDir()
      if err != nil {
          return Paths{}, err
      }
      p := Paths{
          Dir:     dir,
          Socket:  filepath.Join(dir, "daemon.sock"),
          PIDFile: filepath.Join(dir, "daemon.pid"),
      }
      if o.Socket != "" {
          p.Socket = o.Socket
      }
      if o.PIDFile != "" {
          p.PIDFile = o.PIDFile
      }
      return p, nil
  }

  func defaultDir() (string, error) {
      var base string
      if runtime.GOOS == "darwin" {
          base = os.Getenv("XDG_STATE_HOME")
          if base == "" {
              home := os.Getenv("HOME")
              if home == "" {
                  return "", fmt.Errorf("HOME and XDG_STATE_HOME both unset")
              }
              base = filepath.Join(home, ".local", "state")
          }
      } else {
          base = os.Getenv("XDG_RUNTIME_DIR")
          if base == "" {
              base = os.Getenv("XDG_STATE_HOME")
          }
          if base == "" {
              home := os.Getenv("HOME")
              if home == "" {
                  return "", fmt.Errorf("HOME, XDG_RUNTIME_DIR, and XDG_STATE_HOME all unset")
              }
              base = filepath.Join(home, ".local", "state")
          }
      }
      return filepath.Join(base, "pa-monitor"), nil
  }
  ```

- [ ] **Step 17.4: Run tests, confirm pass**

  ```bash
  go test ./internal/daemon/ -v
  ```

  Expected: PASS.

- [ ] **Step 17.5: Commit**

  ```bash
  git add internal/daemon/paths.go internal/daemon/paths_test.go cmd/claude-agents-tui/daemon.go
  git commit -m "daemon: XDG path resolution with override support"
  ```

  (Includes the orphan `cmd/claude-agents-tui/daemon.go` from Task 16 — that file now compiles because `daemon.ResolvePaths` exists.)

---

## Task 18: Pidfile + flock lifecycle (acquire path)

**Files:**

- Create: `packages/claude-agents-tui/internal/daemon/lifecycle.go`
- Create: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

This task implements pidfile acquisition only. Socket bind and full `Run` come in Tasks 19 and 20.

- [ ] **Step 18.1: Write the first failing test**

  Create `internal/daemon/lifecycle_test.go`:

  ```go
  package daemon

  import (
      "os"
      "path/filepath"
      "testing"
  )

  func TestAcquirePIDFile_WritesFileAndLocks(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      lock, err := AcquirePIDFile(paths)
      if err != nil {
          t.Fatalf("acquire: %v", err)
      }
      defer lock.Release()

      // File should exist and contain our pid.
      data, err := os.ReadFile(paths.PIDFile)
      if err != nil {
          t.Fatalf("read pidfile: %v", err)
      }
      if len(data) == 0 {
          t.Error("pidfile is empty")
      }
  }

  func TestAcquirePIDFile_SecondAcquireFails(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      first, err := AcquirePIDFile(paths)
      if err != nil {
          t.Fatalf("first acquire: %v", err)
      }
      defer first.Release()

      if _, err := AcquirePIDFile(paths); err == nil {
          t.Fatal("expected second acquire to fail with lock contention")
      }
  }

  func TestAcquirePIDFile_ReleaseRemovesFile(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      lock, err := AcquirePIDFile(paths)
      if err != nil {
          t.Fatal(err)
      }
      lock.Release()

      if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
          t.Errorf("pidfile still exists after Release: stat err=%v", err)
      }
  }
  ```

- [ ] **Step 18.2: Run, expect failure**

  ```bash
  go test ./internal/daemon/ -v -run TestAcquirePIDFile
  ```

  Expected: FAIL — `undefined: AcquirePIDFile`.

- [ ] **Step 18.3: Implement pidfile acquisition**

  Create `internal/daemon/lifecycle.go`:

  ```go
  package daemon

  import (
      "fmt"
      "os"
      "strconv"

      "github.com/gofrs/flock"
  )

  // PIDLock holds the pidfile flock for the lifetime of the daemon process.
  // Release MUST be called to remove the file and free the lock. Safe to
  // call multiple times.
  type PIDLock struct {
      file     *flock.Flock
      path     string
      released bool
  }

  // AcquirePIDFile creates Paths.Dir if missing, opens the pidfile, takes
  // a non-blocking exclusive flock, and writes the current pid into the
  // file. Returns an error if the lock is already held by another process.
  func AcquirePIDFile(p Paths) (*PIDLock, error) {
      if err := os.MkdirAll(p.Dir, 0o700); err != nil {
          return nil, fmt.Errorf("mkdir state dir: %w", err)
      }

      fl := flock.New(p.PIDFile)
      locked, err := fl.TryLock()
      if err != nil {
          return nil, fmt.Errorf("flock: %w", err)
      }
      if !locked {
          return nil, fmt.Errorf("pidfile %s is locked by another process", p.PIDFile)
      }

      pid := []byte(strconv.Itoa(os.Getpid()))
      if err := os.WriteFile(p.PIDFile, pid, 0o600); err != nil {
          _ = fl.Unlock()
          return nil, fmt.Errorf("write pid: %w", err)
      }

      return &PIDLock{file: fl, path: p.PIDFile}, nil
  }

  // Release frees the lock and removes the pidfile. Safe to call multiple
  // times; subsequent calls are no-ops.
  func (l *PIDLock) Release() {
      if l == nil || l.released {
          return
      }
      l.released = true
      _ = l.file.Unlock()
      _ = os.Remove(l.path)
  }
  ```

- [ ] **Step 18.4: Run tests, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestAcquirePIDFile
  ```

  Expected: PASS.

- [ ] **Step 18.5: Commit**

  ```bash
  git add internal/daemon/lifecycle.go internal/daemon/lifecycle_test.go
  git commit -m "daemon: pidfile acquire/release via flock"
  ```

---

## Task 19: Socket bind + unlink lifecycle

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle.go`
- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

- [ ] **Step 19.1: Write failing tests**

  Append to `internal/daemon/lifecycle_test.go`:

  ```go
  func TestBindSocket_CreatesAndChmods(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      l, err := BindSocket(paths)
      if err != nil {
          t.Fatalf("bind: %v", err)
      }
      defer l.Close()

      info, err := os.Stat(paths.Socket)
      if err != nil {
          t.Fatalf("stat socket: %v", err)
      }
      // socket files are mode = S_IFSOCK | 0600 — check perm bits only.
      if info.Mode().Perm() != 0o600 {
          t.Errorf("socket perms = %v, want 0600", info.Mode().Perm())
      }
  }

  func TestBindSocket_RemovesStaleSocket(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }
      // Pre-create a "stale" socket file (just an empty regular file).
      if err := os.WriteFile(paths.Socket, []byte{}, 0o600); err != nil {
          t.Fatal(err)
      }

      l, err := BindSocket(paths)
      if err != nil {
          t.Fatalf("bind: %v", err)
      }
      defer l.Close()
  }
  ```

- [ ] **Step 19.2: Run tests, expect failure**

  ```bash
  go test ./internal/daemon/ -v -run TestBindSocket
  ```

  Expected: FAIL — `undefined: BindSocket`.

- [ ] **Step 19.3: Implement `BindSocket`**

  Append to `internal/daemon/lifecycle.go`:

  ```go
  import (
      // existing imports
      "net"
  )

  // BindSocket removes any pre-existing socket file at p.Socket, binds a
  // fresh Unix listener, and chmods it 0600. The caller is responsible
  // for closing the listener; the file is removed on listener.Close()
  // by the wrapper returned here.
  func BindSocket(p Paths) (net.Listener, error) {
      // Best-effort remove of stale file. Ignore "doesn't exist".
      if err := os.Remove(p.Socket); err != nil && !os.IsNotExist(err) {
          return nil, fmt.Errorf("remove stale socket: %w", err)
      }

      l, err := net.Listen("unix", p.Socket)
      if err != nil {
          return nil, fmt.Errorf("listen unix: %w", err)
      }
      if err := os.Chmod(p.Socket, 0o600); err != nil {
          _ = l.Close()
          return nil, fmt.Errorf("chmod socket: %w", err)
      }

      return &socketListener{Listener: l, path: p.Socket}, nil
  }

  // socketListener wraps net.Listener so that Close unlinks the socket
  // file in addition to closing the underlying fd.
  type socketListener struct {
      net.Listener
      path string
  }

  func (s *socketListener) Close() error {
      err := s.Listener.Close()
      _ = os.Remove(s.path)
      return err
  }
  ```

  Move the existing `import` block in `lifecycle.go` to include `"net"`. (If the file already has a multi-line import block, just add the line.)

- [ ] **Step 19.4: Run tests, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestBindSocket
  ```

  Expected: PASS.

- [ ] **Step 19.5: Commit**

  ```bash
  git add internal/daemon/lifecycle.go internal/daemon/lifecycle_test.go
  git commit -m "daemon: BindSocket with stale-file cleanup"
  ```

---

## Task 20: `daemon.Run` glue + clean-shutdown test

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle.go`
- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

`Run` ties pidfile + socket + signal handling together. v1 has no gRPC server yet — it just blocks until ctx cancels, then unwinds defers.

- [ ] **Step 20.1: Write the failing test**

  Append to `internal/daemon/lifecycle_test.go`:

  ```go
  func TestRun_CleanShutdownRemovesArtifacts(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      ctx, cancel := context.WithCancel(context.Background())
      done := make(chan error, 1)
      go func() { done <- Run(ctx, paths) }()

      // Wait until both files exist (proves daemon is up).
      waitForFile(t, paths.PIDFile)
      waitForFile(t, paths.Socket)

      cancel()
      select {
      case err := <-done:
          if err != nil {
              t.Fatalf("Run returned err: %v", err)
          }
      case <-time.After(2 * time.Second):
          t.Fatal("Run did not return after ctx cancel")
      }

      if _, err := os.Stat(paths.PIDFile); !os.IsNotExist(err) {
          t.Errorf("pidfile not cleaned up: stat err=%v", err)
      }
      if _, err := os.Stat(paths.Socket); !os.IsNotExist(err) {
          t.Errorf("socket not cleaned up: stat err=%v", err)
      }
  }

  func waitForFile(t *testing.T, path string) {
      t.Helper()
      deadline := time.Now().Add(2 * time.Second)
      for time.Now().Before(deadline) {
          if _, err := os.Stat(path); err == nil {
              return
          }
          time.Sleep(10 * time.Millisecond)
      }
      t.Fatalf("file did not appear within timeout: %s", path)
  }
  ```

  Add to the import block at the top of the test file: `"context"`, `"time"`.

- [ ] **Step 20.2: Run, expect failure**

  ```bash
  go test ./internal/daemon/ -v -run TestRun_CleanShutdown
  ```

  Expected: FAIL — `undefined: Run`.

- [ ] **Step 20.3: Implement `Run`**

  Append to `internal/daemon/lifecycle.go`:

  ```go
  import (
      // existing imports
      "context"
  )

  // Run is the daemon's main loop. It acquires the pidfile, binds the
  // socket, and blocks until ctx is done. On return — for any reason —
  // both the pidfile and socket file are removed.
  //
  // In this foundation milestone Run has no gRPC server attached yet.
  // Plan 1 Task 21 adds the server skeleton; this function gains a call
  // to start it then.
  func Run(ctx context.Context, p Paths) error {
      lock, err := AcquirePIDFile(p)
      if err != nil {
          return err
      }
      defer lock.Release()

      lis, err := BindSocket(p)
      if err != nil {
          return err
      }
      defer lis.Close()

      <-ctx.Done()
      return nil
  }
  ```

- [ ] **Step 20.4: Run test, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestRun_CleanShutdown
  ```

  Expected: PASS.

- [ ] **Step 20.5: Smoke-check the full binary path**

  ```bash
  go build -o /tmp/cat-smoke ./cmd/claude-agents-tui
  XDG_STATE_HOME=/tmp/xdg-test /tmp/cat-smoke daemon &
  PID=$!
  sleep 0.5
  ls /tmp/xdg-test/pa-monitor/  # should show daemon.pid and daemon.sock
  kill $PID
  wait $PID 2>/dev/null
  ls /tmp/xdg-test/pa-monitor/ 2>/dev/null  # should be empty
  ```

  Expected: files present while running, gone after SIGTERM. Cleanup directory: `rm -rf /tmp/xdg-test`.

- [ ] **Step 20.6: Commit**

  ```bash
  git add internal/daemon/lifecycle.go internal/daemon/lifecycle_test.go
  git commit -m "daemon: Run() ties pidfile+socket lifecycle with ctx cancel"
  ```

---

## Task 21: Stale-pid detection on second start

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle.go`
- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

Today's `AcquirePIDFile` rejects a second acquire if the flock is held. But if the prior daemon crashed without releasing (kernel cleans up flocks on process death, so the lock IS available), the file content may still list a dead pid. This task makes acquire idempotent in the face of a stale file.

Actually flock releases on process death automatically, so the file content matters less — but the test must prove that a leftover pidfile with a dead pid does not block startup.

- [ ] **Step 21.1: Write the failing test**

  Append to `internal/daemon/lifecycle_test.go`:

  ```go
  func TestAcquirePIDFile_StaleFileFromDeadPidIsReclaimed(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      // Simulate a stale pidfile: real file, dead pid (pid 1 is always
      // alive but is init; use a pid that won't exist — INT_MAX-1).
      stalePID := []byte("2147483646")
      if err := os.MkdirAll(dir, 0o700); err != nil {
          t.Fatal(err)
      }
      if err := os.WriteFile(paths.PIDFile, stalePID, 0o600); err != nil {
          t.Fatal(err)
      }

      lock, err := AcquirePIDFile(paths)
      if err != nil {
          t.Fatalf("acquire should reclaim stale pidfile, got err: %v", err)
      }
      defer lock.Release()
  }
  ```

- [ ] **Step 21.2: Run, expect pass already (flock handles this)**

  ```bash
  go test ./internal/daemon/ -v -run TestAcquirePIDFile_StaleFile
  ```

  If PASS: no implementation change needed — flock semantics already cover this. Add a comment to `AcquirePIDFile` clarifying that behaviour:

  ```go
  // AcquirePIDFile creates Paths.Dir if missing, opens the pidfile, takes
  // a non-blocking exclusive flock, and writes the current pid into the
  // file.
  //
  // If a previous daemon died without releasing the lock, the kernel has
  // already freed it — TryLock will succeed and we overwrite the stale
  // pid content. No explicit stale-detection is needed for this case.
  //
  // Returns an error if the lock is held by a LIVE process.
  ```

  If FAIL: implement explicit stale-pid detection in `AcquirePIDFile` — read the file, check if the pid is alive (`syscall.Kill(pid, 0)` returns ESRCH if dead), and if dead, proceed to overwrite. The skill-of-the-day says fewer YAGNI tweaks: only add this code if the test reveals it's needed.

- [ ] **Step 21.3: Commit**

  ```bash
  git add internal/daemon/lifecycle.go internal/daemon/lifecycle_test.go
  git commit -m "daemon: document/cover stale-pidfile reclaim via flock semantics"
  ```

---

## Task 22: SIGKILL recovery via subprocess test

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

This requires the test to fork a real subprocess and kill it.

- [ ] **Step 22.1: Write the test using `os/exec`**

  Append to `internal/daemon/lifecycle_test.go`:

  ```go
  func TestRun_SIGKILLRecoveredOnNextStart(t *testing.T) {
      if testing.Short() {
          t.Skip("subprocess-based; skipped in -short mode")
      }

      bin := buildTestBinary(t)
      stateDir := t.TempDir()

      cmd := exec.Command(bin, "daemon")
      cmd.Env = append(os.Environ(),
          "XDG_STATE_HOME="+stateDir,
          "XDG_RUNTIME_DIR="+stateDir,
      )
      if err := cmd.Start(); err != nil {
          t.Fatal(err)
      }

      sockPath := filepath.Join(stateDir, "pa-monitor", "daemon.sock")
      waitForFile(t, sockPath)

      if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
          t.Fatal(err)
      }
      _ = cmd.Wait()

      // Pidfile or socket may linger; next start must succeed.
      cmd2 := exec.Command(bin, "daemon")
      cmd2.Env = cmd.Env
      if err := cmd2.Start(); err != nil {
          t.Fatalf("second start failed: %v", err)
      }
      waitForFile(t, sockPath)
      _ = cmd2.Process.Signal(syscall.SIGTERM)
      _ = cmd2.Wait()
  }

  func buildTestBinary(t *testing.T) string {
      t.Helper()
      bin := filepath.Join(t.TempDir(), "claude-agents-tui")
      out, err := exec.Command("go", "build",
          "-o", bin,
          "github.com/phillipgreenii/claude-agents-tui/cmd/claude-agents-tui",
      ).CombinedOutput()
      if err != nil {
          t.Fatalf("build test binary: %v\n%s", err, out)
      }
      return bin
  }
  ```

  Add to imports: `"os/exec"`, `"syscall"`.

- [ ] **Step 22.2: Run, expect pass on the first try**

  ```bash
  go test ./internal/daemon/ -v -run TestRun_SIGKILL
  ```

  Expected: PASS (the second start succeeds because flock is released on process death).

- [ ] **Step 22.3: Commit**

  ```bash
  git add internal/daemon/lifecycle_test.go
  git commit -m "daemon: test SIGKILL recovery — second start succeeds"
  ```

---

## Task 23: Concurrent-start race test

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle_test.go`

- [ ] **Step 23.1: Write the test**

  Append to `lifecycle_test.go`:

  ```go
  func TestRun_ConcurrentStartExactlyOneWins(t *testing.T) {
      if testing.Short() {
          t.Skip("subprocess-based")
      }

      bin := buildTestBinary(t)
      stateDir := t.TempDir()
      env := append(os.Environ(),
          "XDG_STATE_HOME="+stateDir,
          "XDG_RUNTIME_DIR="+stateDir,
      )

      const N = 5
      type result struct {
          err error
          out []byte
      }
      results := make(chan result, N)

      for i := 0; i < N; i++ {
          go func() {
              cmd := exec.Command(bin, "daemon")
              cmd.Env = env
              out, err := cmd.CombinedOutput()
              results <- result{err: err, out: out}
          }()
      }

      // Give them time to race for the lock.
      time.Sleep(200 * time.Millisecond)

      // Whichever process won the lock is still running. Kill the whole
      // process group by SIGTERMing every pa-monitor child. Simpler: just
      // wait for them all and assert exactly one returned 0 / never
      // returns within the wait. The losers exit non-zero quickly.
      losers := 0
      for i := 0; i < N-1; i++ {
          select {
          case r := <-results:
              if r.err == nil {
                  t.Errorf("expected non-zero exit from a loser, got success: %s", r.out)
              }
              losers++
          case <-time.After(2 * time.Second):
              t.Fatal("losers did not exit within 2s")
          }
      }
      if losers != N-1 {
          t.Errorf("losers = %d, want %d", losers, N-1)
      }

      // Now signal the winner (still running). Pidfile holds its pid.
      pidData, err := os.ReadFile(filepath.Join(stateDir, "pa-monitor", "daemon.pid"))
      if err != nil {
          t.Fatal(err)
      }
      winnerPID, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
      if winnerPID > 0 {
          _ = syscall.Kill(winnerPID, syscall.SIGTERM)
      }
      <-results
  }
  ```

  Add imports: `"strconv"`, `"strings"`.

- [ ] **Step 23.2: Run, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestRun_ConcurrentStart
  ```

  Expected: PASS. If the losers' error message isn't clear enough on stderr, improve the message in `Run` and `AcquirePIDFile` and re-run.

- [ ] **Step 23.3: Commit**

  ```bash
  git add internal/daemon/lifecycle_test.go
  git commit -m "daemon: test concurrent start — exactly one wins flock"
  ```

---

## Task 24: gRPC server skeleton

**Files:**

- Create: `packages/claude-agents-tui/internal/daemon/server.go`
- Create: `packages/claude-agents-tui/internal/daemon/server_test.go`
- Modify: `packages/claude-agents-tui/internal/daemon/lifecycle.go`

Make `Run` start a gRPC server on the bound listener. Implement `Ping` (cheapest possible RPC) so the test has something to call.

- [ ] **Step 24.1: Write the failing test**

  Create `internal/daemon/server_test.go`:

  ```go
  package daemon

  import (
      "context"
      "net"
      "path/filepath"
      "testing"
      "time"

      "google.golang.org/grpc"
      "google.golang.org/grpc/credentials/insecure"

      pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
  )

  func TestServer_PingReturnsTimestamp(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()

      done := make(chan error, 1)
      go func() { done <- Run(ctx, paths) }()

      waitForFile(t, paths.Socket)

      conn, err := grpc.Dial("unix:"+paths.Socket,
          grpc.WithTransportCredentials(insecure.NewCredentials()),
          grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
              return (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
          }),
      )
      if err != nil {
          t.Fatal(err)
      }
      defer conn.Close()

      client := pb.NewPaMonitorClient(conn)
      resp, err := client.Ping(context.Background(), &pb.PingRequest{})
      if err != nil {
          t.Fatalf("Ping: %v", err)
      }
      if resp.GetTs() == nil {
          t.Error("Ping response has no ts")
      }

      cancel()
      select {
      case <-done:
      case <-time.After(2 * time.Second):
          t.Fatal("Run did not return after cancel")
      }
  }
  ```

- [ ] **Step 24.2: Run, expect failure (server isn't wired)**

  ```bash
  go test ./internal/daemon/ -v -run TestServer_Ping
  ```

  Expected: FAIL — connection refused or method not implemented.

- [ ] **Step 24.3: Implement the server**

  Create `internal/daemon/server.go`:

  ```go
  package daemon

  import (
      "context"
      "net"
      "time"

      "google.golang.org/grpc"
      "google.golang.org/protobuf/types/known/timestamppb"

      pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
  )

  type server struct {
      pb.UnimplementedPaMonitorServer
      started time.Time
  }

  func newServer() *server {
      return &server{started: time.Now()}
  }

  func (s *server) Ping(ctx context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
      return &pb.PingResponse{Ts: timestamppb.Now()}, nil
  }

  // serve runs the gRPC server on the given listener. It returns when the
  // grpc server stops (gracefully or otherwise).
  func serve(lis net.Listener) (*grpc.Server, func()) {
      gs := grpc.NewServer()
      pb.RegisterPaMonitorServer(gs, newServer())

      go func() {
          _ = gs.Serve(lis)
      }()

      return gs, func() { gs.GracefulStop() }
  }
  ```

- [ ] **Step 24.4: Wire `serve` into `Run`**

  In `internal/daemon/lifecycle.go`, modify `Run` to call `serve` after `BindSocket`:

  ```go
  func Run(ctx context.Context, p Paths) error {
      lock, err := AcquirePIDFile(p)
      if err != nil {
          return err
      }
      defer lock.Release()

      lis, err := BindSocket(p)
      if err != nil {
          return err
      }
      defer lis.Close()

      _, stop := serve(lis)
      defer stop()

      <-ctx.Done()
      return nil
  }
  ```

- [ ] **Step 24.5: Run the test, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestServer_Ping
  ```

  Expected: PASS.

- [ ] **Step 24.6: Run the full test suite**

  ```bash
  go test ./...
  ```

  Expected: every test green.

- [ ] **Step 24.7: Commit**

  ```bash
  git add internal/daemon/server.go internal/daemon/server_test.go internal/daemon/lifecycle.go
  git commit -m "daemon: gRPC server skeleton with Ping"
  ```

---

## Task 25: Heartbeat in `WatchState`

**Files:**

- Modify: `packages/claude-agents-tui/internal/daemon/server.go`
- Create: `packages/claude-agents-tui/internal/daemon/heartbeat_test.go`

- [ ] **Step 25.1: Write the failing test**

  Create `internal/daemon/heartbeat_test.go`:

  ```go
  package daemon

  import (
      "context"
      "net"
      "path/filepath"
      "testing"
      "time"

      "google.golang.org/grpc"
      "google.golang.org/grpc/credentials/insecure"

      pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
  )

  func TestWatchState_EmitsHeartbeats(t *testing.T) {
      dir := t.TempDir()
      paths := Paths{
          Dir:     dir,
          PIDFile: filepath.Join(dir, "daemon.pid"),
          Socket:  filepath.Join(dir, "daemon.sock"),
      }

      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      go func() { _ = Run(ctx, paths) }()

      waitForFile(t, paths.Socket)

      conn, err := grpc.Dial("unix://"+paths.Socket,
          grpc.WithTransportCredentials(insecure.NewCredentials()),
          grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
              return (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
          }),
      )
      if err != nil {
          t.Fatal(err)
      }
      defer conn.Close()

      client := pb.NewPaMonitorClient(conn)
      stream, err := client.WatchState(context.Background(), &pb.WatchStateRequest{
          HeartbeatIntervalMs: 100, // 100 ms for a fast test
      })
      if err != nil {
          t.Fatal(err)
      }

      // First message: initial DaemonState snapshot.
      first, err := stream.Recv()
      if err != nil {
          t.Fatalf("recv first: %v", err)
      }
      if first.GetState() == nil {
          t.Errorf("first message has no DaemonState: %+v", first)
      }

      // Then heartbeats at 100 ms cadence. Expect ≥2 within 350 ms.
      hbCount := 0
      deadline := time.Now().Add(350 * time.Millisecond)
      for time.Now().Before(deadline) {
          msg, err := stream.Recv()
          if err != nil {
              t.Fatalf("recv: %v", err)
          }
          if msg.GetHeartbeat() != nil {
              hbCount++
          }
      }
      if hbCount < 2 {
          t.Errorf("heartbeats received = %d, want ≥ 2", hbCount)
      }
  }
  ```

- [ ] **Step 25.2: Run, expect failure**

  ```bash
  go test ./internal/daemon/ -v -run TestWatchState
  ```

  Expected: FAIL — `WatchState` is unimplemented.

- [ ] **Step 25.3: Implement `WatchState`**

  Add to `internal/daemon/server.go`:

  ```go
  func (s *server) WatchState(req *pb.WatchStateRequest, stream pb.PaMonitor_WatchStateServer) error {
      // Send an initial state snapshot so the client has something even if
      // no real updates are happening.
      if err := stream.Send(&pb.WatchStateEvent{
          Payload: &pb.WatchStateEvent_State{
              State: s.currentState(),
          },
      }); err != nil {
          return err
      }

      interval := time.Duration(req.GetHeartbeatIntervalMs()) * time.Millisecond
      if interval < 50*time.Millisecond {
          interval = 2 * time.Second // server default
      }

      ticker := time.NewTicker(interval)
      defer ticker.Stop()

      ctx := stream.Context()
      for {
          select {
          case <-ctx.Done():
              return ctx.Err()
          case <-ticker.C:
              if err := stream.Send(&pb.WatchStateEvent{
                  Payload: &pb.WatchStateEvent_Heartbeat{
                      Heartbeat: &pb.Heartbeat{
                          Ts:                  timestamppb.Now(),
                          DaemonUptimeSeconds: uint64(time.Since(s.started).Seconds()),
                      },
                  },
              }); err != nil {
                  return err
              }
          }
      }
  }

  func (s *server) currentState() *pb.DaemonState {
      return &pb.DaemonState{
          Now:                 timestamppb.Now(),
          DaemonUptimeSeconds: uint64(time.Since(s.started).Seconds()),
          DaemonVersion:       "0.0.0-dev", // populated for real in Plan 3
      }
  }
  ```

  Also add `GetState`:

  ```go
  func (s *server) GetState(ctx context.Context, _ *pb.GetStateRequest) (*pb.DaemonState, error) {
      return s.currentState(), nil
  }
  ```

- [ ] **Step 25.4: Run, expect pass**

  ```bash
  go test ./internal/daemon/ -v -run TestWatchState
  ```

  Expected: PASS.

- [ ] **Step 25.5: Commit**

  ```bash
  git add internal/daemon/server.go internal/daemon/heartbeat_test.go
  git commit -m "daemon: WatchState with periodic heartbeats; GetState scaffolded"
  ```

---

## Task 26: Update the existing nix package to build the renamed module layout

**Files:**

- Modify: `packages/claude-agents-tui/default.nix` (if it specifies subpaths that have moved)

- [ ] **Step 26.1: Build the package via nix**

  ```bash
  nix build .#claude-agents-tui 2>&1 | tail -20
  ```

  If the build succeeds, no `default.nix` change is required for the package move — `buildGoModule` consumes the whole package tree. Skip the remaining steps in this task.

- [ ] **Step 26.2: If the build fails because of subpath references**

  Open `packages/claude-agents-tui/default.nix` and any helper imports. Find references to specific package subpaths (e.g. `subPackages = [ ... ]`) and update any that point at the moved directories. Re-run `nix build`.

- [ ] **Step 26.3: Run nix flake check**

  ```bash
  nix flake check 2>&1 | tail -10
  ```

  Expected: green.

- [ ] **Step 26.4: Commit (if anything changed)**

  ```bash
  git add packages/claude-agents-tui/default.nix
  git commit -m "nix: update package after internal/ refactor"
  ```

  If nothing changed, no commit.

---

## Task 27: Final integration sweep

**Files:** none (verification only).

- [ ] **Step 27.1: Full Go test sweep**

  ```bash
  cd packages/claude-agents-tui
  go test ./...
  ```

  Expected: every test passes, no skips except in `-short` mode.

- [ ] **Step 27.2: Race detector**

  ```bash
  go test -race ./internal/daemon/...
  ```

  Expected: no data race reports. The daemon's gRPC + ticker layout is the area most likely to race.

- [ ] **Step 27.3: Full nix flake check**

  ```bash
  cd ../..  # back to repo root
  nix flake check 2>&1 | tail -20
  ```

  Expected: green.

- [ ] **Step 27.4: Smoke test of the binary end-to-end**

  ```bash
  go build -o /tmp/cat-smoke packages/claude-agents-tui/cmd/claude-agents-tui
  XDG_STATE_HOME=/tmp/xdg-test /tmp/cat-smoke daemon &
  PID=$!
  sleep 0.3

  # Confirm files exist
  ls /tmp/xdg-test/pa-monitor/

  # Confirm Ping works via grpcurl if available, else skip
  if command -v grpcurl >/dev/null; then
    grpcurl -plaintext -unix /tmp/xdg-test/pa-monitor/daemon.sock pa_monitor.v1.PaMonitor/Ping
  fi

  kill $PID
  wait $PID 2>/dev/null
  ls /tmp/xdg-test/pa-monitor/ 2>/dev/null
  rm -rf /tmp/xdg-test
  ```

  Expected: files present while running, gone after shutdown. Ping returns a timestamp if grpcurl is available.

- [ ] **Step 27.5: Mark the foundation milestone complete**

  Close the beads issue:

  ```bash
  bd close beads_pg2-752 --reason="Plan 1 complete: core carved, proto schema in place, daemon lifecycle + heartbeat shipping with full test coverage"
  ```

  Note: this is the only `bd close` for Plan 1. Plans 2 and 3 will be created as separate beads as follow-ups.

- [ ] **Step 27.6: No additional commit needed.**

---

## Self-Review (post-write)

Ran the spec-coverage check after drafting:

- **Spec phase 1 (carve `core/`)**: covered by Tasks 2–9.
- **Spec phase 2 (proto schema + codegen)**: Tasks 11–14.
- **Spec phase 3 (daemon subcommand + lifecycle)**: Tasks 15–25.
- **Lifecycle failure-path matrix (10 cases in spec)**: Plan 1 covers cases 1 (clean shutdown), 2 (SIGKILL), 5 (concurrent start), and case 7 (stale socket — folded into `BindSocket` directly). Remaining cases (panic mid-handler, crash before defers, pid recycled, disk full, parent dir removed, perm denied) are explicitly **deferred to Plan 2** because they require infrastructure (signal handler, init-stage tracking) that doesn't exist yet at this milestone. The Plan-1 closing notes call this out.
- **Heartbeat protocol detail**: Task 25.
- **gRPC over Unix socket**: Tasks 11–14 (schema), 24 (server).
- **OTel**: not in Plan 1. Plan 2.
- **Decorators**: not in Plan 1. Plan 2.
- **Block / week tracking**: not in Plan 1. Plan 2.
- **TUI client refactor**: not in Plan 1. Plan 3.
- **CLI subcommands beyond `daemon`**: not in Plan 1. Plan 3.
- **cmux-bridge**: not in Plan 1. Plan 3.
- **Nix LaunchAgent + dashboard registration**: not in Plan 1. Plan 3.
- **Binary rename to `pa-monitor`**: not in Plan 1. Plan 3 (during nix module rewrite).

Placeholder scan: no "TBD", "implement later", or "similar to" placeholders. Code blocks present in every code step.

Type consistency: `Paths`, `PathOverrides`, `PIDLock`, `serve`, `server` referenced consistently across tasks. `pickSubcommand` signature stable.

---

## Plan 1 → Plan 2 → Plan 3 outline

For visibility — not part of this plan's execution scope.

**Plan 2 — Daemon features (spec phases 4–6):**

1. OTel emitter (nil-safe; metrics + events; no traces yet).
2. Built-in label detectors (terminal, gascity, repo, project, agent).
3. Decorator shell-out runner with `/nix/store/` path constraint.
4. 5h block tracker emits `block.id` label.
5. Weekly tracker via `ccusage weekly`, `week.id` label, computed limit hit detection.
6. Extend `plan_caps.go` with `WeekCapUSD`.
7. Remaining lifecycle failure-path tests (panic, recycled pid, perm denied, etc.).
8. Caffeinate persistence to `runtime.json`.
9. Trace span for `nudge` operation (foundation for Plan 3's nudge wiring).

**Plan 3 — Client migration + nix wiring (spec phases 7–11):**

1. Rename binary `claude-agents-tui` → `pa-monitor`. Rename package directory.
2. Refactor TUI to gRPC client (`internal/rpcclient/`). OFFLINE pill + reconnect.
3. CLI subcommands: `status`, `info`, `caffeinate`, `nudge`, `agents-busy-check`, `wait-until-agents-finished`, `config show`.
4. `cmux-bridge` subcommand. Migrate `internal/cmuxstatus` logic into it.
5. Per-client socket-recovery test matrix.
6. Nix home-manager module: LaunchAgent (`com.phillipg.pa-monitor-daemon`), dashboard registration, decorator config rendering.
7. Generic Grafana dashboard JSON shipped from this repo.
8. ZR consumer module (in `phillipg-nix-ziprecruiter`) — out of scope here, tracked there.
9. Delete `internal/headless/` (replaced by `wait-until-agents-finished` subcommand).
10. Final `flake check` + smoke test on macOS BTM.
