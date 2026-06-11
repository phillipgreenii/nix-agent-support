# ccpool Claude-Code Contract Test Harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A local, on-demand Go test suite (behind `//go:build contract`) that drives the **real** `claude` binary through `ccpool` to pin the Claude Code TUI contract and localize drift after upgrades.

**Architecture:** New build-tagged test files in `packages/ccpool/cmd/ccpool/` (same `package main` as the existing integration tests, so they reuse those helpers). A sandbox helper gives each test an isolated XDG config/store + a unique tmux socket, the binary-under-test on `$PATH`, and the repo's source plugin (whose `hooks.json` calls bare `ccpool`). Tests drive ccpool to a target phase via pane-polling gates, then record one of four **machine-distinguishable** outcomes (`OUTCOME=` lines): live-pass, baseline (pins the _currently observed_ value), pending (`t.Skip`, harvested), scaffold-fail (the harness's own driving broke). A `go test -json` classifier summarizes the run.

**Tech Stack:** Go (`testing`, build tags), tmux (`capture-pane`/`send-keys`), sqlite3 (store fixtures), jq (classifier), nix (`ccpool-contract` entrypoint). Real `claude` (uses the real `$HOME` for OAuth).

**Spec:** `docs/superpowers/specs/2026-06-11-ccpool-contract-test-harness-design.md`
**Bead:** `pg2-k6rt`. **Worktree/branch:** `.claude/worktrees/ccpool-contract-harness-spec` / `worktree-ccpool-contract-harness-spec`.

---

## Key facts the engineer needs (verified)

- ccpool config is TOML at `$XDG_CONFIG_HOME/ccpool/config.toml`; store is sqlite at `$XDG_DATA_HOME/ccpool/store.db`; runtime/locks under `$XDG_RUNTIME_DIR/ccpool`. Defaults: `[tmux] socket="ccpool" prefix="cc-"`, `[notify] adapter="desktop"`. (`internal/config/config.go`)
- Subcommands: `new <name> [--cwd d] [--model m]`, `reply <name> <prompt> [--no-wait|--queue-message|--interrupt]`, `cancel <name>`, `close <name> [--purge]`, `attend [--include-done]`, `attach <name>`, `list [--all] [--state s]`, `doctor`, `hook <start|stop|fail|notify>`, `version`.
- Exit codes: `reply` busy→**5**, needs_input→2, failed→3, timeout→4; `cancel` unconfirmed→**6**, generic→1. (`cmd/ccpool/cancel.go:47-56`, `reply.go:91-114`)
- The tmux session name is `<prefix><name>` (default `cc-<name>`). ccpool launches `claude` (config `[claude] bin`, default `claude`) via `tmux -L <socket> new-session`. The session inherits the env of the ccpool process that first starts that socket's server — so exporting XDG + PATH on the ccpool invocation propagates into claude and its hooks.
- The **source** plugin `packages/ccpool/ccpool-plugin/hooks/hooks.json` calls bare `ccpool hook …` (PATH-relative). So: put the binary-under-test on `$PATH` and set `[claude] plugin_dir` to the source plugin dir. Hooks map: start→ready, stop→done, fail→failed, notify(permission_prompt|idle_prompt)→needs_input. (`cmd/ccpool/hook.go:25-30`)
- `new`/`reply` block until the SessionStart hook advances the store generation (proven to fire in the `/tmp/cc-t9` prototype under a sandboxed XDG).
- `doctor` prints one line per session containing `state= live= cwd_trusted= uuid=`; `state=` is the **cached** store row (not reconciled).
- Real `claude` needs the real `$HOME` (OAuth). Therefore **do not** override `HOME`. Consequence: `ccpool`'s truster writes a folder-trust entry to the real `~/.claude.json` for the sandbox cwd. Accept + document; run serially.
- Observed phase rendering (CONTRACT-SENSITIVE — these strings are what SCAFFOLD-FAIL guards):
  - thinking: a spinner line like `✽ Musing… (8s · ↓ 68 tokens · thinking with xhigh effort)`.
  - thinking→stream transition: a completed-thinking line `… for <N>s` (e.g. `Thought for 25s`, `Cooked for 19s`) then assistant prose prefixed `⏺`.
  - streaming interrupt marker: `⎿ Interrupted · What should Claude do instead?`.
  - thinking interrupt: NO marker; double-Escape rewinds to the welcome screen + restores the prompt into the input box (`Ctrl+Y to paste deleted text`).
  - idle prompt box: a line `❯` with the bottom status bar `… | <name> | … | <version>` and no spinner.

---

## File structure

- Create `packages/ccpool/cmd/ccpool/contract_harness_test.go` — `//go:build contract`. TestMain (build once), `sandbox` type + `newSandbox`, `ccp`, `cap`, `setState`, phase gates, outcome helpers, phase-pattern constants.
- Create `packages/ccpool/cmd/ccpool/contract_test.go` — `//go:build contract`. The `TestContract_*` scenarios.
- Create `packages/ccpool/contract/classify.jq` — turns `go test -json` into the four-bucket summary.
- Create `packages/ccpool/contract/README.md` — how to run, what the outcomes mean.
- Modify `flake.nix` — add a `ccpool-contract` app/devShell script.

All test files share `package main` with `integration_test.go`/`new_integration_test.go` and may call their exported-within-package helpers, but this plan defines its own to stay self-contained.

---

## Task 1: Build-tagged skeleton + TestMain (build once) + environment guard

**Files:**

- Create: `packages/ccpool/cmd/ccpool/contract_harness_test.go`
- Test: same file (the smoke test `TestContract_GuardAndBuild`)

- [ ] **Step 1: Write the skeleton with the build tag, TestMain, and a guard**

```go
//go:build contract

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// builtBin is the binary under test, built once. CCPOOL_BIN overrides it with an
// already-built (e.g. installed) binary so the suite can test the shipped ccpool.
var (
	builtBin  string
	buildOnce sync.Once
	buildErr  error
)

func TestMain(m *testing.M) {
	// Hard requirements; without them every scenario is meaningless.
	for _, tool := range []string{"tmux", "claude", "sqlite3"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(os.Stderr, "contract suite skipped: %q not on PATH\n", tool)
			os.Exit(0) // skip cleanly, not a failure
		}
	}
	os.Exit(m.Run())
}

// ccpoolBin returns the path to the binary under test, building it once.
func ccpoolBin(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("CCPOOL_BIN"); env != "" {
		return env
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ccpool-contract-bin-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "ccpool")
		if out, e := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); e != nil {
			buildErr = fmt.Errorf("build ccpool: %v\n%s", e, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return builtBin
}

func TestContract_GuardAndBuild(t *testing.T) {
	bin := ccpoolBin(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("ccpool version: %v\n%s", err, out)
	}
	t.Logf("binary under test: %s (version %s)", bin, out)
}
```

- [ ] **Step 2: Verify it builds and runs only under the tag**

Run: `cd packages/ccpool && go vet ./... && go test -tags contract -run TestContract_GuardAndBuild ./cmd/ccpool/ -v`
Expected: PASS, logs the binary path + version. Also confirm exclusion:
Run: `go build ./... && go test -run TestContract_GuardAndBuild ./cmd/ccpool/`
Expected: `testing: warning: no tests to run` (the file is tag-excluded).

- [ ] **Step 3: Verify the nix build is unaffected**

Run: `nix build .#ccpool 2>&1 | tail -5`
Expected: builds (the tag keeps `contract_*` out of the default `checkPhase`; `vendorHash` unchanged — no new deps).

- [ ] **Step 4: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_harness_test.go
git commit -m "test(ccpool): contract harness skeleton + TestMain build-once + guard"
```

---

## Task 2: `sandbox` — isolated XDG + socket + plugin + PATH

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_harness_test.go`

- [ ] **Step 1: Add the sandbox type and constructor**

```go
// sandbox is one isolated ccpool world: temp XDG dirs, a unique tmux socket, the
// repo's source plugin (hooks call bare `ccpool`), the binary-under-test on PATH.
// Real HOME is preserved (claude needs it for OAuth); the price is a folder-trust
// write to the real ~/.claude.json for cwd — accepted and documented.
type sandbox struct {
	t       *testing.T
	bin     string            // binary under test
	socket  string            // unique tmux socket
	prefix  string            // tmux session name prefix
	cwd     string            // claude project dir (trusted on first launch)
	env     []string          // env for every ccpool invocation
}

const contractModel = "claude-opus-4-8" // pinned: exposes high (xhigh) reasoning effort

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	bin := ccpoolBin(t)

	base := t.TempDir()
	mustMkdir := func(p string) string { _ = os.MkdirAll(p, 0o755); return p }
	cfgHome := mustMkdir(filepath.Join(base, "cfg"))
	dataHome := mustMkdir(filepath.Join(base, "data"))
	stateHome := mustMkdir(filepath.Join(base, "state"))
	runDir := mustMkdir(filepath.Join(base, "run"))
	cwd := mustMkdir(filepath.Join(base, "work"))
	binDir := mustMkdir(filepath.Join(base, "bin"))

	// Put the binary-under-test on PATH so the plugin's `ccpool hook …` resolves to it.
	if err := os.Symlink(bin, filepath.Join(binDir, "ccpool")); err != nil {
		t.Fatalf("symlink ccpool onto PATH: %v", err)
	}

	// Unique socket derived from the tempdir (crash-safe, collision-free).
	socket := "cc-contract-" + filepath.Base(base)
	prefix := "cct-"

	// Source plugin dir (hooks.json uses bare `ccpool`). Resolve from repo root.
	repoRoot, err := filepath.Abs("../..") // cmd/ccpool -> packages/ccpool
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(repoRoot, "ccpool-plugin")

	cfg := "" +
		"[pool]\nmax_sessions = 6\nidle_ttl = \"30m\"\n\n" +
		"[tmux]\nsocket = \"" + socket + "\"\nprefix = \"" + prefix + "\"\n\n" +
		"[claude]\nplugin_dir = \"" + pluginDir + "\"\ndefault_cwd = \"" + cwd + "\"\n" +
		"default_model = \"" + contractModel + "\"\nbin = \"claude\"\n\n" +
		"[wait]\ntimeout = \"5m\"\n\n" +
		"[notify]\nadapter = \"none\"\non = []\n"
	cfgDir := mustMkdir(filepath.Join(cfgHome, "ccpool"))
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+cfgHome,
		"XDG_DATA_HOME="+dataHome,
		"XDG_STATE_HOME="+stateHome,
		"XDG_RUNTIME_DIR="+runDir,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		// HOME deliberately NOT overridden: real claude needs it for OAuth.
	)

	sb := &sandbox{t: t, bin: bin, socket: socket, prefix: prefix, cwd: cwd, env: env}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	return sb
}
```

- [ ] **Step 2: Add a fast smoke test using fake-claude (no tokens) to prove sandbox + hook wiring**

This validates the sandbox/PATH/plugin/socket/teardown mechanics deterministically, before spending tokens on real claude. It overrides `[claude] bin` to the stub by writing a second config — simplest is a dedicated helper:

```go
// withFakeClaude rewrites the sandbox config to launch the fake-claude stub
// instead of real claude (deterministic, token-free mechanics check).
func (sb *sandbox) withFakeClaude() {
	sb.t.Helper()
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		sb.t.Fatal(err)
	}
	// place fake-claude next to the symlinked ccpool, on PATH
	binDir := filepath.Dir(strings.SplitN(sb.envGet("PATH"), string(os.PathListSeparator), 2)[0])
	fake := filepath.Join(binDir, "fake-claude")
	if err := os.WriteFile(fake, src, 0o755); err != nil {
		sb.t.Fatal(err)
	}
	cfgPath := filepath.Join(sb.envGet("XDG_CONFIG_HOME"), "ccpool", "config.toml")
	b, _ := os.ReadFile(cfgPath)
	out := strings.Replace(string(b), `bin = "claude"`, `bin = "`+fake+`"`, 1)
	_ = os.WriteFile(cfgPath, []byte(out), 0o600)
	// fake-claude calls back via CCPOOL_BIN to fire `ccpool hook start`.
	sb.env = append(sb.env, "CCPOOL_BIN="+sb.bin)
}

func (sb *sandbox) envGet(key string) string {
	for i := len(sb.env) - 1; i >= 0; i-- {
		if strings.HasPrefix(sb.env[i], key+"=") {
			return strings.TrimPrefix(sb.env[i], key+"=")
		}
	}
	return ""
}
```

Add `"strings"` to imports. Then the smoke test:

```go
func TestContract_Sandbox_FakeClaudeReachesReady(t *testing.T) {
	sb := newSandbox(t)
	sb.withFakeClaude()
	out, code := sb.ccp("new", "alpha")
	if code != 0 {
		t.Fatalf("new exit=%d: %s", code, out)
	}
	out, _ = sb.ccp("list")
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "ready") {
		t.Fatalf("list missing alpha/ready: %s", out)
	}
}
```

(`ccp` is added in Task 3 — order Task 3 before running this. Mark this step's run after Task 3.)

- [ ] **Step 3: Commit (after Task 3 makes it runnable)**

```bash
git add packages/ccpool/cmd/ccpool/contract_harness_test.go
git commit -m "test(ccpool): contract sandbox (isolated XDG/socket/plugin/PATH) + fake-claude smoke"
```

---

## Task 3: `ccp`, `cap`, `setState` helpers

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_harness_test.go`

- [ ] **Step 1: Add the command/pane/store helpers**

```go
// ccp runs the binary-under-test with the sandbox env, returning combined output
// and exit code. Never fatals on a non-zero exit (callers assert on the code).
func (sb *sandbox) ccp(args ...string) (string, int) {
	sb.t.Helper()
	cmd := exec.Command(sb.bin, args...)
	cmd.Env = sb.env
	cmd.Dir = sb.cwd
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		sb.t.Fatalf("ccp %v: %v\n%s", args, err, out)
	}
	return string(out), code
}

// ccpTimed runs ccp but fails the test if it does not return within budget
// (objective "does not hang" check). Returns output, exit code, elapsed.
func (sb *sandbox) ccpTimed(budget time.Duration, args ...string) (string, int, time.Duration) {
	sb.t.Helper()
	type res struct {
		out  string
		code int
	}
	ch := make(chan res, 1)
	start := time.Now()
	go func() { o, c := sb.ccp(args...); ch <- res{o, c} }()
	select {
	case r := <-ch:
		return r.out, r.code, time.Since(start)
	case <-time.After(budget):
		sb.t.Fatalf("ccp %v did not return within %s (hang)", args, budget)
		return "", 0, 0
	}
}

// cap captures the tmux pane for session <prefix><name>.
func (sb *sandbox) cap(name string) string {
	sb.t.Helper()
	out, _ := exec.Command("tmux", "-L", sb.socket, "capture-pane", "-t", sb.prefix+name, "-p").Output()
	return string(out)
}

// setState writes a store-row state directly (fixture for picker/unconfirmed cases).
func (sb *sandbox) setState(name, state string) {
	sb.t.Helper()
	db := filepath.Join(sb.envGet("XDG_DATA_HOME"), "ccpool", "store.db")
	q := fmt.Sprintf("update sessions set state='%s' where name='%s';", state, name)
	if out, err := exec.Command("sqlite3", db, q).CombinedOutput(); err != nil {
		sb.t.Fatalf("setState: %v\n%s", err, out)
	}
}
```

Add `"time"` to imports.

- [ ] **Step 2: Run the Task-2 fake-claude smoke now that `ccp` exists**

Run: `cd packages/ccpool && go test -tags contract -run TestContract_Sandbox_FakeClaudeReachesReady ./cmd/ccpool/ -v`
Expected: PASS (proves sandbox + PATH + source plugin hook callback + socket + teardown work, token-free).

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_harness_test.go
git commit -m "test(ccpool): contract ccp/ccpTimed/cap/setState helpers"
```

---

## Task 4: Four-outcome helpers (machine-distinguishable)

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_harness_test.go`

- [ ] **Step 1: Add the outcome helpers**

```go
// Outcome helpers emit a single machine-greppable line per judgement so a
// go-test-json classifier can bucket a run. go test only has PASS/FAIL/SKIP, so
// baseline-drift, scaffold-fail, and live-regression would otherwise be
// indistinguishable reds.

// liveAssert is an objective check that MUST hold; failing it is a real regression.
func liveAssert(t *testing.T, desc string, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("OUTCOME=live-fail test=%q desc=%q got=%v want=%v", t.Name(), desc, got, want)
		return
	}
	t.Logf("OUTCOME=live test=%q desc=%q ok=%v", t.Name(), desc, got)
}

// baseline pins the CURRENTLY OBSERVED value (not the desired one). Any drift —
// including a future fix — fails loudly and locates itself. The set of baseline
// calls in code IS the expected-deferred manifest.
func baseline(t *testing.T, bead, desc string, got, wantObserved any) {
	t.Helper()
	if got != wantObserved {
		t.Errorf("OUTCOME=baseline-drift bead=%s test=%q desc=%q got=%v wasObserved=%v (re-triage: behaviour changed)",
			bead, t.Name(), desc, got, wantObserved)
		return
	}
	t.Logf("OUTCOME=baseline bead=%s test=%q desc=%q observed=%v", bead, t.Name(), desc, got)
}

// pending records a check we cannot make until observability exists, then SKIPS.
// MUST be the last call in a test so it never short-circuits a live assert.
func pending(t *testing.T, desc, obsNeeded string) {
	t.Helper()
	t.Skipf("OUTCOME=pending test=%q desc=%q needs=%q", t.Name(), desc, obsNeeded)
}

// scaffoldFail marks the harness's own driving as broken (e.g. pane-rendering
// drift), NOT a verdict on the command under test.
func scaffoldFail(t *testing.T, format string, a ...any) {
	t.Helper()
	t.Fatalf("OUTCOME=scaffold test=%q msg=%q", t.Name(), fmt.Sprintf(format, a...))
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd packages/ccpool && go test -tags contract -run TestContract_GuardAndBuild ./cmd/ccpool/ -v`
Expected: PASS (compilation of the new helpers is exercised).

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_harness_test.go
git commit -m "test(ccpool): four machine-distinguishable outcome helpers"
```

---

## Task 5: Phase gates (active-diff + thinking/streaming/idle/needs_input)

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_harness_test.go`

- [ ] **Step 1: Add phase-pattern constants and gates**

```go
// CONTRACT-SENSITIVE patterns. When a Claude Code upgrade changes pane rendering,
// these stop matching and the gates SCAFFOLD-FAIL (telling you to update them) —
// they are never used for correctness assertions, only to DRIVE to a phase.
var (
	// thinking spinner line, e.g. "✽ Musing… (8s · ↓ 68 tokens · thinking with xhigh effort)"
	reThinking = regexp.MustCompile(`thinking with|· ↓ \d+ tokens|\) +· +thinking`)
	// completed-thinking ("Thought for 25s" / "Cooked for 19s") or assistant bullet "⏺"
	reStreaming = regexp.MustCompile(`\b\w+ for \d+s\b|⏺`)
	// streaming-interrupt marker
	reInterrupted = regexp.MustCompile(`Interrupted`)
)

// active reports whether a turn is producing output: two captures 0.8s apart differ
// (the spinner animates during thinking; prose grows during streaming).
func (sb *sandbox) active(name string) bool {
	a := sb.cap(name)
	time.Sleep(800 * time.Millisecond)
	return a != sb.cap(name)
}

// waitUntil polls fn every 400ms up to budget; scaffoldFails on timeout.
func (sb *sandbox) waitUntil(what string, budget time.Duration, fn func(pane string) bool) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if fn(sb.cap("")) { // unused arg form; real call below passes name
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "phase %q never observed within %s (pane rendering may have changed)", what, budget)
}

// waitForThinking blocks until the thinking spinner is visible, else SCAFFOLD-FAIL.
func (sb *sandbox) waitForThinking(name string, budget time.Duration) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if reThinking.MatchString(sb.cap(name)) {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "thinking phase never observed for %q within %s", name, budget)
}

// waitForStreaming blocks until assistant prose/completed-thinking is visible.
func (sb *sandbox) waitForStreaming(name string, budget time.Duration) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		p := sb.cap(name)
		if reStreaming.MatchString(p) && !reThinking.MatchString(p) {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "streaming phase never observed for %q within %s", name, budget)
}

// waitForNeedsInput blocks until the store row reaches needs_input.
func (sb *sandbox) waitForNeedsInput(name string, budget time.Duration) {
	sb.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if sb.rowState(name) == "needs_input" {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	scaffoldFail(sb.t, "needs_input never reached for %q within %s", name, budget)
}

// rowState reads the cached store state for a session.
func (sb *sandbox) rowState(name string) string {
	out, _ := sb.ccp("list", "--all")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}
```

Add `"regexp"` to imports. (Delete the unused `waitUntil` stub — it is replaced by the typed gates; kept here only to illustrate the polling shape. Remove it before commit.)

- [ ] **Step 2: Verify compile + the thinking gate against real claude (the first token spend)**

```go
func TestContract_PhaseGate_ThinkingObserved(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "p"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "p", thinkingPrompt, "--no-wait")
	sb.waitForThinking("p", 30*time.Second) // scaffoldFails if not seen
	liveAssert(t, "thinking observed", true, true)
}
```

Add near the constants:

```go
// thinkingPrompt reliably produces a multi-second high-effort thinking phase.
const thinkingPrompt = "Think step by step in extensive detail, then write a thorough 1500-word essay on the internal architecture of Unix pipes."
```

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -run TestContract_PhaseGate_ThinkingObserved ./cmd/ccpool/ -v`
Expected: PASS within ~30s; `OUTCOME=live` logged.

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_harness_test.go
git commit -m "test(ccpool): contract phase gates (thinking/streaming/needs_input + active-diff)"
```

---

## Task 6: Lifecycle scenarios

**Files:**

- Create: `packages/ccpool/cmd/ccpool/contract_test.go`

- [ ] **Step 1: Write the lifecycle scenarios**

```go
//go:build contract

package main

import (
	"strings"
	"testing"
	"time"
)

func TestContract_Lifecycle_NewReachesReadyAndLive(t *testing.T) {
	sb := newSandbox(t)
	out, code, _ := sb.ccpTimed(90*time.Second, "new", "alpha")
	liveAssert(t, "new exit code", code, 0)
	liveAssert(t, "new reports ready", strings.Contains(out, "ready"), true)
	out, _ = sb.ccp("doctor")
	liveAssert(t, "doctor shows alpha live", strings.Contains(out, "alpha") && strings.Contains(out, "live=true"), true)
	pending(t, "state is RECONCILED ready (doctor state= is cached)", "reconciled state query")
}

func TestContract_Lifecycle_CloseEndsSession(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "alpha"); code != 0 {
		t.Fatalf("setup new failed")
	}
	_, code, _ := sb.ccpTimed(20*time.Second, "close", "alpha")
	liveAssert(t, "close exit code", code, 0)
	// Objective: the tmux session is gone.
	out, _ := sb.ccp("doctor")
	liveAssert(t, "alpha not live after close", strings.Contains(out, "alpha") && strings.Contains(out, "live=true"), false)
}

func TestContract_Lifecycle_ClosePurgeRemovesRow() {
}
```

Replace the empty `ClosePurge` stub with:

```go
func TestContract_Lifecycle_ClosePurgeRemovesRow(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "alpha"); code != 0 {
		t.Fatalf("setup new failed")
	}
	if _, code := sb.ccp("close", "alpha", "--purge"); code != 0 {
		t.Fatalf("close --purge failed")
	}
	out, _ := sb.ccp("list", "--all")
	liveAssert(t, "alpha purged from list", strings.Contains(out, "alpha"), false)
}
```

- [ ] **Step 2: Run**

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -run 'TestContract_Lifecycle' ./cmd/ccpool/ -v`
Expected: 2 PASS + 1 SKIP (the `pending`).

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract lifecycle scenarios (new/close/purge)"
```

---

## Task 7: Cancel scenarios (the headline area)

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_test.go`

- [ ] **Step 1: Write the cancel scenarios**

```go
func TestContract_Cancel_StreamingInterrupts(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "s"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "s", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("s", 90*time.Second) // scaffoldFails if streaming never starts
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "s")
	liveAssert(t, "cancel during streaming exits 0", code, 0)
	pending(t, "session reaches reconciled idle after cancel", "reconciled state query")
}

func TestContract_Cancel_ThinkingIsUnconfirmed(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "k"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "k", thinkingPrompt, "--no-wait")
	sb.waitForThinking("k", 30*time.Second)
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "k")
	// BASELINE: today thinking-cancel cannot be confirmed -> exit 6. Pinning the
	// observed value means a future fix (exit 0) trips this and forces re-triage.
	baseline(t, "pg2-33gl", "cancel during thinking exit code", code, 6)
	pending(t, "thinking cancel should reach idle + exit 0", "reconciled state query / cancel fix")
}

func TestContract_Cancel_IdleNormalizes(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "i"); code != 0 {
		t.Fatalf("new failed")
	}
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "i") // ready/idle session
	liveAssert(t, "cancel on idle exits 0", code, 0)
}

func TestContract_Cancel_NonexistentErrors(t *testing.T) {
	sb := newSandbox(t)
	_, code := sb.ccp("cancel", "ghost")
	liveAssert(t, "cancel nonexistent is non-zero", code != 0, true)
}

func TestContract_Cancel_StaleMarkerFalsePositive(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "m"); code != 0 {
		t.Fatalf("new failed")
	}
	// Produce a real streaming interrupt so the pane retains "Interrupted".
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("m", 90*time.Second)
	sb.ccp("cancel", "m") // pane now shows "Interrupted"
	// Start a fresh thinking turn; force the row to working so cancel bursts.
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForThinking("m", 30*time.Second)
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "m")
	// BASELINE: the stale "Interrupted" line false-positives interruptLanded ->
	// thinking-cancel wrongly exits 0. Expected to FLIP to 6 (or idle) once fixed.
	baseline(t, "pg2-33gl", "stale-marker thinking cancel (false positive) exit code", code, 0)
}
```

- [ ] **Step 2: Run**

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -run 'TestContract_Cancel' ./cmd/ccpool/ -v`
Expected: live asserts PASS; baselines PASS (observed values match: thinking→6, stale→0); 1 SKIP. If a baseline FAILS, the contract changed — re-triage `pg2-33gl`.

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract cancel scenarios (streaming/thinking/idle/stale-marker)"
```

---

## Task 8: Send + interrupt scenarios

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_test.go`

- [ ] **Step 1: Write the scenarios**

```go
func TestContract_Send_BusyRefused(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "b"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "b", thinkingPrompt, "--no-wait")
	sb.waitForThinking("b", 30*time.Second)
	_, code := sb.ccp("reply", "b", "second message") // no flags -> ModeRefuseIfBusy
	baseline(t, "n/a", "reply on busy session exit code", code, 5)
}

func TestContract_Send_NoWaitReturnsImmediately(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "n"); code != 0 {
		t.Fatalf("new failed")
	}
	_, code, elapsed := sb.ccpTimed(20*time.Second, "reply", "n", thinkingPrompt, "--no-wait")
	liveAssert(t, "--no-wait exit 0", code, 0)
	liveAssert(t, "--no-wait returns under 15s (does not block on the turn)", elapsed < 15*time.Second, true)
	pending(t, "row is 'working' after --no-wait", "reconciled state query")
}

func TestContract_Interrupt_ThinkingAborts(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "x"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "x", thinkingPrompt, "--no-wait")
	sb.waitForThinking("x", 30*time.Second)
	out, code, _ := sb.ccpTimed(20*time.Second, "reply", "x", "PROBE_MUST_NOT_DELIVER", "--interrupt")
	// BASELINE: interrupt during thinking cannot confirm the cancel -> aborts -> exit 1.
	baseline(t, "pg2-33gl", "reply --interrupt during thinking exit code", code, 1)
	liveAssert(t, "interrupt abort does not paste the probe", strings.Contains(sb.cap("x"), "PROBE_MUST_NOT_DELIVER"), false)
	_ = out
	pending(t, "interrupt should carry a distinct exit code, not generic 1", "distinct interrupt exit code (exit-code-1-is-general-error)")
}
```

- [ ] **Step 2: Run**

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -run 'TestContract_Send|TestContract_Interrupt' ./cmd/ccpool/ -v`
Expected: live + baseline PASS; SKIPs for pending.

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract send/interrupt scenarios (busy/no-wait/interrupt-abort)"
```

---

## Task 9: Attend scenarios (picker — mostly live via fixtures)

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_test.go`

- [ ] **Step 1: Write the scenarios**

```go
// attendFixture stands up N live sessions and sets their store states.
func attendFixture(t *testing.T, sb *sandbox, states map[string]string) {
	t.Helper()
	for name := range states {
		if _, code := sb.ccp("new", name); code != 0 {
			t.Fatalf("fixture new %q failed", name)
		}
	}
	for name, st := range states {
		sb.setState(name, st) // both a row AND a live pane (filter drops paneless rows)
	}
}

func TestContract_Attend_NoTTYListsCandidates(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"q1": "needs_input", "q2": "needs_input", "q3": "done"})
	out, code := sb.ccp("attend") // stdin is not a TTY under go test
	liveAssert(t, "attend no-TTY exit 0", code, 0)
	liveAssert(t, "lists q1", strings.Contains(out, "q1"), true)
	liveAssert(t, "lists q2", strings.Contains(out, "q2"), true)
	liveAssert(t, "excludes done q3", strings.Contains(out, "q3"), false)
}

func TestContract_Attend_IncludeDone(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"q1": "needs_input", "q3": "done"})
	out, code := sb.ccp("attend", "--include-done")
	liveAssert(t, "attend --include-done exit 0", code, 0)
	liveAssert(t, "includes done q3", strings.Contains(out, "q3"), true)
}

func TestContract_Attend_ZeroCandidates(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"r1": "ready"})
	out, code := sb.ccp("attend")
	liveAssert(t, "attend zero exit 0", code, 0)
	liveAssert(t, "says none waiting", strings.Contains(out, "no sessions waiting"), true)
}

func TestContract_Attend_NumberedAndFzfBranchSelection(t *testing.T) {
	pending(t, "numbered/fzf TTY branch selection (stdinIsTerminal/LookPath)", "attend.go injection refactor for testable branch selection")
}
```

- [ ] **Step 2: Run**

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -run 'TestContract_Attend' ./cmd/ccpool/ -v`
Expected: live asserts PASS; 1 SKIP.

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract attend scenarios (no-TTY/include-done/zero + pending branch-select)"
```

---

## Task 10: needs_input (AskUserQuestion) + reap scenarios

**Files:**

- Modify: `packages/ccpool/cmd/ccpool/contract_test.go`

- [ ] **Step 1: Write the scenarios**

```go
func TestContract_NeedsInput_AskUserQuestionViaTranscriptFallback(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "a"); code != 0 {
		t.Fatalf("new failed")
	}
	const askPrompt = "Use the AskUserQuestion tool right now as your first action: ask 'CCPROBE which path?' with options 'Alpha' and 'Bravo'. Do nothing else first."
	sb.ccp("reply", "a", askPrompt, "--no-wait")
	// The AskUserQuestion gap: no Notification hook fires; ccpool detects it via the
	// transcript only on a blocking wait. Here we just confirm the picker renders.
	deadline := time.Now().Add(90 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		if strings.Contains(sb.cap("a"), "Alpha") || strings.Contains(sb.cap("a"), "CCPROBE") {
			seen = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !seen {
		scaffoldFail(t, "AskUserQuestion picker never rendered (model may not have called the tool)")
	}
	liveAssert(t, "AskUserQuestion picker rendered", seen, true)
	pending(t, "row reaches needs_input + the pending question text is queryable", "reconciled state + associated info (AskUserQuestion gap)")
}

func TestContract_Reap_EvictsOldestOverCap(t *testing.T) {
	// new does NOT enforce max_sessions; only reap evicts oldest-by-last_activity.
	pending(t, "reap evicts oldest-by-last_activity down to cap", "deterministic reap assertion (needs activity-time control / state query)")
}
```

- [ ] **Step 2: Run**

Run: `cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -run 'TestContract_NeedsInput|TestContract_Reap' ./cmd/ccpool/ -v`
Expected: AskUserQuestion live-assert PASS (or SCAFFOLD-FAIL if the model declines the tool — rerun); reap SKIP.

- [ ] **Step 3: Commit**

```bash
git add packages/ccpool/cmd/ccpool/contract_test.go
git commit -m "test(ccpool): contract needs_input (AskUserQuestion) + reap scenarios"
```

---

## Task 11: `go test -json` classifier + nix entrypoint

**Files:**

- Create: `packages/ccpool/contract/classify.jq`
- Create: `packages/ccpool/contract/README.md`
- Modify: `flake.nix`

- [ ] **Step 1: Write the jq classifier**

`packages/ccpool/contract/classify.jq`:

```jq
# Reads `go test -json` stream; tallies the four OUTCOME= buckets from log output.
select(.Action == "output") | .Output
| capture("OUTCOME=(?<o>[a-z-]+)")? // empty
| .o
```

Wrap it in a tiny shell summary (also in the README) :

```bash
go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... \
  | jq -r -f packages/ccpool/contract/classify.jq \
  | sort | uniq -c
```

Expected buckets: `live`, `baseline`, `baseline-drift`, `live-fail`, `scaffold`, `pending`. Any `baseline-drift`, `live-fail`, or `scaffold` line means investigate.

- [ ] **Step 2: Write the README**

`packages/ccpool/contract/README.md` — document: purpose (post-upgrade drift detection), how to run (`nix run .#ccpool-contract`), the four outcomes, that it needs real `claude`+OAuth and is excluded from CI, the ~8–12 min/token cost, and that `baseline`s pin known-bug values (re-triage on drift).

- [ ] **Step 3: Add the nix entrypoint**

In `flake.nix`, add an app/script `ccpool-contract` that runs:

```
cd packages/ccpool && go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... | tee /tmp/ccpool-contract.json | jq -r -f contract/classify.jq | sort | uniq -c
```

Follow the repo's existing app-definition pattern (grep `flake.nix` for how other entrypoints/`apps` or devShell scripts are declared). Re-run `nix run .#install-pre-commit-hooks` only if hook config changed (it did not).

- [ ] **Step 4: Verify**

Run: `nix run .#ccpool-contract 2>&1 | tail -20`
Expected: the bucket tally; non-zero `baseline-drift`/`live-fail`/`scaffold` counts flagged.

- [ ] **Step 5: Commit**

```bash
git add packages/ccpool/contract/ flake.nix
git commit -m "test(ccpool): contract -json classifier + nix run .#ccpool-contract entrypoint"
```

---

## Task 12: Self-review + bead close-out

- [ ] **Step 1: Spec-coverage pass.** Re-read the spec catalog; confirm a scenario exists for each row (lifecycle, send, cancel, interrupt, attend, needs_input, reap). Add any missing scenario as its own task-style test. Confirm every `pending`/`baseline` maps to a harvest-table row.

- [ ] **Step 2: Run the whole suite once, capture the harvest.**

Run: `nix run .#ccpool-contract`
Expected: all `live` PASS, `baseline`s match observed values, `pending`s SKIP. Save the `pending`/`baseline` lines as the v2 observability backlog. Triage any `live-fail`/`baseline-drift`/`scaffold` (fix harness bug, or convert to baseline + link a bead).

- [ ] **Step 3: Verify `nix build .#ccpool` still green with all contract files present.**

Run: `nix build .#ccpool && echo OK`
Expected: OK (tag excludes contract files from the default checkPhase; no `vendorHash` change).

- [ ] **Step 4: Update beads.**

```bash
bd update pg2-k6rt --notes "Harness landed on worktree-ccpool-contract-harness-spec. Harvest (pending/baseline) recorded for pg2-pndj."
# Close pg2-k6rt only after the harness runs clean per the phase exit criteria.
```

- [ ] **Step 5: Final commit + integrate.** Use superpowers:finishing-a-development-branch to decide merge/PR (rebase + --ff-only onto main; main is the shared multi-agent checkout — coordinate, never force-move).

---

## Self-Review (completed by planner)

- **Spec coverage:** lifecycle ✓(T6), send ✓(T8), cancel incl. stale-marker ✓(T7), interrupt ✓(T8), attend incl. branch-select-pending ✓(T9), needs_input/AskUserQuestion ✓(T10), reap ✓(T10), four outcomes ✓(T4), phase gates+SCAFFOLD-FAIL ✓(T5), `-timeout=0`/`-p 1`/build-once ✓(T1/run cmds), `nix build` unaffected ✓(T1/T12), classifier+entrypoint ✓(T11), auth-not-isolated documented ✓(facts + T2 comment).
- **Placeholder scan:** the two intentionally-empty Go stubs (`ClosePurge`, the `waitUntil` illustration) are explicitly replaced/removed in their own steps; no "TODO/handle edge cases" left. `pending(...)` calls are the _designed_ deferral mechanism, not placeholders.
- **Type consistency:** `sandbox` methods (`ccp`, `ccpTimed`, `cap`, `setState`, `rowState`, `active`, `waitForThinking/Streaming/NeedsInput`, `envGet`, `withFakeClaude`) and outcome helpers (`liveAssert`, `baseline`, `pending`, `scaffoldFail`) are referenced consistently across tasks. `thinkingPrompt`/`contractModel` defined once (T5/T2).
- **Known soft spot:** the phase-detection regexes (`reThinking`/`reStreaming`) are heuristic and CONTRACT-SENSITIVE by design — a miss SCAFFOLD-FAILs (loud, located), never a false green. The implementer should tune them against a first real run and re-pin.
