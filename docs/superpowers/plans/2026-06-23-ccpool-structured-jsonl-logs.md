# ccpool structured JSONL diagnostics + observability logSource — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-yvnp` (P2, labels `has-acceptance-criteria`, `observability`). Dep `pg2-45ab.3` (cross-flake stubs) is CLOSED.

**Goal:** Replace ccpool's free-form `hook.log` diagnostic writes with a structured JSONL logger emitting lowercase `time`/`level`/`msg` to `${XDG_STATE_HOME}/ccpool/diagnostics.jsonl`, and register `phillipgreenii.observability.logSources.ccpool` from ccpool's darwin module so the running otelcol stack tails that file into Loki.

**Architecture:** A new tiny package `internal/diaglog` owns a structured JSONL writer (mirroring `internal/eventlog`'s per-write `O_APPEND` + nil-safe-no-op conventions, but a DISTINCT domain: `eventlog`=domain transitions/inputs in `events.jsonl`; `diaglog`=operator diagnostics in `diagnostics.jsonl`). `cmd/ccpool/hook.go`'s `logHook` is rewritten to emit one JSON line `{"time":...,"level":...,"msg":...}` instead of plain text. The otelcol filelog receiver (declared out-of-repo in `phillipgreenii-nix-support-apps`) parses `attributes.time` (gotime RFC3339) as the timestamp and `attributes.level` via a `severity_parser`; `serviceName` defaults to the attr key → Loki `service_name="ccpool"`. The reap launchd job keeps its own `StandardOutPath`/`StandardErrorPath` (already on `reap.out.log`/`reap.err.log`, NOT the tailed JSONL) — verified-as-correct, not changed.

**Tech Stack:** Go 1.25 (stdlib only — `encoding/json` + `time`; NO new module deps, so no `gomod2nix.toml` change). Nix (home-manager + nix-darwin modules under `phillipgreenii.*`). Repo: `phillipgreenii-nix-agent-support` (package `packages/ccpool` + `darwin/modules/ccpool`).

**Branch:** `ccpool-jsonl-logs` (off `main`).

---

### Design decisions locked before coding

- **Filename = `diagnostics.jsonl`, NOT `hook.log`.** The bead says "to `${XDG_STATE_HOME}/ccpool/*.jsonl`". The filelog default `path` glob is `${env:XDG_STATE_HOME}/<name>/*.jsonl` (`phillipgreenii-nix-support-apps/darwin/modules/observability/registration.nix:85`). That glob ALSO matches `events.jsonl` (the domain event log, schema `ts`/`name`/`kind` — no `level`). To keep the two domains distinct (scope instruction) AND avoid feeding the domain log through the diagnostics severity parser, the ccpool darwin module OVERRIDES `path` to the exact diagnostics file: `${env:XDG_STATE_HOME}/ccpool/diagnostics.jsonl`. `events.jsonl` is deliberately NOT ingested.
- **eventlog stays untouched.** `internal/eventlog/eventlog.go` and `Config.EventLogPath()` are read-only references here. Do not modify them.
- **Never-fail policy preserved.** Like `logHook` today and `eventlog`, the diagnostic writer is best-effort: a write error is swallowed; a nil `*Logger` is a no-op. A wedged hook must never block Claude (spec §9/§15).
- **`level` values are lowercase** (`error`, `warn`, `info`) — matches the bead's "lowercase" AC and the otelcol `severity_parser` (case-insensitive, but lowercase is the convention in `phillipgreenii-nix-support-apps/CLAUDE.md` §"Adding a New Package" item 7). Every existing `hook.log` call site is a diagnostic FAILURE, so they all map to `level:error`.

---

### Task 1: New `internal/diaglog` package — structured JSONL diagnostic writer

**Files:**

- Create: `packages/ccpool/internal/diaglog/diaglog.go`
- Create: `packages/ccpool/internal/diaglog/diaglog_test.go`

- [ ] **Step 1: Write the failing test**

Create `packages/ccpool/internal/diaglog/diaglog_test.go`:

```go
package diaglog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLog_writesLowercaseTimeLevelMsgJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.jsonl")
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Date(2026, 6, 23, 14, 30, 0, 0, time.UTC)
	if err := lg.Log(ts, "error", "hook stop: store open: boom"); err != nil {
		t.Fatalf("Log: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Decode into a generic map so we assert the EXACT lowercase keys/values
	// the otelcol json_parser + severity_parser depend on.
	var got map[string]any
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing \n
		t.Fatalf("unmarshal %q: %v", b, err)
	}
	if got["time"] != "2026-06-23T14:30:00Z" {
		t.Errorf("time = %v, want RFC3339 UTC 2026-06-23T14:30:00Z", got["time"])
	}
	if got["level"] != "error" {
		t.Errorf("level = %v, want error", got["level"])
	}
	if got["msg"] != "hook stop: store open: boom" {
		t.Errorf("msg = %v, want the diagnostic text", got["msg"])
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Errorf("line must end in a newline (JSONL): %q", b)
	}
}

func TestLog_appendsOneLinePerCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.jsonl")
	lg, _ := Open(path)
	ts := time.Unix(0, 0).UTC()
	_ = lg.Log(ts, "error", "first")
	_ = lg.Log(ts, "warn", "second")
	b, _ := os.ReadFile(path)
	lines := 0
	for _, c := range b {
		if c == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("got %d lines, want 2 (one JSON object per Log call)", lines)
	}
}

func TestNilLogger_isNoOp(t *testing.T) {
	var lg *Logger // nil
	if err := lg.Log(time.Now(), "error", "ignored"); err != nil {
		t.Errorf("nil Logger.Log must be a no-op returning nil, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/diaglog/ -v`
Expected: FAIL — build error `package .../internal/diaglog: no Go files` / `undefined: Open` (the package does not exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `packages/ccpool/internal/diaglog/diaglog.go`:

```go
// Package diaglog is an append-only JSONL OPERATOR-DIAGNOSTIC log: one line per
// diagnostic event, schema {"time":"<RFC3339 UTC>","level":"<lowercase>","msg":"<text>"}.
//
// This is DISTINCT from internal/eventlog (events.jsonl), which records the
// ordered DOMAIN sequence of state transitions and input actions
// (ts/name/kind/...). diaglog replaces the old free-form <state-dir>/hook.log
// plain-text diagnostics so the otelcol filelog receiver can parse it: the
// receiver maps attributes.time -> log timestamp (gotime RFC3339) and
// attributes.level -> OTel severity (severity_parser), then ships it to Loki as
// service_name="ccpool". See phillipgreenii-nix-support-apps
// darwin/modules/observability/registration.nix + darwin/services/otelcol/config.yaml.nix.
//
// Canonical location: <state-dir>/diagnostics.jsonl (beside events.jsonl and the
// old hook.log). One JSON object per line.
//
// Writes use O_APPEND|O_CREATE|O_WRONLY and the fd is NOT held open between
// writes: O_APPEND keeps the small single-line writes atomic across the
// concurrent hook/reply processes that share a pool. A nil *Logger is a valid
// no-op so callers can treat it as an optional dependency, mirroring the
// never-fail diagnostics policy of the hook (spec §9/§15).
package diaglog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// entry is one JSONL line. The lowercase json tags time/level/msg are LOAD-BEARING:
// the otelcol json_parser reads attributes.time and the severity_parser reads
// attributes.level by exactly those names.
type entry struct {
	Time  string `json:"time"`  // RFC3339, UTC
	Level string `json:"level"` // lowercase: error | warn | info
	Msg   string `json:"msg"`
}

// Logger appends JSONL entries. The fd is opened per-write (O_APPEND), never
// held; the mutex serializes in-process writes. Methods are nil-safe.
type Logger struct {
	path string
	mu   sync.Mutex
}

// Open ensures the parent dir exists (0o700) and records the path. It does NOT
// open the file — Log opens it per-write so cross-process O_APPEND stays atomic.
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir diaglog parent: %w", err)
	}
	return &Logger{path: path}, nil
}

// Log writes one entry as a single JSON line + "\n". ts is passed explicitly
// (no time.Now inside the package) for deterministic tests; it is normalized to
// RFC3339 UTC. Nil-safe no-op.
func (l *Logger) Log(ts time.Time, level, msg string) error {
	if l == nil {
		return nil
	}
	line, err := json.Marshal(entry{Time: ts.UTC().Format(time.RFC3339), Level: level, Msg: msg})
	if err != nil {
		return fmt.Errorf("marshal diag entry: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open diag log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write diag entry: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/diaglog/ -v`
Expected: PASS (`TestLog_writesLowercaseTimeLevelMsgJSONL`, `TestLog_appendsOneLinePerCall`, `TestNilLogger_isNoOp`).

- [ ] **Step 5: Commit**

```bash
git add internal/diaglog/diaglog.go internal/diaglog/diaglog_test.go
git commit -m "feat(ccpool): add internal/diaglog structured JSONL diagnostic writer"
```

---

### Task 2: Add `DiagLogPath()` to config (beside events.jsonl)

**Files:**

- Modify: `packages/ccpool/internal/config/config.go:176-178` (add accessor after `EventLogPath`)
- Test: `packages/ccpool/internal/config/config_test.go:31-33` (extend the existing pool-mode assertion)

- [ ] **Step 1: Write the failing test**

In `packages/ccpool/internal/config/config_test.go`, inside `TestLoad_poolMode`, immediately after the existing `EventLogPath` assertion (currently lines 31-33), add:

```go
	if c.DiagLogPath() != filepath.Join(poolCanon, "diagnostics.jsonl") {
		t.Errorf("DiagLogPath = %q, want diagnostics.jsonl beside events.jsonl", c.DiagLogPath())
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestLoad_poolMode -v`
Expected: FAIL — compile error `c.DiagLogPath undefined (type Config has no field or method DiagLogPath)`.

- [ ] **Step 3: Add the accessor**

In `packages/ccpool/internal/config/config.go`, after the `EventLogPath` method (currently ending at line 178), add:

```go

// DiagLogPath is the active pool's append-only JSONL operator-diagnostic log
// (<state-dir>/diagnostics.jsonl), sitting beside events.jsonl and replacing the
// old plain-text hook.log. See internal/diaglog; tailed into Loki by the otelcol
// filelog receiver registered in darwin/modules/ccpool.
func (c Config) DiagLogPath() string { return filepath.Join(c.StateDir, "diagnostics.jsonl") }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/config/ -v`
Expected: PASS (all existing config tests still green; `TestLoad_poolMode` now also checks `DiagLogPath`).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(ccpool): add Config.DiagLogPath (diagnostics.jsonl beside events.jsonl)"
```

---

### Task 3: Allow `diagnostics.jsonl` in the pool-dir file allowlist

**Why:** In pool-dir mode `StateDir == Root`, so the diagnostic log lands inside the pool directory. `ValidatePoolDir` (`pool.go:113-124`) rejects a pool dir containing any file not on the `poolFileOK` allowlist (`pool.go:97-107`), which `reap-all`'s GC uses to detect a "foreign" dir. Without adding `diagnostics.jsonl`, the first diagnostic write would make the pool dir fail validation and get unregistered.

**Files:**

- Modify: `packages/ccpool/internal/config/pool.go:99`
- Test: `packages/ccpool/internal/config/pool_test.go` (extend the existing valid-files list)

- [ ] **Step 1: Write the failing test**

In `packages/ccpool/internal/config/pool_test.go`, the test that lists valid pool files currently includes `"hook.log", "events.jsonl"` in its `for _, f := range []string{...}` slice (the first such loop). Add `"diagnostics.jsonl"` to that slice, e.g.:

```go
	for _, f := range []string{"config.toml", "store.db", "store.db-wal", "store.db-shm", "store.db-journal", "alpha.lock", "beta.lock", "hook.log", "events.jsonl", "diagnostics.jsonl"} {
```

(Match the exact existing slice contents in that test; the only change is appending `"diagnostics.jsonl"`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/config/ -run Pool -v`
Expected: FAIL — `ValidatePoolDir` returns `not a ccpool pool dir: ... contains diagnostics.jsonl` (the file is not yet on the allowlist).

- [ ] **Step 3: Add the file to the allowlist**

In `packages/ccpool/internal/config/pool.go`, change line 99 from:

```go
	case name == "config.toml", name == "hook.log", name == "events.jsonl":
```

to:

```go
	case name == "config.toml", name == "hook.log", name == "events.jsonl", name == "diagnostics.jsonl":
```

(Keep `hook.log` on the list: a leftover from before the JSONL migration must not break GC of an existing pool dir.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/pool.go internal/config/pool_test.go
git commit -m "feat(ccpool): allow diagnostics.jsonl in the pool-dir file allowlist"
```

---

### Task 4: Rewrite `hook.go` `logHook` to emit structured JSONL

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/hook.go:260-268` (the `logHook` function), `:5-17` (imports)
- Test: `packages/ccpool/cmd/ccpool/hook_test.go` (add a focused test)

- [ ] **Step 1: Write the failing test**

Add to `packages/ccpool/cmd/ccpool/hook_test.go`:

```go
func TestLogHook_writesStructuredJSONLAtErrorLevel(t *testing.T) {
	dir := t.TempDir()
	logHook(dir, "hook stop: store open: boom")
	b, err := os.ReadFile(filepath.Join(dir, "diagnostics.jsonl"))
	if err != nil {
		t.Fatalf("read diagnostics.jsonl: %v", err)
	}
	var got struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing \n
		t.Fatalf("logHook wrote non-JSON %q: %v", b, err)
	}
	if got.Level != "error" {
		t.Errorf("level = %q, want error (every old hook.log line was a failure)", got.Level)
	}
	if got.Msg != "hook stop: store open: boom" {
		t.Errorf("msg = %q, want the diagnostic text", got.Msg)
	}
	if got.Time == "" {
		t.Error("time must be set (RFC3339)")
	}
}
```

Ensure `hook_test.go` imports `encoding/json`, `os`, `path/filepath`, `testing` (add any that are missing to its import block).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestLogHook_writesStructuredJSONL -v`
Expected: FAIL — `logHook` still writes plain text to `hook.log`, so `diagnostics.jsonl` does not exist (`read diagnostics.jsonl: ... no such file or directory`).

- [ ] **Step 3: Rewrite `logHook`**

In `packages/ccpool/cmd/ccpool/hook.go`, replace the entire `logHook` function (currently lines 260-268):

```go
func logHook(stateDir, msg string) {
	_ = os.MkdirAll(stateDir, 0o700)
	f, err := os.OpenFile(filepath.Join(stateDir, "hook.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, msg)
}
```

with:

```go
// logHook appends one structured JSONL diagnostic line to
// <state-dir>/diagnostics.jsonl. Every logHook call records a hook FAILURE, so
// the level is always "error". Best-effort: an Open/write error is swallowed (a
// wedged hook must never block Claude — spec §9/§15), mirroring the prior
// hook.log behavior. The lowercase time/level/msg schema is what the otelcol
// filelog receiver parses (see internal/diaglog).
func logHook(stateDir, msg string) {
	lg, err := diaglog.Open(filepath.Join(stateDir, "diagnostics.jsonl"))
	if err != nil {
		return
	}
	_ = lg.Log(time.Now(), "error", msg)
}
```

Update the import block (`hook.go:3-17`): add `"github.com/phillipgreenii/ccpool/internal/diaglog"` to the import list. `time` is already imported. `fmt`, `os`, and `filepath` remain in use elsewhere in the file (do not remove them — `fmt.Errorf`/`os.Stdin`/`os.Getenv`/`filepath` are still referenced), but if `go vet` flags any now-unused import after the edit, remove only the genuinely-unused one.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestLogHook -v`
Expected: PASS.

- [ ] **Step 5: Run the full cmd/ccpool package + vet (catch import drift)**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ && go vet ./cmd/ccpool/`
Expected: PASS, no vet diagnostics.

- [ ] **Step 6: Commit**

```bash
git add cmd/ccpool/hook.go cmd/ccpool/hook_test.go
git commit -m "feat(ccpool): emit structured JSONL diagnostics from hook logHook"
```

---

### Task 5: Point `ccpool doctor`'s diagnostic tail at `diagnostics.jsonl`

**Why:** `doctor.go` prints the configured diagnostic-log path and tails it for the operator. It currently references `hook.log`; after Task 4 nothing writes `hook.log`, so the doctor tail would always be empty/misleading.

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/doctor.go:24-25` (header), `:71-76` (tail)

- [ ] **Step 1: Update the doctor header line**

In `packages/ccpool/cmd/ccpool/doctor.go`, replace `doctorPoolHeader` (lines 19-26) body so it reports `diagnostics.jsonl` via the new accessor. Change:

```go
	return fmt.Sprintf("pool: %s\n  db:     %s\n  socket: %s\n  hook.log: %s\n  events.jsonl: %s\n",
		root, cfg.DBPath, cfg.Tmux.Socket, filepath.Join(cfg.StateDir, "hook.log"), cfg.EventLogPath())
```

to:

```go
	return fmt.Sprintf("pool: %s\n  db:     %s\n  socket: %s\n  diagnostics.jsonl: %s\n  events.jsonl: %s\n",
		root, cfg.DBPath, cfg.Tmux.Socket, cfg.DiagLogPath(), cfg.EventLogPath())
```

- [ ] **Step 2: Update the doctor tail**

In `packages/ccpool/cmd/ccpool/doctor.go`, replace the tail block (lines 71-76):

```go
	// hook.log tail.
	logPath := filepath.Join(cfg.StateDir, "hook.log")
	if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
		fmt.Println("--- recent hook.log ---")
		fmt.Print(string(tailBytes(b, 2000)))
	}
```

with:

```go
	// diagnostics.jsonl tail (structured JSONL; printed raw so an operator sees
	// the recent error lines verbatim).
	logPath := cfg.DiagLogPath()
	if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 {
		fmt.Println("--- recent diagnostics.jsonl ---")
		fmt.Print(string(tailBytes(b, 2000)))
	}
```

`filepath` may become unused in `doctor.go` after this edit (it was used by `filepath.Join(home, ".claude.json")` at line 45 — that call REMAINS, so `filepath` stays in use). Leave the import block as-is.

- [ ] **Step 3: Verify the package compiles + tests pass**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ && go vet ./cmd/ccpool/`
Expected: PASS, no vet diagnostics.

- [ ] **Step 4: Commit**

```bash
git add cmd/ccpool/doctor.go
git commit -m "refactor(ccpool): point doctor diagnostic tail at diagnostics.jsonl"
```

---

### Task 6: Register `phillipgreenii.observability.logSources.ccpool` from the darwin module

**Why:** The `observability.logSources.<name>` option is declared at darwin/system scope in `phillipgreenii-nix-support-apps` (`darwin/modules/observability/registration.nix:73-118`), with a cross-flake stub in that flake's `flake.nix` `crossFlakeOptionStubs` (dep `pg2-45ab.3`, CLOSED) that lets sibling modules type-check standalone. ccpool's `darwin/modules/ccpool/default.nix` mirrors `darwin/modules/pr-pool/default.nix` exactly: register from darwin (NOT home-manager — setting it from HM targets an undeclared option and fails eval), guarded on `obs.enable or false`. The default `path` glob (`${env:XDG_STATE_HOME}/<name>/*.jsonl`) would also match `events.jsonl`, so we OVERRIDE `path` to the exact diagnostics file to keep the domain event log out of the diagnostics pipeline.

**Files:**

- Modify: `packages/../darwin/modules/ccpool/default.nix` (`/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/darwin/modules/ccpool/default.nix`)

- [ ] **Step 1: Add the `obs` binding to the `let` block**

In `darwin/modules/ccpool/default.nix`, add an `obs` binding to the existing `let ... in` (after the `stateHome` binding, currently line 27, before the closing `in` at line 28):

```nix
  obs = config.phillipgreenii.observability;
```

- [ ] **Step 2: Register the logSource via `lib.mkMerge`**

The module currently has a single `config = lib.mkIf reapEnabledByAnyUser { ... };` (lines 30-55). Wrap it (and the new registration) in `lib.mkMerge` so the logSource registration is independent of the reap toggle. Replace the `config = lib.mkIf reapEnabledByAnyUser {` line and its matching closing `};` (the outer `config` block, lines 30 and 55) so the structure becomes:

```nix
  config = lib.mkMerge [
    # phillipgreenii.observability.logSources is declared at darwin/system scope
    # in phillipgreenii-nix-support-apps (darwin/modules/observability/
    # registration.nix), so this lives in darwin, not the home-manager module —
    # setting it from HM targets an undeclared option and fails eval (same
    # reasoning as pr-pool's registration). A cross-flake stub in that flake's
    # flake.nix lets it type-check standalone in agent-support CI (pg2-45ab.3).
    #
    # ccpool writes its structured diagnostic log to
    # ${XDG_STATE_HOME}/ccpool/diagnostics.jsonl (time/level/msg JSONL; see
    # internal/diaglog). The default glob ${env:XDG_STATE_HOME}/ccpool/*.jsonl
    # would ALSO match events.jsonl (the DOMAIN event log, no `level` field), so
    # `path` is pinned to the exact diagnostics file to keep that domain log out
    # of the diagnostics->severity pipeline. Guarded on obs.enable so it is a
    # no-op on machines without the stack.
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.logSources.ccpool.path =
        "\${env:XDG_STATE_HOME}/ccpool/diagnostics.jsonl";
    })

    (lib.mkIf reapEnabledByAnyUser {
      phillipgreenii.system.launchdServices.userAgents.ccpool-reap = {
        label = "com.phillipg.ccpool-reap";
        script = ''
          exec ${pkg}/bin/ccpool reap-all
        '';
        runAtLoad = true;
        # `ccpool reap-all` is a periodic short task (StartInterval), not a long-running
        # daemon — it does its work and exits. keepAlive defaults to true in the
        # helper, which would make launchd RESTART it on every exit (a ~10s respawn
        # loop). Disable keepAlive so StartInterval is the only re-trigger (runs at
        # load, then every `interval` seconds), and exempt it from the health check
        # (which expects state=running, which a one-shot never reaches).
        keepAlive = false;
        healthCheck = false;
        serviceConfig = {
          StartInterval = interval; # the periodic re-trigger
          # Surface runtime failures: the agent is keepAlive-off and health-check
          # exempt, so without logs a crashing reap run would be silent. launchd
          # creates the parent dir (~/.local/state/ccpool) if it is missing.
          # Mirrors pa-monitor's stateHome logging pattern.
          #
          # NOTE: these reap stdout/stderr paths are DELIBERATELY their own files
          # (reap.err.log / reap.out.log), NOT the tailed diagnostics.jsonl — the
          # JSONL log is ccpool's structured diagnostics, not launchd's raw
          # process output (pg2-yvnp AC: reap StandardOutPath/StandardErrorPath
          # stay on their own non-tailed paths).
          StandardErrorPath = "${stateHome}/ccpool/reap.err.log";
          StandardOutPath = "${stateHome}/ccpool/reap.out.log";
        };
      };
    })
  ];
```

(The reap block content is byte-identical to today's except for the added NOTE comment; the only structural change is wrapping the two `lib.mkIf` blocks in `lib.mkMerge [ ... ]`.)

- [ ] **Step 3: Format the nix file**

Run (from repo root): `nix fmt darwin/modules/ccpool/default.nix`
Expected: file reformatted (or already-formatted); no errors.

- [ ] **Step 4: Verify the darwin module evaluates (well-formedness)**

Run (from repo root): `nix flake check 2>&1 | tail -30`
Expected: PASS — no eval error referencing `logSources` or `darwin/modules/ccpool`. (The `logSources` option reference is lazy in `darwinModules.default`; it resolves against the cross-flake stub / real declaration only when a consumer system imports it — exactly as pr-pool's existing registration already passes.)

- [ ] **Step 5: Commit**

```bash
git add darwin/modules/ccpool/default.nix
git commit -m "feat(ccpool): register observability.logSources.ccpool (diagnostics.jsonl)"
```

---

### Task 7: Full hermetic verification + repo checks

- [ ] **Step 1: Full Go test + vet**

Run: `cd packages/ccpool && go test ./... && go vet ./...`
Expected: all PASS, no vet diagnostics. (No new module deps were added, so `go.mod`/`gomod2nix.toml` are unchanged.)

- [ ] **Step 2: Manual smoke — a hook failure produces a parseable JSONL error line**

Run:

```bash
cd packages/ccpool
TMP="$(mktemp -d)"
# Force a config-load failure path: point CCPOOL_POOL at a non-creatable pool so
# runHook hits logHook. Simpler: drive logHook through the doctor tail by writing
# a line via a tiny throwaway, OR just assert the unit test artifact:
go test ./internal/diaglog/ ./cmd/ccpool/ -run 'JSONL|Diag' -v
```

Expected: PASS. (Optional deeper smoke: `printf '{}' | CCPOOL_POOL=/nonexistent/parent/pool go run ./cmd/ccpool hook stop; cat /nonexistent.../diagnostics.jsonl` is non-hermetic; the unit tests are the hermetic proof.)

- [ ] **Step 3: Confirm reap launchd paths are unchanged (AC #3)**

Run (from repo root): `git diff main -- darwin/modules/ccpool/default.nix | grep -E 'StandardOutPath|StandardErrorPath'`
Expected: the diff shows the `StandardOutPath`/`StandardErrorPath` lines are CONTEXT (unchanged) — still `reap.out.log` / `reap.err.log`, NOT `diagnostics.jsonl`.

- [ ] **Step 4: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`):

```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```

Expected: both PASS. (No `gomod2nix.toml` change — no new deps. If a pre-commit hook config changed, re-run `nix run .#install-pre-commit-hooks` first — it did NOT here, so this is a no-op.)

- [ ] **Step 5: Close the bead**

```bash
bd update pg2-yvnp --claim   # if not already claimed
bd comment pg2-yvnp "Hermetic AC done: internal/diaglog emits time/level/msg JSONL to \${XDG_STATE_HOME}/ccpool/diagnostics.jsonl (replaces free-form hook.log); doctor tail + pool allowlist updated; darwin module registers observability.logSources.ccpool with path pinned to diagnostics.jsonl (events.jsonl stays distinct); reap StandardOut/ErrorPath unchanged on reap.{out,err}.log. Live Loki check (AC #4) is non-hermetic — operator runbook in the plan."
# Do NOT close yet if the operator wants AC #4 (live Loki) verified first; see Task 8.
```

---

### Task 8: MANUAL OPERATOR VERIFICATION (non-hermetic) — NOT a code step

> This task is the bead's AC #4. It REQUIRES the running observability stack (otelcol → Loki) on a machine that imports both ccpool's darwin module AND `phillipgreenii-nix-support-apps`'s observability module with `phillipgreenii.observability.enable = true` and `signals.logs.enable = true`. An agentic worker CANNOT complete this hermetically — hand it to the operator (Phillip). Do NOT mark it via a code change.

**Operator runbook:**

1. **Build & switch** the darwin host so the new ccpool + the otelcol filelog receiver are live:

   ```bash
   sudo darwin-rebuild switch --flake <your-machine-flake>
   ```

   Confirm the collector picked up the new receiver:

   ```bash
   grep -A6 'filelog/ccpool' "${XDG_STATE_HOME:-$HOME/.local/state}/.../otelcol-config.yaml" 2>/dev/null \
     || launchctl print gui/$(id -u)/com.phillipg... | grep -i otelcol
   ```

   Expected: a `filelog/ccpool` receiver whose `include` is `${env:XDG_STATE_HOME}/ccpool/diagnostics.jsonl`, with a `json_parser` (`timestamp.parse_from: attributes.time`, gotime `2006-01-02T15:04:05Z07:00`) and a `severity.parse_from: attributes.level`.

2. **Write a `level:error` diagnostic line** as ccpool. Either trigger a real hook failure, or append one directly (the filelog `start_at: beginning` + json_parser will ingest it):

   ```bash
   printf '{"time":"%s","level":"error","msg":"pg2-yvnp manual loki verification"}\n' \
     "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     >> "${XDG_STATE_HOME:-$HOME/.local/state}/ccpool/diagnostics.jsonl"
   ```

3. **Query Loki** (the logs signal endpoint is `127.0.0.1:<signals.logs.port>` per `config.yaml.nix:86-87`; default Grafana explore or `logcli`):

   ```bash
   logcli query --addr=http://127.0.0.1:<loki-http-port> \
     '{service_name="ccpool"} |= "pg2-yvnp manual loki verification"'
   ```

   (Or in Grafana → Explore → Loki, run `{service_name="ccpool"}`.)

   **PASS criteria (all three):**
   - The line appears under the **`service_name="ccpool"`** stream label (proves the `add` operator stamped `resource["service.name"] = "ccpool"`).
   - Its **severity is `error`** (proves the `severity_parser` mapped `level:error` → OTel `ERROR`; in Grafana the log level shows red/`error`).
   - Its **timestamp matches the `time` field** (proves the gotime layout parse, not ingestion time).

4. **Confirm `events.jsonl` is NOT ingested**: query `{service_name="ccpool"}` and verify no lines with `kind=transition`/`kind=input` appear (the `path` override excludes the domain event log).

5. **Record the result** on the bead and close:
   ```bash
   bd comment pg2-yvnp "AC #4 (live) verified: level:error line queryable in Loki as service_name=\"ccpool\" with severity=error and time-field timestamp; events.jsonl correctly NOT ingested."
   bd close pg2-yvnp
   ```

---

## Self-review checklist (done while writing)

- **Spec coverage (4 ACs):**
  - AC1 (JSONL `time`/`level`/`msg` lowercase to `${XDG_STATE_HOME}/ccpool/*.jsonl`, replaces free-form hook.log) → Tasks 1 (writer), 2 (path), 4 (logHook rewrite), 5 (doctor tail). Lands at `diagnostics.jsonl`.
  - AC2 (darwin module sets `observability.logSources.ccpool`, stub-backed, evals standalone) → Task 6 (mirrors pr-pool exactly; guarded on `obs.enable`; cross-flake stub from closed `pg2-45ab.3`; verified via `nix flake check` in Task 6 Step 4 / Task 7 Step 4).
  - AC3 (reap `StandardOutPath`/`StandardErrorPath` stay on own non-tailed paths) → Task 6 keeps them byte-identical on `reap.{out,err}.log`; Task 7 Step 3 asserts the diff shows them unchanged.
  - AC4 (live Loki query, non-hermetic) → Task 8 operator runbook (NOT a code step), exactly as instructed.
- **eventlog left distinct & untouched:** `internal/eventlog`/`events.jsonl` only referenced for contrast; `path` override in Task 6 keeps it out of the diagnostics pipeline. No edits to `eventlog.go` or `EventLogPath`.
- **No placeholders:** every code/nix step shows the real current code and the exact replacement (verified against the live files at the cited line ranges, 2026-06-23).
- **Type/name consistency:** package `diaglog`, type `Logger`, method `Log(ts, level, msg)`, accessor `Config.DiagLogPath()`, filename `diagnostics.jsonl`, flag none — used identically across Tasks 1, 2, 4, 5. JSON keys `time`/`level`/`msg` match the otelcol `json_parser`/`severity_parser` field names (`attributes.time`/`attributes.level`).
- **No new deps:** stdlib only → `go.mod`/`gomod2nix.toml` untouched → `nix flake check` needs no `gomod2nix generate`.
- **Frequent commits:** one commit per task (7 code/nix commits + bead close).

```

```
