# Phase 2 — `pb` tool (`pn:applied` gate create/check) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Dispatch a fresh implementer per task; pass it the absolute worktree path.

**Goal:** Build `pb` ("phillip-beads"): `pb gate create` writes `pn:applied` gates keyed to a change's `git patch-id` (co-located in the blocked bead's DB), and `pb gate check` — run inside a `pn` workspace as the apply post-hook — resolves them by scanning each repo's applied history via `pn workspace info`.

**Architecture:** A standalone Go cobra binary (Pattern-A `mkGoApp`, no local replace) that shells out to `pn` (workspace info API), `bd` (beads gates), and `git` (patch-id + history) — all on `PATH` via `wrapProgram`. Internal packages isolate each concern (`duration`, `run`, `pn`, `bd`, `discover`, `patchid`, `gate`); `cmd/pb` is thin cobra wiring. All exec is behind a `run.Runner` interface so unit tests inject a `FakeRunner` and run in the pure-nix sandbox without real binaries; real-binary behavior is pinned by build-tagged contract tests (`//go:build contract`).

**Tech Stack:** Go 1.25.0, `github.com/spf13/cobra` (CLI, matching `pg-pr`), `gomod2nix` engine, nix-darwin/home-manager modules, `bd` 1.0.4 (Dolt-backed), `pn` (Phase 1).

## Global Constraints

- **Module path:** `github.com/phillipgreenii/pb`. **Go version:** `go 1.25.0` (match `pg-pr`/`pr-pool`).
- **Packaging = Pattern A** (no local-replace; `pb` shells out via `PATH`, not Go imports): `src = lib.cleanSource ./.`, `subPackages = [ "cmd/pb" ]`, `versionPath = "main.Version"`, `gomod2nixToml = ./gomod2nix.toml`, `nativeBuildInputs = [ makeWrapper ]`, `nativeCheckInputs = [ git ]`, `postInstall` wraps **`bd` + `git`** onto `PATH`. Template: `packages/pg-pr` + `packages/pr-pool` (wrapProgram block).
- **`pn` is an AMBIENT runtime PATH dep, NOT wrapped (critique fix #1 — was a SHOWSTOPPER).** agent-support's core principle is "standalone / no external flake dependencies" (its CLAUDE.md); it does NOT import repo-base's overlay, so `final.pn` does not resolve, and `pn` is published by repo-base on only 2 of the 4 systems agent-support builds. Therefore `pb` does **not** wrap `pn`. `pn` is expected on the ambient `PATH` at runtime — which the apply post-hook env already guarantees (spec Component 3: "`pb` must be on the apply environment's `PATH`"; `pn` is symmetrically there) and which dev machines have. `run.CLIRunner` already surfaces a clear "is pn on PATH?" error if absent. The `home/programs/pb` module documents `pn` as a required ambient dep. `bd` IS wrappable (sourced from the `llm-agents` input, exactly like `pr-pool` wraps `bd`); `git` is `pkgs.git`.
- **Per-source-digest versioning** (agent-support CLAUDE.md "Versioning"): never thread a repo `gitHash`; `mkGoApp` stamps the digest. Refresh deps with `go mod tidy && nix run github:nix-community/gomod2nix -- generate` (NOT `nix-update`; no `vendorHash`).
- **Test isolation (the Phase-1 lesson, MANDATORY):** every test isolates _all_ filesystem writes to `t.TempDir()`. Any test that invokes real `bd` MUST `t.Setenv("HOME", t.TempDir())` **and** `t.Setenv("XDG_DATA_HOME"/"XDG_STATE_HOME"/"XDG_CONFIG_HOME", <temp>)` and scrub `BEADS_DIR`/`WORKSPACE_ROOT` — the nix sandbox runs with `HOME=/homeless-shelter` (read-only). Real-`bd`/real-`pn` tests call `t.Skip("bd not on PATH")` when the binary is absent (so they skip in the pure sandbox and run on a dev machine); real-`git` tests rely on `nativeCheckInputs = [ git ]` (git is present in the sandbox). Pure-logic tests use `FakeRunner` (no binary).
- **`bd` surface (VERIFIED against bd 1.0.4, the installed version):**
  - A custom `--type=pn:applied` is accepted and round-trips verbatim into `await_type` (no validation). The gate is an issue with `issue_type: "gate"`.
  - `pb` MUST set `BD_JSON_ENVELOPE=1` on every `bd` invocation. JSON output is the envelope `{ "data": <…>, "schema_version": 1 }`. For `gate list`/`show`, `.data` is an **array**; for `create`, `.data` is an **object** (`.data.id`).
  - `bd gate create --type=pn:applied --blocks <bead> --await-id "<wsid>:<repo>:<patchid>" [--reason <r>] --json` → `.data.id` is the new gate id; `.data.await_type`, `.data.await_id` echo back.
  - `bd gate list --limit 0 --json` lists open gates; each carries `id`, `await_type`, `await_id`, **and `metadata`** inline (so check reads the baseline in one call). `--limit` defaults to 50 — MUST pass `--limit 0` for all.
  - `bd update <id> --set-metadata applied_baseline=<ref>` writes `metadata.applied_baseline` (round-trips in `gate list`/`show`).
  - `bd gate resolve <gate-id> [--reason <r>]` closes the gate (≡ `bd close`); the blocked bead then appears in `bd ready`.
  - **convert-to-human (VERIFIED):** `bd human list` shows issues carrying the `human` label. To convert a stale gate, add the label: `bd update <gate-id> --add-label human` (`--add-label` is repeatable; `bd label add human <id>` is equivalent). The gate stays blocking but surfaces in `bd human list` for a human to decide. **close** stale action = `bd gate resolve <gate-id>` (unblocks the bead — deliberate abandon).
  - `bd create "<title>" --defer <date>` creates a bead hidden from `bd ready` until `<date>` (the deferred-create mechanism; date formats like `+1d`, `2030-01-01`).
  - DB targeting: `bd -C <dir>` (run as if cwd=`<dir>`, auto-discovers `.beads`) **or** `bd --db <path>`. `pb` uses `-C <repo-or-root-dir>`.
  - `.beads/metadata.json` Dolt identity fields: `dolt_server_host`, `dolt_database`, `project_id`; the port is in the sibling file `.beads/dolt-server.port`. Today all repos resolve to one project (`dolt_database: pg2`, one `project_id`).
- **Completion gate (BOTH required, the Phase-1 lesson):** `nix flake check` **AND** `prek run --all-files` (or `pre-commit run --all-files`) MUST pass — not just `go test ./...`. When iterating, scope the heavy gate with `nix build .#checks.<system>.pb` (or the `pb-contract` check) rather than the whole flake.
- **Pre-commit gotcha:** `treefmt`/prettier reformats markdown and _fails_ the commit with "files were modified"; just `git add` the reformatted file and re-commit. After editing the `pre-commit` block in `flake.nix`, run `nix run .#install-pre-commit-hooks` to regenerate `.pre-commit-config.yaml`. Never `--no-verify`.
- **Branch safety:** simple branch name (`pb-phase2`); no `Refs:` line (non-ZR repo). Run `git branch --show-current` before any commit. Work happens in the worktree at `.worktrees/pb-phase2`.
- **Task tracking = `bd`.** Phase 2 = `pg2-k43p.4` (already claimed). Run `bd` from `/Users/phillipg/phillipg_mbp` for the `pg2` prefix.

## Design decisions resolved (carry into every task)

- **D1 — `pb gate create` does NOT create or un-defer the bead.** It takes an existing `--blocks <beadid>` (the spec signature), attaches gate(s), writes the baseline, and exits. The fleet-race-safe lifecycle (`bd create --defer …` → `pb gate create` → un-defer) is the _caller's_ responsibility (taught by the Phase-3 plugin) and is exercised end-to-end by pb's fleet-race test (Task 8). Rationale: honors the spec's `--blocks <beadid>` signature and keeps `pb`'s responsibility single; a failed `pb gate create` leaves the bead deferred-and-un-gated, recoverable by re-run.
- **D2 — OverridePaths limitation is out of scope.** `pb` is built against the documented common case (a no-override apply, where `pn workspace info` reports the canonical `<root>/<name>` path). The override-path keying gap is already documented in repo-base ADR 0012 and tracked as `pg2-k43p.3`; `pb` does not work around it here.

## Critique fixes already folded into this plan (from the independent review)

This plan was revised after an independent critique. Already corrected in-line: **(#1, was SHOWSTOPPER)** `pn` is not wrapped — it's an ambient PATH dep (agent-support is standalone/no-external-flake-deps); **(#2)** all `pn workspace info` test fixtures use the real **bare** JSON object (no `{data}` envelope); **(#4)** the `pb gate check` test seam is the `Discover` func field on `CheckDeps` (the `dbsOverride` field was removed); **(#5)** `pb gate create` co-locates the gate in the **bead's own DB** via `discover` + `bd.HasBead` (not a hardcoded root); **(#7)** convert-to-human is the VERIFIED `bd update <id> --add-label human`; **(#8)** `Gate.CreatedAt`/`HasBead`/`AddLabel` live in Task 5; **(#3)** `time.Duration` has no `.Zero()` → guard `> 0`.

## Required spec-named tests (critique #6 — the implementer MUST add these; do not mark a task done without them)

The spec's testing-strategy names scenarios beyond the happy path. Add each in the noted task:

- **Task 8:** `--commits` **multi-commit → one gate per commit**, all blocking the same bead (bead surfaces only when ALL resolve — beads ANDs blockers). Assert N gates created with N distinct patch-ids.
- **Task 9:** **multi-DB scan** — two distinct DBs (`Discover` stub returns two `DB{Dir}`s); a gate lives in the **non-first** DB and is **resolved in that gate's own DB** (assert the `gate resolve` call targets the right `-C dir`).
- **Task 9:** **stale boundary pair** — one gate younger than `--stale-after` (left alone) AND one older (acted on) in the SAME run; assert only the older appears in `StaleActions`.
- **Task 9:** **`>50` gates** — list returns >50 gates (proves `--limit 0`, not the default 50); all are processed.
- **Task 9:** **baseline-ancestry fallback** — baseline set but NOT an ancestor of `applied_ref` (`IsAncestor` false) → scans `-n <N> <ref>` (not a false miss); assert the `log -p … -n` form is used.
- **Tasks 8/9:** **`--json` schema assertions** — unmarshal the CLI `--json` output and assert the EXACT field names from the spec (`gate check`: `resolved`,`would_resolve`,`skipped[].{gate-id,repo,reason}`,`stale_actions[].{gate-id,action}`; `gate create`: `gates[].{gate-id,await_id,repo,patch-id,applied_baseline}`).
- **Task 10 (contract, real bd+git):** **survives a clean rebase end-to-end** — create gate, `git rebase` the repo (SHA changes, diff doesn't), `pb gate check` still resolves it.

## File structure

```
packages/pb/
├── go.mod                       # module github.com/phillipgreenii/pb, go 1.25.0
├── go.sum
├── gomod2nix.toml               # committed; tracks cobra + transitive deps
├── default.nix                  # mkGoApp Pattern A + wrapProgram [pn bd git]
├── README.md
├── cmd/pb/
│   ├── main.go                  # cobra root, var Version, registers `gate`
│   ├── gate.go                  # `pb gate` parent command
│   ├── gate_create.go           # `pb gate create` cobra wiring → internal/gate
│   ├── gate_check.go            # `pb gate check` cobra wiring → internal/gate
│   └── contract_test.go         # //go:build contract — real bd/pn/git surfaces
└── internal/
    ├── duration/
    │   ├── duration.go          # ParseDuration (ms..d, reject <1ms)
    │   └── duration_test.go
    ├── run/
    │   ├── runner.go            # Runner interface + CLIRunner
    │   └── fake.go              # FakeRunner (test double)
    ├── pn/
    │   ├── info.go              # Info/Repo structs + Client.Info()
    │   └── info_test.go
    ├── bd/
    │   ├── bd.go                # Gate struct + Client (gate list/create/resolve, set-metadata)
    │   └── bd_test.go
    ├── discover/
    │   ├── discover.go          # walk-up .beads bounded at root + Dolt-identity dedupe
    │   └── discover_test.go
    ├── patchid/
    │   ├── patchid.go           # compute (show|patch-id), scan (log -p|patch-id), ancestry
    │   └── patchid_test.go
    └── gate/
        ├── create.go            # Create orchestration (T3)
        ├── create_test.go
        ├── check.go             # Check orchestration (T4): discover→list→scan→resolve, stale, dry-run
        └── check_test.go
```

Also modified: `home/programs/pb/default.nix` (new), `flake.nix` (overlay + re-export + hooks), `docs/adr/0018-pb-tool-and-pn-applied-contract.md` (new), `docs/adr/index.md`, `README.md`, `CLAUDE.md`.

---

## Task 1: Scaffolding — `packages/pb` mkGoApp, home module, flake wiring

**Files:**

- Create: `packages/pb/go.mod`, `packages/pb/default.nix`, `packages/pb/cmd/pb/main.go`, `packages/pb/README.md`
- Create: `home/programs/pb/default.nix`
- Modify: `flake.nix` (overlay entry, packages re-export, pre-commit hooks)
- Generate: `packages/pb/go.sum`, `packages/pb/gomod2nix.toml`

**Interfaces:**

- Produces: a runnable `pb` binary with `pb --version` and an empty `pb gate` command group; `main.Version` stamped by `mkGoApp`. Later tasks add subcommands under `cmd/pb/gate.go`.

- [ ] **Step 1: Create `packages/pb/go.mod`**

```
module github.com/phillipgreenii/pb

go 1.25.0

require github.com/spf13/cobra v1.8.1
```

- [ ] **Step 2: Create the cobra root `packages/pb/cmd/pb/main.go`**

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is overridden at build time by mkGoApp (versionPath = "main.Version").
var Version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pb",
		Short:         "phillip-beads: pn:applied gate create/check",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newGateCmd())
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Create the empty gate group `packages/pb/cmd/pb/gate.go`** (subcommands added in Tasks 8–9)

```go
package main

import "github.com/spf13/cobra"

func newGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gate",
		Short: "Manage pn:applied gates",
	}
}
```

- [ ] **Step 4: Generate `go.sum` + `gomod2nix.toml`**

Run (network required — dev machine):

```bash
cd packages/pb
go mod tidy
nix run github:nix-community/gomod2nix -- generate
```

Expected: `go.sum` and `gomod2nix.toml` created; `gomod2nix.toml` lists `spf13/cobra`, `spf13/pflag`, `inconshreveable/mousetrap` (cobra's deps).

- [ ] **Step 5: Create `packages/pb/default.nix`** (Pattern A + wrapProgram, template `pg-pr` + `pr-pool`)

```nix
{
  lib,
  mkGoApp,
  makeWrapper,
  bd,
  git,
}:

mkGoApp {
  pname = "pb";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/pb" ];

  # gomod2nix engine (ADR 0008, Case A): no local replace; third-party deps
  # pinned in gomod2nix.toml; buildGoApplication builds from src — no vendorHash.
  gomod2nixToml = ./gomod2nix.toml;

  # pb exports its version as `main.Version` (capitalised).
  versionPath = "main.Version";

  nativeBuildInputs = [ makeWrapper ];

  # Real-git unit tests run in the build sandbox; bd/pn tests t.Skip when absent.
  nativeCheckInputs = [ git ];

  # Runtime PATH deps wrapped: bd (gates) + git (patch-id/history). `pn` is NOT
  # wrapped — it is an ambient runtime dep (see Global Constraints; agent-support
  # is standalone/no-external-flake-deps so it cannot reference repo-base's pn).
  # pn is present on the apply-env PATH (spec Component 3) and on dev PATHs.
  postInstall = ''
    wrapProgram $out/bin/pb --prefix PATH : ${
      lib.makeBinPath [
        bd
        git
      ]
    }
  '';

  meta = {
    description = "phillip-beads: pn:applied gate create/check (consumes pn workspace info; pn required on PATH)";
    mainProgram = "pb";
  };
}
```

- [ ] **Step 6: Create the home option module `home/programs/pb/default.nix`** (template `home/programs/pr-pool/default.nix`)

```nix
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.pb;
in
{
  options.phillipgreenii.programs.pb = {
    enable = lib.mkEnableOption ''
      pb (phillip-beads: writes and resolves pn:applied gates that hold beads
      until a `pn workspace apply` applies their change). `bd` and `git` are
      wired onto PATH via wrapProgram; `pn` MUST be provided on PATH by the
      environment (the apply post-hook env and dev shells already have it).
    '';
    package = lib.mkPackageOption pkgs "pb" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
  };
}
```

- [ ] **Step 7: Wire `flake.nix` — overlay entry.** Find the `pr-pool = final.callPackage ./packages/pr-pool { … };` block in the overlay and add immediately after it. NOTE (critique fix #1): do **not** pass `pn` — it is not in agent-support's overlay scope (standalone/no-external-flake-deps) and is an ambient runtime dep. Pass only `mkGoApp` + `bd` (sourced exactly like `pr-pool`):

```nix
pb = final.callPackage ./packages/pb {
  inherit (goBuilders) mkGoApp;
  # No top-level bd/beads overlay attr — source it like pr-pool (flake.nix).
  bd = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
  # git is pkgs.git (auto via callPackage); pn is NOT passed (ambient PATH dep).
};
```

- [ ] **Step 8: Wire `flake.nix` — packages re-export.** Find the `inherit (pkgs) … pr-pool … ;` block under `packages = { … }` and add `pb` to the list.

- [ ] **Step 9: Wire `flake.nix` — pre-commit hooks.** In the `pre-commit` block: (a) extend the `gofmt` hook `files` regex to include `pb` (`^packages/(pg-pr|pr-pool|pb)/.*\\.go$`); (b) add a `golangci-lint-pb` hook mirroring `golangci-lint-pr-pool` (same `NIX_BUILD_TOP` sandbox-skip guard, `cd packages/pb`, `files = "^packages/pb/.*\\.go$"`, `pass_filenames = false`).

```nix
golangci-lint-pb = {
  enable = true;
  name = "golangci-lint (pb)";
  entry = toString (
    hookPkgs.writeShellScript "precommit-golangci-lint-pb" ''
      set -e
      export PATH="${hookPkgs.go}/bin:$PATH"
      if [ -n "''${NIX_BUILD_TOP:-}" ]; then
        echo "golangci-lint (pb): skipped — nix build sandbox"
        exit 0
      fi
      cd packages/pb
      ${hookPkgs.golangci-lint}/bin/golangci-lint run ./...
    ''
  );
  files = "^packages/pb/.*\\.go$";
  pass_filenames = false;
};
```

- [ ] **Step 10: Create `packages/pb/README.md`** — a short description (what `pb gate create`/`check` do, the runtime PATH deps, link to the design spec and ADR 0018). One paragraph is fine; expand in Task 11.

- [ ] **Step 11: Regenerate pre-commit config + build + run**

```bash
nix run .#install-pre-commit-hooks
nix build .#pb
"$PWD/result/bin/pb" --version
```

Expected: build succeeds; `--version` prints a `YY.MM.DD.SSSSS+<digest>`-style version (NOT `dev`); `result/bin/pb` is wrapped (check `head -5 result/bin/pb` shows the wrapper exporting PATH with pn/bd/git).

- [ ] **Step 12: Targeted flake check for the new package**

```bash
nix build .#checks.$(nix eval --raw --impure --expr 'builtins.currentSystem').pb 2>&1 | tail -20
```

Expected: the `pb` package check (its `go test ./...` build-sandbox run) passes (no tests yet → trivially green; confirms the derivation wiring).

- [ ] **Step 13: Commit**

```bash
git add packages/pb home/programs/pb flake.nix .pre-commit-config.yaml
git commit -m "feat(pb): scaffold packages/pb (mkGoApp + wrapProgram pn/bd/git) + home module + flake wiring"
```

---

## Task 2: Duration parser (`internal/duration`)

**Files:**

- Create: `packages/pb/internal/duration/duration.go`, `packages/pb/internal/duration/duration_test.go`

**Interfaces:**

- Produces: `func ParseDuration(s string) (time.Duration, error)` — accepts Go's `time.ParseDuration` units plus a `d` (=24h) suffix; rejects anything `< 1ms` (zero, negative, sub-ms like `500us`) and bare numbers (no unit). Consumed by Task 9's `--stale-after`.

- [ ] **Step 1: Write the failing test** `packages/pb/internal/duration/duration_test.go`

```go
package duration

import (
	"testing"
	"time"
)

func TestParseDuration_accepts(t *testing.T) {
	cases := map[string]time.Duration{
		"1ms":   time.Millisecond,
		"100ms": 100 * time.Millisecond,
		"30s":   30 * time.Second,
		"1m":    time.Minute,
		"2h":    2 * time.Hour,
		"3d":    3 * 24 * time.Hour,
		"1d12h": 24*time.Hour + 12*time.Hour,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDuration_rejects(t *testing.T) {
	for _, in := range []string{"", "0", "0ms", "-1s", "500us", "5", "abc", "1d!"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = nil error, want error", in)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/duration/ -run TestParseDuration -v`
Expected: FAIL — `undefined: ParseDuration`.

- [ ] **Step 3: Write minimal implementation** `packages/pb/internal/duration/duration.go`

```go
// Package duration parses human duration strings with millisecond granularity
// and day units. Go's time.ParseDuration covers ns..h but not "d"; this adds d=24h
// and rejects anything below 1ms (the minimum resolvable stale-after unit).
package duration

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// dayRe matches a leading "<int>d" segment so we can translate it to hours
// before delegating the rest to time.ParseDuration. Supports e.g. "3d", "1d12h".
var dayRe = regexp.MustCompile(`^(\d+)d`)

// ParseDuration parses s as a duration. It accepts time.ParseDuration units
// (ns, us, µs, ms, s, m, h) plus a leading "<int>d" (days = 24h). It rejects the
// empty string, a bare number (no unit), and any non-positive or sub-millisecond
// total.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var days time.Duration
	rest := s
	if m := dayRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("invalid day count in %q: %w", s, err)
		}
		days = time.Duration(n) * 24 * time.Hour
		rest = strings.TrimPrefix(s, m[0])
	}
	var sub time.Duration
	if rest != "" {
		var err error
		sub, err = time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
	}
	total := days + sub
	if total < time.Millisecond {
		return 0, fmt.Errorf("duration %q is below the 1ms minimum", s)
	}
	return total, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/duration/ -v`
Expected: PASS (both tests). Note: `time.ParseDuration("5")` already errors (no unit) and `time.ParseDuration("")` errors, so bare numbers and empty `rest` after a non-`d` input are rejected; `"5"` has no `d` prefix so `rest="5"` → `ParseDuration` error. `"0ms"`/`"-1s"`/`"500us"` parse fine but fail the `< 1ms` guard.

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/duration
git commit -m "feat(pb): duration parser (ms..d, reject <1ms)"
```

---

## Task 3: Runner abstraction (`internal/run`)

**Files:**

- Create: `packages/pb/internal/run/runner.go`, `packages/pb/internal/run/fake.go`, `packages/pb/internal/run/runner_test.go`

**Interfaces:**

- Produces:
  - `type Runner interface { Run(ctx context.Context, name string, args []string, opts Options) (Result, error) }`
  - `type Options struct { Dir string; Env []string; Stdin string }`
  - `type Result struct { Stdout, Stderr string; ExitCode int }`
  - `type CLIRunner struct{}` (production; uses `os/exec`)
  - `type FakeRunner struct{…}` with `AddResponse(name string, args []string, res Result, err error)`, `Run(...)`, `Calls() []Call`, and a `Call` type. `FakeRunner` matches a scripted response by `name`+exact-`args`; returns an error if none scripted (so tests fail loudly on an unexpected call).
- Consumed by: `internal/pn`, `internal/bd`, `internal/patchid`, `internal/gate`. (Models `pg-pr`'s `beads.Runner` + `pn`'s `exec.FakeRunner`, generalised to multi-binary: `name` is `pn`/`bd`/`git`.)

- [ ] **Step 1: Write the failing test** `packages/pb/internal/run/runner_test.go`

```go
package run

import (
	"context"
	"testing"
)

func TestCLIRunner_capturesStdoutAndExit(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "printf hi; exit 0"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "hi" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hi")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestCLIRunner_nonZeroExitReturnsError(t *testing.T) {
	r := CLIRunner{}
	res, err := r.Run(context.Background(), "sh", []string{"-c", "echo boom 1>&2; exit 3"}, Options{})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Stderr == "" {
		t.Error("expected stderr captured")
	}
}

func TestFakeRunner_scriptedAndRecords(t *testing.T) {
	f := NewFakeRunner()
	f.AddResponse("bd", []string{"gate", "list"}, Result{Stdout: `{"data":[]}`}, nil)
	res, err := f.Run(context.Background(), "bd", []string{"gate", "list"}, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != `{"data":[]}` {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != "bd" {
		t.Errorf("Calls = %+v", calls)
	}
}

func TestFakeRunner_unscriptedErrors(t *testing.T) {
	f := NewFakeRunner()
	if _, err := f.Run(context.Background(), "bd", []string{"nope"}, Options{}); err == nil {
		t.Fatal("expected error for unscripted call")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/run/ -v`
Expected: FAIL — undefined `CLIRunner`, `Options`, `Result`, `NewFakeRunner`.

- [ ] **Step 3: Write `packages/pb/internal/run/runner.go`**

```go
// Package run abstracts external-process execution behind a Runner interface so
// pb's logic is unit-testable with a FakeRunner (no real pn/bd/git). Mirrors
// pg-pr's beads.Runner and repo-base's exec.FakeRunner, generalised to name the
// binary per call.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Options controls one invocation.
type Options struct {
	Dir   string   // working directory (empty = inherit)
	Env   []string // full env (nil = inherit os.Environ())
	Stdin string   // stdin contents (empty = none)
}

// Result is the captured outcome of one invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner runs `name args...`.
type Runner interface {
	Run(ctx context.Context, name string, args []string, opts Options) (Result, error)
}

// CLIRunner is the production Runner using os/exec.
type CLIRunner struct{}

// Run executes name with args. A non-zero exit returns a non-nil error whose
// message includes a trimmed stderr tail; Result is still populated (ExitCode set).
func (CLIRunner) Run(ctx context.Context, name string, args []string, opts Options) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, fmt.Errorf("%s %s: exit %d: %s",
				name, strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return res, fmt.Errorf("%s %s: %w (is %s on PATH?)", name, strings.Join(args, " "), err, name)
	}
	return res, nil
}
```

- [ ] **Step 4: Write `packages/pb/internal/run/fake.go`**

```go
package run

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call records one Run invocation for assertions.
type Call struct {
	Name string
	Args []string
	Opts Options
}

type fakeResponse struct {
	name string
	args []string
	res  Result
	err  error
}

// FakeRunner is a scripted test double. Responses match by name + exact args and
// are consumed in order; an unscripted call returns an error so tests fail loudly.
type FakeRunner struct {
	mu        sync.Mutex
	responses []fakeResponse
	calls     []Call
}

func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

func (f *FakeRunner) AddResponse(name string, args []string, res Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, fakeResponse{name: name, args: append([]string{}, args...), res: res, err: err})
}

func (f *FakeRunner) Run(_ context.Context, name string, args []string, opts Options) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Name: name, Args: append([]string{}, args...), Opts: opts})
	for i, r := range f.responses {
		if r.name == name && argsEqual(r.args, args) {
			f.responses = append(f.responses[:i], f.responses[i+1:]...)
			return r.res, r.err
		}
	}
	return Result{}, fmt.Errorf("FakeRunner: no scripted response for: %s %s", name, strings.Join(args, " "))
}

func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

var _ Runner = (*FakeRunner)(nil)
var _ Runner = CLIRunner{}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/run/ -v`
Expected: PASS (4 tests).

- [ ] **Step 6: Commit**

```bash
git add packages/pb/internal/run
git commit -m "feat(pb): run.Runner exec abstraction + FakeRunner"
```

---

## Task 4: `pn workspace info` client (`internal/pn`)

**Files:**

- Create: `packages/pb/internal/pn/info.go`, `packages/pb/internal/pn/info_test.go`

**Interfaces:**

- Consumes: `run.Runner`.
- Produces:
  - `type Repo struct { Name, Path, AppliedRef string; Dirty bool }` (json tags `name`,`path`,`applied_ref`,`dirty`)
  - `type Info struct { Wsid, Root, Terminal string; Repos []Repo }` (json tags `wsid`,`root`,`terminal`,`repos`)
  - `type Client struct { R run.Runner }`
  - `func (c Client) Info(ctx context.Context, dir string) (Info, error)` — runs `pn workspace info --json` (cwd=`dir`) and unmarshals. Errors clearly if not in a workspace (non-zero exit) and on empty `root`.
  - `func (i Info) RepoByName(name string) (Repo, bool)`
- Consumed by: `internal/gate` (Tasks 8–9). Schema pinned by repo-base ADR 0012.

- [ ] **Step 1: Write the failing test** `packages/pb/internal/pn/info_test.go`

```go
package pn

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// REAL shape (critique #2): pn workspace info --json emits a BARE object (no
// {data,…} envelope) — verified against repo-base modules/pn/internal/cli/workspace.go
// (enc.Encode(info)). The Client tolerates an envelope too, defensively.
const sampleInfoJSON = `{
  "wsid": "home",
  "root": "/ws",
  "terminal": "machine",
  "repos": [
    {"name": "repo-a", "path": "/ws/repo-a", "applied_ref": "3e1f4b1", "dirty": false},
    {"name": "repo-b", "path": "/ws/repo-b", "applied_ref": "", "dirty": true}
  ]
}`

func TestInfo_parsesEnvelopeAndFields(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: sampleInfoJSON}, nil)
	info, err := Client{R: f}.Info(context.Background(), "/ws")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Wsid != "home" || info.Root != "/ws" || info.Terminal != "machine" {
		t.Fatalf("top-level = %+v", info)
	}
	if len(info.Repos) != 2 {
		t.Fatalf("repos len = %d", len(info.Repos))
	}
	a, ok := info.RepoByName("repo-a")
	if !ok || a.AppliedRef != "3e1f4b1" || a.Dirty {
		t.Errorf("repo-a = %+v ok=%v", a, ok)
	}
	b, _ := info.RepoByName("repo-b")
	if b.AppliedRef != "" || !b.Dirty {
		t.Errorf("repo-b applied_ref must be empty string + dirty: %+v", b)
	}
}

func TestInfo_errorsWhenNotInWorkspace(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stderr: "not in a workspace", ExitCode: 1}, errExit())
	if _, err := (Client{R: f}).Info(context.Background(), "/tmp"); err == nil {
		t.Fatal("expected error when pn exits non-zero")
	}
}

func errExit() error { return &runErr{} }

type runErr struct{}

func (*runErr) Error() string { return "exit 1" }
```

Note: the test feeds both a non-nil error and a non-zero `ExitCode` to mimic `CLIRunner`'s contract (non-zero exit ⇒ error). `Info` MUST treat a non-nil runner error as fatal.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/pn/ -v`
Expected: FAIL — undefined `Client`, `Info`, `Repo`.

- [ ] **Step 3: Write `packages/pb/internal/pn/info.go`**

```go
// Package pn is a read-only client for `pn workspace info --json`. The schema is
// the stable consumed API pinned by phillipg-nix-repo-base ADR 0012:
// {wsid, root, terminal, repos:[{name, path, applied_ref, dirty}]}.
// applied_ref is always present and is "" (never null) when a repo has no
// applied-state record. pb never reads pn's files directly.
package pn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/pb/internal/run"
)

type Repo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	AppliedRef string `json:"applied_ref"`
	Dirty      bool   `json:"dirty"`
}

type Info struct {
	Wsid     string `json:"wsid"`
	Root     string `json:"root"`
	Terminal string `json:"terminal"`
	Repos    []Repo `json:"repos"`
}

func (i Info) RepoByName(name string) (Repo, bool) {
	for _, r := range i.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

type Client struct {
	R run.Runner
}

// envelope is the bd/pn JSON envelope wrapper.
type envelope struct {
	Data json.RawMessage `json:"data"`
}

// Info runs `pn workspace info --json` with cwd=dir and unmarshals the result.
func (c Client) Info(ctx context.Context, dir string) (Info, error) {
	res, err := c.R.Run(ctx, "pn", []string{"workspace", "info", "--json"}, run.Options{Dir: dir})
	if err != nil {
		return Info{}, fmt.Errorf("pn workspace info (is %q a pn workspace?): %w", dir, err)
	}
	var info Info
	// Tolerate both the enveloped form ({"data":{…}}) and a bare object, since
	// pn's --json envelope behaviour may evolve; prefer data when present.
	var env envelope
	if e := json.Unmarshal([]byte(res.Stdout), &env); e == nil && len(env.Data) > 0 {
		if e2 := json.Unmarshal(env.Data, &info); e2 != nil {
			return Info{}, fmt.Errorf("parse pn info data: %w", e2)
		}
	} else if e := json.Unmarshal([]byte(res.Stdout), &info); e != nil {
		return Info{}, fmt.Errorf("parse pn info: %w", e)
	}
	if info.Root == "" {
		return Info{}, errors.New("pn workspace info returned empty root")
	}
	return info, nil
}
```

Implementer note (CONTRACT RISK): repo-base ADR 0012 shows the `pn workspace info --json` schema as a **bare** top-level object `{wsid, root, terminal, repos}` — it does **not** say `pn` wraps it in a `{data,…}` envelope. The code above tolerates both; the Task-10 contract test against real `pn` decides which is emitted. Adjust the test's `sampleInfoJSON` to match whichever real `pn` produces and keep the tolerant parse.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/pn/ -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/pn
git commit -m "feat(pb): pn workspace info --json client (ADR 0012 schema)"
```

---

## Task 5: `bd` client (`internal/bd`)

**Files:**

- Create: `packages/pb/internal/bd/bd.go`, `packages/pb/internal/bd/bd_test.go`

**Interfaces:**

- Consumes: `run.Runner`.
- Produces:
  - `type Gate struct { ID, AwaitType, AwaitID string; Metadata map[string]string }` (json: `id`,`await_type`,`await_id`,`metadata`)
  - `type Client struct { R run.Runner }`
  - `func (c Client) ListGates(ctx context.Context, dir string) ([]Gate, error)` — runs `bd -C <dir> gate list --limit 0 --json` with `BD_JSON_ENVELOPE=1`; parses `.data[]`.
  - `func (c Client) CreateGate(ctx context.Context, dir, blocks, awaitType, awaitID, reason string) (string, error)` — runs `bd -C <dir> gate create --type=<awaitType> --blocks <blocks> --await-id <awaitID> [--reason <reason>] --json`; returns `.data.id`.
  - `func (c Client) SetMetadata(ctx context.Context, dir, id, key, value string) error` — `bd -C <dir> update <id> --set-metadata <key>=<value>`.
  - `func (c Client) ResolveGate(ctx context.Context, dir, id, reason string) error` — `bd -C <dir> gate resolve <id> [--reason <reason>]`.
  - `func (c Client) HasBead(ctx context.Context, dir, id string) bool` — `bd -C <dir> show <id> --json` exit 0 (used by create to co-locate, critique #5).
  - `func (c Client) AddLabel(ctx context.Context, dir, id, label string) error` — `bd -C <dir> update <id> --add-label <label>` (convert-to-human stale action).
  - `Gate.CreatedAt` (`json:"created_at"`, RFC3339) is included for stale-age (Task 9).
  - All methods set `BD_JSON_ENVELOPE=1` in the env (helper `bdEnv()` appends to `os.Environ()`).
  - Add unit tests for `HasBead` (scripted exit-0 → true; unscripted/err → false) and `AddLabel` (args `[-C dir update id --add-label human]`) mirroring the existing `bd_test.go` style.
- Consumed by: `internal/gate` (Tasks 8–9). Surface verified against bd 1.0.4.

- [ ] **Step 1: Write the failing test** `packages/pb/internal/bd/bd_test.go`

```go
package bd

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

const gateListJSON = `{
  "data": [
    {"id":"x-1","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:abc123","metadata":{"applied_baseline":"base1"}},
    {"id":"x-2","issue_type":"gate","await_type":"timer","await_id":""}
  ],
  "schema_version": 1
}`

func TestListGates_parsesEnvelopeAndMetadata(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: gateListJSON}, nil)
	gates, err := Client{R: f}.ListGates(context.Background(), "/db")
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("len = %d", len(gates))
	}
	if gates[0].AwaitType != "pn:applied" || gates[0].AwaitID != "home:repo-a:abc123" {
		t.Errorf("gate0 = %+v", gates[0])
	}
	if gates[0].Metadata["applied_baseline"] != "base1" {
		t.Errorf("baseline = %q", gates[0].Metadata["applied_baseline"])
	}
	// BD_JSON_ENVELOPE=1 must be set on the call.
	call := f.Calls()[0]
	if !envHas(call.Opts.Env, "BD_JSON_ENVELOPE=1") {
		t.Errorf("BD_JSON_ENVELOPE=1 not set; env=%v", call.Opts.Env)
	}
}

func TestCreateGate_returnsID(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd",
		[]string{"-C", "/db", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
			"--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json"},
		run.Result{Stdout: `{"data":{"id":"g-9"},"schema_version":1}`}, nil)
	id, err := Client{R: f}.CreateGate(context.Background(), "/db", "b-1", "pn:applied", "home:repo-a:abc123", "pn:applied gate")
	if err != nil {
		t.Fatalf("CreateGate: %v", err)
	}
	if id != "g-9" {
		t.Errorf("id = %q, want g-9", id)
	}
}

func TestSetMetadata_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "g-9", "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)
	if err := (Client{R: f}).SetMetadata(context.Background(), "/db", "g-9", "applied_baseline", "base1"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
}

func TestResolveGate_buildsArgs(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "gate", "resolve", "g-9", "--reason", "applied"},
		run.Result{}, nil)
	if err := (Client{R: f}).ResolveGate(context.Background(), "/db", "g-9", "applied"); err != nil {
		t.Fatalf("ResolveGate: %v", err)
	}
}

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

var _ = strings.TrimSpace
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/bd/ -v`
Expected: FAIL — undefined `Client`, `ListGates`, etc.

- [ ] **Step 3: Write `packages/pb/internal/bd/bd.go`**

```go
// Package bd is a thin client over the `bd` CLI for pn:applied gates. It always
// sets BD_JSON_ENVELOPE=1 (pb pins the envelope rather than relying on the
// ambient default, which flips in bd v2.0) and parses the {data, schema_version}
// envelope. DB targeting is via `bd -C <dir>` so gates resolve in their own DB.
package bd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/run"
)

type Gate struct {
	ID        string            `json:"id"`
	IssueType string            `json:"issue_type"`
	AwaitType string            `json:"await_type"`
	AwaitID   string            `json:"await_id"`
	CreatedAt string            `json:"created_at"` // RFC3339; used for stale-age (Task 9)
	Metadata  map[string]string `json:"metadata"`
}

type Client struct {
	R run.Runner
}

func bdEnv() []string {
	return append(os.Environ(), "BD_JSON_ENVELOPE=1")
}

// HasBead reports whether a bead with id exists in the DB at dir (used by gate
// create to co-locate the gate in the bead's OWN DB — critique fix #5).
func (c Client) HasBead(ctx context.Context, dir, id string) bool {
	_, err := c.R.Run(ctx, "bd", []string{"-C", dir, "show", id, "--json"}, run.Options{Env: bdEnv()})
	return err == nil
}

// AddLabel adds a label to issue id (convert-to-human stale action: label "human"
// → surfaces in `bd human list`). VERIFIED: `bd update <id> --add-label <label>`.
func (c Client) AddLabel(ctx context.Context, dir, id, label string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--add-label", label},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --add-label: %w", err)
	}
	return nil
}

type listEnvelope struct {
	Data []Gate `json:"data"`
}

type createEnvelope struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListGates returns all open gates in the DB at dir.
func (c Client) ListGates(ctx context.Context, dir string) ([]Gate, error) {
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "gate", "list", "--limit", "0", "--json"},
		run.Options{Env: bdEnv()})
	if err != nil {
		return nil, fmt.Errorf("bd gate list in %q: %w", dir, err)
	}
	var env listEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return nil, fmt.Errorf("parse gate list json: %w", err)
	}
	return env.Data, nil
}

// CreateGate creates a gate of awaitType blocking `blocks`, returning the gate id.
func (c Client) CreateGate(ctx context.Context, dir, blocks, awaitType, awaitID, reason string) (string, error) {
	args := []string{"-C", dir, "gate", "create", "--type=" + awaitType, "--blocks", blocks, "--await-id", awaitID}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, "--json")
	res, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return "", fmt.Errorf("bd gate create: %w", err)
	}
	var env createEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return "", fmt.Errorf("parse gate create json: %w", err)
	}
	if env.Data.ID == "" {
		return "", fmt.Errorf("bd gate create returned no id: %s", res.Stdout)
	}
	return env.Data.ID, nil
}

// SetMetadata sets metadata.<key>=<value> on issue id.
func (c Client) SetMetadata(ctx context.Context, dir, id, key, value string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--set-metadata", key + "=" + value},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --set-metadata: %w", err)
	}
	return nil
}

// ResolveGate closes (resolves) gate id.
func (c Client) ResolveGate(ctx context.Context, dir, id, reason string) error {
	args := []string{"-C", dir, "gate", "resolve", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd gate resolve %s: %w", id, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/bd/ -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/bd
git commit -m "feat(pb): bd client (gate list/create/resolve, set-metadata, BD_JSON_ENVELOPE=1)"
```

---

## Task 6: `.beads` discovery + Dolt-identity dedupe (`internal/discover`)

**Files:**

- Create: `packages/pb/internal/discover/discover.go`, `packages/pb/internal/discover/discover_test.go`

**Interfaces:**

- Produces:
  - `type DB struct { Dir string; Identity string }` — `Dir` is the directory to pass to `bd -C`; `Identity` is the dedupe key.
  - `func FindBeadsDir(start, root string) (string, bool)` — walks up from `start` looking for a `.beads` directory, **bounded at `root`** (never ascends above `root`). Returns the directory **containing** `.beads` (i.e. the dir to pass to `bd -C`), or `false`.
  - `func DoltIdentity(beadsDir string) (string, error)` — reads `<beadsDir>/metadata.json` (`dolt_server_host`,`dolt_database`,`project_id`) + sibling `<beadsDir>/dolt-server.port`; returns `"<host>:<port>|<database>|<project_id>"`. A missing port file ⇒ host-only (embedded mode has no port); a missing/garbage metadata.json ⇒ error.
  - `func DistinctDBs(paths []string, root string) ([]DB, error)` — for `root` and each path: `FindBeadsDir(path, root)`; skip if none; compute `DoltIdentity`; dedupe by identity (first dir wins). Returns the distinct DBs in deterministic order.
- Consumed by: `internal/gate` check (Task 9). Dedupe key pinned by Task-10 contract test.

- [ ] **Step 1: Write the failing test** `packages/pb/internal/discover/discover_test.go`

```go
package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBeads(t *testing.T, dir, host, port, database, projectID string) {
	t.Helper()
	bd := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"dolt_server_host":"` + host + `","dolt_database":"` + database + `","project_id":"` + projectID + `"}`
	if err := os.WriteFile(filepath.Join(bd, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if port != "" {
		if err := os.WriteFile(filepath.Join(bd, "dolt-server.port"), []byte(port+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindBeadsDir_walksUpButStopsAtRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo-a", "sub")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBeads(t, root, "127.0.0.1", "25252", "pg2", "proj-1") // .beads at root only
	got, ok := FindBeadsDir(repo, root)
	if !ok || got != root {
		t.Fatalf("FindBeadsDir = %q, %v; want %q, true", got, ok, root)
	}
}

func TestFindBeadsDir_notFoundAtOrBelowRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo-a")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// .beads exists ABOVE root — must NOT be discovered.
	parent := filepath.Dir(root)
	writeBeads(t, parent, "127.0.0.1", "25252", "pg2", "proj-1")
	defer os.RemoveAll(filepath.Join(parent, ".beads"))
	if _, ok := FindBeadsDir(repo, root); ok {
		t.Fatal("must not ascend above root")
	}
}

func TestDistinctDBs_dedupesByIdentityNotPath(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "repo-a")
	b := filepath.Join(root, "repo-b")
	c := filepath.Join(root, "repo-c")
	for _, d := range []string{a, b, c} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// a and b share host:port|db|project (same project, different .beads dirs);
	// c is a genuinely distinct project.
	writeBeads(t, a, "127.0.0.1", "25252", "pg2", "proj-1")
	writeBeads(t, b, "127.0.0.1", "25252", "pg2", "proj-1")
	writeBeads(t, c, "127.0.0.1", "25252", "pg2", "proj-2")
	dbs, err := DistinctDBs([]string{a, b, c}, root)
	if err != nil {
		t.Fatalf("DistinctDBs: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("distinct = %d, want 2: %+v", len(dbs), dbs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/discover/ -v`
Expected: FAIL — undefined `FindBeadsDir`, `DistinctDBs`.

- [ ] **Step 3: Write `packages/pb/internal/discover/discover.go`**

```go
// Package discover finds the distinct beads DBs reachable from a pn workspace.
// It walks up each repo path (and the root) for a .beads dir, BOUNDED at the
// workspace root (never above — else it could resolve a foreign .beads and, via a
// matching wsid slug, cross-resolve), and dedupes by resolved Dolt identity
// (host:port|database|project_id) — NOT the .beads path or issue prefix, which
// differ per repo even when they map to one shared project.
package discover

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DB struct {
	Dir      string // directory to pass to `bd -C`
	Identity string // dedupe key
}

// FindBeadsDir walks up from start looking for a `.beads` directory, never
// ascending above root. Returns the directory CONTAINING .beads.
func FindBeadsDir(start, root string) (string, bool) {
	cur, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	for {
		if fi, err := os.Stat(filepath.Join(cur, ".beads")); err == nil && fi.IsDir() {
			return cur, true
		}
		if cur == rootAbs {
			return "", false // reached the bound without finding one
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false // filesystem root
		}
		// Stop if the parent would escape the root subtree.
		if !withinRoot(parent, rootAbs) {
			return "", false
		}
		cur = parent
	}
}

func withinRoot(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

type metadata struct {
	Host      string `json:"dolt_server_host"`
	Database  string `json:"dolt_database"`
	ProjectID string `json:"project_id"`
}

// DoltIdentity reads beadsDir/.beads metadata + port and returns the dedupe key.
func DoltIdentity(dir string) (string, error) {
	beads := filepath.Join(dir, ".beads")
	raw, err := os.ReadFile(filepath.Join(beads, "metadata.json"))
	if err != nil {
		return "", fmt.Errorf("read .beads/metadata.json in %q: %w", dir, err)
	}
	var m metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("parse .beads/metadata.json in %q: %w", dir, err)
	}
	port := ""
	if pb, err := os.ReadFile(filepath.Join(beads, "dolt-server.port")); err == nil {
		port = strings.TrimSpace(string(pb))
	}
	return fmt.Sprintf("%s:%s|%s|%s", m.Host, port, m.Database, m.ProjectID), nil
}

// DistinctDBs resolves the distinct beads DBs for root + paths, deduping by Dolt
// identity. Paths with no .beads at/below root are skipped.
func DistinctDBs(paths []string, root string) ([]DB, error) {
	seen := map[string]bool{}
	var out []DB
	for _, p := range append([]string{root}, paths...) {
		dir, ok := FindBeadsDir(p, root)
		if !ok {
			continue
		}
		id, err := DoltIdentity(dir)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, DB{Dir: dir, Identity: id})
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/discover/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/discover
git commit -m "feat(pb): .beads walk-up discovery (bounded at root) + Dolt-identity dedupe"
```

---

## Task 7: `git patch-id` compute + scan + ancestry (`internal/patchid`)

**Files:**

- Create: `packages/pb/internal/patchid/patchid.go`, `packages/pb/internal/patchid/patchid_test.go`

**Interfaces:**

- Consumes: `run.Runner`.
- Produces:
  - `type Client struct { R run.Runner }`
  - `func (c Client) Compute(ctx context.Context, repoPath, commitish string) (string, error)` — `git -C <repoPath> show <commitish>` piped to `git -C <repoPath> patch-id --stable`; returns the patch-id (first field of the first output line). (Implemented as two runner calls: capture `show` stdout, feed as stdin to `patch-id --stable`.)
  - `func (c Client) IsAncestor(ctx context.Context, repoPath, ancestor, descendant string) bool` — `git -C <repoPath> merge-base --is-ancestor <ancestor> <descendant>` (exit 0 ⇒ true).
  - `func (c Client) ScanPatchIDs(ctx context.Context, repoPath, revRange string, lastN int) (map[string]bool, error)` — runs `git -C <repoPath> log -p --no-merges <range>` (range = `revRange` if non-empty else `-n <lastN>` form is built by the caller via revRange) piped to `git patch-id --stable`; returns the SET of patch-ids found.
- Consumed by: `internal/gate` (Tasks 8–9). Behaviour (rebase-stable, within-context MISS, squash LOSS, binary works) pinned by Task-10 contract test. These tests use REAL git (`nativeCheckInputs = [ git ]`).

- [ ] **Step 1: Write the failing test** `packages/pb/internal/patchid/patchid_test.go` (real git in a temp repo)

```go
package patchid

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir) // keep git config writes inside temp
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "commit.gpgsign", "false")
	return dir
}

func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", file}, {"commit", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

func TestComputeAndScan_findsCommitPatchID(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "add a")
	commit(t, dir, "b.txt", "world\n", "add b")
	c := Client{R: run.CLIRunner{}}
	id, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil || id == "" {
		t.Fatalf("Compute: id=%q err=%v", id, err)
	}
	set, err := c.ScanPatchIDs(context.Background(), dir, "-n 10 HEAD", 0)
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if !set[id] {
		t.Errorf("scan did not find HEAD patch-id %q in %v", id, set)
	}
}

func TestIsAncestor(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "1\n", "c1")
	c := Client{R: run.CLIRunner{}}
	first := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
	commit(t, dir, "a.txt", "2\n", "c2")
	if !c.IsAncestor(context.Background(), dir, first, "HEAD") {
		t.Error("first commit should be ancestor of HEAD")
	}
	if c.IsAncestor(context.Background(), dir, "HEAD", first) {
		t.Error("HEAD should not be ancestor of first commit")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/patchid/ -v`
Expected: FAIL — undefined `Client`, `Compute`, `ScanPatchIDs`, `IsAncestor`.

- [ ] **Step 3: Write `packages/pb/internal/patchid/patchid.go`**

```go
// Package patchid computes and scans git patch-ids (--stable), which survive the
// local rebases this workflow uses (commit SHAs change, the diff does not).
// See the design spec "Key facts" and repo-base PoC for the verified behaviour
// (rebase-stable; within-~3-line-context rebase MISSES; squash LOSES; binary works).
package patchid

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pb/internal/run"
)

type Client struct {
	R run.Runner
}

// firstField returns the patch-id (first whitespace-delimited token) of a
// `git patch-id` output line "<patchid> <sha>".
func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// Compute returns the patch-id of commitish in the repo at repoPath:
//   git -C repoPath show <commitish> | git -C repoPath patch-id --stable
func (c Client) Compute(ctx context.Context, repoPath, commitish string) (string, error) {
	show, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "show", commitish}, run.Options{})
	if err != nil {
		return "", fmt.Errorf("git show %s: %w", commitish, err)
	}
	res, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "patch-id", "--stable"},
		run.Options{Stdin: show.Stdout})
	if err != nil {
		return "", fmt.Errorf("git patch-id: %w", err)
	}
	id := firstField(strings.SplitN(strings.TrimSpace(res.Stdout), "\n", 2)[0])
	if id == "" {
		return "", fmt.Errorf("git patch-id produced no id for %s", commitish)
	}
	return id, nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant.
func (c Client) IsAncestor(ctx context.Context, repoPath, ancestor, descendant string) bool {
	_, err := c.R.Run(ctx, "git",
		[]string{"-C", repoPath, "merge-base", "--is-ancestor", ancestor, descendant}, run.Options{})
	return err == nil
}

// ScanPatchIDs returns the set of patch-ids in the given log range:
//   git -C repoPath log -p --no-merges <revRange...> | git patch-id --stable
// revRange is split on spaces into git args (e.g. "base..tip" or "-n 100 tip").
func (c Client) ScanPatchIDs(ctx context.Context, repoPath, revRange string, _ int) (map[string]bool, error) {
	args := []string{"-C", repoPath, "log", "-p", "--no-merges"}
	args = append(args, strings.Fields(revRange)...)
	logRes, err := c.R.Run(ctx, "git", args, run.Options{})
	if err != nil {
		return nil, fmt.Errorf("git log -p %s: %w", revRange, err)
	}
	if strings.TrimSpace(logRes.Stdout) == "" {
		return map[string]bool{}, nil
	}
	idRes, err := c.R.Run(ctx, "git", []string{"-C", repoPath, "patch-id", "--stable"},
		run.Options{Stdin: logRes.Stdout})
	if err != nil {
		return nil, fmt.Errorf("git patch-id (scan): %w", err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(idRes.Stdout, "\n") {
		if id := firstField(line); id != "" {
			set[id] = true
		}
	}
	return set, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/patchid/ -v`
Expected: PASS (2 tests). (If `git` is absent the tests `t.Skip`; in the nix sandbox `git` is a `nativeCheckInput` so they run.)

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/patchid
git commit -m "feat(pb): git patch-id compute/scan + merge-base ancestry"
```

---

## Task 8: `pb gate create` orchestration + CLI (`internal/gate/create.go`, `cmd/pb/gate_create.go`)

**Files:**

- Create: `packages/pb/internal/gate/create.go`, `packages/pb/internal/gate/create_test.go`, `packages/pb/cmd/pb/gate_create.go`

**Interfaces:**

- Consumes: `pn.Client`, `bd.Client`, `patchid.Client`, `run.Runner`.
- Produces:
  - `type CreateDeps struct { PN pn.Client; BD bd.Client; PatchID patchid.Client }`
  - `type CreateParams struct { WorkspaceDir, BeadID, Repo, Commit string; Commits []string; Reason string }`
  - `type CreatedGate struct { GateID, AwaitID, Repo, PatchID, AppliedBaseline string }`
  - `type CreateResult struct { Gates []CreatedGate }`
  - `func Create(ctx context.Context, d CreateDeps, p CreateParams) (CreateResult, error)`
  - JSON output struct mirrors the spec: `{ "gates": [{ "gate-id", "await_id", "repo", "patch-id", "applied_baseline" }] }`.
- Behaviour (per D1 + spec Component 2): resolve workspace via `pn.Info(WorkspaceDir)`; **validate `Repo`** (unknown ⇒ error, never guess); resolve the bead's DB dir = `FindBeadsDir(<repo path or root>)`… **actually co-locate in `BeadID`'s DB**: the gate MUST be created with `bd -C <dir>` where `<dir>` resolves the same DB as `BeadID`. Since all repos in the workspace share `pg2` today, use the workspace `root` as the `-C` dir (its `.beads` resolves the shared project). For correctness in a genuinely multi-DB future, the gate's `-C` dir is the bead's own repo dir if known; default to `root`. Compute patch-id(s) of the commit(s) in the repo's `path`; for each, create a `pn:applied` gate `await_id="<wsid>:<repo>:<patchid>"` and write `metadata.applied_baseline=<repo.AppliedRef>` (may be ""). Does NOT create or un-defer `BeadID` (D1).
- `--commit` defaults to `HEAD` when neither `--commit` nor `--commits` given. `--commits <range>` → one gate per commit (via `git rev-list <range>`).

- [ ] **Step 1: Write the failing test** `packages/pb/internal/gate/create_test.go`

```go
package gate

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

func TestCreate_singleCommitDefaultHEAD(t *testing.T) {
	f := run.NewFakeRunner()
	// pn workspace info
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: `{
		"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"base1","dirty":false}]}`}, nil)
	// git show HEAD | patch-id --stable
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "HEAD"}, run.Result{Stdout: "diff..."}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 deadsha\n"}, nil)
	// bd gate create (co-located at root /ws)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "b-1",
		"--await-id", "home:repo-a:abc123", "--reason", "pn:applied gate", "--json"},
		run.Result{Stdout: `{"data":{"id":"g-1"}}`}, nil)
	// bd update --set-metadata applied_baseline=base1
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-1", "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)

	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}}
	out, err := Create(context.Background(), d, CreateParams{
		WorkspaceDir: "/ws", BeadID: "b-1", Repo: "repo-a", Reason: "pn:applied gate"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(out.Gates) != 1 {
		t.Fatalf("gates = %d", len(out.Gates))
	}
	g := out.Gates[0]
	if g.GateID != "g-1" || g.AwaitID != "home:repo-a:abc123" || g.PatchID != "abc123" || g.AppliedBaseline != "base1" {
		t.Errorf("gate = %+v", g)
	}
}

func TestCreate_unknownRepoErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: `{
		"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"base1","dirty":false}]}`}, nil)
	d := CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}}
	_, err := Create(context.Background(), d, CreateParams{WorkspaceDir: "/ws", BeadID: "b-1", Repo: "nope"})
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/gate/ -run TestCreate -v`
Expected: FAIL — undefined `Create`, `CreateDeps`, etc.

- [ ] **Step 3: Write `packages/pb/internal/gate/create.go`**

```go
// Package gate orchestrates pn:applied gate create and check.
package gate

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

type CreateDeps struct {
	PN       pn.Client
	BD       bd.Client
	PatchID  patchid.Client
	R        run.Runner                                                  // for git rev-list on --commits ranges
	Discover func(paths []string, root string) ([]discover.DB, error)    // nil → discover.DistinctDBs (critique #5)
}

type CreateParams struct {
	WorkspaceDir string
	BeadID       string
	Repo         string
	Commit       string   // single commit-ish; defaults to HEAD
	Commits      string   // optional rev range → one gate per commit
	Reason       string
}

type CreatedGate struct {
	GateID          string `json:"gate-id"`
	AwaitID         string `json:"await_id"`
	Repo            string `json:"repo"`
	PatchID         string `json:"patch-id"`
	AppliedBaseline string `json:"applied_baseline"`
}

type CreateResult struct {
	Gates []CreatedGate `json:"gates"`
}

func Create(ctx context.Context, d CreateDeps, p CreateParams) (CreateResult, error) {
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return CreateResult{}, err
	}
	repo, ok := info.RepoByName(p.Repo)
	if !ok {
		return CreateResult{}, fmt.Errorf("repo %q is not in workspace %q", p.Repo, info.Root)
	}

	// Resolve which commits to gate.
	var commitish []string
	switch {
	case p.Commits != "":
		out, err := d.R.Run(ctx, "git", []string{"-C", repo.Path, "rev-list", "--no-merges", "--reverse", p.Commits}, run.Options{})
		if err != nil {
			return CreateResult{}, fmt.Errorf("git rev-list %s: %w", p.Commits, err)
		}
		for _, l := range strings.Fields(out.Stdout) {
			commitish = append(commitish, l)
		}
		if len(commitish) == 0 {
			return CreateResult{}, fmt.Errorf("rev range %q matched no commits", p.Commits)
		}
	default:
		c := p.Commit
		if c == "" {
			c = "HEAD"
		}
		commitish = []string{c}
	}

	// Co-locate the gate in the BEAD's OWN DB (spec Component 2; critique fix #5):
	// a cross-DB blocks edge silently fails to hold. Discover the workspace's
	// distinct DBs and find the one containing BeadID. With today's single shared
	// pg2 project this resolves to one DB; it stays correct if DBs ever diverge.
	dbDir, err := resolveBeadDB(ctx, d, info, p.BeadID)
	if err != nil {
		return CreateResult{}, err
	}

	var result CreateResult
	for _, cish := range commitish {
		pid, err := d.PatchID.Compute(ctx, repo.Path, cish)
		if err != nil {
			return result, err
		}
		awaitID := fmt.Sprintf("%s:%s:%s", info.Wsid, p.Repo, pid)
		gid, err := d.BD.CreateGate(ctx, dbDir, p.BeadID, "pn:applied", awaitID, p.Reason)
		if err != nil {
			return result, err
		}
		if err := d.BD.SetMetadata(ctx, dbDir, gid, "applied_baseline", repo.AppliedRef); err != nil {
			return result, fmt.Errorf("gate %s created but baseline write failed: %w", gid, err)
		}
		result.Gates = append(result.Gates, CreatedGate{
			GateID: gid, AwaitID: awaitID, Repo: p.Repo, PatchID: pid, AppliedBaseline: repo.AppliedRef,
		})
	}
	return result, nil
}

// resolveBeadDB finds the distinct workspace DB that contains beadID, so the
// gate is co-located with the bead it blocks (critique fix #5).
func resolveBeadDB(ctx context.Context, d CreateDeps, info pn.Info, beadID string) (string, error) {
	paths := make([]string, 0, len(info.Repos))
	for _, r := range info.Repos {
		paths = append(paths, r.Path)
	}
	disc := d.Discover
	if disc == nil {
		disc = discover.DistinctDBs
	}
	dbs, err := disc(paths, info.Root)
	if err != nil {
		return "", err
	}
	for _, db := range dbs {
		if d.BD.HasBead(ctx, db.Dir, beadID) {
			return db.Dir, nil
		}
	}
	return "", fmt.Errorf("bead %q not found in any beads DB under workspace %q", beadID, info.Root)
}
```

(Implementer note: Task-8 tests must now inject `Discover: func(_ []string, _ string) ([]discover.DB, error){ return []discover.DB{{Dir: "/ws"}}, nil }` into `CreateDeps` AND script a `HasBead` response — `bd -C /ws show b-1 --json` → `run.Result{Stdout: "{}"}` (exit 0) — before the `gate create` response. The `dbDir` is then `/ws`, so the existing `bd -C /ws gate create …` fixtures still match.)

(Implementer note: `bd.CreateGate` is called with `reason` defaulting to `"pn:applied gate"` when `p.Reason == ""` — set that default in the CLI layer (Step 5) so the FakeRunner args match. Adjust the test if you choose to default inside `Create` instead; keep test and impl consistent.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/gate/ -run TestCreate -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Write the CLI wiring `packages/pb/cmd/pb/gate_create.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func newGateCreateCmd() *cobra.Command {
	var (
		blocks  string
		repo    string
		commit  string
		commits string
		reason  string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create pn:applied gate(s) blocking a bead until a change is applied",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if blocks == "" {
				return fmt.Errorf("--blocks <beadid> is required")
			}
			if repo == "" {
				return fmt.Errorf("--repo <repo> is required")
			}
			if reason == "" {
				reason = "pn:applied gate"
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r := run.CLIRunner{}
			d := gate.CreateDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}, R: r}
			out, err := gate.Create(context.Background(), d, gate.CreateParams{
				WorkspaceDir: wd, BeadID: blocks, Repo: repo, Commit: commit, Commits: commits, Reason: reason,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			for _, g := range out.Gates {
				fmt.Fprintf(cmd.OutOrStdout(), "created gate %s (await_id=%s baseline=%q)\n", g.GateID, g.AwaitID, g.AppliedBaseline)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&blocks, "blocks", "", "bead id to block (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "workspace repo key (required)")
	cmd.Flags().StringVar(&commit, "commit", "", "commit-ish to gate (default HEAD)")
	cmd.Flags().StringVar(&commits, "commits", "", "commit range → one gate per commit (advanced)")
	cmd.Flags().StringVar(&reason, "reason", "", "gate reason")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}
```

- [ ] **Step 6: Register the subcommand.** Edit `packages/pb/cmd/pb/gate.go` to add `cmd.AddCommand(newGateCreateCmd())` (change `newGateCmd` to build the command, add the child, return it).

- [ ] **Step 7: Add the fleet-race integration test (real bd, skips when absent)** `packages/pb/internal/gate/create_fleetrace_test.go`

```go
package gate_test

// Fleet-race lifecycle test (D1): the recommended usage creates the bead
// DEFERRED, then pb gate create attaches the gate, then the caller un-defers.
// This asserts the bead is NEVER in `bd ready` from create through gate-attach,
// and only becomes ready after the gate resolves. Uses real bd (embedded Dolt in
// a temp dir); skips when bd is not on PATH (so it skips in the pure nix sandbox).
//
// Implementer: build this on the isolated-bd helper (mirror pg-pr's
// packages/pg-pr/pkg/beads/mergerequest_test.go newBDWorkspace): t.TempDir(),
// t.Setenv HOME + XDG_* to temp, scrub BEADS_DIR/WORKSPACE_ROOT, `bd init --prefix`.
// Steps:
//   1. bd create "verify" --defer +100y  → capture bead id; assert NOT in `bd ready`.
//   2. Drive gate.Create against a temp git repo (one commit) with a fake/real pn
//      Info (since pn need not be present, inject a pn.Client backed by a FakeRunner
//      scripted to return the temp workspace info, but bd via a real CLIRunner -C tmp).
//      NOTE: gate.Create takes ONE deps struct with ONE runner per client; to mix a
//      real bd with a faked pn, construct CreateDeps{PN: pn.Client{R: fakePN},
//      BD: bd.Client{R: run.CLIRunner{}}, PatchID: patchid.Client{R: run.CLIRunner{}}, R: run.CLIRunner{}}.
//   3. assert bead STILL not in `bd ready` (gate holds even though defer is +100y).
//   4. bd update <bead> --defer "" (un-defer); assert STILL not ready (gate holds).
//   5. bd gate resolve <gate>; assert bead now IS in `bd ready`.
```

This step's deliverable is the actual test file (replace the comment skeleton with real code following the inline recipe). Run: `cd packages/pb && go test ./internal/gate/ -run FleetRace -v` (PASS on a dev machine with bd; SKIP otherwise).

- [ ] **Step 8: Build + manual smoke**

```bash
nix build .#pb && "$PWD/result/bin/pb" gate create --help
```

Expected: help shows `--blocks`, `--repo`, `--commit`, `--commits`, `--reason`, `--json`.

- [ ] **Step 9: Commit**

```bash
git add packages/pb/internal/gate packages/pb/cmd/pb
git commit -m "feat(pb): pb gate create (HEAD default, validate repo, patch-id, co-locate, baseline; fleet-race test)"
```

---

## Task 9: `pb gate check` orchestration + CLI (`internal/gate/check.go`, `cmd/pb/gate_check.go`)

**Files:**

- Create: `packages/pb/internal/gate/check.go`, `packages/pb/internal/gate/check_test.go`, `packages/pb/cmd/pb/gate_check.go`

**Interfaces:**

- Consumes: `pn.Client`, `bd.Client`, `patchid.Client`, `discover`, `duration`.
- Produces:
  - `type CheckDeps struct { PN pn.Client; BD bd.Client; PatchID patchid.Client }`
  - `type CheckParams struct { WorkspaceDir string; DryRun, Strict bool; LastN int; StaleHandler string; StaleAfter time.Duration; Now time.Time }` (`Now` is the clock seam for stale tests; `StaleHandler` ∈ {"convert-to-human","close"}; `LastN` default 100)
  - `type Skip struct { GateID, Repo, Reason string }` (json `gate-id`,`repo`,`reason`)
  - `type StaleAction struct { GateID, Action string }` (json `gate-id`,`action`)
  - `type CheckResult struct { Resolved, WouldResolve []string; Skipped []Skip; StaleActions []StaleAction }` (json: `resolved`,`would_resolve`,`skipped`,`stale_actions`)
  - `func Check(ctx context.Context, d CheckDeps, p CheckParams) (CheckResult, error)` — exit-non-zero decision is the caller's (`len(Skipped) > 0`).
- Algorithm (spec Component 2 check):
  1. `info := PN.Info(WorkspaceDir)`.
  2. `dbs := discover.DistinctDBs(repoPaths, info.Root)`.
  3. For each DB: `gates := BD.ListGates(dir)`; keep `await_type=="pn:applied"` and `wsid(await_id)==info.Wsid`.
  4. Parse `await_id` → `(wsid, repo, patchid)` via `SplitN(":",3)`. Repo unknown to `info` → `Skipped{reason:"unknown repo"}`.
  5. Group that DB's gates by repo; for each repo with a non-empty `applied_ref`, choose the scan range: if a gate baseline is set AND `PatchID.IsAncestor(baseline, applied_ref)` → `baseline..applied_ref`; else → `-n <LastN> applied_ref`. (Per gate, since baselines differ; compute the union scan per distinct range.) Empty `applied_ref` ⇒ leave the repo's gates blocked (NOT a skip).
  6. If `dirty` and `Strict` → skip the repo's gates (`reason:"dirty (--strict)"`); else scan committed history regardless (lenient default).
  7. `set := PatchID.ScanPatchIDs(path, range)`; for each gate whose `patchid ∈ set`: if `!DryRun` → `BD.ResolveGate(dir, gateID)`, append to `Resolved`; if `DryRun` → append to `WouldResolve`.
  8. **Stale handling:** for each still-open `pn:applied` gate whose age (`Now - created_at`) > `StaleAfter`: if `!DryRun` apply `StaleHandler` (`convert-to-human` = relabel/convert; `close` = `bd close`), append to `StaleActions`; if `DryRun` append the would-action (no mutation). (Gate `created_at` comes from `bd gate list`; add it to `bd.Gate`.)
- `--dry-run` mutates NOTHING on either the resolve or stale path.

- [ ] **Step 1: Confirm prerequisites from Task 5.** `bd.Gate.CreatedAt` and `bd.Client.AddLabel`/`HasBead` were added in Task 5 (critique fix #8). No bd-client change is needed here; just `cd packages/pb && go test ./internal/bd/` to confirm green before starting.

- [ ] **Step 2: Write the failing test** `packages/pb/internal/gate/check_test.go`

```go
package gate

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// helper: a workspace info with one repo applied at "tip", .beads at root.
func infoJSON() string {
	return `{"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false}]}`
}

// stubDiscoverWS injects the Discover seam (critique fix #4): bypass the
// FS-walking discover.DistinctDBs and return a single DB at /ws.
func stubDiscoverWS(_ []string, _ string) ([]discover.DB, error) {
	return []discover.DB{{Dir: "/ws", Identity: "id-ws"}}, nil
}

func TestCheck_resolvesWhenPatchIDInHistory(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: infoJSON()}, nil)
	// discover reads files, not the runner — the test must lay down /ws/.beads on disk
	// OR (preferred) the test injects DistinctDBs via a seam. SIMPLER: make Check take
	// a discover function field so tests bypass the filesystem (see impl note).
	// gate list at /ws
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	// baseline ancestry check: base1 is ancestor of tip
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	// scan base1..tip
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	// resolve
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)

	d := CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: stubDiscoverWS}
	out, err := Check(context.Background(), d, CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
}

func TestCheck_dryRunMutatesNothing(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: infoJSON()}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	d := CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: stubDiscoverWS}
	out, err := Check(context.Background(), d, CheckParams{
		WorkspaceDir: "/ws", LastN: 100, DryRun: true, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.WouldResolve) != 1 || len(out.Resolved) != 0 {
		t.Fatalf("dry-run: resolved=%v would=%v", out.Resolved, out.WouldResolve)
	}
	// assert NO `gate resolve` call was issued
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 4 && c.Args[2] == "gate" && c.Args[3] == "resolve" {
			t.Fatal("dry-run issued a gate resolve")
		}
	}
}

func TestCheck_unknownRepoSkipsAndReports(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: infoJSON()}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-2","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:ghost:zzz","created_at":"2026-06-26T00:00:00Z"}]}`}, nil)
	d := CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: stubDiscoverWS}
	out, err := Check(context.Background(), d, CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Repo != "ghost" {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
}

func TestCheck_staleBoundary(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: infoJSON()}, nil)
	// One old gate (created 2d ago) + patch-id NOT in history → not resolved → stale eligible.
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-old","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:nope","created_at":"2026-06-24T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	// stale → convert-to-human (implementer: confirm the exact bd subcommand; placeholder shown)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-old", "--add-label", "human"}, run.Result{}, nil)
	d := CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: stubDiscoverWS}
	out, err := Check(context.Background(), d, CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 24 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.StaleActions) != 1 || out.StaleActions[0].GateID != "g-old" {
		t.Fatalf("stale = %+v", out.StaleActions)
	}
}
```

**Impl note (test seam — RESOLVED, critique fix #4):** the FS-walking `discover.DistinctDBs` is bypassed via a `Discover func(paths []string, root string) ([]discover.DB, error)` field on `CheckDeps` (defaults to `discover.DistinctDBs` when nil). Tests inject `stubDiscoverWS` (shown above). Do NOT use a `dbsOverride` field on `CheckParams` (it was removed — the earlier draft mixed the two; the tests above are the corrected form).

- [ ] **Step 3: Run test to verify it fails**

Run: `cd packages/pb && go test ./internal/gate/ -run TestCheck -v`
Expected: FAIL — undefined `Check`, `CheckDeps`, etc.

- [ ] **Step 4: Write `packages/pb/internal/gate/check.go`** (implement the algorithm above; key pieces shown)

```go
package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
)

type CheckDeps struct {
	PN       pn.Client
	BD       bd.Client
	PatchID  patchid.Client
	Discover func(paths []string, root string) ([]discover.DB, error) // nil → discover.DistinctDBs
}

type CheckParams struct {
	WorkspaceDir string
	DryRun       bool
	Strict       bool
	LastN        int
	StaleHandler string
	StaleAfter   time.Duration
	Now          time.Time
}

type Skip struct {
	GateID string `json:"gate-id"`
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

type StaleAction struct {
	GateID string `json:"gate-id"`
	Action string `json:"action"`
}

type CheckResult struct {
	Resolved     []string      `json:"resolved"`
	WouldResolve []string      `json:"would_resolve,omitempty"`
	Skipped      []Skip        `json:"skipped"`
	StaleActions []StaleAction `json:"stale_actions"`
}

func parseAwaitID(s string) (wsid, repo, patchID string, ok bool) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func Check(ctx context.Context, d CheckDeps, p CheckParams) (CheckResult, error) {
	if p.LastN == 0 {
		p.LastN = 100
	}
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return CheckResult{}, err
	}
	disc := d.Discover
	if disc == nil {
		disc = discover.DistinctDBs
	}
	paths := make([]string, 0, len(info.Repos))
	for _, r := range info.Repos {
		paths = append(paths, r.Path)
	}
	dbs, err := disc(paths, info.Root)
	if err != nil {
		return CheckResult{}, err
	}

	var result CheckResult
	for _, db := range dbs {
		gates, err := d.BD.ListGates(ctx, db.Dir)
		if err != nil {
			return result, err
		}
		for _, g := range gates {
			if g.AwaitType != "pn:applied" {
				continue
			}
			wsid, repoName, patchID, ok := parseAwaitID(g.AwaitID)
			if !ok || wsid != info.Wsid {
				continue // not ours / malformed → leave alone
			}
			repo, known := info.RepoByName(repoName)
			if !known {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "unknown repo"})
				continue
			}
			// stale check first record (may also resolve below). Resolve takes
			// precedence: an old gate whose change IS now applied resolves below
			// (we only stale-handle on the not-found/never-applied branches).
			stale := false
			if p.StaleAfter > 0 { // critique fix #3: time.Duration has no .Zero()
				if ts, err := time.Parse(time.RFC3339, g.CreatedAt); err == nil {
					if p.Now.Sub(ts) > p.StaleAfter {
						stale = true
					}
				}
			}
			if repo.AppliedRef == "" {
				// never applied → leave blocked; only stale-handle if eligible
				if stale {
					d.applyStale(ctx, db.Dir, g.ID, p, &result)
				}
				continue
			}
			if repo.Dirty && p.Strict {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "dirty (--strict)"})
				continue
			}
			// choose scan range
			rng := fmt.Sprintf("-n %d %s", p.LastN, repo.AppliedRef)
			if base := g.Metadata["applied_baseline"]; base != "" && d.PatchID.IsAncestor(ctx, repo.Path, base, repo.AppliedRef) {
				rng = base + ".." + repo.AppliedRef
			}
			set, err := d.PatchID.ScanPatchIDs(ctx, repo.Path, rng, p.LastN)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "scan failed: " + err.Error()})
				continue
			}
			if set[patchID] {
				if p.DryRun {
					result.WouldResolve = append(result.WouldResolve, g.ID)
				} else if err := d.BD.ResolveGate(ctx, db.Dir, g.ID, ""); err != nil {
					result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "resolve failed: " + err.Error()})
				} else {
					result.Resolved = append(result.Resolved, g.ID)
				}
				continue
			}
			// not found → leave blocked; stale-handle if eligible
			if stale {
				d.applyStale(ctx, db.Dir, g.ID, p, &result)
			}
		}
	}
	return result, nil
}

// applyStale records (and unless DryRun, performs) the stale action.
func (d CheckDeps) applyStale(ctx context.Context, dir, gateID string, p CheckParams, result *CheckResult) {
	action := p.StaleHandler
	if action == "" {
		action = "convert-to-human"
	}
	result.StaleActions = append(result.StaleActions, StaleAction{GateID: gateID, Action: action})
	if p.DryRun {
		return
	}
	switch action {
	case "close":
		_ = d.BD.ResolveGate(ctx, dir, gateID, "stale: closed by pb gate check")
	default: // convert-to-human: add "human" label → surfaces in `bd human list`
		_ = d.BD.AddLabel(ctx, dir, gateID, "human")
	}
}
```

(Implementer notes: (1) convert-to-human uses the VERIFIED `bd.Client.AddLabel` (`bd -C <dir> update <id> --add-label human`) added in Task 5 — the stale test scripts `[-C /ws update g-old --add-label human]`. (2) Best-effort: `Check` returns nil error even with skips; the CLI sets exit code from `len(Skipped) > 0`. (3) `git log -p --no-merges -n 100 tip` arg order is valid.)

- [ ] **Step 5: Run test to verify it passes**

Run: `cd packages/pb && go test ./internal/gate/ -run TestCheck -v`
Expected: PASS (4 tests).

- [ ] **Step 6: Write the CLI wiring `packages/pb/cmd/pb/gate_check.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/duration"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func newGateCheckCmd() *cobra.Command {
	var (
		dryRun       bool
		strict       bool
		lastN        int
		staleHandler string
		staleAfter   string
		asJSON       bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Resolve pn:applied gates whose change has been applied (run inside a workspace)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dur, err := duration.ParseDuration(staleAfter)
			if err != nil {
				return fmt.Errorf("--stale-after: %w", err)
			}
			if staleHandler != "convert-to-human" && staleHandler != "close" {
				return fmt.Errorf("--stale-handler must be convert-to-human or close")
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			r := run.CLIRunner{}
			d := gate.CheckDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}}
			out, err := gate.Check(context.Background(), d, gate.CheckParams{
				WorkspaceDir: wd, DryRun: dryRun, Strict: strict, LastN: lastN,
				StaleHandler: staleHandler, StaleAfter: dur, Now: nowUTC(),
			})
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(out); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "resolved=%d would_resolve=%d skipped=%d stale=%d\n",
					len(out.Resolved), len(out.WouldResolve), len(out.Skipped), len(out.StaleActions))
			}
			if len(out.Skipped) > 0 {
				os.Exit(1) // best-effort: non-zero iff something was undeterminable
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would resolve/convert; change nothing")
	cmd.Flags().BoolVar(&strict, "strict", false, "skip dirty repos")
	cmd.Flags().IntVar(&lastN, "last-n", 100, "commits to scan when baseline is absent/diverged")
	cmd.Flags().StringVar(&staleHandler, "stale-handler", "convert-to-human", "convert-to-human|close")
	cmd.Flags().StringVar(&staleAfter, "stale-after", "3d", "gate age before stale-handling (ms..d, >=1ms)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}
```

Add a `nowUTC()` helper in `main.go` (`func nowUTC() time.Time { return time.Now().UTC() }`) — the only real-clock call, kept out of the unit-tested core (which takes `Now` as a param).

- [ ] **Step 7: Register the subcommand** in `cmd/pb/gate.go`: `cmd.AddCommand(newGateCheckCmd())`.

- [ ] **Step 8: Build + manual smoke**

```bash
nix build .#pb && "$PWD/result/bin/pb" gate check --help
"$PWD/result/bin/pb" gate check --stale-after=bogus 2>&1 || echo "rejected bad duration (expected)"
```

Expected: help lists all flags; bad `--stale-after` exits non-zero with an error.

- [ ] **Step 9: Commit**

```bash
git add packages/pb/internal/gate packages/pb/internal/bd packages/pb/cmd/pb
git commit -m "feat(pb): pb gate check (discover+dedupe, baseline-ancestry scan, dirty-lenient/strict, dry-run, stale, json, non-zero on skip)"
```

---

## Task 10: Contract tests (`//go:build contract`) + `pb-contract` flake check

**Files:**

- Create: `packages/pb/cmd/pb/contract_test.go` (`//go:build contract`)
- Modify: `flake.nix` (add a `pb-contract` `writeShellApplication`, mirroring `ccpool-contract`)

**Interfaces:**

- Pins the real external surfaces against drift (run on dev/CI, NOT the pure-nix sandbox): the `bd` gate surface, the multi-DB dedupe key, the `git patch-id` behaviour, and the `pn workspace info --json` schema.

- [ ] **Step 1: Write `packages/pb/cmd/pb/contract_test.go`** with `//go:build contract` and these tests (each `t.Skip`s if its binary is absent; use the isolated-bd + temp-git helpers, HOME/XDG pinned to temp):
  - `TestContract_BDGateSurface`: in an isolated `bd init`, `bd gate create --type=pn:applied --blocks <b> --await-id "w:r:p" --json` is accepted; `BD_JSON_ENVELOPE=1 bd gate list --limit 0 --json` returns the `{data,schema_version}` envelope carrying `await_type=="pn:applied"`/`await_id`; the blocked bead is absent from `bd ready`; `bd update <g> --set-metadata applied_baseline=X` round-trips in `gate list` metadata; `bd gate resolve <g>` → bead appears in `bd ready`. **Pin the bd version**: read `bd version`, and `t.Logf` it so a future bump is visible.
  - `TestContract_CrossDBBlockDoesNotHold`: two isolated DBs; a gate in DB-A blocking a bead in DB-B does NOT hold the bead out of `bd ready` (the co-location invariant) — so the gate MUST be co-located.
  - `TestContract_MultiDBDedupeKey`: two `.beads` dirs sharing `dolt_server_host`+port+`dolt_database`+`project_id` (differing only by prefix) → `discover.DistinctDBs` yields ONE; two with distinct `project_id` → TWO.
  - `TestContract_GitPatchID`: real git temp repo — patch-id is stable across a clean `git rebase`; found via the bounded `log -p | patch-id --stable` scan; a within-~3-line-context rebase **deterministically MISSES**; a squash **deterministically LOSES** the id; a binary change **does** yield a patch-id; `--stable` ≠ `--verbatim` output.
  - `TestContract_PNInfoSchema`: if `pn` is on PATH and a temp workspace can be stood up, `pn workspace info --json` parses into `pn.Info` with non-empty `root` and each `repo.path`; `applied_ref` is a string (possibly ""), `terminal` is a string. (Skip if standing up a real `pn` workspace is impractical here — the Phase-3 smoke harness covers the live path; `t.Skip` with a clear message.)

  (Write real Go for each; follow the `ccpool` contract style and the `mergerequest_test.go` isolated-bd helper. Keep each assertion deterministic.)

- [ ] **Step 2: Add the `pb-contract` check to `flake.nix`** (mirror `ccpool-contract`)

```nix
pb-contract = pkgs.writeShellApplication {
  name = "pb-contract";
  runtimeInputs = [
    pkgs.go
    pkgs.git
    (final.llm-agentsPkgs.beads or llm-agents.packages.${pkgs.stdenv.hostPlatform.system}.beads)
  ];
  text = ''
    cd "''${1:-packages/pb}"
    go test -tags contract -timeout=0 -p 1 ./...
  '';
};
```

Expose it where `ccpool-contract` is exposed (grep `flake.nix` for `ccpool-contract` and add `pb-contract` alongside, in `packages` and/or `apps`).

- [ ] **Step 3: Run the contract suite on this dev machine**

```bash
cd packages/pb && go test -tags contract -p 1 ./... -v
```

Expected: PASS (or `SKIP` for `pn`-dependent assertions if `pn` workspace setup is deferred). Fix any drift between the assumed and real surfaces (this is where the bd-version pin and patch-id determinism are confirmed).

- [ ] **Step 4: Commit**

```bash
git add packages/pb/cmd/pb/contract_test.go flake.nix
git commit -m "test(pb): contract tests (bd gate surface, cross-DB block, dedupe key, git patch-id, pn info schema) + pb-contract check"
```

---

## Task 11: ADR 0018 + index + README/CLAUDE.md

**Files:**

- Create: `docs/adr/0018-pb-tool-and-pn-applied-contract.md`
- Modify: `docs/adr/index.md`, `packages/pb/README.md`, `README.md` (repo root, if it lists packages), `CLAUDE.md` (if it documents packages/conventions)

**Interfaces:** documents the cross-repo `pn:applied` contract so future work doesn't re-derive it.

- [ ] **Step 1: Write `docs/adr/0018-pb-tool-and-pn-applied-contract.md`** using the repo's ADR template (Status: Accepted; Date: 2026-06-26; Deciders: Phillip Green II with Claude; Tracking: pg2-k43p.4). Content MUST cover:
  - **Context:** agents finishing work create follow-up beads (canonically "verify code works") that must not be workable until the change is applied; release must survive local rebases (→ `git patch-id`); pull model via `pn workspace info`.
  - **Decision:** the `pb` tool; the `pn:applied` gate contract — `await_type="pn:applied"`, `await_id="<wsid>:<repo>:<patch-id>"` (split on first two `:`); the **multi-DB dedupe key** (`dolt_server_host:port|dolt_database|project_id`, NOT the `.beads` path/prefix); the **co-location invariant** (a cross-DB `blocks` edge does not hold a bead out of `bd ready`, so the gate lives in the bead's own DB); the **baseline** (`metadata.applied_baseline` = repo `applied_ref` at create, may be empty; check scans `baseline..applied_ref` when ancestor, else last-N); `BD_JSON_ENVELOPE=1` pinning; dirty-lenient default; stale-handling (`--stale-after` default `3d`, `ms..d`, reject `<1ms`); `--dry-run` mutates nothing; best-effort + non-zero on skip.
  - **Consequences:** positive (rebase-survival, topology-agnostic discovery, fail-closed bias), negative (squash/within-context-rebase miss → stale-handler; **D2** OverridePaths gap deferred to pg2-k43p.3), neutral.
  - **Alternatives Considered:** raw SHA (breaks under rebase); `git notes` (per-repo config, stale copies); single rolling human gate (coarse).
  - **Related Decisions:** `See also: phillipg-nix-repo-base docs/adr/0012-pn-applied-state-store-and-info-api.md` (the consumed API) and ADR-0002 amendment (`[workspace].id`).

- [ ] **Step 2: Add the index row** to `docs/adr/index.md` (match the existing table format):

```
| [0018](0018-pb-tool-and-pn-applied-contract.md)                    | pb tool + pn:applied gate contract                            | Accepted                                                                        | 2026-06-26 |
```

- [ ] **Step 3: Expand `packages/pb/README.md`** — usage for `pb gate create` (HEAD default, `--blocks`/`--repo`, the deferred→gate→undefer lifecycle, link to the Phase-3 plugin) and `pb gate check` (run inside a workspace / as the apply post-hook, flags, `--dry-run`, stale-handling), the runtime PATH deps, and links to the spec + ADR 0018.

- [ ] **Step 4: Update repo `README.md` / `CLAUDE.md`** if they enumerate packages or Go-package conventions — add `pb` (one line). (Skip if neither lists packages; check first.)

- [ ] **Step 5: Commit**

```bash
git add docs/adr/0018-pb-tool-and-pn-applied-contract.md docs/adr/index.md packages/pb/README.md README.md CLAUDE.md
git commit -m "docs(pb): ADR 0018 (pb + pn:applied contract) + index + README/CLAUDE updates"
```

---

## Final completion gate (run after all tasks; the Phase-1 lesson)

- [ ] **Regenerate hooks & format:** `nix run .#install-pre-commit-hooks`
- [ ] **`prek run --all-files`** (or `pre-commit run --all-files`) — MUST pass. If treefmt/prettier reformats files, `git add` them and re-commit (never `--no-verify`).
- [ ] **`nix flake check`** — MUST pass (this is the hermetic gate that caught the Phase-1 XDG isolation bug; if a `pb` test fails here but passed locally, it is almost certainly writing outside `t.TempDir()`/unset XDG — fix the test isolation, not the gate).
- [ ] **Contract suite (dev):** `cd packages/pb && go test -tags contract -p 1 ./...` — PASS (the sandbox skips these; run them explicitly).
- [ ] **Broad whole-branch review** (subagent-driven-development's end-of-plan review): correctness of the patch-id scan ranges, the await_id split, the co-location `-C` dir choice, dry-run no-mutation on BOTH paths, and the best-effort non-zero exit.

## Integration / merge-back (worktree rule)

- [ ] `bd close pg2-k43p.4` once the gate passes and review is clean.
- [ ] ff-merge `pb-phase2` back to agent-support `main`. Because `main` is SHARED and may not be checked out, advance it with `git push . HEAD:main` (ff-enforced) from the worktree rather than `git checkout main && merge`.
- [ ] Remove the worktree: `git worktree remove .worktrees/pb-phase2` (from the primary checkout).
- [ ] Phase 3 (`pg2-k43p.5`) — the `pb` Claude plugin (teaches the deferred→gate→undefer lifecycle), the reusable smoke harness, and downstream wiring — remains a separate plan.

## Spec-coverage self-review (planner checklist — done)

- `pb gate create`: HEAD default ✓(T8), validate repo ✓(T8), patch-id ✓(T7/T8), co-locate ✓(T8), baseline write ✓(T8), multi-commit→N gates ✓(T8 `--commits`), deferred lifecycle ✓(D1, T8 fleet-race), `--json` ✓(T8).
- `pb gate check`: walk-up `.beads` bounded ✓(T6), Dolt-identity dedupe ✓(T6), `BD_JSON_ENVELOPE=1` ✓(T5), `--limit 0` ✓(T5), baseline-ancestry else last-N ✓(T9), bounded `log -p|patch-id` scan ✓(T7/T9), resolve in gate's DB ✓(T9 `-C db.Dir`), dirty-lenient vs `--strict` ✓(T9), `--dry-run` no-mutation both paths ✓(T9), stale-handler convert-to-human/close default `3d` ✓(T9), best-effort + non-zero ✓(T9 CLI), `--json` ✓(T9).
- Duration parser ms..d reject <1ms ✓(T2). Contract tests ✓(T10). ADR 0018 ✓(T11). Packaging mkGoApp+wrapProgram+home+flake+hooks ✓(T1).
- Open item carried to Phase 3: live `pn workspace info` schema confirmation (T10 may `t.Skip` it) + the smoke harness happy-path + ms-precision stale assertion.
