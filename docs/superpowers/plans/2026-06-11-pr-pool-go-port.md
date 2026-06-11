# pr-pool Go Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the `pr-pool.sh` bash orchestrator as a Go application (`packages/pr-pool/`) that delegates all claude+tmux session mechanics to ccpool behind one injectable `ccpool.Runner` interface, preserving the bash's discovery / role / completion semantics exactly.

**Architecture:** A new Go module `github.com/phillipgreenii/pr-pool`, modeled on `packages/ccpool/` (mkGoApp packaging, `cmd/` + `internal/` layout, stdlib-`flag` subcommand dispatch, injectable `run func` adapters tested with argv assertions). All ccpool interaction goes through `ccpool.Runner` (Phase-1 impl shells out to the `ccpool` CLI). All `bd` interaction goes through a local `beads.Runner` (copied from pg-pr's pattern) whose `Dir`/`Env` scrub `BEADS_DIR`/`WORKSPACE_ROOT`. The orchestrator uses **fresh ccpool session per bead** (`pr-pool-<role>-<beadid>`); completion is **bead-status-based**; ccpool state is consulted for **liveness only**.

**Tech Stack:** Go 1.25 (stdlib only — no external deps, `vendorHash = null`), Nix `mkGoApp`/`buildGoModule`, `nix flake check` gate (runs `go test ./...` via `doCheck=true` + pre-commit gofmt/golangci-lint). Shells out to `ccpool`, `bd`, `pg-pr` (all on the wrapped PATH).

**Source specs (read before starting):**

- `docs/superpowers/specs/2026-06-11-pr-pool-go-port-design.md` (authoritative design)
- `docs/superpowers/specs/2026-06-11-pr-pool-user-journeys.md` (J1–J11 behavior)
- `docs/superpowers/specs/2026-06-11-pr-pool-worker-contract-design.md` (worker/feedback contract)
- `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh` (the bash being ported + retired)

**Bead:** `pg2-spgx`. Live verification (Task 11) is **blocked** on `pg2-7mnq.{2,3,4}` (ccpool N1/N2/N3 enhancements, owned by a separate agent). Everything else in this plan is unblocked: all ccpool/bd interaction is mocked in tests, so the full build + unit-test suite goes green now, and the real CLI shell-out drops in once N1/N2/N3 land.

---

## Critical context the engineer must internalize

**This is a _capability_ port, not a line-by-line bash transliteration.** Several bash functions disappear because ccpool now owns them (decision 4 in the design):

| bash mechanic                                                         | fate in Go                                                                                |
| --------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `claude_rename` / `/rename`                                           | **dropped** — the ccpool session _name_ (`pr-pool-<role>-<beadid>`) is the per-bead label |
| `clear_context` / `/clear`                                            | **dropped** — fresh session per bead ⇒ context never carries over                         |
| `wait_ready` (pane-glyph poll)                                        | **dropped** — `ccpool new` blocks until ready internally                                  |
| `ensure_session` (tmux new-session)                                   | → `ccpool.Runner.Ensure`                                                                  |
| `send_nudge`/`submit_line` (tmux send-keys split-Enter)               | → `ccpool.Runner.Send` (ccpool owns paste+Enter)                                          |
| `pane_alive` (tmux capture-pane)                                      | → `ccpool.Runner.List` liveness (`Live` field / `State=="failed"`)                        |
| durable role sessions (`PR FEEDBACK PROCESSOR`/`WORKER`, `-L pgpool`) | → per-bead sessions on ccpool's socket; teardown scoped by the `pr-pool-` name prefix     |
| top-level `unset BEADS_DIR WORKSPACE_ROOT`                            | → `beads.CLIRunner.Env` scrub                                                             |

**What is ported faithfully (the orchestration core):** discovery (feedback parent-author match; worker `--label worker-ready --exclude-label human`), per-role caps + no-starvation, gating (sentinel files, no teardown on gated exit), the completion state machine (`done_signal`/`wait_done`/`seen_claimed`/`re-check-after-death`), failure actions (worker→`human` never unclaim; feedback→unclaim), and teardown-all (reaps strays).

**The ccpool.Runner CLI argv contract (what Phase-1 emits, asserted by tests).** N1 (`--env`), N2 (launch flags), and N3 (`list --json`) are **not yet implemented in ccpool**. pr-pool emits the agreed future argv; tests assert it; real execution is blocked until `pg2-7mnq.{2,3,4}` land. The contract:

| Runner method                    | argv                                                                                                                                                           |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Ensure(name, cwd, env)`         | `ccpool new <name> --cwd <cwd> --env K1=V1 --env K2=V2 … --dangerously-skip-permissions --effort <effort> [--model <model>]` (env keys sorted for determinism) |
| `Send(name, prompt, ModeNoWait)` | `ccpool reply <name> <prompt> --no-wait`                                                                                                                       |
| `Send(…, ModeInterrupt)`         | `ccpool reply <name> <prompt> --interrupt`                                                                                                                     |
| `Send(…, ModeQueue)`             | `ccpool reply <name> <prompt> --queue-message`                                                                                                                 |
| `Cancel(name)`                   | `ccpool cancel <name>`                                                                                                                                         |
| `Close(name)`                    | `ccpool close <name>`                                                                                                                                          |
| `List()`                         | `ccpool list --all --json`                                                                                                                                     |

(Today ccpool already accepts `new --cwd/--model`, `reply --no-wait/--interrupt/--queue-message`, `cancel`, `close`, `list --all`. The _new_ tokens pr-pool emits ahead of ccpool support are `new --env`, `new --dangerously-skip-permissions`, `new --effort`, and `list --json`.)

---

## File Structure

```
packages/pr-pool/
  go.mod                         module github.com/phillipgreenii/pr-pool ; go 1.25.0 ; (no require block — stdlib only)
  default.nix                    mkGoApp { pname="pr-pool"; vendorHash=null; ... } ; wrapProgram PATH += [ ccpool bd pg-pr ]
  update-deps.sh                 copied from pg-pr (PKG_NAME="pr-pool") — only needed if a dep is ever added
  cmd/pr-pool/
    main.go                      stdlib flag + manual subcommand dispatch (ccpool style); default subcommand = "drain"
    drain.go                     runDrain: build deps, precheck, gated, DrainOnce
    args.go                      parseInterspersed (copied verbatim from ccpool)
    args_test.go, drain_test.go
  internal/
    config/config.go             Config struct + Default() + Load() (env+defaults; TOML is a future seam)
    config/config_test.go
    beads/runner.go              Runner interface + CLIRunner{Dir,Env} + NewCLIRunnerForRepo (scrubbed env)
    beads/issue.go               Issue struct + ShowObj/Ready/Status/Unclaim/AddHuman helpers (bd JSON)
    beads/runner_test.go, beads/issue_test.go
    roles/roles.go               RoleKind, Role, Registry, nudge templates (verbatim), SessionName
    roles/roles_test.go
    ccpool/ccpool.go             Runner interface + Session/SessionState/SendMode types
    ccpool/cli.go                CLIRunner (Option-1 shell-out) with injectable run func
    ccpool/cli_test.go           argv-assertion table tests (the contract)
    discover/discover.go         Dispatch + Discover (feedback parent-author match; worker label filter)
    discover/discover_test.go
    complete/complete.go         DoneSignal + OnFailure (unclaim/human)
    complete/complete_test.go
    orchestrator/orchestrator.go Orchestrator{CC,BD,Reg,Cfg} : workOne / waitDone / DrainOnce / teardownAll
    orchestrator/orchestrator_test.go
```

`internal/` is private (nothing external imports pr-pool yet — no `pkg/`). Import DAG (no cycles): `config`, `beads`, `ccpool`, `roles` are leaves; `discover` → {beads, roles}; `complete` → {beads, roles}; `orchestrator` → {config, roles, beads, ccpool, discover, complete}; `cmd/pr-pool` → {config, roles, beads, ccpool, orchestrator}.

---

## Shared types & signatures (the contract all tasks must match)

These signatures are defined once here so later tasks stay consistent. Each task implements its slice; do not drift the names.

```go
// internal/config
type Config struct {
    RepoRoot      string        // monorepo root (default: cwd)
    BeadsPrefix   string        // expected .beads issue_prefix (default "zr")
    WorktreeDir   string        // worker worktree root, interpolated into the worker nudge
    SkillMD       string        // feedback-processor SKILL.md path
    WorkerSkillMD string        // worker SKILL.md path
    MaxFeedback   int           // feedback per-pass cap (default 1)
    MaxWorker     int           // worker per-pass cap (default 1)
    MaxWait       time.Duration // bead-completion timeout (default 1800s)
    PollInterval  time.Duration // status poll cadence (default 10s)
    QuotaPaused   string        // sentinel file path ("" disables)
    CICDDown      string        // sentinel file path ("" disables)
    Effort        string        // claude --effort value (default "max")
    Model         string        // claude --model ("" = ccpool default)
    Dangerous     bool          // emit --dangerously-skip-permissions (default true)
    SessionPrefix string        // ccpool session-name prefix for scoping/teardown (default "pr-pool-")
}
func Default() Config
func Load() Config // Default() overlaid with PR_POOL_* env vars

// internal/beads
type Runner interface { Run(ctx context.Context, args ...string) (stdout string, err error) }
type CLIRunner struct { Dir string; Env []string }
func NewCLIRunnerForRepo(dir string) *CLIRunner // Dir=dir, Env=scrubbed os.Environ()
type Issue struct {
    ID       string         `json:"id"`
    Title    string         `json:"title"`
    Status   string         `json:"status"`
    Type     string         `json:"issue_type"`
    Parent   string         `json:"parent"`
    Metadata map[string]any `json:"metadata"`
}
func ShowObj(ctx context.Context, r Runner, id string) (Issue, error) // bd show <id> --json (array-or-object)
func Ready(ctx context.Context, r Runner, args ...string) ([]Issue, error) // bd ready <args...> --json --limit 0
func Status(ctx context.Context, r Runner, id string) (string, error)
func Unclaim(ctx context.Context, r Runner, id string) error // bd update <id> --status=open --assignee=""
func AddHuman(ctx context.Context, r Runner, id string) error // bd update <id> --add-label human

// internal/roles
type RoleKind int
const ( Feedback RoleKind = iota; Worker )
type Role struct {
    Kind    RoleKind
    Name    string // "feedback-processor" | "worker" (used in session name)
    Actor   string // BEADS_ACTOR
    SkillMD string
    Cap     int
}
type Registry struct { Feedback Role; Worker Role }
func NewRegistry(cfg config.Config) Registry
func (r Role) SessionName(prefix, beadID string) string // prefix + Name + "-" + beadID
func (r Role) Nudge(beadID, worktreeDir string) string  // verbatim per-role template

// internal/ccpool
type SessionState string
type Session struct { Name string; State SessionState; Live bool; TranscriptPath string }
type SendMode int
const ( ModeNoWait SendMode = iota; ModeInterrupt; ModeQueue )
type Runner interface {
    Ensure(ctx context.Context, name, cwd string, env map[string]string) error
    Send(ctx context.Context, name, prompt string, mode SendMode) error
    Cancel(ctx context.Context, name string) error
    Close(ctx context.Context, name string) error
    List(ctx context.Context) ([]Session, error)
}
type CLIRunner struct { Effort, Model string; Dangerous bool /* + injectable run */ }
func NewCLIRunner(cfg config.Config) *CLIRunner

// internal/discover
type Dispatch struct { Role roles.Role; BeadID string }
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry, selfLogin string) ([]Dispatch, error)

// internal/complete
func DoneSignal(kind roles.RoleKind, status string, seenClaimed bool) bool
func OnFailure(ctx context.Context, br beads.Runner, role roles.Role, beadID string) error

// internal/orchestrator
type Orchestrator struct {
    CC  ccpool.Runner
    BD  beads.Runner
    Reg roles.Registry
    Cfg config.Config
}
func (o *Orchestrator) DrainOnce(ctx context.Context, selfLogin string) error
```

---

## Task 0: Scaffold the Go module + nix wiring (build + empty test green)

**Files:**

- Create: `packages/pr-pool/go.mod`
- Create: `packages/pr-pool/cmd/pr-pool/main.go`
- Create: `packages/pr-pool/cmd/pr-pool/args.go`
- Create: `packages/pr-pool/cmd/pr-pool/args_test.go`
- Create: `packages/pr-pool/default.nix`
- Create: `packages/pr-pool/update-deps.sh`
- Modify: `flake.nix` (overlay package def ~line 99; packages re-export ~line 603; pre-commit gofmt + golangci-lint ~lines 240–259)

- [ ] **Step 1: Create `go.mod`** (stdlib only)

```
module github.com/phillipgreenii/pr-pool

go 1.25.0
```

- [ ] **Step 2: Create `cmd/pr-pool/args.go`** (copied verbatim from ccpool — flags may follow positionals)

```go
package main

import "flag"

// parseInterspersed parses a FlagSet allowing flags to appear before, after, or
// between positional arguments. Go's stdlib flag stops at the first positional,
// silently dropping any flags after it. This walks the args, collecting
// positionals and re-parsing the remainder. Returns the positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return positionals
		}
		if fs.NArg() == 0 {
			return positionals
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
```

- [ ] **Step 3: Create `cmd/pr-pool/args_test.go`**

```go
package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantPos []string
		wantCwd string
	}{
		{"flags-first", []string{"--cwd", "/p", "alpha"}, []string{"alpha"}, "/p"},
		{"flag-after-positional", []string{"alpha", "--cwd", "/p"}, []string{"alpha"}, "/p"},
		{"no-flags", []string{"alpha"}, []string{"alpha"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			cwd := fs.String("cwd", "", "")
			pos := parseInterspersed(fs, tc.args)
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
			if *cwd != tc.wantCwd {
				t.Errorf("--cwd = %q, want %q", *cwd, tc.wantCwd)
			}
		})
	}
}
```

- [ ] **Step 4: Create `cmd/pr-pool/main.go`** (dispatch skeleton; `drain` is the default subcommand. `runDrain` is implemented in Task 8; for now stub it so the build compiles.)

```go
package main

import (
	"fmt"
	"os"
)

var version = "dev"

// pickSubcommand returns the subcommand and remaining args. No subcommand ⇒ "drain".
func pickSubcommand(args []string) (cmd string, rest []string) {
	known := map[string]bool{"drain": true, "version": true}
	if len(args) < 2 {
		return "drain", nil
	}
	if known[args[1]] {
		return args[1], args[2:]
	}
	return "drain", args[1:]
}

func main() {
	cmd, rest := pickSubcommand(os.Args)
	switch cmd {
	case "drain":
		os.Exit(runDrain(rest))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", cmd)
		os.Exit(2)
	}
}
```

- [ ] **Step 5: Add a temporary stub for `runDrain`** in `main.go` so the package compiles before Task 8. Append:

```go
// runDrain is implemented in drain.go (Task 8). Temporary stub.
func runDrain(_ []string) int { return 0 }
```

> NOTE for Task 8: delete this stub when `drain.go` is created.

- [ ] **Step 6: Create `default.nix`** (modeled on ccpool; **no claude-transcript replace**, **vendorHash = null** since stdlib-only)

```nix
{
  lib,
  mkGoApp,
  makeWrapper,
  ccpool,
  bd,
  pg-pr,
}:

mkGoApp {
  pname = "pr-pool";

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./cmd
      ./internal
    ];
  };

  # Stdlib-only module: no vendored dependencies.
  vendorHash = null;

  nativeBuildInputs = [ makeWrapper ];

  # pr-pool shells out to ccpool (session mechanics), bd (beads), and pg-pr
  # (config show). Wrap them onto PATH so the binary works under launchd's
  # minimal PATH.
  postInstall = ''
    wrapProgram $out/bin/pr-pool --prefix PATH : ${lib.makeBinPath [ ccpool bd pg-pr ]}
  '';

  meta = {
    description = "PR-pool orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pr-pool";
  };
}
```

> **`bd` sourcing (verified — MUST get right):** there is **no** top-level `bd`/`beads` overlay attr. `callPackage` auto-resolves `ccpool` (flake.nix:97) and `pg-pr` (flake.nix:90) from the overlay, but `bd` must be passed **explicitly**, sourced exactly like `gascity`'s `beads` arg (flake.nix:181): `final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads`. Step 8 passes it. Without this, `callPackage` fails with `attribute 'bd' missing`.

- [ ] **Step 7: Create `update-deps.sh`** by copying `packages/pg-pr/update-deps.sh` and changing `PKG_NAME="pg-pr"` to `PKG_NAME="pr-pool"`. (Only used if a dependency is ever added; harmless to include now.) `chmod 0755`.

- [ ] **Step 8: Wire the overlay package** — in `flake.nix`, after the `ccpool = …` block (~line 99), add:

```nix
          pr-pool = final.callPackage ./packages/pr-pool {
            inherit (goBuilders) mkGoApp;
            # No top-level bd/beads overlay attr — source it like gascity (flake.nix:181).
            bd = final.llm-agentsPkgs.beads or llm-agents.packages.${final.stdenv.hostPlatform.system}.beads;
          };
```

- [ ] **Step 9: Re-export the package** — in `flake.nix` `packages = { inherit (pkgs) … }` list (~line 587), add `pr-pool` to the inherited names.

- [ ] **Step 10: Add pre-commit hooks.** The git-hooks framework only supplies builtin defaults (including the **required** `entry`) for _canonical_ hook keys; a custom key with only `enable`+`files` and no `entry` **fails `nix flake check` evaluation** (`option hooks.<key>.entry is used but not defined`). So handle the two hooks differently:
  - **gofmt** (canonical; no per-module `cd` needed): **widen** the existing `gofmt` hook's `files` regex (flake.nix ~line 242) from `^packages/pg-pr/.*\\.go$` to `^packages/(pg-pr|pr-pool)/.*\\.go$`. Do NOT add a `gofmt-pr-pool` key.
  - **golangci-lint** (must run inside each module's own dir): add a NEW `golangci-lint-pr-pool` hook that supplies its **own** explicit `entry` (valid precisely because `entry` is set), and leave the canonical `golangci-lint` (pg-pr) untouched:

```nix
              golangci-lint-pr-pool = {
                enable = true;
                files = "^packages/pr-pool/.*\\.go$";
                entry = toString (
                  pkgs.writeShellScript "precommit-golangci-lint-pr-pool" ''
                    set -e
                    cd packages/pr-pool
                    ${pkgs.golangci-lint}/bin/golangci-lint run ./...
                  ''
                );
                pass_filenames = false;
              };
```

> Verify with `nix run .#install-pre-commit-hooks` (per repo CLAUDE.md) then `git diff .pre-commit-config.yaml` (confirm both the widened gofmt glob and the new pr-pool golangci-lint hook appear).

- [ ] **Step 11: Reinstall pre-commit hooks & verify build + empty test pass**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-go-impl
nix run .#install-pre-commit-hooks
cd packages/pr-pool && go test ./... && go vet ./...
cd ../.. && nix build .#pr-pool --no-link 2>&1 | tail -5
```

Expected: `go test` reports `ok` for `cmd/pr-pool` (args_test passes), `go vet` clean, `nix build .#pr-pool` succeeds (binary builds, wraps ccpool/bd/pg-pr onto PATH).

- [ ] **Step 12: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-go-impl
git add packages/pr-pool flake.nix .pre-commit-config.yaml
git commit -m "feat(pr-pool): scaffold Go module + nix wiring (pg2-spgx)

New stdlib-only Go app modeled on ccpool: mkGoApp package (vendorHash=null),
cmd/pr-pool dispatch skeleton (default subcommand 'drain'), parseInterspersed,
wrapProgram PATH += [ccpool bd pg-pr]. flake overlay + packages re-export +
pre-commit gofmt/golangci-lint hooks. Build + empty test green."
```

---

## Task 1: `internal/config` — env+defaults config

**Files:**

- Create: `packages/pr-pool/internal/config/config.go`
- Test: `packages/pr-pool/internal/config/config_test.go`

The bash config is entirely `${VAR:-default}`. Port faithfully: `Default()` plus `PR_POOL_*` env overlay. TOML is intentionally deferred — `Load()` is the seam where a future TOML layer slots between defaults and env.

- [ ] **Step 1: Write the failing test** `config_test.go`

```go
package config

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	d := Default()
	if d.BeadsPrefix != "zr" {
		t.Errorf("BeadsPrefix = %q, want zr", d.BeadsPrefix)
	}
	if d.MaxFeedback != 1 || d.MaxWorker != 1 {
		t.Errorf("caps = %d/%d, want 1/1", d.MaxFeedback, d.MaxWorker)
	}
	if d.MaxWait != 1800*time.Second {
		t.Errorf("MaxWait = %v, want 1800s", d.MaxWait)
	}
	if d.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", d.PollInterval)
	}
	if d.Effort != "max" {
		t.Errorf("Effort = %q, want max", d.Effort)
	}
	if !d.Dangerous {
		t.Error("Dangerous should default true")
	}
	if d.SessionPrefix != "pr-pool-" {
		t.Errorf("SessionPrefix = %q, want pr-pool-", d.SessionPrefix)
	}
}

func TestLoad_envOverrides(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "3")
	t.Setenv("PR_POOL_MAX_WAIT", "60")
	t.Setenv("PR_POOL_BEADS_PREFIX", "pg2")
	t.Setenv("PR_POOL_MODEL", "claude-opus-4-8")
	t.Setenv("PR_POOL_DANGEROUS", "0")
	c := Load()
	if c.MaxWorker != 3 {
		t.Errorf("MaxWorker = %d, want 3", c.MaxWorker)
	}
	if c.MaxWait != 60*time.Second {
		t.Errorf("MaxWait = %v, want 60s", c.MaxWait)
	}
	if c.BeadsPrefix != "pg2" {
		t.Errorf("BeadsPrefix = %q, want pg2", c.BeadsPrefix)
	}
	if c.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.Dangerous {
		t.Error("PR_POOL_DANGEROUS=0 should disable Dangerous")
	}
}

func TestLoad_badIntFallsBackToDefault(t *testing.T) {
	t.Setenv("PR_POOL_MAX_WORKER", "notanint")
	if c := Load(); c.MaxWorker != 1 {
		t.Errorf("bad int should fall back to default 1, got %d", c.MaxWorker)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `cd packages/pr-pool && go test ./internal/config/` → FAIL (undefined: Default/Load).

- [ ] **Step 3: Implement `config.go`**

```go
// Package config holds pr-pool's runtime configuration. The bash pr-pool used
// env-var-with-default for everything; this preserves that exactly. TOML/XDG is
// a deliberate future seam: a loader could layer a file between Default() and
// the env overlay in Load() without changing callers.
package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	RepoRoot      string
	BeadsPrefix   string
	WorktreeDir   string
	SkillMD       string
	WorkerSkillMD string
	MaxFeedback   int
	MaxWorker     int
	MaxWait       time.Duration
	PollInterval  time.Duration
	QuotaPaused   string
	CICDDown      string
	Effort        string
	Model         string
	Dangerous     bool
	SessionPrefix string
}

// Default returns the built-in defaults (mirrors pr-pool.sh's ${VAR:-default}).
func Default() Config {
	cwd, _ := os.Getwd()
	state := stateHome()
	return Config{
		RepoRoot:      cwd,
		BeadsPrefix:   "zr",
		WorktreeDir:   state + "/pr-pool/worktrees",
		SkillMD:       "",
		WorkerSkillMD: "",
		MaxFeedback:   1,
		MaxWorker:     1,
		MaxWait:       1800 * time.Second,
		PollInterval:  10 * time.Second,
		QuotaPaused:   "",
		CICDDown:      "",
		Effort:        "max",
		Model:         "",
		Dangerous:     true,
		SessionPrefix: "pr-pool-",
	}
}

// Load returns Default() overlaid with any PR_POOL_* environment variables.
func Load() Config {
	c := Default()
	c.RepoRoot = envStr("PR_POOL_REPO_ROOT", c.RepoRoot)
	c.BeadsPrefix = envStr("PR_POOL_BEADS_PREFIX", c.BeadsPrefix)
	c.WorktreeDir = envStr("PR_POOL_WORKTREE_DIR", c.WorktreeDir)
	c.SkillMD = envStr("PR_POOL_SKILL_MD", c.SkillMD)
	c.WorkerSkillMD = envStr("PR_POOL_WORKER_SKILL_MD", c.WorkerSkillMD)
	c.MaxFeedback = envInt("PR_POOL_MAX_FEEDBACK", c.MaxFeedback)
	c.MaxWorker = envInt("PR_POOL_MAX_WORKER", c.MaxWorker)
	c.MaxWait = envSecs("PR_POOL_MAX_WAIT", c.MaxWait)
	c.PollInterval = envSecs("PR_POOL_POLL_INTERVAL", c.PollInterval)
	c.QuotaPaused = envStr("PR_POOL_QUOTA_PAUSED", c.QuotaPaused)
	c.CICDDown = envStr("PR_POOL_CICD_DOWN", c.CICDDown)
	c.Effort = envStr("PR_POOL_EFFORT", c.Effort)
	c.Model = envStr("PR_POOL_MODEL", c.Model)
	c.Dangerous = envBool("PR_POOL_DANGEROUS", c.Dangerous)
	c.SessionPrefix = envStr("PR_POOL_SESSION_PREFIX", c.SessionPrefix)
	return c
}

func stateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.local/state"
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envSecs(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		switch v {
		case "0", "false", "no", "":
			return false
		default:
			return true
		}
	}
	return def
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/config/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/config
git commit -m "feat(pr-pool): config (env+defaults, TOML-ready seam) (pg2-spgx)"
```

---

## Task 2: `internal/beads` — local bd runner (copied pattern, scrubbed env)

**Files:**

- Create: `packages/pr-pool/internal/beads/runner.go`
- Test: `packages/pr-pool/internal/beads/runner_test.go`

Copy pg-pr's `pkg/beads/runner.go` pattern. The key pr-pool addition: `NewCLIRunnerForRepo` builds `Env` by scrubbing `BEADS_DIR`/`WORKSPACE_ROOT` from `os.Environ()` (replaces the bash top-level `unset`), so pr-pool's own `bd` resolves the monorepo store from `Dir=RepoRoot`.

- [ ] **Step 1: Write the failing test** `runner_test.go`

```go
package beads

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestScrubbedEnv_removesBeadsTaint(t *testing.T) {
	t.Setenv("BEADS_DIR", "/wrong/.beads")
	t.Setenv("WORKSPACE_ROOT", "/wrong")
	t.Setenv("PATH", "/usr/bin")
	r := NewCLIRunnerForRepo("/repo")
	if r.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", r.Dir)
	}
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			t.Errorf("scrubbed env still contains %q", kv)
		}
	}
	var sawPath bool
	for _, kv := range r.Env {
		if strings.HasPrefix(kv, "PATH=") {
			sawPath = true
		}
	}
	if !sawPath {
		t.Error("scrubbed env dropped PATH; should only remove BEADS_DIR/WORKSPACE_ROOT")
	}
}

func TestScrubEnv_pure(t *testing.T) {
	in := []string{"A=1", "BEADS_DIR=/x", "B=2", "WORKSPACE_ROOT=/y", "C=3"}
	got := scrubEnv(in)
	want := []string{"A=1", "B=2", "C=3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scrubEnv = %v, want %v", got, want)
	}
	_ = os.Environ // referenced to keep import if test is trimmed
}

// fakeRunner returns canned stdout/err without spawning bd. Reused by other pkgs.
type fakeRunner struct {
	out  string
	err  error
	args [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.args = append(f.args, args)
	return f.out, f.err
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/beads/` → FAIL (undefined NewCLIRunnerForRepo/scrubEnv).

- [ ] **Step 3: Implement `runner.go`**

```go
// Package beads is pr-pool's local bd client. It copies pg-pr's Runner/CLIRunner
// pattern rather than importing pg-pr's heavy module. The CLIRunner's Dir/Env
// carry the env scrub (the bash's top-level `unset BEADS_DIR WORKSPACE_ROOT`),
// so pr-pool's own bd resolves the monorepo store from Dir, ignoring any ambient
// BEADS_DIR/WORKSPACE_ROOT inherited from a parent shell.
package beads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner shells out to `bd`. Production uses CLIRunner; tests inject a fake.
type Runner interface {
	Run(ctx context.Context, args ...string) (stdout string, err error)
}

// CLIRunner invokes the `bd` binary from PATH.
type CLIRunner struct {
	Dir string   // working dir bd resolves its workspace from ("" = inherit cwd)
	Env []string // env block ("" / nil = inherit process env)
}

// NewCLIRunnerForRepo returns a CLIRunner rooted at dir, with BEADS_DIR and
// WORKSPACE_ROOT scrubbed from the inherited environment.
func NewCLIRunnerForRepo(dir string) *CLIRunner {
	return &CLIRunner{Dir: dir, Env: scrubEnv(os.Environ())}
}

func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "BEADS_DIR=") || strings.HasPrefix(kv, "WORKSPACE_ROOT=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (r *CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "bd", args...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	if r.Env != nil {
		cmd.Env = r.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("bd %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), fmt.Errorf("bd %s: %w (is bd on PATH?)",
			strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/beads/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/beads
git commit -m "feat(pr-pool): local bd Runner with BEADS_DIR/WORKSPACE_ROOT scrub (pg2-spgx)"
```

---

## Task 3: `internal/beads` — Issue helpers (bd JSON parsing)

**Files:**

- Create: `packages/pr-pool/internal/beads/issue.go`
- Test: `packages/pr-pool/internal/beads/issue_test.go`

Ports `bd_obj`, `bead_status`, `unclaim`, `mark_human`, and the `bd ready`/`bd show --json` shapes. `ShowObj` normalizes bd's array-or-object JSON (the bash `if type=="array" then .[0] else .`).

- [ ] **Step 1: Write the failing test** `issue_test.go`

```go
package beads

import (
	"context"
	"testing"
)

func TestShowObj_object(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"open","parent":"zr-pr","metadata":{"author":"phillipg"}}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-1" || iss.Status != "open" || iss.Parent != "zr-pr" {
		t.Errorf("got %+v", iss)
	}
	if iss.Metadata["author"] != "phillipg" {
		t.Errorf("author = %v", iss.Metadata["author"])
	}
}

func TestShowObj_array(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-2","status":"closed"}]`}
	iss, err := ShowObj(context.Background(), fr, "zr-2")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-2" || iss.Status != "closed" {
		t.Errorf("got %+v", iss)
	}
}

func TestStatus(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"in_progress"}`}
	s, err := Status(context.Background(), fr, "zr-1")
	if err != nil || s != "in_progress" {
		t.Fatalf("status=%q err=%v", s, err)
	}
}

func TestReady_emptyAndArray(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-1","issue_type":"task","title":"process-feedback: x"}]`}
	got, err := Ready(context.Background(), fr, "--label", "worker-ready")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "zr-1" {
		t.Fatalf("got %+v", got)
	}
	// argv assertion: Ready appends --json --limit 0
	last := fr.args[len(fr.args)-1]
	wantTail := []string{"ready", "--label", "worker-ready", "--json", "--limit", "0"}
	if joinArgs(last) != joinArgs(wantTail) {
		t.Errorf("argv = %v, want %v", last, wantTail)
	}
}

func TestReady_handlesNonArray(t *testing.T) {
	fr := &fakeRunner{out: `null`}
	got, err := Ready(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("null should yield empty slice, got %v", got)
	}
}

func TestUnclaim_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Unclaim(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--status=open", "--assignee="}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestAddHuman_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := AddHuman(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--add-label", "human"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func joinArgs(a []string) string {
	s := ""
	for _, x := range a {
		s += "\x00" + x
	}
	return s
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/beads/` → FAIL.

- [ ] **Step 3: Implement `issue.go`**

```go
package beads

import (
	"context"
	"encoding/json"
	"fmt"
)

// Issue is the subset of a bd issue pr-pool reads. Metadata is left as a generic
// map (bd serializes merge-request fields like author/repo/pr_number into it).
type Issue struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   string         `json:"status"`
	Type     string         `json:"issue_type"`
	Parent   string         `json:"parent"`
	Metadata map[string]any `json:"metadata"`
}

// ShowObj runs `bd show <id> --json` and normalizes bd's array-or-object output
// (mirrors the bash bd_obj helper).
func ShowObj(ctx context.Context, r Runner, id string) (Issue, error) {
	out, err := r.Run(ctx, "show", id, "--json")
	if err != nil {
		return Issue{}, err
	}
	return decodeOne([]byte(out))
}

// Ready runs `bd ready <args...> --json --limit 0` and returns the issues.
// A non-array / null payload yields an empty slice (mirrors the bash
// `if type=="array" then . else []`).
func Ready(ctx context.Context, r Runner, args ...string) ([]Issue, error) {
	full := append(append([]string{"ready"}, args...), "--json", "--limit", "0")
	out, err := r.Run(ctx, full...)
	if err != nil {
		return nil, err
	}
	return decodeMany([]byte(out)), nil
}

// Status returns the issue's current status ("" if unset).
func Status(ctx context.Context, r Runner, id string) (string, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return "", err
	}
	return iss.Status, nil
}

// Unclaim returns a bead to the open pool: `bd update <id> --status=open --assignee=`.
func Unclaim(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--status=open", "--assignee=")
	if err != nil {
		return fmt.Errorf("unclaim %s: %w", id, err)
	}
	return nil
}

// AddHuman flags a bead for a human: `bd update <id> --add-label human`.
func AddHuman(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--add-label", "human")
	if err != nil {
		return fmt.Errorf("add-human %s: %w", id, err)
	}
	return nil
}

func decodeOne(b []byte) (Issue, error) {
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		if len(arr) > 0 {
			return arr[0], nil
		}
		return Issue{}, nil
	}
	var one Issue
	if err := json.Unmarshal(b, &one); err != nil {
		return Issue{}, fmt.Errorf("decode issue: %w", err)
	}
	return one, nil
}

func decodeMany(b []byte) []Issue {
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr
	}
	return nil
}
```

> Note `--assignee=` (empty value) — bd accepts the empty form; the bash used `--assignee=""` which is the same token after shell quoting.

- [ ] **Step 4: Run tests** — `go test ./internal/beads/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/beads
git commit -m "feat(pr-pool): bd Issue helpers (ShowObj/Ready/Status/Unclaim/AddHuman) (pg2-spgx)"
```

---

## Task 4: `internal/roles` — role registry + verbatim nudge templates

**Files:**

- Create: `packages/pr-pool/internal/roles/roles.go`
- Test: `packages/pr-pool/internal/roles/roles_test.go`

Ports the role config table, the per-role caps, the session-name convention (`pr-pool-<role>-<beadid>`), and the **verbatim** nudge templates. The nudge text is copied EXACTLY from `pr-pool.sh` (the bats tests assert specific substrings; the contract depends on the exact wording).

- [ ] **Step 1: Write the failing test** `roles_test.go`

```go
package roles

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestNewRegistry_fromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.SkillMD = "/skills/fb.md"
	cfg.WorkerSkillMD = "/skills/wk.md"
	cfg.MaxWorker = 2
	reg := NewRegistry(cfg)
	if reg.Feedback.Actor != "pgii-pool__process-feedback" {
		t.Errorf("feedback actor = %q", reg.Feedback.Actor)
	}
	if reg.Worker.Actor != "pgii-pool__worker" {
		t.Errorf("worker actor = %q", reg.Worker.Actor)
	}
	if reg.Worker.Cap != 2 || reg.Feedback.Cap != 1 {
		t.Errorf("caps = %d/%d", reg.Worker.Cap, reg.Feedback.Cap)
	}
	if reg.Feedback.Name != "feedback-processor" || reg.Worker.Name != "worker" {
		t.Errorf("names = %q/%q", reg.Feedback.Name, reg.Worker.Name)
	}
	if reg.Feedback.Kind != Feedback || reg.Worker.Kind != Worker {
		t.Error("kinds wrong")
	}
}

func TestSessionName(t *testing.T) {
	reg := NewRegistry(config.Default())
	if got := reg.Worker.SessionName("pr-pool-", "zr-lweh.2"); got != "pr-pool-worker-zr-lweh.2" {
		t.Errorf("worker session name = %q", got)
	}
	if got := reg.Feedback.SessionName("pr-pool-", "zr-7"); got != "pr-pool-feedback-processor-zr-7" {
		t.Errorf("feedback session name = %q", got)
	}
}

func TestWorkerNudge_contract(t *testing.T) {
	reg := NewRegistry(withSkills(config.Default(), "/fb.md", "/wk.md"))
	n := reg.Worker.Nudge("zr-w1", "/state/worktrees")
	for _, sub := range []string{
		"/wk.md", "zr-w1", "bd update zr-w1 --claim", "phillipg.",
		"--add-label human", "/state/worktrees", "force-with-lease",
		"bd comment", "NEVER leave the bead in_progress",
	} {
		if !strings.Contains(n, sub) {
			t.Errorf("worker nudge missing %q\n---\n%s", sub, n)
		}
	}
	if strings.Contains(n, "needs-push") {
		t.Errorf("worker nudge must not mention needs-push")
	}
}

func TestFeedbackNudge_contract(t *testing.T) {
	reg := NewRegistry(withSkills(config.Default(), "/fb.md", "/wk.md"))
	n := reg.Feedback.Nudge("zr-c1", "/ignored")
	for _, sub := range []string{"/fb.md", "zr-c1", "open work bead", "child of the PR bead", "Close each feedback"} {
		if !strings.Contains(n, sub) {
			t.Errorf("feedback nudge missing %q\n---\n%s", sub, n)
		}
	}
	if strings.Contains(n, "/exit") {
		t.Errorf("feedback nudge must not mention /exit")
	}
}

func withSkills(c config.Config, fb, wk string) config.Config {
	c.SkillMD, c.WorkerSkillMD = fb, wk
	return c
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/roles/` → FAIL.

- [ ] **Step 3: Implement `roles.go`** (nudge text copied verbatim from `pr-pool.sh`)

```go
// Package roles is pr-pool's role registry: the per-role actor, skill, cap, and
// nudge template. The nudge text is copied verbatim from pr-pool.sh — the
// worker/feedback contracts depend on the exact wording.
package roles

import (
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

type RoleKind int

const (
	Feedback RoleKind = iota
	Worker
)

type Role struct {
	Kind    RoleKind
	Name    string // session-name token: "feedback-processor" | "worker"
	Actor   string // BEADS_ACTOR
	SkillMD string
	Cap     int
}

type Registry struct {
	Feedback Role
	Worker   Role
}

func NewRegistry(cfg config.Config) Registry {
	return Registry{
		Feedback: Role{
			Kind:    Feedback,
			Name:    "feedback-processor",
			Actor:   "pgii-pool__process-feedback",
			SkillMD: cfg.SkillMD,
			Cap:     cfg.MaxFeedback,
		},
		Worker: Role{
			Kind:    Worker,
			Name:    "worker",
			Actor:   "pgii-pool__worker",
			SkillMD: cfg.WorkerSkillMD,
			Cap:     cfg.MaxWorker,
		},
	}
}

// SessionName builds the per-bead ccpool session name: <prefix><role>-<beadid>,
// e.g. "pr-pool-worker-zr-lweh.2".
func (r Role) SessionName(prefix, beadID string) string {
	return prefix + r.Name + "-" + beadID
}

// Nudge returns the role's prompt for the given bead. worktreeDir is only used
// by the worker template.
func (r Role) Nudge(beadID, worktreeDir string) string {
	switch r.Kind {
	case Worker:
		return fmt.Sprintf(workerNudge, r.SkillMD, beadID, beadID, beadID, worktreeDir, beadID, beadID)
	default:
		return fmt.Sprintf(feedbackNudge, r.SkillMD, beadID)
	}
}

// feedbackNudge args: SKILL_MD, cycle id.
const feedbackNudge = `Read %s and process process-feedback cycle %s: claim it, read its feedback children (bd children %[2]s), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback — but if that work matches an existing open work bead, link/update it instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary.`

// workerNudge args: WORKER_SKILL_MD, id, id, id, WORKTREE_DIR, id, id.
const workerNudge = `Read %s and implement work bead %s. Claim it (bd update %s --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed); assert metadata.author is me AND the branch starts with 'phillipg.'. If you cannot resolve the PR, it is not mine, or the branch is not phillipg.-prefixed, make NO changes, comment why, and add the human label (bd update %s --add-label human). Otherwise work in a clean isolated git worktree for that branch under %s (never start or leave it dirty), implement the change the bead describes, and commit it. Push ONLY if the bead's instructions say to (git push or git push --force-with-lease; NEVER git push --force). Record what you did with bd comment FIRST, then end by EITHER closing the bead (bd close %s — including when the work is already present at HEAD) OR, if handing it back, unclaiming it (bd update %s --status=open --assignee=""). NEVER leave the bead in_progress; do not push by default.`
```

> Verify the verbatim text against `pr-pool.sh` during the step — diff the substring set. The `%[2]s` in the feedback template re-uses the cycle id for `bd children`.

- [ ] **Step 4: Run tests** — `go test ./internal/roles/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/roles
git commit -m "feat(pr-pool): role registry + verbatim nudge templates (pg2-spgx)"
```

---

## Task 5: `internal/ccpool` — Runner interface + types

**Files:**

- Create: `packages/pr-pool/internal/ccpool/ccpool.go`
- Test: (covered by Task 6's cli*test.go; this task is types only — no behavior to test directly, but a compile-only `var * Runner = (\*CLIRunner)(nil)` lands in Task 6)

- [ ] **Step 1: Implement `ccpool.go`** (interface + types; the anti-corner-painting boundary)

```go
// Package ccpool is pr-pool's seam onto the ccpool session manager. ALL session
// mechanics flow through Runner. The Phase-1 implementation (cli.go) shells out
// to the `ccpool` CLI; a future in-process implementation wrapping ccpool's
// session.Service is a drop-in replacement behind this same interface.
package ccpool

import "context"

// SessionState mirrors ccpool's store states.
type SessionState string

const (
	StateStarting   SessionState = "starting"
	StateReady      SessionState = "ready"
	StateWorking    SessionState = "working"
	StateNeedsInput SessionState = "needs_input"
	StateDone       SessionState = "done"
	StateFailed     SessionState = "failed"
)

// Session is one row from `ccpool list --all --json`.
type Session struct {
	Name           string       `json:"name"`
	State          SessionState `json:"state"`
	Live           bool         `json:"live"`            // tmux has-session (liveness, NOT a store state)
	TranscriptPath string       `json:"transcript_path"` // consumed by chunk B (token observation)
}

type SendMode int

const (
	ModeNoWait SendMode = iota // deliver and return immediately (orchestrator default)
	ModeInterrupt              // cancel the current turn, then deliver
	ModeQueue                  // deliver into claude's native queue (fire-and-forget)
)

// Runner is the full ccpool capability surface pr-pool needs. Cancel is present
// only as a chunk-B seam (90/100% budget cancels); Phase-1 never calls it.
type Runner interface {
	Ensure(ctx context.Context, name, cwd string, env map[string]string) error
	Send(ctx context.Context, name, prompt string, mode SendMode) error
	Cancel(ctx context.Context, name string) error
	Close(ctx context.Context, name string) error
	List(ctx context.Context) ([]Session, error)
}
```

- [ ] **Step 2: Verify it compiles** — `go build ./internal/ccpool/`.

- [ ] **Step 3: Commit**

```bash
git add packages/pr-pool/internal/ccpool
git commit -m "feat(pr-pool): ccpool.Runner interface + Session/SendMode types (pg2-spgx)"
```

---

## Task 6: `internal/ccpool` — CLI implementation (argv contract)

**Files:**

- Create: `packages/pr-pool/internal/ccpool/cli.go`
- Test: `packages/pr-pool/internal/ccpool/cli_test.go`

The CLI impl wraps `exec.Command("ccpool", …)` behind an injectable `run func([]string)([]byte,error)`, exactly like ccpool's `tmux.Client`. **Tests inject a fake and assert the exact argv** (the contract from the top of this plan). This is where the N1/N2/N3 dependency lives: the argv includes the not-yet-implemented `--env`/`--dangerously-skip-permissions`/`--effort`/`list --json` tokens, which the mock accepts freely.

- [ ] **Step 1: Write the failing test** `cli_test.go`

```go
package ccpool

import (
	"context"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

var _ Runner = (*CLIRunner)(nil)

func newSpy() (*CLIRunner, *[][]string, func(out []byte)) {
	var got [][]string
	var canned []byte
	cli := NewCLIRunner(config.Default())
	cli.run = func(args []string) ([]byte, error) {
		got = append(got, args)
		return canned, nil
	}
	setOut := func(out []byte) { canned = out }
	return cli, &got, setOut
}

func TestEnsure_argv(t *testing.T) {
	cli, got, _ := newSpy()
	err := cli.Ensure(context.Background(), "pr-pool-worker-zr-1", "/repo",
		map[string]string{"WORKSPACE_ROOT": "/repo", "BEADS_ACTOR": "pgii-pool__worker", "BEADS_DIR": "/repo/.beads"})
	if err != nil {
		t.Fatal(err)
	}
	// env keys sorted: BEADS_ACTOR, BEADS_DIR, WORKSPACE_ROOT
	want := []string{
		"new", "pr-pool-worker-zr-1", "--cwd", "/repo",
		"--env", "BEADS_ACTOR=pgii-pool__worker",
		"--env", "BEADS_DIR=/repo/.beads",
		"--env", "WORKSPACE_ROOT=/repo",
		"--dangerously-skip-permissions", "--effort", "max",
	}
	if !reflect.DeepEqual((*got)[0], want) {
		t.Errorf("argv =\n %v\nwant\n %v", (*got)[0], want)
	}
}

func TestEnsure_argv_withModel_noDangerous(t *testing.T) {
	var got [][]string
	cfg := config.Default()
	cfg.Model = "claude-opus-4-8"
	cfg.Dangerous = false
	cfg.Effort = "high"
	cli := NewCLIRunner(cfg)
	cli.run = func(args []string) ([]byte, error) { got = append(got, args); return nil, nil }
	if err := cli.Ensure(context.Background(), "s", "/r", nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"new", "s", "--cwd", "/r", "--effort", "high", "--model", "claude-opus-4-8"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("argv = %v, want %v", got[0], want)
	}
}

func TestSend_modes(t *testing.T) {
	cases := []struct {
		mode SendMode
		flag string
	}{
		{ModeNoWait, "--no-wait"},
		{ModeInterrupt, "--interrupt"},
		{ModeQueue, "--queue-message"},
	}
	for _, tc := range cases {
		cli, got, _ := newSpy()
		if err := cli.Send(context.Background(), "s", "hello world", tc.mode); err != nil {
			t.Fatal(err)
		}
		want := []string{"reply", "s", "hello world", tc.flag}
		if !reflect.DeepEqual((*got)[0], want) {
			t.Errorf("mode %d argv = %v, want %v", tc.mode, (*got)[0], want)
		}
	}
}

func TestCancelCloseList_argv(t *testing.T) {
	cli, got, setOut := newSpy()
	_ = cli.Cancel(context.Background(), "s")
	_ = cli.Close(context.Background(), "s")
	setOut([]byte(`[{"name":"pr-pool-worker-zr-1","state":"working","live":true,"transcript_path":"/t/x.jsonl"}]`))
	sessions, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*got)[0], []string{"cancel", "s"}) {
		t.Errorf("cancel argv = %v", (*got)[0])
	}
	if !reflect.DeepEqual((*got)[1], []string{"close", "s"}) {
		t.Errorf("close argv = %v", (*got)[1])
	}
	if !reflect.DeepEqual((*got)[2], []string{"list", "--all", "--json"}) {
		t.Errorf("list argv = %v", (*got)[2])
	}
	if len(sessions) != 1 || sessions[0].Name != "pr-pool-worker-zr-1" ||
		sessions[0].State != StateWorking || !sessions[0].Live ||
		sessions[0].TranscriptPath != "/t/x.jsonl" {
		t.Errorf("parsed session = %+v", sessions)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/ccpool/` → FAIL (undefined NewCLIRunner / .run).

- [ ] **Step 3: Implement `cli.go`**

```go
package ccpool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

// CLIRunner is the Phase-1 Runner: it shells out to the `ccpool` binary on PATH.
// run is injectable for tests (zero real processes), exactly like ccpool's
// tmux.Client. The launch-flag fields come from config and are emitted on
// `ccpool new` per the agreed contract (ccpool N2 — see pg2-7mnq.3).
type CLIRunner struct {
	Effort    string
	Model     string
	Dangerous bool
	run       func(args []string) ([]byte, error)
}

func NewCLIRunner(cfg config.Config) *CLIRunner {
	c := &CLIRunner{Effort: cfg.Effort, Model: cfg.Model, Dangerous: cfg.Dangerous}
	c.run = func(args []string) ([]byte, error) {
		return exec.Command("ccpool", args...).CombinedOutput()
	}
	return c
}

func (c *CLIRunner) ccpool(args ...string) ([]byte, error) {
	out, err := c.run(args)
	if err != nil {
		return out, fmt.Errorf("ccpool %v: %w (%s)", args, err, bytes.TrimSpace(out))
	}
	return out, nil
}

// Ensure: ccpool new <name> --cwd <cwd> --env K=V… --dangerously-skip-permissions
// --effort <effort> [--model <model>]. env keys sorted for deterministic argv.
func (c *CLIRunner) Ensure(_ context.Context, name, cwd string, env map[string]string) error {
	args := []string{"new", name, "--cwd", cwd}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+env[k])
	}
	if c.Dangerous {
		args = append(args, "--dangerously-skip-permissions")
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	_, err := c.ccpool(args...)
	return err
}

// Send: ccpool reply <name> <prompt> <mode-flag>.
func (c *CLIRunner) Send(_ context.Context, name, prompt string, mode SendMode) error {
	flag := "--no-wait"
	switch mode {
	case ModeInterrupt:
		flag = "--interrupt"
	case ModeQueue:
		flag = "--queue-message"
	}
	_, err := c.ccpool("reply", name, prompt, flag)
	return err
}

func (c *CLIRunner) Cancel(_ context.Context, name string) error {
	_, err := c.ccpool("cancel", name)
	return err
}

func (c *CLIRunner) Close(_ context.Context, name string) error {
	_, err := c.ccpool("close", name)
	return err
}

// List: ccpool list --all --json.
func (c *CLIRunner) List(_ context.Context) ([]Session, error) {
	out, err := c.ccpool("list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(out, &sessions); err != nil {
		return nil, fmt.Errorf("ccpool list --json decode: %w", err)
	}
	return sessions, nil
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/ccpool/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/ccpool
git commit -m "feat(pr-pool): ccpool CLI Runner (argv contract for N1/N2/N3) (pg2-spgx)"
```

---

## Task 7: `internal/discover` — bd-ready discovery per role

**Files:**

- Create: `packages/pr-pool/internal/discover/discover.go`
- Test: `packages/pr-pool/internal/discover/discover_test.go`

Ports `discover_feedback` / `discover_worker` / `discover`. Feedback: `bd ready` → filter `issue_type=="task" && title startswith "process-feedback:"` → resolve parent → assert `parent.metadata.author == selfLogin`. Worker: `bd ready --label worker-ready --exclude-label human`. Order: feedback first, then worker.

The fake bd Runner must route by the args it receives (the test inspects `args[0]=="ready"` vs `args[0]=="show"`).

- [ ] **Step 1: Write the failing test** `discover_test.go`

```go
package discover

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// routingRunner answers bd calls based on argv, simulating a small bead store.
type routingRunner struct {
	readyFeedback string // JSON for `bd ready` (no label filter)
	readyWorker   string // JSON for `bd ready --label worker-ready ...`
	show          map[string]string
	sawWorkerArgs []string
}

func (r *routingRunner) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if contains(args, "--label") {
			r.sawWorkerArgs = args
			return r.readyWorker, nil
		}
		return r.readyFeedback, nil
	case "show":
		return r.show[args[1]], nil
	}
	return "", nil
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestDiscover_feedbackOwnership(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[
			{"id":"zr-mine","issue_type":"task","title":"process-feedback: A","parent":"zr-prA"},
			{"id":"zr-other","issue_type":"task","title":"process-feedback: B","parent":"zr-prB"},
			{"id":"zr-nottask","issue_type":"feature","title":"process-feedback: C","parent":"zr-prA"},
			{"id":"zr-nofb","issue_type":"task","title":"some other task","parent":"zr-prA"}
		]`,
		readyWorker: `[]`,
		show: map[string]string{
			"zr-prA": `{"id":"zr-prA","metadata":{"author":"phillipg"}}`,
			"zr-prB": `{"id":"zr-prB","metadata":{"author":"someoneelse"}}`,
		},
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-mine" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("feedback discovery = %+v (want only zr-mine)", got)
	}
}

func TestDiscover_workerLabelFilter(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[]`,
		readyWorker:   `[{"id":"zr-w1"},{"id":"zr-w2"}]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].BeadID != "zr-w1" || got[0].Role.Kind != roles.Worker {
		t.Fatalf("worker discovery = %+v", got)
	}
	// the worker query must carry the native label filters
	a := strings.Join(rr.sawWorkerArgs, " ")
	for _, sub := range []string{"--label worker-ready", "--exclude-label human"} {
		if !strings.Contains(a, sub) {
			t.Errorf("worker bd ready missing %q; got %q", sub, a)
		}
	}
}

func TestDiscover_orderFeedbackThenWorker(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x","parent":"zr-p"}]`,
		readyWorker:   `[{"id":"zr-w"}]`,
		show:          map[string]string{"zr-p": `{"id":"zr-p","metadata":{"author":"phillipg"}}`},
	}
	reg := roles.NewRegistry(config.Default())
	got, _ := Discover(context.Background(), rr, reg, "phillipg")
	if len(got) != 2 || got[0].Role.Kind != roles.Feedback || got[1].Role.Kind != roles.Worker {
		t.Fatalf("order wrong: %+v", got)
	}
}

func TestDiscover_emptySelfLoginErrors(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[]`}
	reg := roles.NewRegistry(config.Default())
	if _, err := Discover(context.Background(), rr, reg, ""); err == nil {
		t.Error("empty selfLogin should error (cannot resolve feedback ownership)")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/discover/` → FAIL.

- [ ] **Step 3: Implement `discover.go`**

```go
// Package discover turns the bead store's ready queue into role→bead dispatches.
// Feedback cycles are owned by self (the parent merge-request bead's author);
// worker beads are filtered natively by bd labels. Order is feedback-first.
package discover

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

type Dispatch struct {
	Role   roles.Role
	BeadID string
}

// Discover returns feedback dispatches (owned by selfLogin) then worker
// dispatches, in priority order. selfLogin must be non-empty.
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry, selfLogin string) ([]Dispatch, error) {
	if selfLogin == "" {
		return nil, fmt.Errorf("discover: empty self_login (cannot resolve feedback ownership)")
	}
	var out []Dispatch
	fb, err := discoverFeedback(ctx, br, reg.Feedback, selfLogin)
	if err != nil {
		return nil, err
	}
	out = append(out, fb...)
	wk, err := discoverWorker(ctx, br, reg.Worker)
	if err != nil {
		return nil, err
	}
	out = append(out, wk...)
	return out, nil
}

func discoverFeedback(ctx context.Context, br beads.Runner, role roles.Role, selfLogin string) ([]Dispatch, error) {
	issues, err := beads.Ready(ctx, br) // bd ready --json --limit 0
	if err != nil {
		return nil, err
	}
	var out []Dispatch
	for _, iss := range issues {
		if iss.Type != "task" || !strings.HasPrefix(iss.Title, "process-feedback:") {
			continue
		}
		if iss.Parent == "" {
			continue
		}
		parent, err := beads.ShowObj(ctx, br, iss.Parent)
		if err != nil {
			return nil, err
		}
		if author, _ := parent.Metadata["author"].(string); author == selfLogin {
			out = append(out, Dispatch{Role: role, BeadID: iss.ID})
		}
	}
	return out, nil
}

func discoverWorker(ctx context.Context, br beads.Runner, role roles.Role) ([]Dispatch, error) {
	issues, err := beads.Ready(ctx, br, "--label", "worker-ready", "--exclude-label", "human")
	if err != nil {
		return nil, err
	}
	var out []Dispatch
	for _, iss := range issues {
		out = append(out, Dispatch{Role: role, BeadID: iss.ID})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/discover/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/discover
git commit -m "feat(pr-pool): bd-ready discovery (feedback ownership + worker label filter) (pg2-spgx)"
```

---

## Task 8: `internal/complete` — completion signal + failure actions

**Files:**

- Create: `packages/pr-pool/internal/complete/complete.go`
- Test: `packages/pr-pool/internal/complete/complete_test.go`

Ports `done_signal` and `wait_done_fail`. The polling loop + re-check-after-death lives in the orchestrator (Task 9), which calls these.

- [ ] **Step 1: Write the failing test** `complete_test.go`

```go
package complete

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestDoneSignal(t *testing.T) {
	cases := []struct {
		name        string
		kind        roles.RoleKind
		status      string
		seenClaimed bool
		want        bool
	}{
		{"feedback closed", roles.Feedback, "closed", false, true},
		{"feedback open not done", roles.Feedback, "open", false, false},
		{"feedback in_progress not done", roles.Feedback, "in_progress", true, false},
		{"worker closed", roles.Worker, "closed", false, true},
		{"worker open after claim = handback done", roles.Worker, "open", true, true},
		{"worker open pre-claim NOT done (startup race)", roles.Worker, "open", false, false},
		{"worker in_progress not done", roles.Worker, "in_progress", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(tc.kind, tc.status, tc.seenClaimed); got != tc.want {
				t.Errorf("DoneSignal(%v,%q,%v) = %v, want %v", tc.kind, tc.status, tc.seenClaimed, got, tc.want)
			}
		})
	}
}

func TestOnFailure_workerAddsHumanNeverUnclaims(t *testing.T) {
	fr := &recRunner{}
	reg := roles.NewRegistry(config.Default())
	if err := OnFailure(context.Background(), fr, reg.Worker, "zr-w1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-w1 --add-label human") {
		t.Errorf("worker failure must add human; calls=%v", fr.calls)
	}
	if fr.has("--status=open") {
		t.Errorf("worker failure must NOT unclaim; calls=%v", fr.calls)
	}
}

func TestOnFailure_feedbackUnclaims(t *testing.T) {
	fr := &recRunner{}
	reg := roles.NewRegistry(config.Default())
	if err := OnFailure(context.Background(), fr, reg.Feedback, "zr-c1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-c1 --status=open --assignee=") {
		t.Errorf("feedback failure must unclaim; calls=%v", fr.calls)
	}
	if fr.has("--add-label human") {
		t.Errorf("feedback failure must NOT add human; calls=%v", fr.calls)
	}
}

type recRunner struct{ calls []string }

func (r *recRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, join(args))
	return "", nil
}
func (r *recRunner) has(sub string) bool {
	for _, c := range r.calls {
		if c == sub {
			return true
		}
	}
	return false
}
func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/complete/` → FAIL.

- [ ] **Step 3: Implement `complete.go`**

```go
// Package complete holds pr-pool's completion semantics: when a dispatched bead
// counts as done, and what to do when it fails. The polling loop lives in the
// orchestrator; this package is the pure decision + the failure side effects.
package complete

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DoneSignal reports whether a bead has completed for the given role.
//   - feedback: done iff status == "closed".
//   - worker:   done iff status == "closed", OR (seenClaimed && status == "open")
//     — the seenClaimed guard prevents a freshly-dispatched, not-yet-claimed
//     "open" bead from being mistaken for a hand-back (the startup race).
func DoneSignal(kind roles.RoleKind, status string, seenClaimed bool) bool {
	if status == "closed" {
		return true
	}
	if kind == roles.Worker && seenClaimed && status == "open" {
		return true
	}
	return false
}

// OnFailure applies the role-specific failure action:
//   - worker:   add the `human` label, NEVER unclaim (a dead worker may hold a
//     half-built worktree; blind retry is unsafe).
//   - feedback: unclaim (status=open, assignee cleared) so the next pass retries.
func OnFailure(ctx context.Context, br beads.Runner, role roles.Role, beadID string) error {
	if role.Kind == roles.Worker {
		return beads.AddHuman(ctx, br, beadID)
	}
	return beads.Unclaim(ctx, br, beadID)
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/complete/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/complete
git commit -m "feat(pr-pool): completion signal + role failure actions (pg2-spgx)"
```

---

## Task 9: `internal/orchestrator` — drive loop, completion polling, drain, teardown

**Files:**

- Create: `packages/pr-pool/internal/orchestrator/orchestrator.go`
- Test: `packages/pr-pool/internal/orchestrator/orchestrator_test.go`

The heart of the port. `workOne` = Ensure → Send(ModeNoWait) → waitDone → (teardown handled by drain). `waitDone` ports the bash polling loop with the **re-check-after-death** behavior, using `ccpool.List` for liveness. `DrainOnce` ports `gated`/`discover`/per-role-cap-drain/`teardownAll`. **No live processes** — tests inject fake `ccpool.Runner` and `beads.Runner`.

Because `waitDone` polls on an interval, the orchestrator takes an injectable clock/poll override so tests run instantly. Use a `pollInterval`/`maxWait` pulled from `Cfg` plus an injectable `sleep func(time.Duration)` defaulting to `time.Sleep`, and an injectable `now func() time.Time`. Keep it minimal: a `tick()` seam.

- [ ] **Step 1: Write the failing test** `orchestrator_test.go`

```go
package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeCC records calls and serves scripted List results.
type fakeCC struct {
	ensured  []string
	sent     []string
	closed   []string
	sendErr  error
	listSeq  [][]ccpool.Session // one entry consumed per List call (last repeats)
	listIdx  int
}

func (f *fakeCC) Ensure(_ context.Context, name, _ string, _ map[string]string) error {
	f.ensured = append(f.ensured, name)
	return nil
}
func (f *fakeCC) Send(_ context.Context, name, _ string, _ ccpool.SendMode) error {
	f.sent = append(f.sent, name)
	return f.sendErr
}
func (f *fakeCC) Cancel(_ context.Context, _ string) error { return nil }
func (f *fakeCC) Close(_ context.Context, name string) error {
	f.closed = append(f.closed, name)
	return nil
}
func (f *fakeCC) List(_ context.Context) ([]ccpool.Session, error) {
	if len(f.listSeq) == 0 {
		return nil, nil
	}
	i := f.listIdx
	if i >= len(f.listSeq) {
		i = len(f.listSeq) - 1
	}
	f.listIdx++
	return f.listSeq[i], nil
}

// scriptBD serves a status sequence per bead id and records update calls.
type scriptBD struct {
	statusSeq map[string][]string
	idx       map[string]int
	updates   []string
	ready     map[string]string // keyed by "feedback"/"worker"
	show      map[string]string
}

func (s *scriptBD) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if contains(args, "--label") {
			return s.ready["worker"], nil
		}
		return s.ready["feedback"], nil
	case "show":
		id := args[1]
		if v, ok := s.show[id]; ok {
			return v, nil
		}
		// status sequence
		if s.idx == nil {
			s.idx = map[string]int{}
		}
		seq := s.statusSeq[id]
		i := s.idx[id]
		if i >= len(seq) {
			i = len(seq) - 1
		}
		s.idx[id]++
		return `{"id":"` + id + `","status":"` + seq[i] + `"}`, nil
	case "update":
		s.updates = append(s.updates, join(args))
	}
	return "", nil
}

func contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}
func join(a []string) string {
	out := ""
	for i, x := range a {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

func newOrch(cc ccpool.Runner, bd *scriptBD, cfg config.Config) *Orchestrator {
	o := &Orchestrator{CC: cc, BD: bd, Reg: roles.NewRegistry(cfg), Cfg: cfg}
	o.sleep = func(time.Duration) {} // instant polling in tests
	return o
}

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	return c
}

// --- waitDone scenarios (ports bats: wait_done cases) ---

func TestWaitDone_workerCloses(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("expected success, got %v; updates=%v", err, bd.updates)
	}
	if len(bd.updates) != 0 {
		t.Errorf("success must not unclaim/human; updates=%v", bd.updates)
	}
}

func TestWaitDone_workerHandbackToOpen(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("handback after seen_claimed should be success, got %v", err)
	}
}

func TestWaitDone_workerTimeoutAddsHumanNoUnclaim(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("timeout should be failure")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") || hasUpdate(bd, "--status=open") {
		t.Errorf("worker timeout must add human and not unclaim; updates=%v", bd.updates)
	}
}

func TestWaitDone_paneDiesAsBeadCloses_success(t *testing.T) {
	// first poll: in_progress + live; second poll: status reads closed AND session not live.
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}},
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateDone}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("bead closed as pane died = success, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("must not flag on race-success; updates=%v", bd.updates)
	}
}

func TestWaitDone_paneDiesStillInProgress_failure(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateFailed}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("dead session + in_progress = failure")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("must add human; updates=%v", bd.updates)
	}
}

func TestWaitDone_feedbackTimeoutUnclaims(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("timeout should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback timeout must unclaim; updates=%v", bd.updates)
	}
}

// --- DrainOnce scenarios (ports bats: drain_once cases) ---

func TestDrainOnce_gatedNoTeardown(t *testing.T) {
	f, _ := writeTemp(t) // creates a sentinel file
	cfg := fastCfg()
	cfg.QuotaPaused = f
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	cc := &fakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background(), "phillipg"); err != nil {
		t.Fatal(err)
	}
	if len(cc.ensured) != 0 || len(cc.closed) != 0 {
		t.Errorf("gated pass must not dispatch or teardown; ensured=%v closed=%v", cc.ensured, cc.closed)
	}
}

func TestDrainOnce_workerCapZeroSkips(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxWorker = 0
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w"}]`}}
	cc := &fakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background(), "phillipg"); err != nil {
		t.Fatal(err)
	}
	for _, n := range cc.ensured {
		if n == "pr-pool-worker-zr-w" {
			t.Errorf("cap=0 should skip worker; ensured=%v", cc.ensured)
		}
	}
}

func TestDrainOnce_capStopsAtOne(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{
		ready:     map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w1"},{"id":"zr-w2"}]`},
		statusSeq: map[string][]string{"zr-w1": {"in_progress", "closed"}, "zr-w2": {"in_progress", "closed"}},
	}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-worker-zr-w1", Live: true}, {Name: "pr-pool-worker-zr-w2", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background(), "phillipg")
	if len(cc.sent) != 1 {
		t.Errorf("MAX_WORKER=1 should dispatch one worker, sent=%v", cc.sent)
	}
}

func TestDrainOnce_noStarvation(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{
		ready: map[string]string{
			"feedback": `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x","parent":"zr-p"}]`,
			"worker":   `[{"id":"zr-w"}]`,
		},
		show:      map[string]string{"zr-p": `{"id":"zr-p","metadata":{"author":"phillipg"}}`},
		statusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}, "zr-w": {"in_progress", "closed"}},
	}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-feedback-processor-zr-c", Live: true}, {Name: "pr-pool-worker-zr-w", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background(), "phillipg")
	if len(cc.sent) != 2 {
		t.Errorf("one of each role should be worked, sent=%v", cc.sent)
	}
}

func TestDrainOnce_teardownReapsStrays(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	// a stray session from a prior crashed run remains in the list
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-worker-zr-stray", Live: true},
		{Name: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background(), "phillipg")
	if !contains(cc.closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must reap pr-pool- strays; closed=%v", cc.closed)
	}
	if contains(cc.closed, "cc-unrelated") {
		t.Errorf("teardown must NOT close non-pr-pool sessions; closed=%v", cc.closed)
	}
}

func TestWorkOne_sendFailFeedbackUnclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{}
	cc := &fakeCC{sendErr: errSend}
	o := newOrch(cc, bd, cfg)
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	_ = o.workOne(context.Background(), d)
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback send-fail must unclaim; updates=%v", bd.updates)
	}
}

func TestWorkOne_sendFailWorkerNotUnclaimed(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{}
	cc := &fakeCC{sendErr: errSend}
	o := newOrch(cc, bd, cfg)
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	_ = o.workOne(context.Background(), d)
	if hasUpdate(bd, "--status=open") {
		t.Errorf("worker send-fail must NOT unclaim; updates=%v", bd.updates)
	}
}

func hasUpdate(bd *scriptBD, sub string) bool {
	for _, u := range bd.updates {
		if u == sub {
			return true
		}
	}
	return false
}
```

> Add these helpers at the bottom of the test file (the `"errors"`/`"os"` imports above are for these):
>
> ```go
> var errSend = errors.New("send failed")
>
> // writeTemp creates a sentinel file and returns its path (+ a no-op cleanup;
> // t.TempDir() handles removal). Used by the gated-pass test.
> func writeTemp(t *testing.T) (string, func()) {
> 	t.Helper()
> 	p := t.TempDir() + "/sentinel"
> 	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
> 		t.Fatal(err)
> 	}
> 	return p, func() {}
> }
> ```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/orchestrator/` → FAIL.

- [ ] **Step 3: Implement `orchestrator.go`**

```go
// Package orchestrator is pr-pool's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Completion is bead-status-based; ccpool state is liveness only.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

type Orchestrator struct {
	CC    ccpool.Runner
	BD    beads.Runner
	Reg   roles.Registry
	Cfg   config.Config
	sleep func(time.Duration) // injectable for instant tests; nil ⇒ time.Sleep
}

func (o *Orchestrator) nap(d time.Duration) {
	if o.sleep != nil {
		o.sleep(d)
		return
	}
	time.Sleep(d)
}

// DrainOnce runs one pass: gate check, discover, drain each role up to its cap,
// teardown all pr-pool sessions. Returns nil even when individual beads fail
// (failures are recorded on the beads via OnFailure), matching the bash.
func (o *Orchestrator) DrainOnce(ctx context.Context, selfLogin string) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil // NOTE: gated exit does NOT teardown (no sessions were created)
	}
	dispatches, err := discover.Discover(ctx, o.BD, o.Reg, selfLogin)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	o.drain(ctx, o.Reg.Feedback, dispatches)
	o.drain(ctx, o.Reg.Worker, dispatches)
	o.teardownAll(ctx)
	return nil
}

func (o *Orchestrator) drain(ctx context.Context, role roles.Role, all []discover.Dispatch) {
	worked := 0
	for _, d := range all {
		if d.Role.Kind != role.Kind {
			continue
		}
		if worked >= role.Cap {
			break
		}
		if err := o.workOne(ctx, d); err != nil {
			slog.Warn("bead flagged", "role", role.Name, "bead", d.BeadID, "err", err)
		} else {
			slog.Info("bead complete", "role", role.Name, "bead", d.BeadID)
		}
		worked++
	}
}

// workOne dispatches a single bead: Ensure a fresh per-bead session, Send the
// nudge (async), then wait for completion. The session is torn down by the
// pass-level teardownAll, not here (so strays are reaped uniformly).
func (o *Orchestrator) workOne(ctx context.Context, d discover.Dispatch) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	env := map[string]string{
		"BEADS_ACTOR":    d.Role.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, name, o.Cfg.RepoRoot, env); err != nil {
		// Could not even create the session. Match the bash (work_one:
		// `ensure_session || return 1`): NO failure action here — the bead was
		// never dispatched, so we do not flag/unclaim it. A transient ccpool
		// launch hiccup must not permanently mark a worker bead `human`.
		return fmt.Errorf("ensure %s: %w", name, err)
	}
	nudge := d.Role.Nudge(d.BeadID, o.Cfg.WorktreeDir)
	if err := o.CC.Send(ctx, name, nudge, ccpool.ModeNoWait); err != nil {
		// J-dispatch-fail: feedback unclaims; worker is left for human inspection.
		if d.Role.Kind == roles.Feedback {
			_ = beads.Unclaim(ctx, o.BD, d.BeadID)
		}
		return fmt.Errorf("send %s: %w", name, err)
	}
	return o.waitDone(ctx, d, name)
}

// waitDone polls the bead status until DoneSignal fires (success) or MAX_WAIT
// elapses / the session dies (failure). On detecting death it re-reads the bead
// status once more before failing (a bead that closed in the same instant the
// session ended is a success). On failure it applies the role's OnFailure.
func (o *Orchestrator) waitDone(ctx context.Context, d discover.Dispatch, name string) error {
	deadline := time.Now().Add(o.Cfg.MaxWait)
	seenClaimed := false
	for time.Now().Before(deadline) {
		status, err := beads.Status(ctx, o.BD, d.BeadID)
		if err != nil {
			return o.fail(ctx, d, fmt.Sprintf("bead status: %v", err))
		}
		if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
			return nil
		}
		if d.Role.Kind == roles.Worker && status == "in_progress" {
			seenClaimed = true
		}
		if !o.live(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			return o.fail(ctx, d, "session exited before completing")
		}
		o.nap(o.Cfg.PollInterval)
	}
	// final status check after the deadline.
	status, _ := beads.Status(ctx, o.BD, d.BeadID)
	if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
		return nil
	}
	return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
}

func (o *Orchestrator) fail(ctx context.Context, d discover.Dispatch, reason string) error {
	_ = complete.OnFailure(ctx, o.BD, d.Role, d.BeadID)
	return fmt.Errorf("%s: %s", d.BeadID, reason)
}

// live reports whether the named session is still alive per ccpool. A session
// that is not Live, or whose store state is "failed", counts as dead. ccpool
// store states done/needs_input (a finished/paused TURN) are normal multi-turn
// operation, NOT death.
func (o *Orchestrator) live(ctx context.Context, name string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume alive; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.Live && s.State != ccpool.StateFailed
		}
	}
	return false // absent ⇒ gone
}

// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior). Sessions outside the prefix are left untouched.
func (o *Orchestrator) teardownAll(ctx context.Context) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		slog.Warn("teardown list failed", "err", err)
		return
	}
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, o.Cfg.SessionPrefix) {
			if err := o.CC.Close(ctx, s.Name); err != nil {
				slog.Warn("teardown close failed", "session", s.Name, "err", err)
			}
		}
	}
}

func (o *Orchestrator) gated() bool {
	if o.Cfg.QuotaPaused != "" && fileExists(o.Cfg.QuotaPaused) {
		return true
	}
	if o.Cfg.CICDDown != "" && fileExists(o.Cfg.CICDDown) {
		return true
	}
	return false
}
```

- [ ] **Step 4: Add `fileExists` helper** in `orchestrator.go`:

```go
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

(add `"os"` to imports)

- [ ] **Step 5: Run tests** — `go test ./internal/orchestrator/` → PASS (all waitDone + drain scenarios).

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/orchestrator
git commit -m "feat(pr-pool): orchestrator drive loop, completion polling, drain, teardown (pg2-spgx)"
```

---

## Task 10: `cmd/pr-pool` — precheck, resolve_self, gating, wiring

**Files:**

- Create: `packages/pr-pool/cmd/pr-pool/drain.go`
- Test: `packages/pr-pool/cmd/pr-pool/drain_test.go`
- Modify: `packages/pr-pool/cmd/pr-pool/main.go` (delete the Task-0 `runDrain` stub)

`resolve_self` shells out to `pg-pr config show --json`; `precheck` asserts the `.beads` prefix and bd reachability. Exit codes follow ccpool's convention (1 generic, 2 usage, ≥3 specific).

- [ ] **Step 1: Write the failing test** `drain_test.go`

```go
package main

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

func TestParseSelfLogin(t *testing.T) {
	got, err := parseSelfLogin([]byte(`{"self_login":"phillipg","worktree_root":"/x"}`))
	if err != nil || got != "phillipg" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParseSelfLogin_empty(t *testing.T) {
	if _, err := parseSelfLogin([]byte(`{"self_login":""}`)); err == nil {
		t.Error("empty self_login should error")
	}
}

func TestPrecheck_prefixMismatch(t *testing.T) {
	// precheck reads the prefix via a beads runner indirection we can fake:
	br := beads.Runner(&fakePrefixRunner{prefix: "wrong"})
	if err := precheckPrefix(context.Background(), br, "zr"); err == nil {
		t.Error("prefix mismatch should fail precheck")
	}
	if err := precheckPrefix(context.Background(), br, "wrong"); err != nil {
		t.Errorf("matching prefix should pass, got %v", err)
	}
}

// fakePrefixRunner answers `bd list --limit 1 --json` ok and exposes a prefix.
type fakePrefixRunner struct{ prefix string }

func (f *fakePrefixRunner) Run(_ context.Context, args ...string) (string, error) {
	return "[]", nil
}
```

> The exact precheck decomposition is up to the implementer, but keep these two pure, testable seams: `parseSelfLogin([]byte) (string, error)` and `precheckPrefix(ctx, beads.Runner, want string) error`. `precheckPrefix` reads `<RepoRoot>/.beads/config.yaml`'s `issue_prefix:` — to keep it testable without disk, read the prefix from a small helper `readBeadsPrefix(repoRoot string) (string, error)` and have `precheckPrefix` take the already-read prefix, OR inject the repo root via a temp dir in the test. Choose the temp-dir approach if simpler; adjust the test accordingly. Do NOT leave `precheckPrefix` untested.

- [ ] **Step 2: Run to verify it fails** — `go test ./cmd/pr-pool/` → FAIL.

- [ ] **Step 3: Implement `drain.go`**

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// Exit codes (ccpool convention: 1 generic, 2 usage, ≥3 specific).
const (
	exitOK          = 0
	exitGeneric     = 1
	exitUsage       = 2
	exitPrecheck    = 3
)

func runDrain(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: pr-pool [drain]")
		return exitUsage
	}
	ctx := context.Background()
	cfg := config.Load()

	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)

	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	selfLogin, err := resolveSelf(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve self:", err)
		return exitPrecheck
	}

	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: roles.NewRegistry(cfg),
		Cfg: cfg,
	}
	if err := o.DrainOnce(ctx, selfLogin); err != nil {
		fmt.Fprintln(os.Stderr, "drain:", err)
		return exitGeneric
	}
	return exitOK
}

// resolveSelf shells out to `pg-pr config show --json` and reads .self_login.
func resolveSelf(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "pg-pr", "config", "show", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("pg-pr config show: %w", err)
	}
	return parseSelfLogin(out)
}

func parseSelfLogin(b []byte) (string, error) {
	var cfg struct {
		SelfLogin string `json:"self_login"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("parse pg-pr config: %w", err)
	}
	if cfg.SelfLogin == "" {
		return "", fmt.Errorf("self_login is empty")
	}
	return cfg.SelfLogin, nil
}

// precheck asserts the bead store is the expected one and bd is reachable.
func precheck(ctx context.Context, cfg config.Config, br beads.Runner) error {
	if _, err := os.Stat(cfg.RepoRoot + "/.beads"); err != nil {
		return fmt.Errorf("no .beads under %s", cfg.RepoRoot)
	}
	prefix, err := readBeadsPrefix(cfg.RepoRoot)
	if err != nil {
		return err
	}
	if prefix != cfg.BeadsPrefix {
		return fmt.Errorf("bead prefix %q != expected %q", prefix, cfg.BeadsPrefix)
	}
	if _, err := br.Run(ctx, "list", "--limit", "1", "--json"); err != nil {
		return fmt.Errorf("bd unreachable: %w", err)
	}
	return nil
}

func precheckPrefix(_ context.Context, _ beads.Runner, _ string) error { return nil } // see note

// readBeadsPrefix reads issue_prefix from <repoRoot>/.beads/config.yaml.
func readBeadsPrefix(repoRoot string) (string, error) {
	f, err := os.Open(repoRoot + "/.beads/config.yaml")
	if err != nil {
		return "", fmt.Errorf("open beads config: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "issue_prefix:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "issue_prefix:")), nil
		}
	}
	return "", fmt.Errorf("issue_prefix not found in .beads/config.yaml")
}
```

> RECONCILE the test seam with the implementation: the Task-10 test references `precheckPrefix(ctx, beads.Runner, want)`. Either (a) make `precheckPrefix` real by having it call `readBeadsPrefix` against a repo root the test sets up in a temp dir (preferred — delete the stub above and give `precheckPrefix` a repoRoot param), or (b) drop the `precheckPrefix` test and instead test `readBeadsPrefix` + the prefix comparison directly. Pick one and make the test green with a genuine assertion. Do not ship the no-op stub.

- [ ] **Step 4: Delete the Task-0 stub** in `main.go` (remove the temporary `func runDrain(_ []string) int { return 0 }`).

- [ ] **Step 5: Run tests** — `go test ./cmd/pr-pool/` → PASS. Then `go test ./...` from `packages/pr-pool` → all packages PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/cmd/pr-pool
git commit -m "feat(pr-pool): cmd wiring — precheck, resolve_self, gating, drain dispatch (pg2-spgx)"
```

---

## Task 11: Full gate + ccpool-contract coordination

**Files:**

- Modify: bead comments only (no code). Optionally `docs/superpowers/specs/2026-06-11-pr-pool-go-port-design.md` (record the resolved argv contract).

- [ ] **Step 1: Run the full gate**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-go-impl
cd packages/pr-pool && gofmt -l . && go vet ./... && go test ./... && cd ../..
nix build .#pr-pool --no-link 2>&1 | tail -5     # runs go test via doCheck
nix flake check 2>&1 | tail -30
```

Expected: `gofmt -l` prints nothing (all formatted), `go vet` clean, `go test ./...` all PASS, `nix build .#pr-pool` succeeds, `nix flake check` green.

- [ ] **Step 2: Pin the ccpool argv contract into the blocker beads.** So the separate ccpool agent implements flags pr-pool actually emits, add a comment to each of `pg2-7mnq.{2,3,4}` stating the exact tokens (use the `env -u BEADS_DIR -u WORKSPACE bd comment …` prefix):
  - `pg2-7mnq.2` (N1 env): pr-pool emits `ccpool new <name> --cwd <dir> --env KEY=VAL` (repeatable, keys sorted). Three keys: `BEADS_ACTOR`, `BEADS_DIR`, `WORKSPACE_ROOT`.
  - `pg2-7mnq.3` (N2 launch flags): pr-pool emits `--dangerously-skip-permissions` and `--effort <value>` (default `max`) and `--model <value>` (only when set) on `ccpool new`. ccpool must accept these as `new` flags (the design's "per-new flags" option).
  - `pg2-7mnq.4` (N3 list --json): pr-pool calls `ccpool list --all --json` and unmarshals an array of `{"name","state","live","transcript_path"}`. Confirm these exact JSON field names + that `live` is a separate boolean (not folded into state) and `--all` includes terminal/reaped rows.

- [ ] **Step 3: Commit** (if the design doc was updated)

```bash
git add docs/superpowers/specs/2026-06-11-pr-pool-go-port-design.md
git commit -m "docs(pr-pool): record resolved ccpool argv contract (pg2-spgx)"
```

---

## Task 12: Live verification + bash retirement — BLOCKED on pg2-7mnq

> **DO NOT START until `pg2-7mnq.{2,3,4}` are merged into the impl branch's base.** These steps require a real ccpool that accepts `--env`, the launch flags, and `list --json`. Tracked separately; the orchestration above is fully validated by mocks without them.

- [ ] **Step 1: Rebase/merge** the impl branch onto a `main` that includes the ccpool N1/N2/N3 enhancements. Re-run the full gate (Task 11 Step 1).

- [ ] **Step 2: Live smoke** via the Go `pr-pool` against the `zr` store (same shape as the `zr-lweh.*` run). Confirm: ccpool dispatch with `--dangerously-skip-permissions`/`--effort max`, env injection (`BEADS_ACTOR`/`BEADS_DIR`/`WORKSPACE_ROOT` visible to the worker), completion via bead status, `human` on a forced failure, and clean teardown. **Confirm the live target with the user first** (it mutates real beads + spawns claude).

- [ ] **Step 3: Retire the bash** (exact unwiring, verified against flake.nix):
  - delete `packages/pgii-pack-pr-support/pack-src/scripts/pr-pool.sh`
  - delete `packages/pgii-pack-pr-support/pack-src/scripts/tests/pr-pool.bats`
  - delete the `test-pgii-pack-pr-support-bats` flake check block (flake.nix ~488–501) — the only nix reference to `pr-pool.bats`. The `check-pgii-pack-pr-support-layout` block does NOT assert `pr-pool.sh` exists, so it needs no edit. Confirm with `grep -rnE 'pr-pool\.(sh|bats)' flake.nix` returning nothing after the deletion.

- [ ] **Step 4: Final gate** — `nix flake check` green; `git commit` the retirement.

- [ ] **Step 5: Close `pg2-spgx`** with a summary; update `pg2-y991` (chunk B, blocked-by this) to ready.

---

## Self-Review (run against the spec)

**Spec coverage** — every design unit maps to a task:

- Option-1 ccpool shell-out behind one interface → Tasks 5/6. ✅
- one-for-one capability port (discovery, roles, caps, completion, failure actions, teardown) → Tasks 3/4/7/8/9. ✅
- fresh-session-per-bead, name = `pr-pool-<role>-<beadid>` → Task 4 (SessionName) + Task 9 (workOne). ✅
- async Send + bead-poll completion + ccpool-state-for-liveness + re-check-after-death → Task 9 (waitDone). ✅
- local bd runner with env scrub → Task 2. ✅
- precheck/resolve_self shell-out → Task 10. ✅
- gated (no teardown) → Task 9 (DrainOnce/gated). ✅
- three ccpool enhancements as the argv contract + coordination → Task 6 (emit) + Task 11 (pin to beads). ✅
- B-seams (Cancel present, TranscriptPath surfaced, worker-nudge budget point) → Task 5 (Cancel + TranscriptPath) + Task 4 (nudge). ✅ (budget LINE interpolation is chunk B; the nudge function already takes params, so the seam exists.)
- bash retirement → Task 12 (blocked). ✅
- mkGoApp packaging + flake wiring + gate → Tasks 0/11. ✅

**Bats scenario coverage** — every portable bats case has a Go test: discover feedback ownership (Task 7), worker label filter (Task 7), caps/cap=0/no-starvation (Task 9), done_signal feedback+worker+seen_claimed (Task 8), wait_done close/timeout-unclaim/timeout-human (Task 9), pane-dies-as-closes success both roles (Task 9), pane-dies-still-in-progress failure (Task 9), send-fail unclaim/no-unclaim (Task 9), teardown reaps strays (Task 9), nudge contract substrings (Task 4). Dropped-by-design (now ccpool's): wait_ready glyph, submit_line split-Enter, claude_rename, clear_context, ensure_session tmux env — these are validated by ccpool's own tests, not pr-pool's.

**Placeholder scan** — two intentional, flagged seams: the Task-0 `runDrain` stub (deleted in Task 10 Step 4) and the Task-10 `precheckPrefix` reconciliation note (the implementer must make it a genuine test, not the no-op). No other TODO/TBD/"add error handling" placeholders.

**Type consistency** — signatures match the Shared types section: `beads.Runner.Run(ctx, ...string)(string,error)`; `ccpool.Runner` 5 methods; `roles.Role.SessionName(prefix, beadID)` / `.Nudge(beadID, worktreeDir)`; `complete.DoneSignal(RoleKind, status, seenClaimed)`; `discover.Dispatch{Role, BeadID}`; `orchestrator.Orchestrator{CC, BD, Reg, Cfg}` + `.DrainOnce(ctx, selfLogin)`. Consistent across tasks.
