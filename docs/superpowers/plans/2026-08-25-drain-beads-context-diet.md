# drain-beads Context Diet Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps
> use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut the `/drain-beads` orchestrator's per-bead context cost from ~35K to ~8–12K
tokens by making briefs pointers, delegating LAND to a subagent, collapsing the isolate
and gate dances into two new `pb` subcommands, and splitting rare-path procedures into
on-demand skills.

**Architecture:** Two new cobra subcommands in the existing `pb` Go module
(`pb drain isolate`, `pb gate attach-verified-child`), each a thin CLI shell over an
`internal/` package using the established `run.Runner` dependency-injection seam; two new
auto-discovered skills in the `claude-marketplace/pb` plugin; a rewrite of
`claude-marketplace/pb/commands/drain-beads.md` that references them.

**Tech Stack:** Go 1.25 + cobra (no new deps), FakeRunner unit tests + real-git tempdir
tests, Claude Code marketplace skills (markdown), nix flake checks.

**Spec:** `docs/superpowers/specs/2026-08-25-drain-beads-context-diet-design.md`
(same repo — read it first; every design decision below is argued there).

## Global Constraints

- Repo root (absolute): `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`.
  Work in the existing worktree `.worktrees/drain-context-diet` (branch
  `drain-context-diet`); all relative paths below are repo-relative.
- FIRST ACTION before Task 1: the spec and this plan are UNTRACKED in the worktree —
  `git add docs/superpowers && git commit -m "docs: drain-beads context-diet spec + plan"`,
  so Task 9's `main...HEAD` prek sweep sees them.
- Go module: `github.com/phillipgreenii/pb` at `packages/pb/`, Go 1.25, cobra is the only
  direct dep. Do NOT add third-party deps (that would force a gomod2nix regen).
- New code may shell out ONLY to `bd`, `git` (both on pb's wrapped PATH), and `pn`
  (a deliberate ambient PATH dependency — `packages/pb/default.nix` documents why it is
  NOT wrapped). Anything else requires a wrapProgram change — out of scope.
- All bd calls go through `internal/bd.Client` with `BD_JSON_ENVELOPE=1` (its `bdEnv()`),
  targeting a DB via `bd -C <dir>`.
- Exit codes: 1 stays generic; every branchable meaning uses a distinct value >= 2.
- Unit tests are isolated: filesystem tests generate their scenario in `t.TempDir()`.
  Real-git tests are established practice (patchid); `checks.pb-go-tests` provides git.
- Markdown must be prettier-clean (`treefmt` hook formats `*.md`/`*.json`); watch
  trailing whitespace and end-of-file hooks.
- `git add` new files BEFORE any `prek run --files …` (prek silently skips untracked).
- Long commands (`nix build` of checks, `nix flake check`) MUST set an explicit timeout
  (>= 600000 ms) or run in the background — never re-issue unchanged after a timeout.
- Public repo: no user identifiers other than `phillipgreenii` anywhere.
- Commit messages: plain conventional subjects, no `Refs:` line (personal repo).
- This plan edits `claude-marketplace/pb/commands/drain-beads.md` — the live command a
  running drain session self-checks against. Landing it mid-drain will (correctly) halt
  those sessions at their next CLAIM; that is the designed behavior, not a bug.

---

### Task 1: `internal/bd` client methods for attach

**Files:**

- Modify: `packages/pb/internal/bd/bd.go`
- Test: `packages/pb/internal/bd/bd_test.go` (append; match its existing style)

**Interfaces:**

- Consumes: existing `bd.Client{R run.Runner}`, `bdEnv()`, envelope structs.
- Produces (Task 2 relies on these exact signatures):
  - `func (c Client) CreateBead(ctx context.Context, dir, title, deferUntil, deps, actor string) (string, error)`
  - `func (c Client) ReadyIDs(ctx context.Context, dir string) ([]string, error)`
  - `func (c Client) UpdateDefer(ctx context.Context, dir, id, deferUntil, actor string) error`
  - `func (c Client) Comment(ctx context.Context, dir, id, text, actor string) error`

- [ ] **Step 1: Write the failing tests**

Append to `packages/pb/internal/bd/bd_test.go` (style: exact-argv FakeRunner scripting,
one named scenario per function, mirroring the existing tests in that file):

```go
func TestCreateBead_argvAndID(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "verify x after apply (pg2-a)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-a",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	c := Client{R: f}
	id, err := c.CreateBead(context.Background(), "/db",
		"verify x after apply (pg2-a)", "2126-01-01", "discovered-from:pg2-a", "sess-1")
	if err != nil {
		t.Fatalf("CreateBead: %v", err)
	}
	if id != "pg2-child" {
		t.Errorf("id = %q, want pg2-child", id)
	}
}

func TestCreateBead_arrayEnvelope(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "t", "--defer", "2126-01-01",
		"--deps", "discovered-from:pg2-a", "--actor", "s", "--json",
	}, run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)
	id, err := Client{R: f}.CreateBead(context.Background(), "/db", "t", "2126-01-01", "discovered-from:pg2-a", "s")
	if err != nil || id != "pg2-child" {
		t.Fatalf("id, err = %q, %v; want pg2-child, nil", id, err)
	}
}

func TestCreateBead_noIDErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{
		"-C", "/db", "create", "t", "--defer", "2126-01-01",
		"--deps", "discovered-from:pg2-a", "--actor", "s", "--json",
	}, run.Result{Stdout: `{"data":{}}`}, nil)
	if _, err := (Client{R: f}).CreateBead(context.Background(), "/db", "t", "2126-01-01", "discovered-from:pg2-a", "s"); err == nil {
		t.Fatal("expected error when bd create returns no id")
	}
}

func TestReadyIDs_uncappedQueryAndParse(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-x"},{"id":"pg2-y"}]}`}, nil)
	ids, err := Client{R: f}.ReadyIDs(context.Background(), "/db")
	if err != nil {
		t.Fatalf("ReadyIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "pg2-x" || ids[1] != "pg2-y" {
		t.Errorf("ids = %v", ids)
	}
}

func TestReadyIDs_emptyQueueIsNotAnError(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	ids, err := Client{R: f}.ReadyIDs(context.Background(), "/db")
	if err != nil || len(ids) != 0 {
		t.Fatalf("ids, err = %v, %v; want empty, nil", ids, err)
	}
}

// The `data` key's PRESENCE is the positive control: output that parses but
// carries no data key (an error envelope, `{}`) must be an ERROR, never an
// empty set — an absence check against a vacuous parse proves nothing.
func TestReadyIDs_missingDataKeyErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{}`}, nil)
	if _, err := (Client{R: f}).ReadyIDs(context.Background(), "/db"); err == nil {
		t.Fatal("expected error for envelope without a data key")
	}
}

func TestReadyIDs_nullDataErrors(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":null,"error":"boom"}`}, nil)
	if _, err := (Client{R: f}).ReadyIDs(context.Background(), "/db"); err == nil {
		t.Fatal("expected error for null data")
	}
}

func TestUpdateDefer_clearUsesEmptyValue(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "update", "pg2-c", "--defer", "", "--actor", "s"},
		run.Result{}, nil)
	if err := (Client{R: f}).UpdateDefer(context.Background(), "/db", "pg2-c", "", "s"); err != nil {
		t.Fatalf("UpdateDefer: %v", err)
	}
}

func TestComment_argv(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", "/db", "comment", "pg2-a",
		"post-deploy verification gated as pg2-c (pn:applied).", "--actor", "s"},
		run.Result{}, nil)
	if err := (Client{R: f}).Comment(context.Background(), "/db", "pg2-a",
		"post-deploy verification gated as pg2-c (pn:applied).", "s"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pb && go test ./internal/bd/`
Expected: FAIL — `c.CreateBead undefined` (and the other three methods).

- [ ] **Step 3: Implement the methods**

Append to `packages/pb/internal/bd/bd.go`:

```go
// beadCreateEnvelope tolerates both envelope shapes bd has emitted for
// `create --json`: {"data":{"id":...}} and {"data":[{"id":...}]}.
type beadCreateEnvelope struct {
	Data json.RawMessage `json:"data"`
}

// readyEnvelope's Data is a POINTER so a missing or null `data` key is
// distinguishable from a legitimately empty queue: presence of the key is the
// positive control the prose procedure implemented as "non-empty bd ready".
type readyEnvelope struct {
	Data *[]struct {
		ID string `json:"id"`
	} `json:"data"`
}

// CreateBead creates a bead titled title (born deferred until deferUntil when
// non-empty, with deps such as "discovered-from:<id>") and returns the new id.
func (c Client) CreateBead(ctx context.Context, dir, title, deferUntil, deps, actor string) (string, error) {
	args := []string{"-C", dir, "create", title}
	if deferUntil != "" {
		args = append(args, "--defer", deferUntil)
	}
	if deps != "" {
		args = append(args, "--deps", deps)
	}
	args = append(args, "--actor", actor, "--json")
	res, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return "", fmt.Errorf("bd create: %w", err)
	}
	return parseCreatedBeadID(res.Stdout)
}

func parseCreatedBeadID(out string) (string, error) {
	var env beadCreateEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return "", fmt.Errorf("parse bd create json: %w", err)
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &obj); err == nil && obj.ID != "" {
		return obj.ID, nil
	}
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &arr); err == nil && len(arr) == 1 && arr[0].ID != "" {
		return arr[0].ID, nil
	}
	return "", fmt.Errorf("bd create returned no id: %s", out)
}

// ReadyIDs returns the ids of ALL ready beads in the DB at dir. -n 0 is
// load-bearing: bd ready caps its rows by default, and a capped absence check
// proves nothing.
func (c Client) ReadyIDs(ctx context.Context, dir string) ([]string, error) {
	res, err := c.R.Run(ctx, "bd", []string{"-C", dir, "ready", "--json", "-n", "0"},
		run.Options{Env: bdEnv()})
	if err != nil {
		return nil, fmt.Errorf("bd ready in %q: %w", dir, err)
	}
	var env readyEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return nil, fmt.Errorf("parse bd ready json: %w", err)
	}
	if env.Data == nil {
		return nil, fmt.Errorf("bd ready returned no data envelope (positive control failed): %s", res.Stdout)
	}
	ids := make([]string, 0, len(*env.Data))
	for _, d := range *env.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// UpdateDefer sets (or, with deferUntil == "", clears) the defer on issue id.
func (c Client) UpdateDefer(ctx context.Context, dir, id, deferUntil, actor string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--defer", deferUntil, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --defer: %w", err)
	}
	return nil
}

// Comment appends a comment to issue id.
func (c Client) Comment(ctx context.Context, dir, id, text, actor string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "comment", id, text, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd comment: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pb && go test ./internal/bd/`
Expected: PASS.

- [ ] **Step 5: Confirm the real envelope shape from the existing contract evidence**

Do NOT probe with an ad-hoc `bd init`/`bd create` — real-`bd` runs on this machine MUST
use the isolated-HOME/XDG harness (`cmd/pb/contract_test.go`'s `isolate(t)`) or they hit
the shared Dolt server. The evidence already exists: `cmd/pb/contract_test.go`'s `dataID`
helper (around line 109) pins the real `bd … --json` envelope as `{"data":{"id":…}}` —
exactly `parseCreatedBeadID`'s object branch. Read that helper to confirm; the
array-tolerant branch stays as forward-compat for bd v2's envelope flip.

For `ReadyIDs`' pointer-based positive control: verified 2026-08-25 (read-only probe on
this machine) that an empty ready set emits `{"data": [], "schema_version": 1}` — the
`data` key present and non-null — so the control passes on the last bead of a drain and
errors only on a genuinely malformed/error envelope.

- [ ] **Step 6: Commit**

```bash
git add packages/pb/internal/bd/bd.go packages/pb/internal/bd/bd_test.go
git commit -m "feat(pb): bd client methods for verified-child gating (CreateBead, ReadyIDs, UpdateDefer, Comment)"
```

---

### Task 2: `internal/gate.Attach` — the deferred-first gate sequence

**Files:**

- Create: `packages/pb/internal/gate/attach.go`
- Test: `packages/pb/internal/gate/attach_test.go`

**Interfaces:**

- Consumes: Task 1's four `bd.Client` methods; existing `gate.Create`,
  `gate.CreateDeps`, `resolveBeadDB`, `pn.Client.Info`.
- Produces (Task 3 relies on these):
  - `gate.GateSpec{Repo, Commit string}`
  - `gate.AttachParams{WorkspaceDir, ImplID, Title string, Gates []GateSpec, Actor, Reason string}`
  - `gate.AttachResult{ChildID string, Gates []CreatedGate, CommentFailed bool}` (JSON tags `child`, `gates`, `comment_failed`)
  - `func Attach(ctx context.Context, d CreateDeps, p AttachParams) (AttachResult, error)`
  - Sentinels: `gate.ErrGatingIncomplete` (child left DEFERRED — safe),
    `gate.ErrChildMayBeWorkable` (dangerous — do not close the impl bead)

- [ ] **Step 1: Write the failing tests**

Create `packages/pb/internal/gate/attach_test.go`. Reuse `stubDiscoverWS` and
`createInfoJSON` from `create_test.go` (same package). Note: `Attach` calls
`gate.Create` once per `--gate` pair, and `Create` re-runs `pn workspace info` and the
`HasBead` DB probe internally — the FakeRunner consumes responses in order, so script
those repeats explicitly.

```go
package gate

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

func attachDeps(f *run.FakeRunner) CreateDeps {
	return CreateDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, R: f, Discover: stubDiscoverWS}
}

// scriptAttachPreamble scripts Attach's own preamble: info, impl-DB resolve,
// child create, and the deferred-first ready check (child absent).
func scriptAttachPreamble(f *run.FakeRunner) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-impl", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "create", "verify thing after apply (pg2-impl)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-impl",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-other"}]}`}, nil)
}

// scriptOneGate scripts one inner gate.Create for repo-a at sha1 blocking the child.
func scriptOneGate(f *run.FakeRunner, gateID string) {
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-child", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "show", "sha1"}, run.Result{Stdout: "diff..."}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "pid1 sha1\n"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "gate", "create", "--type=pn:applied", "--blocks", "pg2-child",
		"--await-id", "home:repo-a:pid1", "--reason", "post-deploy verify for pg2-impl", "--json",
	}, run.Result{Stdout: `{"data":{"id":"` + gateID + `"}}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", gateID, "--set-metadata", "applied_baseline=base1"},
		run.Result{}, nil)
}

func attachParams() AttachParams {
	return AttachParams{
		WorkspaceDir: "/ws", ImplID: "pg2-impl",
		Title: "verify thing after apply (pg2-impl)",
		Gates: []GateSpec{{Repo: "repo-a", Commit: "sha1"}},
		Actor: "sess-1", Reason: "post-deploy verify for pg2-impl",
	}
}

func TestAttach_happyPathDeferredFirst(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	scriptOneGate(f, "g-1")
	// un-defer, then re-confirm the GATES (not the defer) hold the child
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "comment", "pg2-impl",
		"post-deploy verification gated as pg2-child (pn:applied).", "--actor", "sess-1"},
		run.Result{}, nil)

	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if out.ChildID != "pg2-child" || len(out.Gates) != 1 || out.CommentFailed {
		t.Errorf("out = %+v", out)
	}
}

func TestAttach_gateCreateFailureLeavesChildDeferred(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	// inner Create fails at pn info (unscripted call → FakeRunner error)
	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if !errors.Is(err, ErrGatingIncomplete) {
		t.Fatalf("err = %v, want ErrGatingIncomplete", err)
	}
	if out.ChildID != "pg2-child" {
		t.Errorf("partial result must still name the child: %+v", out)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 6 && c.Args[2] == "update" && c.Args[4] == "--defer" && c.Args[5] == "" {
			t.Fatal("child must NOT be un-deferred after a gate failure")
		}
	}
}

func TestAttach_childInReadyRepairsOnceThenFails(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "show", "pg2-impl", "--json"}, run.Result{Stdout: "{}"}, nil)
	f.AddResponse("bd", []string{
		"-C", "/ws", "create", "verify thing after apply (pg2-impl)",
		"--defer", "2126-01-01", "--deps", "discovered-from:pg2-impl",
		"--actor", "sess-1", "--json",
	}, run.Result{Stdout: `{"data":{"id":"pg2-child"}}`}, nil)
	// child present in ready → repair (re-apply defer) → still present → fail
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "2126-01-01", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[{"id":"pg2-child"}]}`}, nil)

	_, err := Attach(context.Background(), attachDeps(f), attachParams())
	if !errors.Is(err, ErrChildMayBeWorkable) {
		t.Fatalf("err = %v, want ErrChildMayBeWorkable", err)
	}
}

func TestAttach_zeroGatesRejected(t *testing.T) {
	f := run.NewFakeRunner()
	p := attachParams()
	p.Gates = nil
	if _, err := Attach(context.Background(), attachDeps(f), p); err == nil {
		t.Fatal("expected error for zero gates (would un-defer a completely ungated child)")
	}
	if len(f.Calls()) != 0 {
		t.Errorf("no external call may run for an invalid invocation: %v", f.Calls())
	}
}

func TestAttach_unknownGateRepoFailsBeforeChildCreate(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: createInfoJSON}, nil)
	p := attachParams()
	p.Gates = []GateSpec{{Repo: "typo", Commit: "sha1"}}
	_, err := Attach(context.Background(), attachDeps(f), p)
	if err == nil || errors.Is(err, ErrGatingIncomplete) || errors.Is(err, ErrChildMayBeWorkable) {
		t.Fatalf("want a plain pre-creation error (exit 1, an invocation typo), got %v", err)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 3 && c.Args[2] == "create" {
			t.Fatal("child must not be created for an invalid --gate repo")
		}
	}
}

func TestAttach_commentFailureIsNotFatal(t *testing.T) {
	f := run.NewFakeRunner()
	scriptAttachPreamble(f)
	scriptOneGate(f, "g-1")
	f.AddResponse("bd", []string{"-C", "/ws", "update", "pg2-child", "--defer", "", "--actor", "sess-1"},
		run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "ready", "--json", "-n", "0"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	// comment unscripted → fails; gating is already complete and safe
	out, err := Attach(context.Background(), attachDeps(f), attachParams())
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !out.CommentFailed {
		t.Error("CommentFailed must be reported")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pb && go test ./internal/gate/ -run TestAttach`
Expected: FAIL — `undefined: Attach` (compile error).

- [ ] **Step 3: Implement `Attach`**

Create `packages/pb/internal/gate/attach.go`:

```go
package gate

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrGatingIncomplete: the child exists but is not fully gated; it was LEFT
	// DEFERRED, so no peer can claim it. Safe to retry; the impl bead must not
	// be closed. The CLI maps this to exit 3.
	ErrGatingIncomplete = errors.New("gating incomplete; child left deferred")
	// ErrChildMayBeWorkable: the child could not be proven absent from bd ready.
	// A peer draining the queue could claim it and "verify" unapplied code. The
	// CLI maps this to exit 4; the impl bead must NOT be closed.
	ErrChildMayBeWorkable = errors.New("child may be workable")
)

// childDeferUntil is a far-future ABSOLUTE date: --defer takes --due's formats,
// which have no year unit.
const childDeferUntil = "2126-01-01"

type GateSpec struct {
	Repo   string
	Commit string
}

type AttachParams struct {
	WorkspaceDir string
	ImplID       string
	Title        string
	Gates        []GateSpec
	Actor        string
	Reason       string
}

type AttachResult struct {
	ChildID       string        `json:"child"`
	Gates         []CreatedGate `json:"gates"`
	CommentFailed bool          `json:"comment_failed,omitempty"`
}

// Attach runs the deferred-first verified-child sequence: create the child
// DEFERRED, prove it is not workable, attach one pn:applied gate per GateSpec,
// un-defer, prove the gates now hold it, and record the link on the impl bead.
// The ordering closes the fleet-claim race: the child is never simultaneously
// workable and ungated.
func Attach(ctx context.Context, d CreateDeps, p AttachParams) (AttachResult, error) {
	if len(p.Gates) == 0 {
		return AttachResult{}, errors.New("at least one gate is required")
	}
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return AttachResult{}, err
	}
	// Fail on a bad --gate repo key BEFORE the child exists: gate.Create would
	// catch it per gate, but only after CreateBead — turning an invocation typo
	// into a STUCK-routed exit 3.
	for _, g := range p.Gates {
		if _, ok := info.RepoByName(g.Repo); !ok {
			return AttachResult{}, fmt.Errorf("repo %q is not in workspace %q", g.Repo, info.Root)
		}
	}
	dbDir, err := resolveBeadDB(ctx, d, info, p.ImplID)
	if err != nil {
		return AttachResult{}, err
	}

	childID, err := d.BD.CreateBead(ctx, dbDir, p.Title, childDeferUntil,
		"discovered-from:"+p.ImplID, p.Actor)
	if err != nil {
		return AttachResult{}, err
	}
	res := AttachResult{ChildID: childID}

	if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
		// One repair attempt: re-apply the defer, re-check.
		if uerr := d.BD.UpdateDefer(ctx, dbDir, childID, childDeferUntil, p.Actor); uerr != nil {
			return res, fmt.Errorf("%w: %s: defer re-apply failed: %v", ErrChildMayBeWorkable, childID, uerr)
		}
		if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
			return res, fmt.Errorf("%w: %s: %v", ErrChildMayBeWorkable, childID, err)
		}
	}

	for _, g := range p.Gates {
		out, err := Create(ctx, d, CreateParams{
			WorkspaceDir: p.WorkspaceDir, BeadID: childID,
			Repo: g.Repo, Commit: g.Commit, Reason: p.Reason,
		})
		res.Gates = append(res.Gates, out.Gates...)
		if err != nil {
			return res, fmt.Errorf("%w: gate for %s@%s: %v", ErrGatingIncomplete, g.Repo, g.Commit, err)
		}
	}

	if err := d.BD.UpdateDefer(ctx, dbDir, childID, "", p.Actor); err != nil {
		return res, fmt.Errorf("%w: %s: un-defer failed: %v", ErrGatingIncomplete, childID, err)
	}
	// The gates, not the defer, must now hold the child out of bd ready.
	if err := confirmNotReady(ctx, d, dbDir, childID); err != nil {
		return res, fmt.Errorf("%w: %s after un-defer: %v", ErrChildMayBeWorkable, childID, err)
	}

	if err := d.BD.Comment(ctx, dbDir, p.ImplID,
		fmt.Sprintf("post-deploy verification gated as %s (pn:applied).", childID),
		p.Actor); err != nil {
		res.CommentFailed = true // gating is complete and safe; only the record failed
	}
	return res, nil
}

// confirmNotReady proves childID is absent from the UNCAPPED bd ready set. A
// successful parse of the envelope is itself the positive control (the prose
// procedure needed a non-empty queue because a text agent cannot verify the
// envelope; the client can, so an empty queue — normal when draining the last
// bead — passes).
func confirmNotReady(ctx context.Context, d CreateDeps, dbDir, childID string) error {
	ids, err := d.BD.ReadyIDs(ctx, dbDir)
	if err != nil {
		return fmt.Errorf("could not prove absence: %v", err)
	}
	for _, id := range ids {
		if id == childID {
			return fmt.Errorf("child %s is present in bd ready", childID)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pb && go test ./internal/gate/ -run TestAttach`
Expected: PASS. Then run the full package (`go test ./internal/gate/`) to confirm no
existing test broke.

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/gate/attach.go packages/pb/internal/gate/attach_test.go
git commit -m "feat(pb): gate.Attach — deferred-first verified-child gating sequence"
```

---

### Task 3: `pb gate attach-verified-child` CLI

**Files:**

- Create: `packages/pb/cmd/pb/gate_attach_verified_child.go`
- Modify: `packages/pb/cmd/pb/gate.go` (register the new subcommand)
- Modify: `packages/pb/README.md` (document the verb + exit codes)
- Test: `packages/pb/cmd/pb/gate_attach_verified_child_test.go`

**Interfaces:**

- Consumes: Task 2's `gate.Attach`, `gate.AttachParams`, `gate.GateSpec`, sentinels.
- Produces: the CLI contract Task 8's markdown cites — flags `--impl --title --gate
--actor --reason --json`, human output `child=<id> gates=<n>`, exit codes 0/1/3/4.

- [ ] **Step 1: Write the failing tests (CLI boundary only — flag parsing/validation)**

Create `packages/pb/cmd/pb/gate_attach_verified_child_test.go`:

```go
package main

import (
	"bytes"
	"testing"
)

func TestParseGateSpecs(t *testing.T) {
	specs, err := parseGateSpecs([]string{"repo-a=sha1", "repo-b=sha2"})
	if err != nil {
		t.Fatalf("parseGateSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0].Repo != "repo-a" || specs[0].Commit != "sha1" ||
		specs[1].Repo != "repo-b" || specs[1].Commit != "sha2" {
		t.Errorf("specs = %+v", specs)
	}
	for _, bad := range []string{"", "repo-a", "=sha1", "repo-a="} {
		if _, err := parseGateSpecs([]string{bad}); err == nil {
			t.Errorf("parseGateSpecs(%q): expected error", bad)
		}
	}
}

func TestAttachVerifiedChildCmd_requiredFlags(t *testing.T) {
	cmd := newGateAttachVerifiedChildCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{}) // nothing supplied
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected required-flag error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pb && go test ./cmd/pb/ -run 'TestParseGateSpecs|TestAttachVerifiedChildCmd'`
Expected: FAIL — `undefined: parseGateSpecs` (compile error).

- [ ] **Step 3: Implement the command**

Create `packages/pb/cmd/pb/gate_attach_verified_child.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

func parseGateSpecs(raw []string) ([]gate.GateSpec, error) {
	specs := make([]gate.GateSpec, 0, len(raw))
	for _, s := range raw {
		repo, sha, ok := strings.Cut(s, "=")
		if !ok || repo == "" || sha == "" {
			return nil, fmt.Errorf("--gate %q: want <repo-key>=<sha>", s)
		}
		specs = append(specs, gate.GateSpec{Repo: repo, Commit: sha})
	}
	return specs, nil
}

func newGateAttachVerifiedChildCmd() *cobra.Command {
	var (
		impl, title, actor, reason string
		gates                      []string
		asJSON                     bool
	)
	cmd := &cobra.Command{
		Use:   "attach-verified-child",
		Short: "Create a DEFERRED verification child of --impl, gate it on the landed commits, then un-defer",
		Long: `Runs the deferred-first post-deploy gate sequence for a landed bead:
create the verification child deferred, prove it is absent from bd ready,
attach one pn:applied gate per --gate <repo-key>=<sha>, un-defer, re-prove
absence, and comment the link on the implementation bead.

Exit codes: 0 fully gated; 1 generic failure; 3 gating incomplete and the
child was LEFT DEFERRED (safe — route the impl bead to STUCK); 4 the child
could not be proven un-workable (do NOT close the impl bead).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			specs, err := parseGateSpecs(gates)
			if err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			if reason == "" {
				reason = "post-deploy verify for " + impl
			}
			r := run.CLIRunner{}
			d := gate.CreateDeps{PN: pn.Client{R: r}, BD: bd.Client{R: r}, PatchID: patchid.Client{R: r}, R: r}
			out, aerr := gate.Attach(context.Background(), d, gate.AttachParams{
				WorkspaceDir: wd, ImplID: impl, Title: title,
				Gates: specs, Actor: actor, Reason: reason,
			})
			// Emit the result BEFORE exiting: on partial failure the child id and
			// the gates already created are what the operator needs.
			if asJSON {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			} else if out.ChildID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "child=%s gates=%d\n", out.ChildID, len(out.Gates))
			}
			// The warning goes to stderr under BOTH output modes: a --json caller
			// still gets "comment_failed":true, but stderr is what a human sees.
			if out.CommentFailed {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb: warning: gating complete but the impl-bead comment failed; record the link manually")
			}
			if aerr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb:", aerr)
				switch {
				case errors.Is(aerr, gate.ErrChildMayBeWorkable):
					os.Exit(4)
				case errors.Is(aerr, gate.ErrGatingIncomplete):
					os.Exit(3)
				}
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&impl, "impl", "", "implementation bead id (required)")
	cmd.Flags().StringVar(&title, "title", "", "verification child title (required)")
	cmd.Flags().StringArrayVar(&gates, "gate", nil, "<repo-key>=<sha> to gate on; repeatable, one per changed repo (required)")
	cmd.Flags().StringVar(&actor, "actor", "", "bd actor id (required)")
	cmd.Flags().StringVar(&reason, "reason", "", `gate reason (default "post-deploy verify for <impl>")`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("impl")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("gate")
	_ = cmd.MarkFlagRequired("actor")
	return cmd
}
```

(Deliberate convention change: existing pb commands validate required flags manually in
`RunE`; `MarkFlagRequired` fails earlier with cobra's standard message. Keep it. The
`os.Exit` calls inside `RunE` match the existing `gate_check.go` precedent.)

In `packages/pb/cmd/pb/gate.go`, add the registration line beside the existing two:

```go
cmd.AddCommand(newGateAttachVerifiedChildCmd())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pb && go test ./cmd/pb/`
Expected: PASS (new tests plus the existing gate_check tests).

- [ ] **Step 5: Document in the pb README**

In `packages/pb/README.md`, add an `attach-verified-child` subsection beside the
existing `gate create` / `gate check` docs covering: purpose (one paragraph — the
deferred-first sequence and why ordering matters), the flag list, the exit-code table
(0/1/3/4 with the safe-vs-dangerous distinction), and one example invocation:

```bash
pb gate attach-verified-child \
  --impl pg2-huyhg \
  --title "verify tldr wsplan renders after apply (pg2-huyhg): run tldr wsplan, compare against a known-good sibling page" \
  --gate phillipg-nix-repo-base=9167a60 \
  --actor "$CLAUDE_SESSION_ID-drain"
```

- [ ] **Step 6: Commit**

```bash
git add packages/pb/cmd/pb/gate_attach_verified_child.go packages/pb/cmd/pb/gate_attach_verified_child_test.go packages/pb/cmd/pb/gate.go packages/pb/README.md
git commit -m "feat(pb): gate attach-verified-child subcommand (exit 3=left-deferred, 4=may-be-workable)"
```

(No contract-test scenario: `contract_test.go` has no helper that runs a compiled `pb`,
and invoking the cobra command in-process would `os.Exit` the test binary on the 3/4
branches. The exit-code branches are covered at the unit level via the `errors.Is`
sentinels; the real-bd envelope shape is already pinned by the existing `dataID`
contract helper.)

---

### Task 4: `internal/drain.Isolate` — worktree + prek-config in one call

**Files:**

- Create: `packages/pb/internal/drain/isolate.go`
- Test: `packages/pb/internal/drain/isolate_test.go`

**Interfaces:**

- Consumes: `run.Runner` (real `CLIRunner` in tests — real-git tempdir style, like
  `internal/patchid`).
- Produces (Task 5 relies on these):
  - `drain.Params{RepoPath, BeadID string}`
  - `drain.Result{Worktree, Branch, Reused, Precommit string}` (JSON tags `worktree`,
    `branch`, `reused`, `precommit`; `Reused` ∈ none|worktree|branch, `Precommit` ∈
    linked|present|none)
  - `func Isolate(ctx context.Context, r run.Runner, p Params) (Result, error)`
  - Sentinel: `drain.ErrConflict` (CLI exit 3)

- [ ] **Step 1: Write the failing tests**

Create `packages/pb/internal/drain/isolate_test.go`. These use REAL git in `t.TempDir()`
(mirror `internal/patchid`'s test setup for env-isolated git; the committer identity is
passed per-call so no global config is read):

```go
package drain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// gitTest runs git with a hermetic config (no user/global gitconfig).
func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := run.CLIRunner{}
	full := append([]string{"-C", dir,
		"-c", "user.email=test@example.com", "-c", "user.name=test",
		"-c", "commit.gpgsign=false"}, args...)
	res, err := r.Run(context.Background(), "git", full, run.Options{
		Env: append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"),
	})
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, res.Stderr)
	}
	return res.Stdout
}

// newRepo creates a repo on branch main with one commit and returns its path.
// The tempdir is symlink-resolved up front (macOS t.TempDir() lives under
// /var/folders → /private/var/folders; git reports resolved paths, so the test
// must compare in resolved space).
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "f.txt")
	gitTest(t, dir, "commit", "-m", "init")
	return dir
}

func TestIsolate_freshWorktree(t *testing.T) {
	repo := newRepo(t)
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x1"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	want := filepath.Join(repo, ".worktrees", "pg2-x1")
	if out.Worktree != want || out.Branch != "drain/pg2-x1" || out.Reused != "none" || out.Precommit != "none" {
		t.Errorf("out = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(want, "f.txt")); err != nil {
		t.Errorf("worktree not materialized: %v", err)
	}
}

func TestIsolate_reusesExistingWorktree(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x2"}); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x2"})
	if err != nil {
		t.Fatalf("second Isolate: %v", err)
	}
	if out.Reused != "worktree" {
		t.Errorf("Reused = %q, want worktree", out.Reused)
	}
}

func TestIsolate_reusesParkedBranch(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x3"}); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "remove", filepath.Join(repo, ".worktrees", "pg2-x3"))
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x3"})
	if err != nil {
		t.Fatalf("re-Isolate: %v", err)
	}
	if out.Reused != "branch" {
		t.Errorf("Reused = %q, want branch (parked branch must be reused, not recreated)", out.Reused)
	}
}

func TestIsolate_conflictingCheckoutErrors(t *testing.T) {
	repo := newRepo(t)
	// occupy the worktree path with the WRONG branch
	gitTest(t, repo, "worktree", "add", filepath.Join(repo, ".worktrees", "pg2-x4"), "-b", "other", "main")
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x4"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestIsolate_branchCheckedOutElsewhereErrors(t *testing.T) {
	repo := newRepo(t)
	gitTest(t, repo, "worktree", "add", filepath.Join(repo, "elsewhere"), "-b", "drain/pg2-x5", "main")
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x5"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestIsolate_linksPrecommitConfig(t *testing.T) {
	repo := newRepo(t)
	target := filepath.Join(t.TempDir(), "generated-config.yaml")
	if err := os.WriteFile(target, []byte("repos: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// canonical clones carry a gitignored SYMLINK to the nix-generated config
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x6"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if out.Precommit != "linked" {
		t.Errorf("Precommit = %q, want linked", out.Precommit)
	}
	// The worktree links to the CANONICAL config path (symlink-to-symlink), so a
	// later hook re-install in the canonical clone propagates to the worktree.
	got, err := os.Readlink(filepath.Join(out.Worktree, ".pre-commit-config.yaml"))
	if err != nil || got != src {
		t.Errorf("worktree config link = %q, %v; want %q", got, err, src)
	}
}

func TestIsolate_detachedWorktreeAtPathConflicts(t *testing.T) {
	repo := newRepo(t)
	sha := strings.TrimSpace(gitTest(t, repo, "rev-parse", "HEAD"))
	gitTest(t, repo, "worktree", "add", "--detach", filepath.Join(repo, ".worktrees", "pg2-x9"), sha)
	_, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x9"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict (detached-HEAD occupant, not exit-1 noise)", err)
	}
}

func TestIsolate_staleWorktreeEntryIsPrunedAndRecreated(t *testing.T) {
	repo := newRepo(t)
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xa"}); err != nil {
		t.Fatal(err)
	}
	// delete the directory WITHOUT `git worktree remove` — a stale registration
	if err := os.RemoveAll(filepath.Join(repo, ".worktrees", "pg2-xa")); err != nil {
		t.Fatal(err)
	}
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-xa"})
	if err != nil {
		t.Fatalf("re-Isolate after stale entry: %v", err)
	}
	if out.Reused != "branch" {
		t.Errorf("Reused = %q, want branch (entry pruned, surviving branch reused)", out.Reused)
	}
	if _, err := os.Stat(filepath.Join(out.Worktree, "f.txt")); err != nil {
		t.Errorf("worktree not re-materialized on disk: %v", err)
	}
}

func TestIsolate_primaryBranchFromGitConfig(t *testing.T) {
	repo := newRepo(t)
	gitTest(t, repo, "branch", "trunk")
	gitTest(t, repo, "config", "pgii-integrate-branch.primaryBranch", "trunk")
	// advance main past trunk so basing on the wrong branch is detectable
	if err := os.WriteFile(filepath.Join(repo, "g.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "g.txt")
	gitTest(t, repo, "commit", "-m", "main moves on")
	out, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: repo, BeadID: "pg2-x7"})
	if err != nil {
		t.Fatalf("Isolate: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(out.Worktree, "g.txt")); statErr == nil {
		t.Error("worktree contains main's commit; branch was not based on the configured primary (trunk)")
	}
}

func TestIsolate_notAGitRepoErrors(t *testing.T) {
	if _, err := Isolate(context.Background(), run.CLIRunner{}, Params{RepoPath: t.TempDir(), BeadID: "pg2-x8"}); err == nil {
		t.Fatal("expected error for non-repo path")
	}
}
```

(Imports for the test file: `context`, `errors`, `os`, `path/filepath`, `strings`,
`testing`, and `github.com/phillipgreenii/pb/internal/run` — matching the code block
above.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pb && go test ./internal/drain/`
Expected: FAIL — package does not exist / `undefined: Isolate`.

- [ ] **Step 3: Implement `Isolate`**

Create `packages/pb/internal/drain/isolate.go`:

```go
// Package drain implements /drain-beads isolation: one call that creates (or
// reuses) a bead's worktree on its drain/<id> branch and links the canonical
// clone's nix-generated pre-commit config into it (the config is a gitignored
// symlink — absent from fresh worktrees, so commits there would abort;
// phillipg-nix-repo-base ADR 0016).
package drain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/pb/internal/run"
)

// ErrConflict: the isolation state on disk contradicts the request (the
// worktree path holds another branch, or drain/<id> is checked out elsewhere).
// Never resolved by force — the caller routes the bead to STUCK. CLI exit 3.
var ErrConflict = errors.New("conflicting isolation state")

type Params struct {
	RepoPath string // absolute canonical clone path
	BeadID   string
}

type Result struct {
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Reused    string `json:"reused"`    // none | worktree | branch
	Precommit string `json:"precommit"` // linked | present | none
}

func Isolate(ctx context.Context, r run.Runner, p Params) (Result, error) {
	top, err := r.Run(ctx, "git", []string{"-C", p.RepoPath, "rev-parse", "--show-toplevel"}, run.Options{})
	if err != nil {
		return Result{}, fmt.Errorf("%s is not a git repo: %w", p.RepoPath, err)
	}
	// Use git's own view of the toplevel from here on: `git worktree list
	// --porcelain` reports symlink-RESOLVED paths (macOS /var → /private/var),
	// so building wt from the caller's spelling would miss the map lookup and
	// misread an existing worktree as absent.
	repo := strings.TrimSpace(top.Stdout)
	branch := "drain/" + p.BeadID
	ref := "refs/heads/" + branch
	wt := filepath.Join(repo, ".worktrees", p.BeadID)
	res := Result{Worktree: wt, Branch: branch}

	checkouts, err := worktreeBranches(ctx, r, repo)
	if err != nil {
		return Result{}, err
	}

	if got, registered := checkouts[wt]; registered {
		if got != ref {
			return Result{}, fmt.Errorf("%w: %s has %s checked out, expected %s",
				ErrConflict, wt, got, ref)
		}
		if _, statErr := os.Stat(wt); statErr == nil {
			res.Reused = "worktree"
		} else {
			// Stale registration: the directory was deleted without
			// `git worktree remove`. Prune it and recreate below.
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "prune"}, run.Options{}); err != nil {
				return Result{}, err
			}
			delete(checkouts, wt)
		}
	}

	if res.Reused != "worktree" {
		// A path that exists but is not registered on our branch is occupied by
		// something else — a plain directory, or a detached-HEAD worktree (which
		// has no branch line in the porcelain output). Never force it.
		if _, lstatErr := os.Lstat(wt); lstatErr == nil {
			return Result{}, fmt.Errorf("%w: %s exists but does not hold %s", ErrConflict, wt, ref)
		}
		for path, b := range checkouts {
			if b == ref {
				return Result{}, fmt.Errorf("%w: branch %s is already checked out at %s",
					ErrConflict, branch, path)
			}
		}
		if branchExists(ctx, r, repo, ref) {
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "add", wt, branch}, run.Options{}); err != nil {
				return Result{}, err
			}
			res.Reused = "branch"
		} else {
			primary := primaryBranch(ctx, r, repo)
			if _, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "add", wt, "-b", branch, primary}, run.Options{}); err != nil {
				return Result{}, err
			}
			res.Reused = "none"
		}
	}

	pc, err := linkPrecommitConfig(repo, wt)
	if err != nil {
		return Result{}, err
	}
	res.Precommit = pc
	return res, nil
}

// primaryBranch resolves the integration branch exactly as the R-rules do:
// pgii-integrate-branch.primaryBranch → origin/HEAD → "main".
func primaryBranch(ctx context.Context, r run.Runner, repo string) string {
	if out, err := r.Run(ctx, "git", []string{"-C", repo, "config", "pgii-integrate-branch.primaryBranch"}, run.Options{}); err == nil {
		if b := strings.TrimSpace(out.Stdout); b != "" {
			return b
		}
	}
	if out, err := r.Run(ctx, "git", []string{"-C", repo, "symbolic-ref", "refs/remotes/origin/HEAD"}, run.Options{}); err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(out.Stdout), "refs/remotes/origin/"); b != "" {
			return b
		}
	}
	return "main"
}

func branchExists(ctx context.Context, r run.Runner, repo, ref string) bool {
	_, err := r.Run(ctx, "git", []string{"-C", repo, "rev-parse", "--verify", "--quiet", ref}, run.Options{})
	return err == nil
}

// worktreeBranches parses `git worktree list --porcelain` into path → branch
// ref (detached-HEAD worktrees are omitted; they have no branch line).
func worktreeBranches(ctx context.Context, r run.Runner, repo string) (map[string]string, error) {
	out, err := r.Run(ctx, "git", []string{"-C", repo, "worktree", "list", "--porcelain"}, run.Options{})
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	var current string
	for _, line := range strings.Split(out.Stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch ") && current != "":
			m[current] = strings.TrimPrefix(line, "branch ")
		}
	}
	return m, nil
}

// linkPrecommitConfig links the CANONICAL clone's .pre-commit-config.yaml PATH
// (itself a gitignored symlink into the nix store) into the worktree — a
// symlink-to-symlink, deliberately NOT the resolved store target, so a later
// `nix run .#install-pre-commit-hooks` in the canonical clone propagates to
// long-lived worktrees instead of pinning a stale hook generation. Returns
// linked | present | none.
func linkPrecommitConfig(repo, wt string) (string, error) {
	dst := filepath.Join(wt, ".pre-commit-config.yaml")
	if _, err := os.Lstat(dst); err == nil {
		if _, err := os.Stat(dst); err == nil {
			return "present", nil
		}
		// A DANGLING link would read as "present" while prek fails on it —
		// exactly the failure this verb exists to prevent. Re-point it.
		if err := os.Remove(dst); err != nil {
			return "", fmt.Errorf("remove dangling pre-commit link: %w", err)
		}
	}
	src := filepath.Join(repo, ".pre-commit-config.yaml")
	if _, err := os.Lstat(src); err != nil {
		return "none", nil // canonical has no config; nothing to link
	}
	if err := os.Symlink(src, dst); err != nil {
		return "", fmt.Errorf("link pre-commit config into worktree: %w", err)
	}
	return "linked", nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pb && go test ./internal/drain/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pb/internal/drain/
git commit -m "feat(pb): drain.Isolate — reuse-or-create worktree + prek config link"
```

---

### Task 5: `pb drain isolate` CLI

**Files:**

- Create: `packages/pb/cmd/pb/drain.go`
- Create: `packages/pb/cmd/pb/drain_isolate.go`
- Modify: `packages/pb/cmd/pb/main.go` (register `newDrainCmd()`; widen the root
  `Short` to `"phillip-beads: pn:applied gates + drain-loop helpers"`)
- Modify: `packages/pb/default.nix` (widen `meta.description` — currently
  "gate create/check" — to match the new root `Short`)
- Modify: `packages/pb/README.md` (document the verb + exit codes)
- Test: `packages/pb/cmd/pb/drain_isolate_test.go`

**Interfaces:**

- Consumes: Task 4's `drain.Isolate`, `drain.Params`, `drain.ErrConflict`.
- Produces: the CLI contract Task 8's markdown cites — `pb drain isolate --bead <id>
--repo <abs-path> [--json]`, one-line output
  `worktree=<abs> branch=drain/<id> reused=<none|worktree|branch> precommit=<linked|present|none>`,
  exit codes 0/1/3.

- [ ] **Step 1: Write the failing test (flag validation at the CLI boundary)**

Create `packages/pb/cmd/pb/drain_isolate_test.go`:

```go
package main

import (
	"bytes"
	"testing"
)

func TestDrainIsolateCmd_requiredFlags(t *testing.T) {
	cmd := newDrainIsolateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected required-flag error")
	}
}

func TestDrainIsolateCmd_rejectsRelativeRepo(t *testing.T) {
	cmd := newDrainIsolateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--bead", "pg2-x", "--repo", "relative/path"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for a relative --repo (orchestrators must pass observed absolute roots)")
	}
}

func TestDrainIsolateCmd_rejectsPathShapedBeadID(t *testing.T) {
	// The bead id lands in a filesystem path and a branch ref; live ids contain
	// dots (.worktrees/pg2-4dz88.2.3 exists), so dots pass but separators and
	// bare dot-dirs must not.
	for _, bad := range []string{"../evil", "a/b", ".", ".."} {
		cmd := newDrainIsolateCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--bead", bad, "--repo", "/abs/repo"})
		if err := cmd.Execute(); err == nil {
			t.Errorf("--bead %q: expected rejection", bad)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pb && go test ./cmd/pb/ -run TestDrainIsolateCmd`
Expected: FAIL — `undefined: newDrainIsolateCmd`.

- [ ] **Step 3: Implement**

Create `packages/pb/cmd/pb/drain.go`:

```go
package main

import "github.com/spf13/cobra"

func newDrainCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "drain", Short: "Helpers for the /drain-beads work loop"}
	cmd.AddCommand(newDrainIsolateCmd())
	return cmd
}
```

Create `packages/pb/cmd/pb/drain_isolate.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/phillipgreenii/pb/internal/drain"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/spf13/cobra"
)

// beadIDRe: the id lands in a filesystem path and a branch ref. Dots are legal
// (live ids like pg2-4dz88.2.3); separators are not.
var beadIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func newDrainIsolateCmd() *cobra.Command {
	var (
		bead, repo string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "isolate",
		Short: "Create or reuse the bead's worktree (.worktrees/<bead> on drain/<bead>) and link the pre-commit config",
		Long: `Idempotent isolation for one bead: reuses an existing worktree or parked
branch, otherwise branches off the repo's primary branch, then links the
canonical clone's gitignored nix-generated .pre-commit-config.yaml into the
worktree so commits there run the hooks.

Exit codes: 0 isolated (created or reused); 1 generic failure; 3 conflicting
isolation state (the worktree path holds another branch, or drain/<bead> is
checked out elsewhere) — never forced; route the bead to STUCK.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(repo) {
				return fmt.Errorf("--repo must be an absolute path, got %q", repo)
			}
			if bead == "." || bead == ".." || !beadIDRe.MatchString(bead) {
				return fmt.Errorf("--bead %q: want a bead id (letters, digits, dot, dash, underscore)", bead)
			}
			out, err := drain.Isolate(context.Background(), run.CLIRunner{},
				drain.Params{RepoPath: repo, BeadID: bead})
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "pb:", err)
				if errors.Is(err, drain.ErrConflict) {
					os.Exit(3)
				}
				os.Exit(1)
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "worktree=%s branch=%s reused=%s precommit=%s\n",
				out.Worktree, out.Branch, out.Reused, out.Precommit)
			return nil
		},
	}
	cmd.Flags().StringVar(&bead, "bead", "", "bead id (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "absolute path to the canonical clone (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("bead")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
```

(Naming note: `--repo` here is an absolute PATH, while `pb gate create --repo` and
`--gate <repo-key>=` take a workspace repo KEY. The overload is acknowledged; the
`IsAbs` validation makes a key passed by mistake fail loudly, and the help text says
"absolute path".)

In `packages/pb/cmd/pb/main.go`, add beside the existing registration and update the
root `Short`:

```go
root.AddCommand(newDrainCmd())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pb && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Document in the pb README**

Add a `drain isolate` section to `packages/pb/README.md`: purpose (one paragraph), flags,
output line format, exit codes 0/1/3, and one example. Also widen the README's H1/intro
(currently "gate create/check") to match `main.go`'s new `Short`
("pn:applied gates + drain-loop helpers"):

```bash
pb drain isolate --bead pg2-1qcro.7 --repo /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
```

- [ ] **Step 6: Lint + build gates for the Go work**

```bash
# from the worktree root; explicit timeout ≥ 600000 ms, or run in background
nix build .#checks.aarch64-darwin.pb-go-tests
nix build .#checks.aarch64-darwin.pb-golangci
```

Expected: both succeed. Fix findings; never add lint excludes.

- [ ] **Step 7: Commit**

```bash
git add packages/pb/cmd/pb/drain.go packages/pb/cmd/pb/drain_isolate.go packages/pb/cmd/pb/drain_isolate_test.go packages/pb/cmd/pb/main.go packages/pb/default.nix packages/pb/README.md
git commit -m "feat(pb): drain isolate subcommand (exit 3=conflicting isolation state)"
```

---

### Task 6: skill `pb:drain-stuck` (STUCK + CLOSE-AS-MOOT + CONVERT-TO-DEPENDENCY)

**Files:**

- Create: `claude-marketplace/pb/skills/drain-stuck/SKILL.md`
- Source: `claude-marketplace/pb/commands/drain-beads.md` (content MOVES from here; the
  removal happens in Task 8 so the two stay reviewable side-by-side until then)

**Interfaces:**

- Consumes: the section text currently in `drain-beads.md`: `## STUCK — cannot complete
a claimed bead (LAST RESORT: escalate to a human)` (steps 1–9), `## CLOSE-AS-MOOT
(STUCK step 2 disproved the premise)`, and `## CONVERT-TO-DEPENDENCY (STUCK step 3
found the blocker is another bead)` including its MIXED-blocker tail.
- Produces: skill name `drain-stuck` (invoked as `pb:drain-stuck`), which Task 8's stub
  cites. Inputs the skill body must state it expects: bead id, actor ID, worktree/branch
  path, and what was tried.

- [ ] **Step 1: Create the skill file**

Frontmatter (exactly two keys, matching `pb-gate-lifecycle`; keep the description this
tight — it rides in EVERY session's skill listing):

```yaml
---
name: drain-stuck
description: Disposition a claimed drain-beads bead that cannot complete — run the freshness probes, then park it for a human, close it as moot, or convert its blockers into bd dependencies. Invoked by /drain-beads with the bead id, session actor id, worktree/branch location, and what was tried. Do NOT use outside a drain session.
---
```

Body: after the frontmatter, add this header, then MOVE the three sections named above
from `drain-beads.md` verbatim, applying only these adaptations:

```markdown
# drain-stuck

You were invoked from a /drain-beads session holding a claimed bead it cannot
complete. Required context from the caller: the bead id (<id>), the session
actor id (ID), the worktree/branch location, and what was tried. Every `bd`
write below passes `--actor "ID"`.

`human` means A PERSON IS THE BLOCKER — the LAST RESORT. Work through STUCK in
order; it exits by exactly one of: PARK (labeled `human`, claim released),
CLOSE-AS-MOOT, or CONVERT-TO-DEPENDENCY. Whatever the exit, the claim is
RELEASED or the bead CLOSED before you return — never return still holding a
claim in a state this skill does not define.
```

Adaptation rules for the moved text (apply mechanically, change nothing else):

1. Every `Return to the MAIN LOOP's step 1 (CLAIM).` →
   `Done — return to the drain loop's CLAIM step.` (three occurrences: CLOSE-AS-MOOT,
   ABSORPTION's twin lives in Task 7, CONVERT-TO-DEPENDENCY).
2. STUCK's own step 9 ends differently: `Return to step 1 (CLAIM).` — moved verbatim
   into a skill whose OWN step 1 is "PARK the change", that reads as a re-park loop.
   Rewrite it to `Done — return to the drain loop's CLAIM step.` This is the line
   where the claim is released; do not lose it.
3. `this command` → `the drain loop` (one occurrence, in CONVERT-TO-DEPENDENCY).
4. The STUCK intro's `(that is the POST-DEPLOY VERIFICATION GATE's job)` →
   `(that is the drain loop's POST-DEPLOY VERIFICATION GATE section's job — run
pb gate attach-verified-child there, not here)`: that section stays behind in
   `drain-beads.md`, so the bare reference would dangle. NOTE: this target (and
   any quoted target in this plan) may wrap across a line break in the source —
   match on content, not on a single-line literal.
5. The CLOSE-AS-MOOT intro `Reached ONLY from a FRESHNESS CHECK…` and the
   CONVERT-TO-DEPENDENCY intro keep their internal references to "STUCK step 2/3" —
   those steps now live in this same file, so the references stay resolvable.
6. Keep every `bd` code block, every FRESHNESS/PRECONDITION template, and every rule
   citation verbatim — with ONE exception: the source's `(F-1..F-8)` gloss is stale
   (the always-on rule set has F-9, "ABSENCE IS AMBIGUOUS", directly germane to the
   `path-exists?` probe this section runs). Update it to `(F-1..F-9)` so the skill and
   the rewritten `drain-beads.md` (Task 8 Step 8 also cites F-1..F-9) agree.
7. Keep the CLOSE-AS-MOOT step-4 `worktree-review` bead template verbatim (it is the
   entry point for that label and its wording is load-bearing).

- [ ] **Step 2: Verify formatting**

```bash
git add claude-marketplace/pb/skills/drain-stuck/
prek run --files claude-marketplace/pb/skills/drain-stuck/SKILL.md
```

Expected: hooks pass (prettier may rewrap; re-stage if it does).

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(pb-plugin): drain-stuck skill — STUCK/CLOSE-AS-MOOT/CONVERT-TO-DEPENDENCY moved out of /drain-beads"
```

---

### Task 7: skill `pb:drain-absorb-pointer` (CLOSE-WITH-ABSORPTION-TRACE)

**Files:**

- Create: `claude-marketplace/pb/skills/drain-absorb-pointer/SKILL.md`
- Source: `claude-marketplace/pb/commands/drain-beads.md` section
  `## CLOSE-WITH-ABSORPTION-TRACE (a handoff pointer whose items are already absorbed)`

**Interfaces:**

- Produces: skill name `drain-absorb-pointer` (invoked as `pb:drain-absorb-pointer`),
  cited by Task 8's UNDERSTAND step. Expects: the bead id and the session actor id.

- [ ] **Step 1: Create the skill file**

```yaml
---
name: drain-absorb-pointer
description: Close a session-wrapup handoff-pointer bead (a Resume/next-session bead holding no executable work) by tracing every item in its body to the bead or label where it durably lives, filing anything untraced as its own bead first. Invoked by /drain-beads at UNDERSTAND with the bead id and session actor id. Do NOT use for beads with executable work.
---
```

Body: a short header in the same shape as Task 6's (caller context: bead id + actor ID;
every `bd` write passes `--actor "ID"`), then the CLOSE-WITH-ABSORPTION-TRACE section
moved verbatim with one adaptation: `Return to the MAIN LOOP's step 1 (CLAIM).` →
`Done — return to the drain loop's CLAIM step.` (`this command` does not occur in this
section — no other rewrite applies). Compress the section's intro narrative (the
`pg2-m2qxu` cost-accounting sentences) to its final provenance line
(`Provenance: pg2-9ifbn; see also pg2-m2qxu, pg2-8wy25`).

- [ ] **Step 2: Verify formatting + commit**

```bash
git add claude-marketplace/pb/skills/drain-absorb-pointer/
prek run --files claude-marketplace/pb/skills/drain-absorb-pointer/SKILL.md
git commit -m "feat(pb-plugin): drain-absorb-pointer skill — handoff-pointer disposition moved out of /drain-beads"
```

---

### Task 8: rewrite `drain-beads.md` around the new verbs, subagents, and skills

**Files:**

- Modify: `claude-marketplace/pb/commands/drain-beads.md`
- Modify: `claude-marketplace/pb/.claude-plugin/plugin.json` (`"version": "1.0.0"` →
  `"1.1.0"`)

**Interfaces:**

- Consumes: Task 3's and Task 5's CLI contracts (flags, output lines, exit codes 3/4);
  Task 6's and Task 7's skill names.
- Produces: the final command body. Target size: under 545 lines (from 962 — the
  enumerated edits sum to ~536; the growth sits in steps 1–4 and LAND's retained
  normative blocks, which stay because they are load-bearing).

This task is a set of section-by-section edits. Sections NOT named below stay unchanged
(frontmatter description gets one edit, noted last).

- [ ] **Step 1: Replace step 2 (UNDERSTAND)**

New text:

```markdown
2. **UNDERSTAND** (orchestrator reads the BEAD ONLY): `bd show <id>` to learn the
   target repo(s), whether the work spans repos, and whether any acceptance
   criterion can only be confirmed once the change is LIVE. You MUST NOT Read any
   file, plan, spec, or doc the bead references — those are the implementation
   subagent's to read (measured: one session read the same referenced plan doc
   eight times to compose briefs, ~20K tokens of pure duplication). Record the
   referenced paths; step 4 passes them through as pointers.
   If the bead is a HANDOFF POINTER holding no executable work of its own — a
   `session-wrapup` `Resume: …` / next-session bead, born P0 to let one session
   resume cold — do NOT ISOLATE or DELEGATE it: invoke the
   `pb:drain-absorb-pointer` skill with the bead id and your actor ID, follow it
   to the close, then return to CLAIM.
```

- [ ] **Step 2: Replace step 3 (ISOLATE)**

New text:

````markdown
3. **ISOLATE** off local main (never work a primary branch directly):
   - Single repo → ONE call:

     ```bash
     pb drain isolate --bead <id> --repo <abs-canonical-clone-path>
     ```

     It reuses an existing worktree or parked branch, otherwise creates
     `.worktrees/<id>` on `drain/<id>` off the repo's primary branch, and links
     the nix-generated pre-commit config into the worktree. Exit 0 → proceed
     (the output line names the worktree). Exit 3 → conflicting isolation state
     (someone else's checkout) — do NOT force anything; route to STUCK. Any
     other failure → transient-vs-genuine per the Rules.

   - Multiple repos → a coordinated set via the
     `pn-workspace-rules:fork-workforest` skill, keyed to the bead id.
````

- [ ] **Step 3: Replace step 4 (DELEGATE THE WORK)**

New text. The report-status contract is unchanged: keep the four status sub-bullets
(`done` / `done-pending-apply-verification` / `stuck` / `needs-more-repos`), the
`also include:` line, and the `Re-dispatch with guidance…` paragraph with their CONTENT
unchanged, re-attached immediately after this new prose. Their parent bullet
(`CLASSIFY the outcome…`) is absorbed into the new prose, so prettier will re-indent
them one level out (5→3 spaces) — accept that reformat. One kept line changes:
`The subagent lands nothing — YOU land.` →
`The implementation subagent lands nothing — the LANDER subagent (step 6) does.`

```markdown
4. **DELEGATE THE WORK** to a subagent (REQUIRED — this preserves your context).
   The brief is a POINTER, not a payload. It MUST contain exactly:
   - the bead id, with the instruction to run `bd show <id>` ITSELF for the full
     description and acceptance criteria;
   - the absolute repo root and the worktree/set path (state the root once —
     A-3);
   - the paths of any docs the bead references, with the instruction to read
     them ITSELF from inside the worktree;
   - the standing constraints: explicit timeouts or `run_in_background` for
     builds/checks (L-3); never `run_in_background` for git commits; report
     fully in ONE turn (no waiting/monitoring across turns).

   The brief MUST NOT transcribe the bead description, doc content, or plan
   steps — if you are pasting more than paths and ids, you are doing the
   subagent's reading for it.

   Instruct it to: implement inside THAT worktree/set only, following repo
   conventions; run every gate that CAN run pre-apply (pre-commit hooks SCOPED
   to its own diff via `prek run --files <changed files>` — never
   `--all-files`; `nix flake check` / `pn workspace build` for nix repos; the
   repo's tests); NOT claim/close the bead, NOT land/merge, NOT touch any other
   worktree, NOT create gates; and CLASSIFY the outcome as one of the four
   statuses below (the kept `also include:` line carries the gate-evidence and
   repos-touched requirements).
```

- [ ] **Step 4: Replace step 6 (LAND)**

New text. Keep three existing blocks verbatim after it — the "**What "LANDED" means
depends on the resolved strategy.**" block, the "**Autonomy — push and draft-PR are
PRE-AUTHORIZED; merging is not.**" block, and the `stopped:` classification paragraph.
DELETE the SHA-recording paragraph (`After a successful land, RECORD the SHA … keys on
this SHA.`) — its content now lives in the lander-brief contract and the verification
paragraph below:

```markdown
6. **LAND via a dedicated LANDER SUBAGENT** — dispatched synchronously, ONE at a
   time, never in parallel with another land and never fanned out. Landing must
   go through the repo-declared strategy, so the lander invokes the dispatcher
   itself in its own context (a subagent has its own persistent shell; keeping
   the ~37KB of dispatcher+handler skill text out of YOUR context is the point).

   The lander brief MUST contain: the bead id; the absolute canonical repo root;
   the worktree path and branch `drain/<id>`; and these instructions:
   - invoke the `integrate-branch:integrate-branch` skill and let IT resolve the
     strategy — NEVER name a handler (naming one hardcodes ff-merge and breaks
     `pull-request` repos). For a workforest set, invoke
     `pn-workspace-rules:land-workforest` instead;
   - a lost FAST-FORWARD RACE or a REJECTED NON-FAST-FORWARD PUSH is TRANSIENT:
     re-rebase and re-invoke, at most 3 attempts, then report `stopped:` with
     the reason;
   - MUST NOT merge any PR, MUST NOT push any primary branch, MUST NOT use
     `run_in_background` for git operations, and MUST report fully in ONE turn;
   - return a structured report: `outcome` (`landed` | `pr-opened` |
     `pr-updated` | `stopped:<reason>`), the landed/pushed SHA per changed repo
     (the tip of `drain/<id>` — NOT a re-read of the shared primary branch,
     which a peer may have advanced), and the PR number + URL when applicable.

   VERIFY the verdict with ONE observation of your own before recording — the
   report is a subagent's prose, not evidence:
   - `landed` →
     `git -C <repo> merge-base --is-ancestor drain/<id> <primary>; echo $?`
     must print 0, and take the gate SHA from
     `git -C <repo> rev-parse drain/<id>` YOURSELF rather than from the report
     (never from the shared primary branch, which a peer may have advanced).
   - `pr-opened` / `pr-updated` → `gh pr view <n> --json state,isDraft` must
     show OPEN and draft; record the pushed head from
     `git -C <repo> rev-parse drain/<id>`.

   A verdict that fails its check is `stopped:<unverified>` — never record it
   as landed. What "LANDED" means per strategy, the pre-authorized-push rule
   for `pull-request` repos, and the draft-PR requirements are unchanged (kept
   verbatim below).
```

Then keep the existing "**What "LANDED" means depends on the resolved strategy.**" block,
the "**Autonomy — push and draft-PR are PRE-AUTHORIZED; merging is not.**" block, and the
`stopped:` classification paragraph verbatim, with two stage-direction edits: in the
`stopped:` paragraph, `re-invoke LAND a few more times (short backoff) before giving up`
→ `re-dispatch the lander at most ONCE more (it already retried 3× internally); a second
failure is a GENUINE stop → STUCK` (same edit for the `pull-request` analogue's
`re-invoke LAND a few more times`), and delete the sentence
`Keep landing in THIS session — the skills need persistent shell/cwd state:` (its claim
is what this change corrects — a subagent has its own persistent shell).

- [ ] **Step 5: Shrink the POST-DEPLOY VERIFICATION GATE section**

Replace everything from `## POST-DEPLOY VERIFICATION GATE` up to (not including)
`## STUCK` with:

````markdown
## POST-DEPLOY VERIFICATION GATE (use INSTEAD of `human` for deploy-only tails)

When a bead is implemented, its pre-apply gates PASS, and it has LANDED, but
the only thing left is confirming it works on the LIVE machine (subagent status
`done-pending-apply-verification`), DO NOT label it `human`. Attach a
`pn:applied` gate to a fresh verification child bead — ONE call, which runs the
whole deferred-first sequence (create the child DEFERRED → prove it is absent
from `bd ready` → attach every gate → un-defer → re-prove absence → comment the
link on the impl bead):

```bash
pb gate attach-verified-child \
  --impl <impl-id> \
  --title "verify <thing> works after apply (<impl-id>): <concrete checks>" \
  --gate <repo-key>=<landed-sha> \
  --actor "ID"
# one --gate per changed repo; the child unblocks only when ALL are applied
```

Pin `<landed-sha>` to the SHA the lander reported — never HEAD (a peer may have
advanced it). Branch on the exit code:

- `0` → fully gated; the output names the child. CLEANUP per FINISH, then close
  the impl bead naming the child.
- `0` with a `comment failed` warning on stderr (JSON: `"comment_failed": true`)
  → gating is complete and safe, but the provenance link was not recorded:
  record it yourself —
  `bd comment <impl-id> "post-deploy verification gated as <child> (pn:applied)." --actor "ID"`
  — before closing.
- `3` → gating INCOMPLETE and the child was left DEFERRED (safe — no peer can
  claim it). Do NOT close the impl bead; route it to STUCK naming the child.
- `4` → the child could NOT be proven un-workable. Do NOT close the impl bead;
  route it to STUCK and say so in the park comment — a peer could otherwise
  claim the child and "verify" unapplied code.
- `1` with `is not in workspace` in the error → an INVOCATION mistake, not a
  transient: a mistyped `--gate` repo key (fix it and re-run — nothing was
  created), or a repo genuinely outside the workspace (take the FALLBACK below
  instead).
- any other non-zero → transient-vs-genuine per the Rules; retry once, then
  STUCK.

The gate resolves via `pn workspace apply`'s post-hook (`pb gate check`); a
gate left unapplied past its stale window auto-converts to a `human` bead. Gate
semantics, stale handling, and the squash-merge prohibition:
the `pb:pb-gate-lifecycle` skill.

**SCOPE — this gate path applies ONLY when the changed repo is a `pn workspace`
MEMBER and its resolved strategy is `ff-merge-to-main`.** `pb gate create`
cannot resolve `--repo` outside the workspace, and a squash-merged PR rewrites
the patch-id so a gate could never auto-resolve (provenance: the
`pb:pb-gate-lifecycle` skill).

**FALLBACK when the gate path does NOT apply** (repo outside a pn-workspace, or
resolved strategy `pull-request`): file the verification child as a `human`
bead instead — CORRECT under **D-1**, because a PERSON's out-of-band action
(merging the draft PR, then deploying) stands between the code and the live
machine:

```bash
bd create "verify <thing> works once <pr-url> is merged and deployed (<impl-id>): <concrete checks>" \
  --labels human --deps "discovered-from:<impl-id>" --actor "ID" --json
# capture the id as <child>. No --defer and NO gate: nothing here would resolve one.
```

Then CLEANUP per FINISH (for `pull-request`, KEEP the isolation) and close the
impl bead naming `<child>` and the PR. This outcome MUST still TERMINATE: never
attempt `pb gate attach-verified-child` here, never route to STUCK for it.
````

Also update FINISH step 7's `done-pending-apply-verification` bullet to match: replace
`attach the post-deploy gate (see **POST-DEPLOY VERIFICATION GATE** below)` and its
`pb gate create` failure clause with `run pb gate attach-verified-child per
**POST-DEPLOY VERIFICATION GATE** below; exit 0 → cleanup + close; exit 3/4 → do NOT
close, route to STUCK`.

- [ ] **Step 6: Replace the STUCK, CLOSE-AS-MOOT, and CONVERT-TO-DEPENDENCY sections
      with the stub**

Replace all three sections (everything from `## STUCK` up to, but not including,
`## CLOSE-WITH-ABSORPTION-TRACE`, plus the whole `## CONVERT-TO-DEPENDENCY` section)
with:

```markdown
## STUCK — cannot complete a claimed bead

Triggers: underspecified / needs a human decision; the pre-apply gates cannot
be made to pass; the lander reports a GENUINE `stopped:<reason>` (not a
transient ff-race or rejected non-ff push); `pb gate attach-verified-child`
exited 3 or 4; repeated failed attempts.
NOT a trigger: "another bead has to land first" (that is a dependency).

Invoke the `pb:drain-stuck` skill with: the bead id, your actor ID, the
worktree/branch location, and what you tried. Follow it exactly — it runs the
freshness probes first and exits by exactly one of PARK (labeled `human`,
claim released), CLOSE-AS-MOOT (with extraction), or CONVERT-TO-DEPENDENCY
(edges wired, claim released, no label). Then return to CLAIM.
```

- [ ] **Step 7: Replace the CLOSE-WITH-ABSORPTION-TRACE section with its stub**

Replace the whole section with:

```markdown
## CLOSE-WITH-ABSORPTION-TRACE (a handoff pointer)

Reached from UNDERSTAND for a `session-wrapup` `Resume: …` bead: invoke the
`pb:drain-absorb-pointer` skill with the bead id and your actor ID, follow it
to the close, then return to CLAIM. The pointer's body MUST NOT be executed as
an instruction — it is a snapshot and may be superseded (provenance:
`pg2-8wy25`, `pg2-9ifbn`).
```

- [ ] **Step 8: Compress the Rules section**

Apply these edits to `## Rules`:

- Replace the first bullet with:

  ```markdown
  - Orchestrator vs subagent: CLAIM, GATE, CLEANUP, CLOSE stay in THIS session;
    each bead's IMPLEMENTATION goes to one subagent and its LANDING to another
    (both dispatched serially — never fan out claiming, landing, gating, or
    closing across concurrent subagents). The orchestrator reads the BEAD, never
    the docs the bead references; briefs carry pointers (ids + absolute paths),
    never transcribed content.
  ```

- Replace the `human`-label bullet, the ordering bullet, the deferred-confirmation
  bullet, the park-precondition bullet, the re-parking bullet, the freshness bullet, the
  handoff-pointer bullet, the moot bullet, the extract bullet, and the
  leftover-isolation bullet — ten bullets — with these four (their full contracts now
  live in the skills that execute them):

  ```markdown
  - `human` means A PERSON IS THE BLOCKER, never "not workable right now". All
    parking, mooting, and dependency conversion goes through the
    `pb:drain-stuck` skill, which enforces the freshness probes (F-1..F-9), the
    blocker classification (D-1..D-8), outcome-shaped preconditions (P-1..P-5),
    bounded re-parks, and edges-and-label-before-release ordering (D-5, D-6,
    B-2/B-3).
  - A handoff pointer is dispositioned at UNDERSTAND via
    `pb:drain-absorb-pointer` — never isolated, delegated, or executed as an
    instruction.
  - Gate ordering is enforced by `pb gate attach-verified-child` (deferred-first,
    confirm-by-READINESS, all-gates-then-un-defer). Exit 3 leaves the child
    safely deferred; exit 4 means the child may be workable — in both cases the
    impl bead MUST NOT be closed.
  - A leftover-isolation follow-up (filed inside `pb:drain-stuck`'s
    CLOSE-AS-MOOT) is born with BOTH `human` and `worktree-review` plus the
    entry marker; this command never adjudicates such a bead and MUST NOT be
    given `/unblock-human-beads`' provably-lossless teardown carve-out (an
    unattended session cannot re-prove losslessness — F-1).
  ```

- Keep unchanged: the worktree/workforest bullet, the land-then-teardown bullet, the
  gate-instead-of-`human` bullet (`Post-deploy-only verification uses a pn:applied gate
on a verification child bead, NOT the human label…` — the only surviving statement of
  that invariant once STUCK's intro moves into the skill), the post-deploy-gate-scope
  bullet, the SELF-CHECK bullet, the canonical-clone bullet, the transient-infra bullet,
  the `--no-verify` bullet, the landing-dispatcher bullet, and the unpushed-landing
  bullet — ten kept, so with the first bullet and the ten replaced, all 21 Rules bullets
  are accounted for.

- [ ] **Step 9: Update the frontmatter description and bump the plugin version**

In the frontmatter `description`, change `delegate the implementation to a subagent →
validate → land` to `delegate the implementation to a subagent → validate → land via a
lander subagent`.

In `claude-marketplace/pb/.claude-plugin/plugin.json`: `"version": "1.0.0"` →
`"version": "1.1.0"`.

- [ ] **Step 10: Compress incident narratives to provenance citations**

The RFC 2119 rule lines stay; the incident STORIES live in the cited bead records.
Apply these compressions (each keeps its rule plus a `(provenance: pg2-xxxxx)`
citation, drops the retelling):

- `## Your actor id (do this ONCE, reuse all session)`: keep the derivation order and
  the `-drain`-suffix MUST with `(operator ruling, pg2-mcp1j)`; drop the multi-line
  narrative explaining how a subagent would otherwise inherit its orchestrator's id
  (~8 lines saved).
- `### Unpushed commits when you STOP`: keep the REPORT-NOTHING rule, the one-line
  consequence exception, and the `U-1..U-6` citation; compress the
  `pg2-5subz`/`pg2-dawg2` standing-push-bead story to
  `(provenance: pg2-5subz, pg2-dawg2)` (~10 lines saved).
- `## Known limitations (accepted trade-offs)`: keep all six limitation bullets but
  compress each to at most 4 lines PLUS any RFC 2119 lines it carries — normative
  MUST/MUST NOT content stays (the Stranded-orphans bullet keeps its two release-note
  MUSTs and the dormant-vs-gone MUST; only the retellings go), citing bead ids
  (`pg2-xx1y5`, `pg2-2l8ip`, `pg2-8u0ul`) instead of narrating. In the
  unscoped-claims bullet, rewrite
  `Follow-ups this command FILES are covered by construction` to
  `Follow-ups filed via pb:drain-stuck's CLOSE-AS-MOOT are covered by construction`
  (the skill files them now) (~15 lines saved).
- `## Running several at once`: rewrite `A bead routed through
CONVERT-TO-DEPENDENCY` (wraps across a line break in the source — match on
  content) to `A bead whose blockers were converted to dependencies (via
pb:drain-stuck)` — the section name no longer exists in this file.

- [ ] **Step 11: Verify size, formatting, and internal references**

```bash
wc -l claude-marketplace/pb/commands/drain-beads.md   # expect < 545
# no dangling references to moved sections:
grep -n 'CONVERT-TO-DEPENDENCY\|CLOSE-AS-MOOT\|STUCK step' claude-marketplace/pb/commands/drain-beads.md
# every remaining mention must point at pb:drain-stuck, not at an in-file section
git add claude-marketplace/pb/commands/drain-beads.md claude-marketplace/pb/.claude-plugin/plugin.json
prek run --files claude-marketplace/pb/commands/drain-beads.md claude-marketplace/pb/.claude-plugin/plugin.json
```

Expected: hooks pass; every surviving cross-reference resolves within the file or names
a skill/rule set explicitly. Prettier (treefmt, printWidth 80) WILL rewrap and re-indent
the edited markdown — accept its output and re-stage before committing.

- [ ] **Step 12: Commit**

```bash
git commit -m "refactor(pb-plugin): drain-beads context diet — pointer briefs, lander subagent, pb verbs, rare paths as skills"
```

---

### Task 9: repo validation gates

**Files:** none created — this is the completion gate.

- [ ] **Step 1: Scoped pre-commit over everything this plan touched**

```bash
git diff --name-only main...HEAD | xargs prek run --files
```

Expected: all hooks pass (files were staged/committed in earlier tasks, so nothing is
untracked-and-skipped).

- [ ] **Step 2: Full flake check (required completion gate for a flake repo)**

`nix flake check` outruns the 10-minute Bash cap on this machine — detach it
(`nohup`, since macOS lacks `setsid`) and watch the log with Monitor; do NOT poll with
sleep:

```bash
nohup sh -c 'nix flake check; echo "FLAKE_CHECK_EXIT=$?"' >/tmp/drain-diet-flake-check.log 2>&1 &
```

Expected: `FLAKE_CHECK_EXIT=0` in the log tail (a bare `nohup cmd &` never records the
exit status, so the echo is what makes the log checkable). A from-source rebuild of an unrelated cacheable
package right after an upstream bump is usually transient Hydra lag — disambiguate
before acting.

- [ ] **Step 3: Confirm the marketplace ships the new skills**

```bash
# set an explicit Bash timeout (>= 600000 ms) per Global Constraints
nix build .#phillipgreenii-nix-agent-support-marketplace
ls result/pb/skills/
```

Expected: `drain-stuck`, `drain-absorb-pointer`, `pb-gate-lifecycle`. (The attr is
re-exported at `flake.nix:3355`; the derivation lives at `flake.nix:508-515`. Do not
mask the build with `|| true` — a swallowed failure makes the `ls` read a stale
`result`.)

- [ ] **Step 4: Hand back for landing**

Do NOT land in this plan. Report completion; landing goes through the
`integrate-branch:integrate-branch` skill per R-9, invoked by whoever executes the
session close-out. Note for the lander: this change edits the LIVE `/drain-beads`
command — running drain sessions will halt at their next CLAIM self-check, by design.
