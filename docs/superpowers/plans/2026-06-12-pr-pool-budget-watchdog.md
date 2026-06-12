# pr-pool Budget/Time Watchdog Implementation Plan (chunk B)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-worker-session budget watchdog to the Go `pr-pool` orchestrator: track cumulative-token / estimated-cost / wall-clock usage against a per-session `Budget` and escalate (72.5% queued reminder → 90% cancel+wrap-up → 100% terminal: guarded worktree reset, note, unclaim, close, `ErrBudgetExceeded`, JSONL event).

**Architecture:** Builds on chunk A (`pg2-spgx`). Four **prerequisites** are front-loaded (they touch A's code / packaging and are NOT additive): P-A (claude-transcript packaging), P-B (`beads.Comment`), P-C (`ccpool.ErrCancelUnconfirmed`), P-D (rework A's `waitDone` for context-cancellation + a clock seam). Then B's own leaves (`usage`, `budget`, `eventlog`), the `watchdog`, and the orchestrator integration (run `waitDone` + watchdog concurrently; first terminal wins; the loser skips its action). Token/cost reading is mocked in tests and blocked-live on ccpool N3 — exactly like A. The **time** dimension is fully live.

**Tech Stack:** Go 1.25; imports `github.com/phillipgreenii/claude-transcript` (the only non-stdlib dep — see P-A); `mkGoApp`/`buildGoModule`; `nix flake check` gate.

**Spec:** `docs/superpowers/specs/2026-06-11-pr-pool-budget-watchdog-design.md` (read it first). Worktree: `.claude/worktrees/pr-pool-b-watchdog` (branched off A's `worktree-pr-pool-go-impl`). All commands use the absolute worktree path; another agent is active in the main checkout — never touch it.

---

## File Structure

```
packages/pr-pool/
  go.mod                         + require/replace github.com/phillipgreenii/claude-transcript => ../claude-transcript   (P-A)
  default.nix                    rewritten: root ./.. , fileset.unions [./. ../claude-transcript], modRoot="pr-pool"      (P-A)
  internal/
    beads/issue.go               + Comment(ctx, r, id, text)                                                              (P-B)
    ccpool/cli.go                + ErrCancelUnconfirmed (exit-6 detection in Cancel)                                       (P-C)
    ccpool/ccpool.go             + CWD field on Session                                                                    (Task 7)
    orchestrator/orchestrator.go waitDone reworked (ctx + clock seam); workOne runs watchdog for workers                  (P-D, Task 9)
    usage/usage.go               Reader iface + Snapshot{model,components}+Total()                                         (Task 4)
    usage/transcript.go          claudeTranscriptReader: assistant-only JSONL scan                                        (Task 4)
    usage/cost.go                ModelPrice/PriceTable/DefaultPrices/EstimateCents(int64)                                  (Task 5)
    budget/budget.go             Limit/Thresholds/Level/Budget/Evaluate/PromptLine                                        (Task 6)
    config/config.go             + budget scalars (TimeMax 25m default, token/cost unlimited, thresholds, LogDir, msgs)   (Task 6b)
    eventlog/eventlog.go         Writer.Emit (sync.Mutex, O_APPEND JSONL)                                                 (Task 7b)
    watchdog/watchdog.go         Watchdog.Run: ladder, fire-once, ErrBudgetExceeded                                       (Task 8)
    watchdog/terminal.go         terminal seq + guarded worktree reset (EvalSymlinks/Rel/toplevel)                       (Task 8b)
```

Import DAG (acyclic): `usage` → claude-transcript; `budget` → usage; `eventlog`, `config` stdlib-only; `watchdog` → {usage, budget, eventlog, ccpool, beads, roles, config}; `orchestrator` → +watchdog.

---

## Shared types & signatures (keep consistent across tasks)

```go
// internal/beads
func Comment(ctx context.Context, r Runner, id, text string) error // bd comment <id> <text>

// internal/ccpool
var ErrCancelUnconfirmed = errors.New("ccpool cancel unconfirmed (exit 6)")
type Session struct { Name string; State SessionState; Live bool; TranscriptPath string; CWD string `json:"cwd"` }

// internal/usage
type Snapshot struct { Model string; InputTokens, CacheCreationTokens, CacheReadTokens, OutputTokens int }
func (s Snapshot) Total() int
type Reader interface { Read(ctx context.Context, transcriptPath string) (Snapshot, error) }
func NewTranscriptReader() Reader
type ModelPrice struct { InputPerMTok, OutputPerMTok, CacheWritePerMTok, CacheReadPerMTok float64 }
type PriceTable map[string]ModelPrice
func DefaultPrices() PriceTable
func EstimateCents(s Snapshot, t PriceTable) int64

// internal/budget
type Limit int64
func (l Limit) Unlimited() bool
type Thresholds struct { Reminder, Cancel, Hard float64 }
type Level int   // None, Reminder, Cancel, Hard
type Budget struct { Tokens, Cost Limit; Time time.Duration; Thresholds Thresholds; Prices usage.PriceTable }
func (b Budget) Evaluate(s usage.Snapshot, elapsed time.Duration) (pct float64, level Level)
func (b Budget) PromptLine() string

// internal/eventlog
type Writer struct { /* mu, f */ }
func New(path string) (*Writer, error)
func (w *Writer) Emit(kind string, fields map[string]any) error
func (w *Writer) Close() error

// internal/watchdog
var ErrBudgetExceeded = errors.New("session budget exceeded")
type Watchdog struct {
    Reader  usage.Reader
    CC      ccpool.Runner
    BD      beads.Runner
    Log     *eventlog.Writer
    Budget  budget.Budget
    RepoRoot, WorktreeDir string
    ReminderMsg, WrapUpMsg string
    Git     GitRunner          // injectable: Run(ctx, dir string, args ...string) error
    Now     func() time.Time   // clock seam
    Poll    time.Duration
}
func (w *Watchdog) Run(ctx context.Context, sessionName, beadID string) error

// internal/orchestrator (P-D additions)
//   Orchestrator gains: now func() time.Time ; tick func(ctx, d) error   (replace the `sleep` seam)
//   waitDone returns ctx.Err() on cancellation WITHOUT calling o.fail.
```

---

## Task P-A: Package claude-transcript into pr-pool (go.mod + default.nix)

**Files:**

- Modify: `packages/pr-pool/go.mod`
- Modify: `packages/pr-pool/default.nix`

This is purely the build/packaging change so later tasks can `import` claude-transcript. No Go logic yet (we add a throwaway import to force the dep, then remove it — or simply land go.mod and let Task 4 add the first real import). To keep this task verifiable on its own, add a tiny compile-only reference.

- [ ] **Step 1: Edit `go.mod`** to require + replace claude-transcript (mirror ccpool):

```
module github.com/phillipgreenii/pr-pool

go 1.25.0

require github.com/phillipgreenii/claude-transcript v0.0.0

replace github.com/phillipgreenii/claude-transcript => ../claude-transcript
```

- [ ] **Step 2: Rewrite `default.nix`** to root the source at `packages/` so the relative `replace` resolves (mirror `packages/ccpool/default.nix`). Replace the `src`/add `modRoot`:

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

  # The module uses `replace ../claude-transcript`, so the build sandbox must
  # contain BOTH package dirs at their relative positions (mirror ccpool).
  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ./.
      ../claude-transcript
    ];
  };
  modRoot = "pr-pool";

  # claude-transcript is itself stdlib-only; vendorHash MAY stay null. Determine
  # empirically in Step 4 — if the build complains, set the printed hash.
  vendorHash = null;

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    wrapProgram $out/bin/pr-pool --prefix PATH : ${lib.makeBinPath [ ccpool bd pg-pr ]}
  '';

  meta = {
    description = "PR-pool orchestrator (delegates claude+tmux to ccpool)";
    mainProgram = "pr-pool";
  };
}
```

- [ ] **Step 3: Add a temporary compile reference** so the dep is actually used (Task 4 replaces this). In `packages/pr-pool/cmd/pr-pool/main.go`, this is NOT touched; instead create `packages/pr-pool/internal/usage/doc.go`:

```go
// Package usage reads per-session token usage from a Claude transcript.
package usage

import _ "github.com/phillipgreenii/claude-transcript" // wired in Task 4; forces the dep
```

- [ ] **Step 4: Tidy + determine vendorHash + build.**

Run:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-b-watchdog/packages/pr-pool
go mod tidy
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-b-watchdog
nix build .#pr-pool --no-link 2>&1 | tail -20
```

Expected: build succeeds with `vendorHash = null`. **If** nix reports a hash mismatch (`got: sha256-…`), set `vendorHash` in `default.nix` to the printed value and rebuild. Record which outcome occurred.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-b-watchdog
git add packages/pr-pool/go.mod packages/pr-pool/go.sum packages/pr-pool/default.nix packages/pr-pool/internal/usage/doc.go
git commit -m "build(pr-pool): vendor claude-transcript for the budget watchdog (pg2-y991)"
```

---

## Task P-B: `beads.Comment` helper

**Files:**

- Modify: `packages/pr-pool/internal/beads/issue.go`
- Test: `packages/pr-pool/internal/beads/issue_test.go`

- [ ] **Step 1: Write the failing test** (append to `issue_test.go`)

```go
func TestComment_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Comment(context.Background(), fr, "zr-1", "interrupted — budget"); err != nil {
		t.Fatal(err)
	}
	want := []string{"comment", "zr-1", "interrupted — budget"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `cd packages/pr-pool && go test ./internal/beads/` → FAIL (undefined Comment).

- [ ] **Step 3: Implement** (append to `issue.go`)

```go
// Comment adds a comment to a bead: `bd comment <id> <text>`.
func Comment(ctx context.Context, r Runner, id, text string) error {
	_, err := r.Run(ctx, "comment", id, text)
	if err != nil {
		return fmt.Errorf("comment %s: %w", id, err)
	}
	return nil
}
```

- [ ] **Step 4: Run** — `go test ./internal/beads/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/beads
git commit -m "feat(pr-pool): beads.Comment helper (pg2-y991)"
```

---

## Task P-C: `ccpool.ErrCancelUnconfirmed` (exit-6 detection)

**Files:**

- Modify: `packages/pr-pool/internal/ccpool/cli.go`
- Test: `packages/pr-pool/internal/ccpool/cli_test.go`

ccpool's CLI exits **6** when a cancel is unconfirmed. Surface that across pr-pool's seam so the watchdog can (optionally) react. Detect it via `errors.As` on the wrapped `*exec.ExitError` — no runner restructuring.

- [ ] **Step 1: Write the failing test** (append to `cli_test.go`)

```go
func TestCancel_unconfirmedExit6(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(args []string) ([]byte, error) {
		return []byte("cancel may not have landed"), &fakeExit{code: 6}
	}
	err := cli.Cancel(context.Background(), "s")
	if !errors.Is(err, ErrCancelUnconfirmed) {
		t.Errorf("exit 6 should map to ErrCancelUnconfirmed, got %v", err)
	}
}

func TestCancel_otherErrorNotUnconfirmed(t *testing.T) {
	cli := NewCLIRunner(config.Default())
	cli.run = func(args []string) ([]byte, error) { return nil, &fakeExit{code: 1} }
	err := cli.Cancel(context.Background(), "s")
	if err == nil || errors.Is(err, ErrCancelUnconfirmed) {
		t.Errorf("exit 1 must not be ErrCancelUnconfirmed, got %v", err)
	}
}

// fakeExit implements the bits of *exec.ExitError errors.As + ExitCode() need.
type fakeExit struct{ code int }
func (e *fakeExit) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *fakeExit) ExitCode() int { return e.code }
```

> NOTE: `errors.As(err, &*exec.ExitError)` requires a real `*exec.ExitError`. Since the fake `run` can't easily produce one, the implementation must detect the code via a small interface (`interface{ ExitCode() int }`) rather than the concrete type — see Step 3. Add `"errors"` and `"fmt"` to the test imports.

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/ccpool/` → FAIL.

- [ ] **Step 3: Implement.** Add the sentinel + detection in `cli.go`:

```go
// ErrCancelUnconfirmed is returned by Cancel when `ccpool cancel` exits 6
// (the interrupt could not be confirmed — the turn may still be running).
var ErrCancelUnconfirmed = errors.New("ccpool cancel unconfirmed")

// exitCoder is satisfied by *exec.ExitError (and test fakes).
type exitCoder interface{ ExitCode() int }
```

Rework `Cancel` to inspect the exit code (the existing `c.ccpool(...)` wraps the run error with `%w`, so `errors.As` reaches the coder):

```go
func (c *CLIRunner) Cancel(_ context.Context, name string) error {
	_, err := c.ccpool("cancel", name)
	if err != nil {
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 6 {
			return fmt.Errorf("%w: %s", ErrCancelUnconfirmed, name)
		}
		return err
	}
	return nil
}
```

(Add `"errors"` to `cli.go` imports if not present.)

- [ ] **Step 4: Run** — `go test ./internal/ccpool/` → PASS (existing argv tests still green).

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/ccpool
git commit -m "feat(pr-pool): ccpool.ErrCancelUnconfirmed via cancel exit-6 (pg2-y991)"
```

---

## Task P-D: rework `waitDone` for context-cancellation + clock seam

**Files:**

- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go`
- Modify: `packages/pr-pool/internal/orchestrator/orchestrator_test.go`

The watchdog must be able to cancel `waitDone`. Today `waitDone` ignores `ctx` and loops on real wall-clock with a `o.sleep` no-op seam. Rework it to: (1) drive its deadline from an injectable `o.now`, (2) wait via an injectable cancellable `o.tick`, (3) **return `ctx.Err()` on cancellation WITHOUT calling `o.fail`** (the structural single-terminal guarantee). Preserve A's exact ordering: status read → DoneSignal → set seenClaimed → liveness/re-check-after-death → deadline check.

- [ ] **Step 1: Replace the `sleep` seam with `now` + `tick`.** In `orchestrator.go`, change the `Orchestrator` struct field `sleep func(time.Duration)` to:

```go
	now  func() time.Time              // clock seam (default time.Now)
	tick func(context.Context, time.Duration) error // cancellable wait (default below)
```

Replace the `nap` helper:

```go
func (o *Orchestrator) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// waitPoll blocks for d or until ctx is cancelled; returns ctx.Err() if cancelled.
func (o *Orchestrator) waitPoll(ctx context.Context, d time.Duration) error {
	if o.tick != nil {
		return o.tick(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
```

- [ ] **Step 2: Rewrite `waitDone`** preserving ordering, adding ctx-cancel (no-fail) + clock seam:

```go
func (o *Orchestrator) waitDone(ctx context.Context, d discover.Dispatch, name string) error {
	deadline := o.clock().Add(o.Cfg.MaxWait)
	seenClaimed := false
	for {
		status, _ := beads.Status(ctx, o.BD, d.BeadID)
		if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
			return nil
		}
		if d.Role.Kind == roles.Worker && status == "in_progress" {
			seenClaimed = true
		}
		if !o.live(ctx, name) {
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			return o.fail(ctx, d, "session exited before completing")
		}
		if !o.clock().Before(deadline) {
			status, _ = beads.Status(ctx, o.BD, d.BeadID)
			if complete.DoneSignal(d.Role.Kind, status, seenClaimed) {
				return nil
			}
			return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
		}
		// cancellable wait — on cancellation return ctx.Err() and DO NOT fail
		// (the watchdog won the race and owns the terminal outcome).
		if err := o.waitPoll(ctx, o.Cfg.PollInterval); err != nil {
			return err
		}
	}
}
```

- [ ] **Step 3: Add a manual clock test helper** to `orchestrator_test.go`:

```go
// manualClock advances only when the test ticks it, so waitDone polling is
// deterministic and instant.
type manualClock struct{ t time.Time }
func (c *manualClock) now() time.Time { return c.t }
// tickAdvancing returns a tick func that advances the clock by d each poll, so a
// finite-deadline loop terminates without real sleeping.
func (c *manualClock) tickAdvancing() func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.t = c.t.Add(d)
		return nil
	}
}
```

- [ ] **Step 4: Update the existing waitDone tests** to use the clock seam instead of `o.sleep`. In `newOrch` (helper in `orchestrator_test.go`) replace `o.sleep = func(time.Duration) {}` with:

```go
	clk := &manualClock{t: time.Unix(0, 0)}
	o.now = clk.now
	o.tick = clk.tickAdvancing()
```

(With `MaxWait` small in `fastCfg()` and the clock advancing `PollInterval` per tick, timeout tests reach the deadline in a bounded number of ticks, deterministically.) Keep `fastCfg()`'s `MaxWait`/`PollInterval`.

- [ ] **Step 5: Add a cancellation test** proving `waitDone` returns `ctx.Err()` and does NOT fail the bead:

```go
func TestWaitDone_ctxCancelDoesNotFail(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true}}}}
	o := newOrch(cc, bd, fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	err := o.waitDone(ctx, d, "pr-pool-worker-zr-w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("cancellation must NOT run a failure action; updates=%v", bd.updates)
	}
}
```

(Add `"context"`, `"errors"`, `"time"` to test imports as needed.)

- [ ] **Step 6: Run all orchestrator tests** — `go test ./internal/orchestrator/ -v` → all existing scenarios PASS + the new cancellation test PASS. Then `go test ./... && go vet ./... && gofmt -l . && nix run nixpkgs#golangci-lint -- run ./...` → clean.

- [ ] **Step 7: Commit**

```bash
git add packages/pr-pool/internal/orchestrator
git commit -m "refactor(pr-pool): waitDone honors ctx cancellation + clock seam (pg2-y991)

Replace the sleep no-op seam with now()+tick() (deterministic instant tests).
On ctx cancellation waitDone returns ctx.Err() WITHOUT running a failure action
(the structural single-terminal guarantee for the watchdog race). A's ordering
(done-check-first, seenClaimed, re-check-after-death, post-deadline final check)
preserved; existing tests updated to the clock seam."
```

---

## Task 4: `internal/usage` — Reader + assistant-only transcript scan

**Files:**

- Modify: `packages/pr-pool/internal/usage/doc.go` (replace the temp import)
- Create: `packages/pr-pool/internal/usage/usage.go`
- Create: `packages/pr-pool/internal/usage/transcript.go`
- Test: `packages/pr-pool/internal/usage/transcript_test.go`

- [ ] **Step 1: Delete the temp `doc.go`** from P-A (its `_` import is replaced by real use):

```bash
rm packages/pr-pool/internal/usage/doc.go
```

- [ ] **Step 2: Write the failing test** `transcript_test.go` — a fixture with two assistant turns + a NON-assistant line carrying a stray `usage` (must be excluded):

```go
package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(p, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func joinLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func TestTranscriptReader_assistantOnlyCumulative(t *testing.T) {
	path := writeJSONL(t,
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"cache_creation_input_tokens":10,"cache_read_input_tokens":1000,"output_tokens":50}}}`,
		`{"type":"user","message":{"usage":{"input_tokens":99999,"output_tokens":99999}}}`, // STRAY usage — must be excluded
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":2000,"output_tokens":80}}}`,
	)
	r := NewTranscriptReader()
	got, err := r.Read(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Model: "claude-opus-4-8", InputTokens: 300, CacheCreationTokens: 10, CacheReadTokens: 3000, OutputTokens: 130}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if got.Total() != 3440 {
		t.Errorf("Total = %d, want 3440", got.Total())
	}
}

func TestTranscriptReader_missingFileIsZero(t *testing.T) {
	r := NewTranscriptReader()
	got, err := r.Read(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("missing transcript must be (zero, nil), got err %v", err)
	}
	if got != (Snapshot{}) {
		t.Errorf("want zero Snapshot, got %+v", got)
	}
}
```

- [ ] **Step 3: Run to verify it fails** — `go test ./internal/usage/` → FAIL.

- [ ] **Step 4: Implement `usage.go`**:

```go
// Package usage reads per-session token usage from a Claude transcript, behind a
// Reader interface so the watchdog never couples to the claude-transcript types.
package usage

import "context"

// Snapshot is cumulative token usage for one session at one instant.
type Snapshot struct {
	Model               string
	InputTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	OutputTokens        int
}

// Total is the cumulative-tokens meter (all components summed).
func (s Snapshot) Total() int {
	return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens + s.OutputTokens
}

// Reader reads a session's cumulative usage from its transcript file.
type Reader interface {
	Read(ctx context.Context, transcriptPath string) (Snapshot, error)
}
```

- [ ] **Step 5: Implement `transcript.go`** (assistant-only scan over claude-transcript's exported types):

```go
package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	ct "github.com/phillipgreenii/claude-transcript"
)

type transcriptReader struct{}

// NewTranscriptReader returns a Reader backed by Claude transcript JSONL files.
func NewTranscriptReader() Reader { return transcriptReader{} }

func (transcriptReader) Read(_ context.Context, path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, nil // worker hasn't produced a transcript yet
		}
		return Snapshot{}, err
	}
	defer f.Close()

	var s Snapshot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24) // transcript lines are huge
	for sc.Scan() {
		var ev ct.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // tolerate a malformed line
		}
		if ev.Type != "assistant" { // decision 2: assistant turns only
			continue
		}
		u := ev.Message.Usage
		s.InputTokens += u.InputTokens
		s.CacheCreationTokens += u.CacheCreationInputTokens
		s.CacheReadTokens += u.CacheReadInputTokens
		s.OutputTokens += u.OutputTokens
		if ev.Message.Model != "" {
			s.Model = ev.Message.Model
		}
	}
	if err := sc.Err(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
```

- [ ] **Step 6: Run** — `go test ./internal/usage/` → PASS.

- [ ] **Step 7: Commit**

```bash
git add packages/pr-pool/internal/usage
git commit -m "feat(pr-pool): usage.Reader + assistant-only transcript scan (pg2-y991)"
```

---

## Task 5: `internal/usage` — cost estimation

**Files:**

- Create: `packages/pr-pool/internal/usage/cost.go`
- Test: `packages/pr-pool/internal/usage/cost_test.go`

- [ ] **Step 1: Write the failing test** `cost_test.go`:

```go
package usage

import "testing"

func TestEstimateCents_knownModel(t *testing.T) {
	pt := PriceTable{"m": {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5}}
	s := Snapshot{Model: "m", InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheCreationTokens: 1_000_000}
	// $15 + $75 + $1.5 + $18.75 = $110.25 -> 11025 cents
	if got := EstimateCents(s, pt); got != 11025 {
		t.Errorf("EstimateCents = %d, want 11025", got)
	}
}

func TestEstimateCents_unknownModelFallback(t *testing.T) {
	pt := PriceTable{"_default": {InputPerMTok: 3, OutputPerMTok: 15}}
	s := Snapshot{Model: "unknown", InputTokens: 1_000_000, OutputTokens: 1_000_000}
	// fallback: $3 + $15 = $18 -> 1800 cents
	if got := EstimateCents(s, pt); got != 1800 {
		t.Errorf("EstimateCents = %d, want 1800 (default fallback)", got)
	}
}

func TestEstimateCents_largeMagnitudeNoOverflow(t *testing.T) {
	pt := PriceTable{"m": {CacheReadPerMTok: 1.5}}
	s := Snapshot{Model: "m", CacheReadTokens: 50_000_000} // tens of millions
	// 50M * $1.5/MTok = $75 -> 7500 cents
	if got := EstimateCents(s, pt); got != 7500 {
		t.Errorf("EstimateCents = %d, want 7500", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/usage/` → FAIL.

- [ ] **Step 3: Implement `cost.go`** (truncate toward zero; int64):

```go
package usage

// ModelPrice is per-MTok USD pricing for one model.
type ModelPrice struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// PriceTable maps a model id to its prices. The special key "_default" is the
// fallback for an unknown model.
type PriceTable map[string]ModelPrice

// DefaultPrices is the built-in table (USD/MTok). Update as Anthropic pricing
// changes; this is data, not logic. Values current as of 2026-06.
func DefaultPrices() PriceTable {
	return PriceTable{
		"_default":          {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-opus-4-8":   {InputPerMTok: 15, OutputPerMTok: 75, CacheWritePerMTok: 18.75, CacheReadPerMTok: 1.5},
		"claude-sonnet-4-6": {InputPerMTok: 3, OutputPerMTok: 15, CacheWritePerMTok: 3.75, CacheReadPerMTok: 0.3},
		"claude-haiku-4-5":  {InputPerMTok: 1, OutputPerMTok: 5, CacheWritePerMTok: 1.25, CacheReadPerMTok: 0.1},
	}
}

// EstimateCents estimates a Snapshot's cost in integer cents (truncated toward
// zero). Unknown model falls back to the "_default" price.
func EstimateCents(s Snapshot, t PriceTable) int64 {
	p, ok := t[s.Model]
	if !ok {
		p = t["_default"]
	}
	dollars := float64(s.InputTokens)/1e6*p.InputPerMTok +
		float64(s.OutputTokens)/1e6*p.OutputPerMTok +
		float64(s.CacheCreationTokens)/1e6*p.CacheWritePerMTok +
		float64(s.CacheReadTokens)/1e6*p.CacheReadPerMTok
	return int64(dollars * 100)
}
```

- [ ] **Step 4: Run** — `go test ./internal/usage/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/usage
git commit -m "feat(pr-pool): cost estimation (per-model price table, int64 cents) (pg2-y991)"
```

---

## Task 6: `internal/budget` — Budget model + Evaluate + PromptLine

**Files:**

- Create: `packages/pr-pool/internal/budget/budget.go`
- Test: `packages/pr-pool/internal/budget/budget_test.go`

- [ ] **Step 1: Write the failing test** `budget_test.go`:

```go
package budget

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/usage"
)

func mk(tok, cost Limit, tm time.Duration) Budget {
	return Budget{Tokens: tok, Cost: cost, Time: tm,
		Thresholds: Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00},
		Prices:     usage.PriceTable{"_default": {OutputPerMTok: 75}}}
}

func TestLimitUnlimited(t *testing.T) {
	if !Limit(0).Unlimited() || !Limit(-1).Unlimited() || Limit(1).Unlimited() {
		t.Error("Unlimited semantics wrong")
	}
}

func TestEvaluate_levels(t *testing.T) {
	b := mk(1000, 0, 0) // token-only cap of 1000
	cases := []struct {
		out  int
		want Level
	}{
		{700, None}, {725, Reminder}, {900, Cancel}, {1000, Hard}, {5000, Hard},
	}
	for _, c := range cases {
		_, lvl := b.Evaluate(usage.Snapshot{OutputTokens: c.out}, 0)
		if lvl != c.want {
			t.Errorf("tokens=%d -> %v, want %v", c.out, lvl, c.want)
		}
	}
}

func TestEvaluate_maxAcrossDimensions(t *testing.T) {
	b := mk(1000, 0, 10*time.Minute) // tokens 1000, time 10m
	// tokens 50% but time 95% -> Cancel (max)
	_, lvl := b.Evaluate(usage.Snapshot{OutputTokens: 500}, 95*time.Minute/10)
	if lvl != Cancel {
		t.Errorf("max%% should pick time (Cancel), got %v", lvl)
	}
}

func TestEvaluate_unlimitedContributesZero(t *testing.T) {
	b := mk(0, 0, 0) // everything unlimited
	pct, lvl := b.Evaluate(usage.Snapshot{OutputTokens: 1 << 30}, 1000*time.Hour)
	if lvl != None || pct != 0 {
		t.Errorf("fully unlimited -> None/0, got %v/%v", lvl, pct)
	}
}

func TestPromptLine_omitsUnlimited(t *testing.T) {
	if s := mk(0, 0, 0).PromptLine(); s != "" {
		t.Errorf("fully unlimited PromptLine should be empty, got %q", s)
	}
	s := mk(0, 0, 25*time.Minute).PromptLine()
	if s == "" {
		t.Error("time-limited budget should produce a prompt line")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/budget/` → FAIL.

- [ ] **Step 3: Implement `budget.go`**:

```go
// Package budget models a per-session usage budget and the escalation level a
// given usage snapshot implies. Pure: no I/O.
package budget

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// Limit is a budget ceiling (tokens or cents). <= 0 means Unlimited.
type Limit int64

func (l Limit) Unlimited() bool { return l <= 0 }

type Thresholds struct{ Reminder, Cancel, Hard float64 }

type Level int

const (
	None Level = iota
	Reminder
	Cancel
	Hard
)

type Budget struct {
	Tokens     Limit
	Cost       Limit
	Time       time.Duration
	Thresholds Thresholds
	Prices     usage.PriceTable
}

// Evaluate returns the max fraction-of-budget across the set dimensions and the
// escalation Level it implies. Unlimited dimensions contribute 0.
func (b Budget) Evaluate(s usage.Snapshot, elapsed time.Duration) (float64, Level) {
	pct := 0.0
	if !b.Tokens.Unlimited() {
		pct = max(pct, float64(s.Total())/float64(b.Tokens))
	}
	if !b.Cost.Unlimited() {
		pct = max(pct, float64(usage.EstimateCents(s, b.Prices))/float64(b.Cost))
	}
	if b.Time > 0 {
		pct = max(pct, float64(elapsed)/float64(b.Time))
	}
	return pct, b.level(pct)
}

func (b Budget) level(pct float64) Level {
	switch {
	case pct >= b.Thresholds.Hard:
		return Hard
	case pct >= b.Thresholds.Cancel:
		return Cancel
	case pct >= b.Thresholds.Reminder:
		return Reminder
	default:
		return None
	}
}

// PromptLine returns a one-sentence budget statement for the worker prompt, or
// "" when fully unlimited. Unlimited dimensions are omitted.
func (b Budget) PromptLine() string {
	var parts []string
	if !b.Tokens.Unlimited() {
		parts = append(parts, fmt.Sprintf("%d tokens", int64(b.Tokens)))
	}
	if !b.Cost.Unlimited() {
		parts = append(parts, fmt.Sprintf("$%.2f", float64(b.Cost)/100))
	}
	if b.Time > 0 {
		parts = append(parts, b.Time.String())
	}
	if len(parts) == 0 {
		return ""
	}
	return " You have a budget of up to " + strings.Join(parts, " / ") +
		" for this bead; if you receive a 'wrap up' message, commit your notes and finish promptly."
}
```

(`max` is the Go 1.21+ builtin.)

- [ ] **Step 4: Run** — `go test ./internal/budget/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/budget
git commit -m "feat(pr-pool): budget model + Evaluate (max%% across dims) + PromptLine (pg2-y991)"
```

---

## Task 6b: `internal/config` — budget scalars + WorkerBudget()

**Files:**

- Modify: `packages/pr-pool/internal/config/config.go`
- Test: `packages/pr-pool/internal/config/config_test.go`

Keep `config` dependency-light: hold raw budget scalars + a `WorkerBudget()` that assembles a `budget.Budget` using `usage.DefaultPrices()`. (config → budget → usage is acyclic.)

- [ ] **Step 1: Write the failing test** (append to `config_test.go`):

```go
func TestWorkerBudget_defaults(t *testing.T) {
	b := Default().WorkerBudget()
	if !b.Tokens.Unlimited() || !b.Cost.Unlimited() {
		t.Error("token/cost default must be unlimited")
	}
	if b.Time != 25*time.Minute {
		t.Errorf("time default = %v, want 25m (< MaxWait 30m)", b.Time)
	}
	if b.Thresholds.Reminder != 0.725 || b.Thresholds.Cancel != 0.90 || b.Thresholds.Hard != 1.0 {
		t.Errorf("thresholds = %+v", b.Thresholds)
	}
	if b.Time >= Default().MaxWait {
		t.Errorf("budget time %v must be < MaxWait %v", b.Time, Default().MaxWait)
	}
}

func TestWorkerBudget_envOverrides(t *testing.T) {
	t.Setenv("PR_POOL_BUDGET_TOKENS", "1000000")
	t.Setenv("PR_POOL_BUDGET_TIME", "600")
	b := Load().WorkerBudget()
	if int64(b.Tokens) != 1000000 || b.Time != 600*time.Second {
		t.Errorf("env overrides not applied: %+v", b)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/config/` → FAIL.

- [ ] **Step 3: Implement.** Add fields to `Config` (raw scalars + log dir + messages):

```go
	// Budget watchdog (chunk B). Token/Cost <= 0 means unlimited.
	BudgetTokens int64
	BudgetCost   int64 // cents
	BudgetTime   time.Duration
	ReminderPct  float64
	CancelPct    float64
	HardPct      float64
	LogDir       string
	ReminderMsg  string
	WrapUpMsg    string
```

In `Default()` add:

```go
		BudgetTokens: 0, // unlimited until ccpool N3
		BudgetCost:   0, // unlimited until ccpool N3
		BudgetTime:   25 * time.Minute, // strictly < MaxWait (30m)
		ReminderPct:  0.725,
		CancelPct:    0.90,
		HardPct:      1.00,
		LogDir:       state + "/pr-pool/log",
		ReminderMsg:  "You are nearing your budget for this bead — start wrapping up: record progress with bd comment.",
		WrapUpMsg:    "Budget nearly exhausted. Stop now: commit your notes with bd comment, then finish or hand back. Do not start new work.",
```

In `Load()` add the overlays:

```go
	c.BudgetTokens = int64(envInt("PR_POOL_BUDGET_TOKENS", int(c.BudgetTokens)))
	c.BudgetCost = int64(envInt("PR_POOL_BUDGET_COST", int(c.BudgetCost)))
	c.BudgetTime = envSecs("PR_POOL_BUDGET_TIME", c.BudgetTime)
	c.LogDir = envStr("PR_POOL_LOG_DIR", c.LogDir)
```

Add the assembler (new file `config/budget.go` to avoid bloating config.go, or append to config.go):

```go
// WorkerBudget assembles the per-worker Budget from config scalars + the default
// price table. Today one budget for all workers; future per-agent budgets are a
// different constructor, no refactor.
func (c Config) WorkerBudget() budget.Budget {
	return budget.Budget{
		Tokens:     budget.Limit(c.BudgetTokens),
		Cost:       budget.Limit(c.BudgetCost),
		Time:       c.BudgetTime,
		Thresholds: budget.Thresholds{Reminder: c.ReminderPct, Cancel: c.CancelPct, Hard: c.HardPct},
		Prices:     usage.DefaultPrices(),
	}
}
```

(Add imports `"github.com/phillipgreenii/pr-pool/internal/budget"` and `".../internal/usage"` to the file holding `WorkerBudget`.)

- [ ] **Step 4: Run** — `go test ./internal/config/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/config
git commit -m "feat(pr-pool): config budget scalars + WorkerBudget() (time default 25m) (pg2-y991)"
```

---

## Task 7: `ccpool.Session.CWD` field

**Files:**

- Modify: `packages/pr-pool/internal/ccpool/ccpool.go`
- Test: `packages/pr-pool/internal/ccpool/cli_test.go`

- [ ] **Step 1: Write the failing test** — extend the existing List test fixture in `cli_test.go` to include `cwd`:

```go
func TestList_parsesCWD(t *testing.T) {
	cli, _, setOut := newSpy()
	setOut([]byte(`[{"name":"s","state":"working","live":true,"transcript_path":"/t.jsonl","cwd":"/wt/repo-pr1"}]`))
	got, err := cli.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CWD != "/wt/repo-pr1" {
		t.Errorf("CWD = %q, want /wt/repo-pr1", got[0].CWD)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/ccpool/` → FAIL (Session has no CWD).

- [ ] **Step 3: Implement** — add the field to `Session` in `ccpool.go`:

```go
type Session struct {
	Name           string       `json:"name"`
	State          SessionState `json:"state"`
	Live           bool         `json:"live"`
	TranscriptPath string       `json:"transcript_path"`
	CWD            string       `json:"cwd"` // session working path (for the budget watchdog's guarded reset)
}
```

- [ ] **Step 4: Run** — `go test ./internal/ccpool/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/ccpool
git commit -m "feat(pr-pool): ccpool.Session.CWD (worktree path for the watchdog) (pg2-y991)"
```

---

## Task 7b: `internal/eventlog` — structured JSONL writer

**Files:**

- Create: `packages/pr-pool/internal/eventlog/eventlog.go`
- Test: `packages/pr-pool/internal/eventlog/eventlog_test.go`

- [ ] **Step 1: Write the failing test** `eventlog_test.go`:

```go
package eventlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEmit_writesJSONLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Emit("reminder", map[string]any{"session": "s", "pct": 0.73}); err != nil {
		t.Fatal(err)
	}
	if err := w.Emit("hard_stop", map[string]any{"session": "s", "bead": "zr-1"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v", n, err)
		}
		if m["kind"] == nil {
			t.Errorf("line %d missing kind", n)
		}
		n++
	}
	if n != 2 {
		t.Errorf("want 2 lines, got %d", n)
	}
}

func TestEmit_concurrentNoInterleave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := New(p)
	defer w.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _ = w.Emit("tick", map[string]any{"i": i}) }(i)
	}
	wg.Wait()
	f, _ := os.Open(p)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("interleaved/corrupt line: %v", err)
		}
		n++
	}
	if n != 50 {
		t.Errorf("want 50 lines, got %d", n)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/eventlog/` → FAIL.

- [ ] **Step 3: Implement `eventlog.go`**:

```go
// Package eventlog is pr-pool's own structured per-run event log (JSONL). It is
// NOT Claude's transcript. Safe for concurrent emitters (a mutex serializes
// marshal+write so lines never interleave).
package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Writer struct {
	mu sync.Mutex
	f  *os.File
}

// New opens (creating parent dirs) the JSONL log at path in append mode.
func New(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f}, nil
}

// Emit writes one JSON object as a line. `kind` is always present; fields are
// merged in (fields named "kind" are ignored).
func (w *Writer) Emit(kind string, fields map[string]any) error {
	rec := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		if k != "kind" {
			rec[k] = v
		}
	}
	rec["kind"] = kind
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.f.Write(b)
	return err
}

func (w *Writer) Close() error { return w.f.Close() }
```

- [ ] **Step 4: Run** — `go test ./internal/eventlog/ -race` → PASS (the `-race` proves the mutex).

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/eventlog
git commit -m "feat(pr-pool): eventlog JSONL writer (mutex-guarded) (pg2-y991)"
```

---

## Task 8: `internal/watchdog` — escalation ladder + Run

**Files:**

- Create: `packages/pr-pool/internal/watchdog/watchdog.go`
- Test: `packages/pr-pool/internal/watchdog/watchdog_test.go`

- [ ] **Step 1: Write the failing test** `watchdog_test.go` (fakes for Reader/CC/BD + a scripted usage ramp + manual clock):

```go
package watchdog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

type fakeReader struct{ seq []usage.Snapshot; i int }
func (f *fakeReader) Read(context.Context, string) (usage.Snapshot, error) {
	s := f.seq[min(f.i, len(f.seq)-1)]
	f.i++
	return s, nil
}

type fakeCC struct {
	sent    []string // "<flag>:<prompt>"
	cancels int
	closed  []string
	list    []ccpool.Session
}
func (f *fakeCC) Ensure(context.Context, string, string, map[string]string) error { return nil }
func (f *fakeCC) Send(_ context.Context, _ , prompt string, m ccpool.SendMode) error {
	flag := "queue"
	f.sent = append(f.sent, flag+":"+prompt)
	return nil
}
func (f *fakeCC) Cancel(context.Context, string) error { f.cancels++; return nil }
func (f *fakeCC) Close(_ context.Context, n string) error { f.closed = append(f.closed, n); return nil }
func (f *fakeCC) List(context.Context) ([]ccpool.Session, error) { return f.list, nil }

type recBD struct{ calls []string }
func (r *recBD) Run(_ context.Context, args ...string) (string, error) {
	out := ""
	for i, a := range args { if i > 0 { out += " " }; out += a }
	r.calls = append(r.calls, out)
	return "", nil
}
func (r *recBD) has(s string) bool { for _, c := range r.calls { if c == s { return true } }; return false }

func tokBudget(maxTok budget.Limit) budget.Budget {
	return budget.Budget{Tokens: maxTok, Thresholds: budget.Thresholds{Reminder: 0.725, Cancel: 0.90, Hard: 1.00}, Prices: usage.DefaultPrices()}
}

func newWD(r usage.Reader, cc ccpool.Runner, bd *recBD, b budget.Budget) *Watchdog {
	return &Watchdog{
		Reader: r, CC: cc, BD: bd, Budget: b,
		RepoRoot: "/repo", WorktreeDir: "/wt",
		ReminderMsg: "near limit", WrapUpMsg: "wrap up now",
		Git: noopGit{}, Now: func() time.Time { return time.Unix(0, 0) }, Poll: time.Millisecond,
	}
}

type noopGit struct{}
func (noopGit) Run(context.Context, string, ...string) error { return nil }

func TestRun_firesEachLevelOnceThenHardStop(t *testing.T) {
	// token cap 1000; ramp crosses 72.5% -> 90% -> 100%
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 700}, {OutputTokens: 730}, {OutputTokens: 920}, {OutputTokens: 1000}}}
	cc := &fakeCC{list: []ccpool.Session{{Name: "s", Live: true, CWD: "/repo"}}}
	bd := &recBD{}
	wd := newWD(r, cc, bd, tokBudget(1000))
	err := wd.Run(context.Background(), "s", "zr-1")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
	// one queued reminder, one wrap-up; 2 cancels (90% + 100%)
	if len(cc.sent) != 2 {
		t.Errorf("want reminder+wrapup queued, got %v", cc.sent)
	}
	if cc.cancels != 2 {
		t.Errorf("want 2 cancels (90%% + 100%%), got %d", cc.cancels)
	}
	// terminal: comment + unclaim (NOT human)
	if !bd.has("comment zr-1 interrupted — budget") && !hasPrefix(bd.calls, "comment zr-1") {
		t.Errorf("missing budget note; calls=%v", bd.calls)
	}
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Errorf("hard stop must NOT add human")
		}
	}
}

func TestRun_ctxCancelReturnsCtxErr(t *testing.T) {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 0}}}
	cc := &fakeCC{list: []ccpool.Session{{Name: "s", Live: true}}}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wd.Run(ctx, "s", "zr-1"); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func hasPrefix(calls []string, p string) bool {
	for _, c := range calls { if len(c) >= len(p) && c[:len(p)] == p { return true } }
	return false
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/watchdog/` → FAIL.

- [ ] **Step 3: Implement `watchdog.go`** (the ladder; terminal lives in Task 8b's `terminal.go`):

```go
// Package watchdog meters a running worker session against a Budget and escalates.
package watchdog

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// ErrBudgetExceeded is returned by Run when the session hit 100% of its budget.
var ErrBudgetExceeded = errors.New("session budget exceeded")

// GitRunner runs `git -C <dir> <args...>` (injectable for tests).
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) error
}

type Watchdog struct {
	Reader                 usage.Reader
	CC                     ccpool.Runner
	BD                     beads.Runner
	Log                    *eventlog.Writer // may be nil (no-op)
	Budget                 budget.Budget
	RepoRoot, WorktreeDir  string
	ReminderMsg, WrapUpMsg string
	Git                    GitRunner
	Now                    func() time.Time
	Poll                   time.Duration
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watchdog) emit(kind string, fields map[string]any) {
	if w.Log != nil {
		_ = w.Log.Emit(kind, fields)
	}
}

// Run meters the session until ctx is cancelled (the bead-poll won the race) or
// the budget hard stop fires. Returns ctx.Err() on cancellation (no action), or
// ErrBudgetExceeded after running the terminal sequence at 100%.
func (w *Watchdog) Run(ctx context.Context, sessionName, beadID string) error {
	start := w.now()
	highest := budget.None
	for {
		snap, _ := w.Reader.Read(ctx, w.transcriptPath(ctx, sessionName))
		_, level := w.Budget.Evaluate(snap, w.now().Sub(start))
		if level > highest {
			highest = level
			switch level {
			case budget.Reminder:
				_ = w.CC.Send(ctx, sessionName, w.ReminderMsg, ccpool.ModeQueue)
				w.emit("reminder", map[string]any{"session": sessionName, "bead": beadID})
			case budget.Cancel:
				_ = w.CC.Cancel(ctx, sessionName)
				_ = w.CC.Send(ctx, sessionName, w.WrapUpMsg, ccpool.ModeQueue)
				w.emit("cancel", map[string]any{"session": sessionName, "bead": beadID})
			case budget.Hard:
				w.terminal(ctx, sessionName, beadID)
				return ErrBudgetExceeded
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.Poll):
		}
	}
}

// transcriptPath looks up the session's transcript path from ccpool.List.
func (w *Watchdog) transcriptPath(ctx context.Context, name string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.TranscriptPath
		}
	}
	return ""
}
```

> NOTE on the test's `select { case <-time.After(w.Poll) }`: with `Poll = 1ms` the ladder advances one Read per ~1ms; the ramp has 4 entries so the hard stop is reached in a few ms. The time dimension is driven by `w.Now` (fixed in the test ⇒ time%=0, so only the token ramp escalates). Deterministic.

- [ ] **Step 4: Run** — will still fail until `terminal` exists (Task 8b). That's expected; proceed to 8b, then run.

---

## Task 8b: `internal/watchdog` — terminal sequence + guarded worktree reset

**Files:**

- Create: `packages/pr-pool/internal/watchdog/terminal.go`
- Test: `packages/pr-pool/internal/watchdog/terminal_test.go`

- [ ] **Step 1: Write the failing test** `terminal_test.go` (guard table + the terminal side effects):

```go
package watchdog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// realGit runs real git (used to make a genuine worktree for the guard test).
type realGit struct{}
func (realGit) Run(_ context.Context, dir string, args ...string) error {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return c.Run()
}

// recGit records reset/clean calls without touching disk.
type recGit struct{ ran [][]string }
func (g *recGit) Run(_ context.Context, dir string, args ...string) error {
	g.ran = append(g.ran, append([]string{dir}, args...))
	return nil
}

func TestSafeToReset_guard(t *testing.T) {
	repo := t.TempDir()
	// path == repoRoot -> never
	if safeToReset(repo, repo, repo) {
		t.Error("must refuse to reset repoRoot")
	}
	// outside worktreeDir -> never
	if safeToReset("/somewhere/else", repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a path outside WorktreeDir")
	}
	// non-existent / not-a-worktree -> never (safe no-op)
	if safeToReset(filepath.Join(repo, "wt", "ghost"), repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a non-worktree path")
	}
}

func TestTerminal_unclaimsNotesNoHuman(t *testing.T) {
	cc := &fakeCC{}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.Git = &recGit{}
	// session cwd == repoRoot -> reset is a guarded no-op (the v1 reality)
	cc.list = []ccpool.Session{{Name: "s", CWD: "/repo"}}
	wd.terminal(context.Background(), "s", "zr-1")
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Error("must NOT add human")
		}
	}
}
```

(Reuse `fakeReader`, `fakeCC`, `recBD`, `tokBudget`, `newWD` from `watchdog_test.go` — same package.)

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/watchdog/` → FAIL (undefined terminal/safeToReset).

- [ ] **Step 3: Implement `terminal.go`**:

```go
package watchdog

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// terminal runs the 100% hard-stop sequence: 2nd cancel, guarded worktree reset,
// budget note, unclaim, eventlog. (Session close is done by the orchestrator's
// pass-level teardownAll, as in A.) Each step is best-effort.
func (w *Watchdog) terminal(ctx context.Context, sessionName, beadID string) {
	_ = w.CC.Cancel(ctx, sessionName) // 2nd cancel (idempotent/safe)

	wt := w.sessionCWD(ctx, sessionName)
	didReset := false
	if safeToReset(wt, w.RepoRoot, w.WorktreeDir) {
		if err := w.Git.Run(ctx, wt, "reset", "--hard"); err == nil {
			_ = w.Git.Run(ctx, wt, "clean", "-fd")
			didReset = true
		}
	}

	_ = beads.Comment(ctx, w.BD, beadID, "interrupted — budget")
	_ = beads.Unclaim(ctx, w.BD, beadID)
	w.emit("hard_stop", map[string]any{"session": sessionName, "bead": beadID, "worktree_reset": didReset, "worktree": wt})
}

func (w *Watchdog) sessionCWD(ctx context.Context, name string) string {
	sessions, err := w.CC.List(ctx)
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if s.Name == name {
			return s.CWD
		}
	}
	return ""
}

// safeToReset returns true only when path is a real git worktree root, distinct
// from repoRoot, inside worktreeDir. Symlink-resolved, boundary-checked (never a
// prefix-string match). On ANY uncertainty it returns false (no-op = safe).
func safeToReset(path, repoRoot, worktreeDir string) bool {
	if path == "" {
		return false
	}
	rp, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false // path doesn't exist -> safe no-op
	}
	rr, err := filepath.EvalSymlinks(repoRoot)
	if err == nil && rp == rr {
		return false // never the monorepo
	}
	wd, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(wd, rp)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false // outside worktreeDir
	}
	// backstop: must be a worktree ROOT (toplevel == path), not REPO_ROOT.
	tl, err := gitToplevel(rp)
	if err != nil || tl != rp {
		return false
	}
	return true
}
```

Add a tiny real-git helper used only by `safeToReset` (production), kept injectable-free since it only reads:

```go
// gitToplevel returns `git -C path rev-parse --show-toplevel` (EvalSymlinks'd).
func gitToplevel(path string) (string, error) {
	out, err := execGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	tl := strings.TrimSpace(out)
	if resolved, err := filepath.EvalSymlinks(tl); err == nil {
		return resolved, nil
	}
	return tl, nil
}
```

And `execGit` (production) in the same file:

```go
import "os/exec"

func execGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}
```

> The guard's `gitToplevel` reads real git (safe — read-only); the _mutating_ reset/clean go through the injectable `w.Git` so tests assert argv without touching disk. `safeToReset`'s worktree-root test in `TestSafeToReset_guard` uses temp dirs that are not git repos, so `gitToplevel` errors → returns false (the expected "refuse" outcomes). A positive-path test (a real worktree passes) is optional and may use `realGit` to `git init` + `git worktree add`.

- [ ] **Step 4: Run** — `go test ./internal/watchdog/ -race -v` → PASS (both watchdog.go and terminal.go tests). Then `go test ./... && go vet ./... && gofmt -l . && nix run nixpkgs#golangci-lint -- run ./...` → clean.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/watchdog
git commit -m "feat(pr-pool): budget watchdog ladder + terminal (guarded reset/note/unclaim) (pg2-y991)"
```

---

## Task 9: Orchestrator integration (run watchdog ‖ waitDone for workers)

**Files:**

- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go`
- Test: `packages/pr-pool/internal/orchestrator/orchestrator_test.go`

Wire the watchdog into `workOne` for **worker** dispatches: build the budget from config, append `budget.PromptLine()` to the worker nudge, then run `waitDone` and `watchdog.Run` under a shared cancellable context — the first to return cancels the other; the watchdog's `ErrBudgetExceeded` wins over a cancelled `waitDone`'s `ctx.Err()`.

- [ ] **Step 1: Write the failing test** (append to `orchestrator_test.go`) — a worker whose usage ramp forces a hard stop ends `open`-unclaimed, no `human`, and `workOne` returns a budget error; a feedback dispatch never runs a watchdog. Use a fake `usage.Reader` + the existing fakes. (Construct the orchestrator with an injected watchdog factory — see Step 3.)

```go
func TestWorkOne_workerBudgetHardStopUnclaimsNoHuman(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000 // finite token cap so the ramp can trip it
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}} // never completes on its own
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &rampReader{seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately over 100%
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	err := o.workOne(context.Background(), d)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if !hasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; updates=%v", bd.updates)
	}
	if hasUpdate(bd, "update zr-w --add-label human") {
		t.Error("hard stop must NOT add human")
	}
}
```

(Add a `rampReader` implementing `usage.Reader` and an `o.usageReader` injection point — Step 3 — plus `usage` to the test imports. `fakeCC.List` must serve `CWD`/`TranscriptPath`; extend it if needed.)

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/orchestrator/` → FAIL.

- [ ] **Step 3: Implement.** Give `Orchestrator` an injectable usage reader (so tests don't need a real transcript) and rewrite `workOne`'s worker path to run the watchdog concurrently:

Add a field + default:

```go
	usageReader usage.Reader // default usage.NewTranscriptReader()
```

```go
func (o *Orchestrator) reader() usage.Reader {
	if o.usageReader != nil {
		return o.usageReader
	}
	return usage.NewTranscriptReader()
}
```

Rewrite `workOne` (the Send → wait portion). For a worker, run `waitDone` + watchdog under an errgroup-style first-wins:

```go
func (o *Orchestrator) workOne(ctx context.Context, d discover.Dispatch) error {
	name := d.Role.SessionName(o.Cfg.SessionPrefix, d.BeadID)
	env := map[string]string{
		"BEADS_ACTOR":    d.Role.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, name, o.Cfg.RepoRoot, env); err != nil {
		return fmt.Errorf("ensure %s: %w", name, err)
	}
	nudge := d.Role.Nudge(d.BeadID, o.Cfg.WorktreeDir)
	if d.Role.Kind == roles.Worker {
		nudge += o.Cfg.WorkerBudget().PromptLine()
	}
	if err := o.CC.Send(ctx, name, nudge, ccpool.ModeNoWait); err != nil {
		if d.Role.Kind == roles.Feedback {
			_ = beads.Unclaim(ctx, o.BD, d.BeadID)
		}
		return fmt.Errorf("send %s: %w", name, err)
	}
	if d.Role.Kind != roles.Worker {
		return o.waitDone(ctx, d, name) // feedback: no watchdog (unchanged behavior)
	}
	return o.workerWaitWithWatchdog(ctx, d, name)
}

// workerWaitWithWatchdog runs waitDone and the budget watchdog concurrently.
// First to return a terminal result wins and cancels the other. The cancelled
// loser returns ctx.Err() (waitDone skips its failure action by design), so only
// the winner's outcome takes effect.
func (o *Orchestrator) workerWaitWithWatchdog(ctx context.Context, d discover.Dispatch, name string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	wd := &watchdog.Watchdog{
		Reader: o.reader(), CC: o.CC, BD: o.BD, Budget: o.Cfg.WorkerBudget(),
		RepoRoot: o.Cfg.RepoRoot, WorktreeDir: o.Cfg.WorktreeDir,
		ReminderMsg: o.Cfg.ReminderMsg, WrapUpMsg: o.Cfg.WrapUpMsg,
		Git: watchdog.OSGit{}, Now: o.now, Poll: o.Cfg.PollInterval,
	}

	type res struct{ err error }
	done := make(chan res, 2)
	go func() { done <- res{o.waitDone(ctx, d, name)} }()
	go func() { done <- res{wd.Run(ctx, name, d.BeadID)} }()

	first := <-done
	cancel()         // stop the loser
	<-done           // drain it (loser returns ctx.Err())
	return first.err // the winner's outcome (nil, fail-err, or ErrBudgetExceeded)
}
```

> `watchdog.OSGit{}` is the production `GitRunner` (add to `terminal.go`: `type OSGit struct{}; func (OSGit) Run(ctx context.Context, dir string, args ...string) error { return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Run() }`). Add config fields `ReminderMsg`/`WrapUpMsg` with defaults in Task 6b (e.g. "You are nearing your budget for this bead." / "Budget nearly exhausted — stop now, save your notes (bd comment), and finish/hand back."). Add `"github.com/phillipgreenii/pr-pool/internal/watchdog"` + `"...internal/usage"` imports to `orchestrator.go`.

- [ ] **Step 4: Run** — `go test ./internal/orchestrator/ -race -v` → all PASS (existing + new). Then the full gate: `go test ./... && go vet ./... && gofmt -l . && nix run nixpkgs#golangci-lint -- run ./...` → clean.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/orchestrator packages/pr-pool/internal/config packages/pr-pool/internal/watchdog
git commit -m "feat(pr-pool): run budget watchdog concurrently with waitDone for workers (pg2-y991)"
```

---

## Task 10: Full gate + ccpool N3 contract pin

**Files:** bead comment only (+ optional spec touch).

- [ ] **Step 1: Full gate**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/.claude/worktrees/pr-pool-b-watchdog
cd packages/pr-pool && gofmt -l . && go vet ./... && go test ./... -race && cd ../..
nix build .#pr-pool --no-link 2>&1 | tail -5
nix flake check 2>&1 | tail -20
```

Expected: gofmt clean, vet clean, all tests pass under `-race`, `nix build` + `nix flake check` green.

- [ ] **Step 2: Pin the N3 `cwd` addition** into `pg2-7mnq.4` (the watchdog's worktree-reset source), using the bd prefix:

```bash
env -u BEADS_DIR -u WORKSPACE bd comment pg2-7mnq.4 "Chunk B (pg2-y991) also consumes ccpool list --json: add a 'cwd' string field per session = the path the session is OPERATING in (ideally the LIVE path, e.g. tmux pane_current_path — NOT just the launch --cwd, which pr-pool sets to REPO_ROOT). pr-pool maps it to ccpool.Session.CWD and uses it for the budget watchdog's guarded worktree reset (reset only fires when cwd is a distinct worktree root, never REPO_ROOT). Until live-cwd exists, the reset is a safe no-op."
```

- [ ] **Step 3: Commit** any spec/doc touch (if made).

---

## Task 11: Live verification — BLOCKED on ccpool N3 (`pg2-7mnq.4`)

> DO NOT START until ccpool N3 lands (`list --json` with `transcript_path` + `cwd`). The token/cost dimensions + the worktree reset cannot be live-verified without it. The time dimension + the full unit suite are already verified. When unblocked: rebase onto a main with N3, set a finite `PR_POOL_BUDGET_TOKENS`/`PR_POOL_BUDGET_COST`, run a real worker pass, and confirm the reminder/cancel/hard-stop ladder + unclaim + eventlog against a live session. Confirm the live target with the user first.

---

## Self-Review

**Spec coverage:** 3 budget dims (Tasks 5/6 cost+budget; time in budget; tokens in usage) ✅; unlimited Limit (Task 6) ✅; cost estimate + price table (Task 5) ✅; Budget data object per-session (Tasks 6/6b/9) ✅; usage.Reader boundary (Task 4) ✅; hard-stop unclaim-not-human (Tasks 8b/9 + tests) ✅; ErrBudgetExceeded (Task 8) ✅; JSONL eventlog (Task 7b) ✅; worktree from ccpool + guard (Tasks 7/8b) ✅; worker-only scope (Task 9) ✅. Prereqs: P-A packaging ✅, P-B Comment ✅, P-C ErrCancelUnconfirmed ✅, P-D waitDone ctx+clock ✅.

**Placeholder scan:** No TBD/TODO. The two open spec choices were resolved as the spec's defaults (reminder 0.725; reset = `reset --hard && clean -fd`). The N3-blocked live verification (Task 11) is explicitly out-of-band, not a placeholder.

**Type consistency:** `ccpool.Session.CWD`, `beads.Comment`, `ccpool.ErrCancelUnconfirmed`, `usage.Snapshot/Reader/EstimateCents(int64)`, `budget.Budget/Evaluate/PromptLine/Limit`, `eventlog.Writer.Emit`, `watchdog.Watchdog{...}.Run`/`ErrBudgetExceeded`/`GitRunner`/`OSGit`, orchestrator `now`/`tick`/`usageReader` — all used consistently across tasks. Note: the spec referred to `roles.BudgetLine`; this plan instead puts `PromptLine()` on `budget.Budget` and appends it in the orchestrator (zero `roles` blast radius — a strict improvement on the spec's already-shrunk plan). `config` gains `ReminderMsg`/`WrapUpMsg` (Task 6b/9) — add them alongside the budget scalars.
