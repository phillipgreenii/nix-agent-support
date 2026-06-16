# pr-pool: eliminate the feedback-discovery join — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stamp a `mine` label on self-owned processing-cycle beads at creation (pg-pr) so pr-pool's feedback discovery drops its per-candidate `bd show <parent>` ownership join and becomes a single `bd ready --label mine` query.

**Architecture:** Ownership moves from a runtime parent-author lookup to a creation-time label. pg-pr's `CreateProcessingCycle` (which already knows the PR author) stamps `mine` for self-authored PRs; pr-pool's `discoverFeedback` queries by that label instead of fetching each parent. The now-unused `selfLogin` value is removed from pr-pool's discovery call chain, while its config precheck (`resolveSelf`) is retained on the daemon (`drain`) path.

**Tech Stack:** Go (two separate modules: `packages/pg-pr` and `packages/pr-pool`); `bd` (beads) CLI; nix flake checks + pre-commit hooks.

**Spec:** `docs/superpowers/specs/2026-06-15-pr-pool-eliminate-feedback-discovery-join-design.md`

**Branch:** `pr-pool-spec-b-eliminate-join` (already created; the spec lives there).

---

## File Structure

**pg-pr (`packages/pg-pr/`):**

- Modify `pkg/beads/processingcycle.go` — `CreateProcessingCycle` method gains `mine bool`; package-level wrapper updated.
- Modify `internal/sync/sync.go` — `BeadClient` interface decl + the call site in `processFeedback`.
- Modify tests: `pkg/beads/processingcycle_test.go`, `internal/sync/sync_test.go`, `cmd/pg-pr/sync_test.go`, and every other `CreateProcessingCycle` call site (grep-driven).

**pr-pool (`packages/pr-pool/`):**

- Modify `internal/discover/discover.go` — drop the join from `discoverFeedback`; drop `selfLogin` from `discoverFeedback`/`ForRole`/`Discover`; rewrite doc comments.
- Modify `internal/orchestrator/orchestrator.go` (+ `orchestrator_test.go`) — drop `selfLogin` from `DrainOnce`; fix the `scriptBD` fake's label routing and the discover-error test.
- Modify `cmd/pr-pool/drain.go` — `runDrain` discards `resolveSelf`'s return, calls `DrainOnce(ctx)`.
- Modify `cmd/pr-pool/runrole.go` — `runRunQuery` drops its `resolveSelf`/`selfLogin` block; rewrite doc comment.
- Modify `internal/discover/discover_test.go` — rewrite the fake's routing; delete/rewrite/arity-fix tests.

**Cutover (operational, no code):** documented in Task 5.

> **Note on TDD for signature changes:** changing the `CreateProcessingCycle` signature is a compile-breaking change across many files. The standard "write failing test → run → implement" loop is adapted: each task first makes the tree compile (mechanical arg propagation), then drives the new _behavior_ with a test that fails on its assertion before the behavior is implemented.

---

## Task 1: pg-pr — `CreateProcessingCycle` stamps `mine`

**Files:**

- Modify: `packages/pg-pr/pkg/beads/processingcycle.go:35` (method), `:232` (wrapper)
- Modify (compile): `packages/pg-pr/internal/sync/sync.go:110` (interface), `:1483` (call site); `packages/pg-pr/internal/sync/sync_test.go:596` (`noopBeads` stub); `packages/pg-pr/cmd/pg-pr/sync_test.go:61` (`stubBeads` stub); all other call sites
- Test: `packages/pg-pr/pkg/beads/processingcycle_test.go`

- [ ] **Step 1: Add the failing behavior test (arg-capturing fake Runner)**

Add to `packages/pg-pr/pkg/beads/processingcycle_test.go` (the `recordingRunner` returns a non-empty id for `create` so `CreateProcessingCycle` proceeds to the `dep add`):

```go
// recordingRunner captures the argv of each bd call so a test can assert
// which flags CreateProcessingCycle passes to `bd create`.
type recordingRunner struct {
	createArgs []string
}

func (r *recordingRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "create" {
		r.createArgs = args
		return "cyc-rec", nil // non-empty id so CreateProcessingCycle continues to dep-add
	}
	return "", nil // dep add and anything else
}

func TestCreateProcessingCycle_StampsMineWhenSelf(t *testing.T) {
	ctx := context.Background()
	r := &recordingRunner{}
	c := NewClientWithRunner(r)

	if _, err := c.CreateProcessingCycle(ctx, "pr-1", "foo/bar#7", true); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	joined := strings.Join(r.createArgs, " ")
	if !strings.Contains(joined, "-l mine") {
		t.Fatalf("self cycle: `bd create` args missing `-l mine`; got %q", joined)
	}
}

func TestCreateProcessingCycle_TeamCycleUnlabeled(t *testing.T) {
	ctx := context.Background()
	r := &recordingRunner{}
	c := NewClientWithRunner(r)

	if _, err := c.CreateProcessingCycle(ctx, "pr-1", "foo/bar#7", false); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	for _, a := range r.createArgs {
		if a == "mine" {
			t.Fatalf("team cycle must not be labeled mine; got args %v", r.createArgs)
		}
	}
}
```

- [ ] **Step 2: Update the signature + all call sites so the package compiles (behavior not yet added)**

In `packages/pg-pr/pkg/beads/processingcycle.go`, change the method signature (`:35`) and build the create args as a slice WITHOUT yet appending the label:

```go
func (c *Client) CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	if prBeadID == "" {
		return "", errors.New("processing-cycle: pr bead id required")
	}
	if title == "" {
		title = prBeadID
	}
	fullTitle := processingCycleTitlePrefix + title
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	}
	out, err := c.Runner.Run(ctx, createArgs...)
	// ... rest unchanged ...
```

Update the package-level wrapper (`:232`):

```go
func CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error) {
	return NewClient().CreateProcessingCycle(ctx, prBeadID, title, mine)
}
```

Update the `BeadClient` interface decl (`packages/pg-pr/internal/sync/sync.go:110`):

```go
	CreateProcessingCycle(ctx context.Context, prBeadID, title string, mine bool) (string, error)
```

Update the call site (`sync.go:1483`) — pass `e.isSelfAuthored(pr.Author)` (both are in scope):

```go
			id, err := bdc.CreateProcessingCycle(ctx, prBeadID,
				fmt.Sprintf("%s#%d", repo, pr.Number), e.isSelfAuthored(pr.Author))
```

Update both interface stubs:

- `packages/pg-pr/internal/sync/sync_test.go:596` → `func (noopBeads) CreateProcessingCycle(context.Context, string, string, bool) (string, error) { return "", nil }`
- `packages/pg-pr/cmd/pg-pr/sync_test.go:61` → `func (s *stubBeads) CreateProcessingCycle(_ context.Context, _, _ string, _ bool) (string, error) { return "", nil }`

Find and fix every remaining call site (pass `false` — ownership is irrelevant to those tests):

Run: `grep -rn "CreateProcessingCycle(ctx" packages/pg-pr --include="*.go"`
This lists the test call sites in `pkg/beads/{feedback,tickcache,mergerequest,action,processingcycle}_test.go` (including `mergerequest_test.go:352`) and `internal/sync/sync_test.go` (including the `selfCycle`/`teamCycle` seeds at `:1127`/`:1131`). Append `, false` to each 3-arg **call site** — lines that _invoke_ it (`c.CreateProcessingCycle(...)` / `bd.CreateProcessingCycle(...)`). Do NOT touch the interface decl, the method def, or the wrapper def/body — those were already edited above and are not 3-arg calls. Example (`internal/sync/sync_test.go:675`): `bd.CreateProcessingCycle(ctx, prID, repo+"#seed", false)`.

- [ ] **Step 3: Run the new tests to verify they fail on the assertion (compiles, behavior missing)**

Run: `(cd packages/pg-pr && go test ./pkg/beads/ -run 'TestCreateProcessingCycle_(StampsMineWhenSelf|TeamCycleUnlabeled)' -v)`
Expected: `TestCreateProcessingCycle_StampsMineWhenSelf` FAILS ("missing `-l mine`"); `TestCreateProcessingCycle_TeamCycleUnlabeled` PASSES (no label appended yet).

- [ ] **Step 4: Implement the stamping**

In `processingcycle.go`, after building `createArgs` and before the `Run` call:

```go
	createArgs := []string{
		"create",
		"--type=task",
		"--title", fullTitle,
		"-d", fullTitle,
		"--silent",
	}
	if mine {
		createArgs = append(createArgs, "-l", "mine")
	}
	out, err := c.Runner.Run(ctx, createArgs...)
```

- [ ] **Step 5: Run the beads package tests to verify pass**

Run: `(cd packages/pg-pr && go test ./pkg/beads/ -v)`
Expected: PASS (both new tests + the updated existing `CreateProcessingCycle`/feedback/tickcache tests).

- [ ] **Step 6: Build the whole pg-pr module to confirm all call sites compile**

Run: `(cd packages/pg-pr && go build ./... && go vet ./...)`
Expected: no output (success).

- [ ] **Step 7: Commit**

```bash
git add packages/pg-pr
git commit -m "feat(pg-pr): stamp 'mine' label on self-owned processing cycles (pg2-ktqh)"
```

---

## Task 2: pg-pr — regression guard for the ownership wiring

The call site was wired in Task 1 (`e.isSelfAuthored(pr.Author)`). This task adds a `processFeedback`-level test proving a self PR's cycle is stamped `mine` and a team PR's is not.

**Files:**

- Test: `packages/pg-pr/internal/sync/sync_test.go`

- [ ] **Step 1: Add the wiring test (recording BeadClient embedding `noopBeads`)**

Add to `packages/pg-pr/internal/sync/sync_test.go`. First ensure the `vcs` package is imported (the file imports `pkg/api` but not `pkg/provider/vcs`); add to the import block:

```go
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
```

`recordingCycleBeads` embeds `noopBeads` (so it satisfies the full `BeadClient` interface) and overrides only `CreateProcessingCycle` to capture `mine`. A stub that does NOT implement `feedbackSubtreeReader` makes `prFeedbackSubtree` return `(nil, nil)`, and `FindOpenProcessingCycle` (from `noopBeads`) returns `found=false`, so a single new comment event triggers exactly one cycle creation:

```go
type recordingCycleBeads struct {
	noopBeads
	mineSeen []bool
}

func (r *recordingCycleBeads) CreateProcessingCycle(_ context.Context, _, _ string, mine bool) (string, error) {
	r.mineSeen = append(r.mineSeen, mine)
	return "cyc-rec", nil
}

func TestProcessFeedback_StampsOwnershipFromAuthor(t *testing.T) {
	ctx := context.Background()
	mkEngine := func(b BeadClient) *Engine {
		e, err := New(Deps{
			Cfg:      minimalCfg(),
			VCS:      map[string]VCSProvider{"github": newFakeVCS()},
			Beads:    b,
			StateDir: t.TempDir(),
			Now:      func() time.Time { return time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	// One net-new comment event is enough to trigger lazy cycle creation.
	enriched := &vcs.EnrichedPR{Comments: []api.Comment{{ID: "c1", Author: "reviewer", Body: "please fix"}}}

	// self-authored PR (author == SelfLogin "phillipg") -> mine=true
	recSelf := &recordingCycleBeads{}
	selfPR := samplePR(42, "foo/bar", "feat/mine")
	if err := mkEngine(recSelf).processFeedback(ctx, recSelf, nil, enriched, "foo/bar", selfPR, "pr-bead-self", &Summary{}); err != nil {
		t.Fatalf("processFeedback (self): %v", err)
	}
	if len(recSelf.mineSeen) != 1 || recSelf.mineSeen[0] != true {
		t.Fatalf("self PR: want exactly one cycle stamped mine=true; got %v", recSelf.mineSeen)
	}

	// team-authored PR (author "coworker") -> mine=false
	recTeam := &recordingCycleBeads{}
	teamPR := samplePR(99, "foo/bar", "feat/theirs")
	teamPR.Author = "coworker"
	if err := mkEngine(recTeam).processFeedback(ctx, recTeam, nil, enriched, "foo/bar", teamPR, "pr-bead-team", &Summary{}); err != nil {
		t.Fatalf("processFeedback (team): %v", err)
	}
	if len(recTeam.mineSeen) != 1 || recTeam.mineSeen[0] != false {
		t.Fatalf("team PR: want exactly one cycle stamped mine=false; got %v", recTeam.mineSeen)
	}
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `(cd packages/pg-pr && go test ./internal/sync/ -run TestProcessFeedback_StampsOwnershipFromAuthor -v)`
Expected: PASS.

- [ ] **Step 3: Confirm the test has teeth (red check)**

Temporarily change `sync.go:1483`'s last arg from `e.isSelfAuthored(pr.Author)` to `false`, re-run the command from Step 2, and confirm the **self** case now FAILS (`want mine=true; got [false]`). Then revert the arg back to `e.isSelfAuthored(pr.Author)` and re-run to confirm PASS.

- [ ] **Step 4: Commit**

```bash
git add packages/pg-pr/internal/sync/sync_test.go
git commit -m "test(pg-pr): processFeedback stamps cycle ownership from PR author (pg2-ktqh)"
```

---

## Task 3: pr-pool — drop the join and the unused `selfLogin`

One cohesive change: pr-pool no longer compiles between the discover edit and its callers, so discover + orchestrator + cmd change together. Tests are edited first to define the new behavior.

**Files:**

- Modify: `packages/pr-pool/internal/discover/discover.go` (`Discover` :44, `ForRole` :71, `discoverFeedback` :85, doc comments :1-3/:40-43/:67-70)
- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go:67` (`DrainOnce`)
- Modify: `packages/pr-pool/cmd/pr-pool/drain.go:51` (`runDrain`), `packages/pr-pool/cmd/pr-pool/runrole.go:66-94` (`runRunQuery`)
- Test: `packages/pr-pool/internal/discover/discover_test.go`, `packages/pr-pool/internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Rewrite the test fake's routing and the feedback test**

In `packages/pr-pool/internal/discover/discover_test.go`, replace the `routingRunner` type, its `Run` method, and the `case "show"` branch (the join is gone, so `show` is no longer called by discovery). New version routes by label _value_ — worker carries `worker-ready`, feedback carries `mine`:

```go
// routingRunner answers bd `ready` calls based on the label in argv.
type routingRunner struct {
	readyFeedback   string // JSON for `bd ready --label mine`
	readyWorker     string // JSON for `bd ready --label worker-ready ...`
	sawFeedbackArgs []string
	sawWorkerArgs   []string
	readyErr        error // if set, returned from any "ready" branch
}

func (r *routingRunner) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if r.readyErr != nil {
			return "", r.readyErr
		}
		if contains(args, "worker-ready") {
			r.sawWorkerArgs = args
			return r.readyWorker, nil
		}
		r.sawFeedbackArgs = args // feedback carries --label mine
		return r.readyFeedback, nil
	}
	return "", nil
}
```

Replace `TestDiscover_feedbackOwnership` (`:55`) with a label-based test (note: no `show` map, no parent author, and the third arg to `Discover` is gone):

```go
func TestDiscover_feedbackByLabel(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[
			{"id":"zr-c1","issue_type":"task","title":"process-feedback: A"},
			{"id":"zr-nottask","issue_type":"feature","title":"process-feedback: B"},
			{"id":"zr-nofb","issue_type":"task","title":"some other task"}
		]`,
		readyWorker: `[]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg)
	if err != nil {
		t.Fatal(err)
	}
	// Only the task whose title has the cycle prefix survives the type/title guard.
	if len(got) != 1 || got[0].BeadID != "zr-c1" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("feedback discovery = %+v (want only zr-c1)", got)
	}
	// The feedback query must be `bd ready --label mine` — no parent `bd show`.
	a := strings.Join(rr.sawFeedbackArgs, " ")
	if !strings.Contains(a, "--label mine") {
		t.Fatalf("feedback bd ready missing `--label mine`; got %q", a)
	}
}
```

- [ ] **Step 2: Delete the join/ownership tests and fix arity on the rest**

In `discover_test.go`:

**Delete** these four functions entirely (they only test the removed join / the removed empty-`selfLogin` guard):

- `TestDiscover_skipsBeadOnParentShowError` (`:180`)
- `TestDiscover_emptySelfLoginErrors` (`:140`)
- `TestForRole_feedbackEmptySelfLoginErrors` (`:234`)
- `TestDiscover_feedbackDisabled_emptySelfLoginOK` (`:242`)

**Rewrite** these two `ForRole` tests to drop the `selfLogin` arg:

```go
func TestForRole_feedbackBypassesEnabled(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x"}]`,
	}
	cfg := config.Default()
	cfg.FeedbackEnabled = false // ForRole must run the query regardless of Enabled
	reg := roles.NewRegistry(cfg)
	got, err := ForRole(context.Background(), rr, reg.Feedback)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-c" {
		t.Fatalf("ForRole(feedback) = %+v (want zr-c even though disabled)", got)
	}
}

func TestForRole_unknownKindErrors(t *testing.T) {
	rr := &routingRunner{}
	if _, err := ForRole(context.Background(), rr, roles.Role{Name: "bogus", Kind: 999}); err == nil {
		t.Fatal("ForRole with unknown kind must error")
	}
}
```

(`TestForRole_workerIgnoresSelfLogin` at `:222` is now meaningless — its point was that worker ignores `selfLogin`. Delete it; worker behavior is already covered by `TestDiscover_workerLabelFilter`.)

**Arity-only** — in each of these, change `Discover(context.Background(), rr, reg, "phillipg")` to `Discover(context.Background(), rr, reg)` (drop the trailing login arg): `TestDiscover_workerLabelFilter` (`:79`), `TestDiscover_skipsDisabledRole` (`:101`), `TestDiscover_orderFeedbackThenWorker` (`:127`), `TestDiscover_propagatesReadyError` (`:151`), `TestDiscover_propagatesWorkerReadyError` (`:169`). For `TestDiscover_skipsDisabledRole` (`:101`) also remove its now-unused `show:` map field from the `routingRunner` literal.

- [ ] **Step 3: Run discover tests to verify they fail (discover.go still has the old signatures)**

Run: `(cd packages/pr-pool && go test ./internal/discover/ 2>&1 | head -20)`
Expected: COMPILE FAILURE (`Discover`/`ForRole` still take `selfLogin`; `routingRunner` fields changed). This is the red state.

- [ ] **Step 4: Rewrite `discover.go` — drop the join and `selfLogin`**

Replace the package doc (`:1-3`), `Discover` (`:40-65`), `ForRole` (`:67-83`), and `discoverFeedback` (`:85-111`) with:

```go
// Package discover turns the bead store's ready queue into role→bead dispatches.
// Feedback cycles are identified by a `mine` ownership label stamped at creation
// (pg-pr); worker beads are filtered natively by bd labels. Order is feedback-first.
package discover
```

```go
// Discover returns feedback dispatches then worker dispatches, in priority order,
// honoring each role's Enabled flag. Both queries are pure `bd ready` label filters
// — ownership is read from the `mine` label on the cycle, not joined from its parent.
func Discover(ctx context.Context, br beads.Runner, reg roles.Registry) ([]DispatchContext, error) {
	var out []DispatchContext
	if reg.Feedback.Enabled {
		fb, err := ForRole(ctx, br, reg.Feedback)
		if err != nil {
			return nil, err
		}
		out = append(out, fb...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Feedback.Name)
	}
	if reg.Worker.Enabled {
		wk, err := ForRole(ctx, br, reg.Worker)
		if err != nil {
			return nil, err
		}
		out = append(out, wk...)
	} else {
		slog.Info("role disabled; skipping discovery", "role", reg.Worker.Name)
	}
	return out, nil
}

// ForRole runs ONE role's discovery query, regardless of the role's Enabled flag
// (the smoke harness must be able to query a role disabled in config). Both paths
// are self-relative by construction (label filters), so neither needs a self_login.
func ForRole(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
	switch role.Kind {
	case roles.Feedback:
		return discoverFeedback(ctx, br, role)
	case roles.Worker:
		return discoverWorker(ctx, br, role)
	default:
		return nil, fmt.Errorf("discover: unknown role kind %v", role.Kind)
	}
}

func discoverFeedback(ctx context.Context, br beads.Runner, role roles.Role) ([]DispatchContext, error) {
	issues, err := beads.Ready(ctx, br, "--label", "mine") // self-relative: only my cycles carry `mine`
	if err != nil {
		// Propagate: a bd failure must NOT masquerade as "no ready work", or the
		// pool silently idles on infra failure. (pg2-qq9v)
		return nil, fmt.Errorf("discover feedback: bd ready: %w", err)
	}
	var out []DispatchContext
	for _, iss := range issues {
		// The `mine` label scopes to my cycles; the type/title guard confirms the
		// bead is a feedback cycle (the cycle-identity contract; no custom type).
		if iss.Type == "task" && strings.HasPrefix(iss.Title, "process-feedback:") {
			out = append(out, DispatchContext{Role: role, BeadID: iss.ID})
		}
	}
	return out, nil
}
```

(`discoverWorker` is unchanged. The `beads`, `fmt`, `slog`, and `strings` imports are all still used; no import edits.)

- [ ] **Step 5: Drop `selfLogin` from `DrainOnce` and its callers**

`packages/pr-pool/internal/orchestrator/orchestrator.go` — change the `DrainOnce` signature (`:67`) and the `Discover` call (`:73`):

```go
func (o *Orchestrator) DrainOnce(ctx context.Context) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil
	}
	defer o.teardownAll(ctx)
	dispatches, err := discover.Discover(ctx, o.BD, o.Reg)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	o.drain(ctx, o.Reg.Feedback, dispatches)
	o.drain(ctx, o.Reg.Worker, dispatches)
	return nil
}
```

`packages/pr-pool/cmd/pr-pool/drain.go` — in `runDrain` (`:51`), keep `resolveSelf` as the precheck but discard its value, and call `DrainOnce(ctx)` (`:69`):

```go
	if _, err := resolveSelf(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "resolve self:", err)
		return exitPrecheck
	}
```

```go
	if err := o.DrainOnce(ctx); err != nil {
```

`packages/pr-pool/cmd/pr-pool/runrole.go` — in `runRunQuery`, rewrite the doc comment (`:66-68`), delete the `var selfLogin … if role.Kind == roles.Feedback { … }` block (`:83-93`), and drop the arg from the `ForRole` call (`:94`):

```go
// runRunQuery runs one role's discovery query read-only and prints the matches
// (id, type, title). Discovery is label-based, so it needs no self_login.
func runRunQuery(roleName string) int {
	ctx := context.Background()
	cfg := config.Load()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	reg := roles.NewRegistry(cfg)
	role, ok := resolveRole(reg, roleName)
	if !ok {
		printUsageErr("run-query: unknown role: " + roleName)
		return exitUsage
	}
	dispatches, err := discover.ForRole(ctx, br, role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-query:", err)
		return exitGeneric
	}
	for _, d := range dispatches {
		iss, err := beads.ShowObj(ctx, br, d.BeadID)
		if err != nil {
			fmt.Printf("%s\t(show failed: %v)\n", d.BeadID, err)
			continue
		}
		fmt.Printf("%s\t%s\t%s\n", iss.ID, iss.Type, iss.Title)
	}
	fmt.Printf("# %d %s dispatch(es)\n", len(dispatches), role.Name)
	return exitOK
}
```

(The per-dispatch `beads.ShowObj` here is a display lookup for printing id/type/title — unrelated to the removed ownership join — and stays.)

- [ ] **Step 6: Fix the orchestrator tests (arity, fake routing, discover-error test)**

`DrainOnce` lost its `selfLogin` param and the feedback query is now `bd ready --label mine`, so `packages/pr-pool/internal/orchestrator/orchestrator_test.go` needs three edits or the package will not compile / `TestDrainOnce_noStarvation` will misroute:

(a) Drop the trailing arg from all six `DrainOnce` calls (`orchestrator_test.go:281,295,315,335,350,542`) → `o.DrainOnce(context.Background())`.

(b) Route the `scriptBD` fake by label _value_ (the bare `--label` check now also matches the feedback `--label mine`), and add a `readyErr` field so a discover error can be induced without `selfLogin`. Change the struct (`:77`) and the `ready` case in `Run` (`:91`):

```go
type scriptBD struct {
	mu          sync.Mutex
	statusSeq   map[string][]string
	idx         map[string]int
	updates     []string
	ready       map[string]string // keyed by "feedback"/"worker"
	readyErr    error             // if set, every `bd ready` returns this error
	show        map[string]string
	showErrOnce map[string]error // returns error once per id, then clears
}
```

```go
	case "ready":
		if s.readyErr != nil {
			return "", s.readyErr
		}
		if contains(args, "worker-ready") {
			return s.ready["worker"], nil
		}
		return s.ready["feedback"], nil
```

(c) Rewrite `TestDrainOnce_teardownRunsOnDiscoverError` (`:533`) — it forced a discover error via empty `selfLogin`, but that path is gone. Induce the error via `readyErr` instead (ensure `errors` is imported in the file):

```go
func TestDrainOnce_teardownRunsOnDiscoverError(t *testing.T) {
	cfg := fastCfg()
	// a bd ready failure makes Discover return an error
	bd := &scriptBD{readyErr: errors.New("bd ready failed")}
	// a stray pr-pool session exists from a prior run
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-worker-zr-stray", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err == nil {
		t.Fatal("a bd ready failure should return an error from DrainOnce")
	}
	if !contains(cc.closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must run even on discover error; closed=%v", cc.closed)
	}
}
```

- [ ] **Step 7: Build pr-pool and run its tests**

Run: `(cd packages/pr-pool && go build ./... && go vet ./... && go test ./...)`
Expected: PASS across all packages (`internal/discover`, `internal/orchestrator`, `cmd/pr-pool` including the existing `TestParseSelfLogin`/`TestParseSelfLogin_empty` which still guard the retained precheck).

- [ ] **Step 8: Commit**

```bash
git add packages/pr-pool
git commit -m "refactor(pr-pool): discover feedback by 'mine' label, drop the parent-author join (pg2-ktqh)"
```

---

## Task 4: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run both Go module test suites**

Run: `(cd packages/pg-pr && go test ./...) && (cd packages/pr-pool && go test ./...)`
Expected: PASS for both modules.

- [ ] **Step 2: Run the repo's flake check and pre-commit hooks**

Run: `nix flake check`
Expected: success (per repo CLAUDE.md, this MUST pass before claiming complete).

Run: `prek run --all-files` (or `pre-commit run --all-files` if `prek` is unavailable)
Expected: all hooks pass (treefmt, shellcheck, statix, trailing-whitespace).

- [ ] **Step 3: Confirm the discovery query shape end-to-end (optional, requires a live bd workspace)**

Run: `pr-pool run-query feedback`
Expected: prints matching cycles (id/type/title) and `# N feedback dispatch(es)`; with no `mine`-labeled cycles it prints `# 0 feedback dispatch(es)`. (This exercises `discoverFeedback`'s `bd ready --label mine` with no parent-author fetch during discovery.)

---

## Task 5: Cutover (operational — run at deploy, not part of the code commits)

This is a runbook the deploying agent executes; it produces no committed code. Reference: spec §3.

- [ ] **Step 1: Ship pg-pr first**

Build/deploy the new pg-pr so it begins stamping `mine` on new self-owned cycles. The old pr-pool join is still running and continues to discover everything correctly.

- [ ] **Step 2: Backfill any open cycle (expected no-op — 0 open cycles)**

```bash
SELF_LOGIN=$(pg-pr config show --json | jq -r .self_login)
bd list --type=task --status=open --json --limit 0 \
  | jq -r '.[] | select(.title|startswith("process-feedback:"))
                 | select((.labels//[])|index("mine")|not) | .id' \
  | while read -r cycle; do
      parent=$(bd dep list "$cycle" --direction=down --json | jq -r '.[0].id // empty')
      [ -z "$parent" ] && continue
      author=$(bd show "$parent" --json | jq -r '.metadata.author // ""')
      [ "$author" = "$SELF_LOGIN" ] && bd update "$cycle" --add-label mine
    done
```

- [ ] **Step 3: Verify no self-owned cycle is left unstamped**

```bash
# Should print nothing. Any id printed is a self-cycle that would be silently
# skipped after the flip — re-run Step 2 before proceeding.
bd list --type=task --status=open --json --limit 0 \
  | jq -r '.[] | select(.title|startswith("process-feedback:"))
                 | select((.labels//[])|index("mine")|not) | .id'
```

- [ ] **Step 4: Ship the pr-pool discovery flip**

Build/deploy the new pr-pool. From here, feedback discovery is `bd ready --label mine`. The old join is gone.

---

## Self-Review

**Spec coverage:**

- §1 (stamp `mine` at creation): Task 1 (method + impl + interface + stubs + wrapper + call site) + Task 2 (wiring guard). ✓
- §2 (drop join + `selfLogin` from `discoverFeedback`/`ForRole`/`Discover`/`DrainOnce`/`runRunQuery`/`runDrain`; rewrite doc comments; retain `resolveSelf` precheck): Task 3. ✓
- §3 (agent-run backfill + 3-step cutover): Task 5. ✓
- Testing section (delete/rewrite/arity test lists; pg-pr ripple incl. second stub; precheck via existing `TestParseSelfLogin_empty`): Tasks 1–3. The precheck-relocation note in the spec is satisfied by the pre-existing `TestParseSelfLogin_empty` (`drain.go`'s `parseSelfLogin` is unchanged), so no new precheck test is required. ✓
- `metadata.author` retained (no code touches `mergerequest.go:351`/`detector.go`/`roles.go`): nothing in any task modifies those — retained by omission. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code; mechanical edits (arity drops) name exact files+lines and the unambiguous transformation.

**Type consistency:** `CreateProcessingCycle(..., mine bool)` is used identically in the method, wrapper, interface decl, both stubs, the recording stubs, and all call sites. `Discover(ctx, br, reg)` / `ForRole(ctx, br, role)` / `DrainOnce(ctx)` are used consistently across discover.go, orchestrator.go, runrole.go, and the rewritten tests.
