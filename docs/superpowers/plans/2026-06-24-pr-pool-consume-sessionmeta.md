# pr-pool: consume ccpool sessionmeta (write at dispatch + `sessions` read) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-5o5i` (P2, feature, labels `ccpool`/`pr-pool`). Depends on `pg2-87ly` (done).
**Spec:** `docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md`

**Goal:** pr-pool writes `prpool.bead`/`prpool.role`/`prpool.pool` atomically at dispatch
(via `ccpool new --meta`) and reads it back through both query APIs in a new read-only
`pr-pool sessions` command.

**Architecture:** A `ccpool.DispatchMeta(beadID, role)` helper builds the `prpool.*`
map; the executor passes it to a new `meta map[string]string` parameter on the CC
`Runner.Ensure`, which `CLIRunner.Ensure` emits as sorted `--meta k=v` argv. The read
path opens `sessionmeta.OpenPool(os.Getenv("CCPOOL_POOL"))` — the SAME pool resolution
`ccpool new` uses (`Load()` honors `CCPOOL_POOL`; `LoadForPool("")` would not) — and
`ListByMeta(prpool.pool=pr-pool)` + `Meta(id)` per session.

**Tech Stack:** Go 1.25. Repo: `packages/pr-pool`, module
`github.com/phillipgreenii/pr-pool`; imports `github.com/phillipgreenii/ccpool/sessionmeta`
(already wired: go.mod require + `replace => ../ccpool`). Run `go` from `packages/pr-pool`.

---

## File structure

- `internal/ccpool/meta.go` (new) — `MetaKeyBead/MetaKeyRole/MetaKeyPool` consts,
  `PoolName`, and `DispatchMeta(beadID, role)`.
- `internal/ccpool/ccpool.go` — `Runner.Ensure` gains `meta map[string]string`.
- `internal/ccpool/cli.go` — `CLIRunner.Ensure` gains `meta`, emits sorted `--meta`.
- `internal/executor/ccpool.go` — pass `ccpool.DispatchMeta(d.Item.ID, d.Role.Name)`.
- `internal/dtest/dtest.go`, `internal/watchdog/watchdog_test.go` — fake `Ensure`
  signatures (FakeCC records `EnsuredMeta`).
- `internal/ccpool/cli_test.go` — existing call sites + a `--meta` argv test.
- `cmd/pr-pool/args.go`, `cmd/pr-pool/main.go`, `cmd/pr-pool/sessions_cmd.go` (new),
  `cmd/pr-pool/sessions_cmd_test.go` (new) — the `sessions` command.
- `README.md` — document the `sessions` subcommand.

---

## Task 1: namespace consts + `DispatchMeta` + `Ensure` meta param (write path)

**Files:**

- Create: `internal/ccpool/meta.go`
- Modify: `internal/ccpool/ccpool.go:65`, `internal/ccpool/cli.go:118`,
  `internal/executor/ccpool.go:63`, `internal/dtest/dtest.go:46,60`,
  `internal/watchdog/watchdog_test.go:37`, `internal/ccpool/cli_test.go`
- Test: `internal/ccpool/meta_test.go`, `internal/ccpool/cli_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/ccpool/meta_test.go`:

```go
package ccpool

import (
	"reflect"
	"testing"
)

func TestDispatchMeta_buildsPrpoolNamespacedMap(t *testing.T) {
	got := DispatchMeta("zr-1", "worker")
	want := map[string]string{
		"prpool.bead": "zr-1",
		"prpool.role": "worker",
		"prpool.pool": "pr-pool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DispatchMeta = %v, want %v", got, want)
	}
}
```

Add to `internal/ccpool/cli_test.go` (mirrors `TestEnsure_argv`; the `cli.run`
capture seam is already used there):

```go
func TestEnsure_argv_includesMeta(t *testing.T) {
	var got [][]string
	cli := &CLIRunner{PermissionMode: "dontAsk"}
	cli.run = func(_ context.Context, args []string) ([]byte, []byte, error) {
		got = append(got, args)
		return nil, nil, nil
	}
	meta := map[string]string{"prpool.role": "worker", "prpool.bead": "zr-1", "prpool.pool": "pr-pool"}
	if err := cli.Ensure(context.Background(), "s", "", "/r", nil, meta); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	joined := strings.Join(got[0], " ")
	// keys are emitted sorted for deterministic argv
	for _, want := range []string{
		"--meta prpool.bead=zr-1",
		"--meta prpool.pool=pr-pool",
		"--meta prpool.role=worker",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q; got %v", want, got[0])
		}
	}
	if i, j := strings.Index(joined, "prpool.bead"), strings.Index(joined, "prpool.pool"); i > j {
		t.Errorf("--meta keys not sorted: %v", got[0])
	}
}
```

(`context` and `strings` are already imported in `cli_test.go`.)

- [ ] **Step 2: Run to verify failure**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ -run 'DispatchMeta|Ensure_argv_includesMeta'`
Expected: compile error — `undefined: DispatchMeta`, and `Ensure` arity mismatch
(no `meta` param yet).

- [ ] **Step 3: Create `internal/ccpool/meta.go`**

```go
package ccpool

// pr-pool's session-metadata key namespace. Keys are PREFIXED (prpool.*) because
// they live in a KV store shared with ccpool and any other consumer; the prefix
// prevents collision with a key ccpool or another writer might use. (Design:
// docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md.)
const (
	MetaKeyBead = "prpool.bead" // the bead id the session is working
	MetaKeyRole = "prpool.role" // the pr-pool role name
	MetaKeyPool = "prpool.pool" // owner tag; always PoolName
)

// PoolName is the owner value stamped on prpool.pool, identifying pr-pool's sessions
// among all sessions sharing a ccpool pool DB.
const PoolName = "pr-pool"

// DispatchMeta builds the session metadata pr-pool stamps on a session at dispatch.
func DispatchMeta(beadID, role string) map[string]string {
	return map[string]string{
		MetaKeyBead: beadID,
		MetaKeyRole: role,
		MetaKeyPool: PoolName,
	}
}
```

- [ ] **Step 4: Add `meta` to the `Runner` interface**

In `internal/ccpool/ccpool.go`, change the `Ensure` line:

```go
	Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error
```

- [ ] **Step 5: Emit `--meta` in `CLIRunner.Ensure`**

In `internal/ccpool/cli.go`, change the signature and append sorted `--meta` after the
`--env` block (before `--permission-mode`):

```go
func (c *CLIRunner) Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error {
	args := []string{"new", externalID, "--cwd", cwd}
	if name != "" {
		args = append(args, "--name", name)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+env[k])
	}
	mkeys := make([]string, 0, len(meta))
	for k := range meta {
		mkeys = append(mkeys, k)
	}
	sort.Strings(mkeys)
	for _, k := range mkeys {
		args = append(args, "--meta", k+"="+meta[k])
	}
	if c.PermissionMode != "" {
		args = append(args, "--permission-mode", c.PermissionMode)
	}
	// ... rest unchanged (allowed-tools, effort, model, autonomous, c.ccpool call) ...
```

Also update the doc comment above `Ensure` to mention `--meta K=V…`.

- [ ] **Step 6: Update the fakes**

In `internal/dtest/dtest.go`, add a field to `FakeCC` (after `EnsuredCwd`):

```go
	EnsuredMeta map[string]string // the meta of the last Ensure call
```

and change `FakeCC.Ensure`:

```go
func (f *FakeCC) Ensure(_ context.Context, externalID, name, cwd string, _, meta map[string]string) error {
	f.Ensured = append(f.Ensured, externalID)
	f.EnsureNames = append(f.EnsureNames, name)
	f.EnsuredCwd = cwd
	f.EnsuredMeta = meta
	return f.EnsureErr
}
```

In `internal/watchdog/watchdog_test.go`, change the fake (it ignores everything):

```go
func (f *fakeCC) Ensure(context.Context, string, string, string, map[string]string, map[string]string) error {
	return nil
}
```

- [ ] **Step 7: Pass meta from the executor**

In `internal/executor/ccpool.go`, change the `Ensure` call (~`:63`):

```go
	if err := r.deps.CC.Ensure(ctx, r.deps.ExternalID, display, wt, env, ccpool.DispatchMeta(d.Item.ID, d.Role.Name)); err != nil {
```

(`ccpool` is already imported in this file.)

- [ ] **Step 8: Update existing `cli_test.go` Ensure call sites**

The 6 existing `cli.Ensure(...)` calls (lines ~31, 68, 79, 102, 125, 146) gain a
trailing `nil` meta arg, e.g.:

```go
	err := cli.Ensure(context.Background(), "pr-pool-worker-zr-1-20260616T010203", "pr-pool-worker-zr-1", "/repo",
		map[string]string{...existing env...}, nil)
```

and the no-env ones become `cli.Ensure(context.Background(), "s", "", "/r", nil, nil)`.

- [ ] **Step 9: Run the package tests**

Run: `cd packages/pr-pool && go test ./internal/ccpool/ ./internal/executor/ ./internal/watchdog/ ./internal/dtest/`
Expected: PASS (including the two new tests).

- [ ] **Step 10: Commit**

```bash
git add internal/ccpool internal/executor/ccpool.go internal/dtest/dtest.go internal/watchdog/watchdog_test.go
git commit -m "feat(pr-pool): stamp prpool.* session metadata at dispatch via ccpool new --meta (pg2-5o5i)"
```

---

## Task 2: read path — `pr-pool sessions`

**Files:**

- Modify: `cmd/pr-pool/args.go` (route + usage/help), `cmd/pr-pool/main.go` (dispatch)
- Create: `cmd/pr-pool/sessions_cmd.go`
- Test: `cmd/pr-pool/sessions_cmd_test.go`
- Modify: `README.md`

- [ ] **Step 1: Write the failing tests**

Create `cmd/pr-pool/sessions_cmd_test.go` (round-trips through the REAL
`sessionmeta.Store` — the pr-pool→ccpool seam — exercising both query APIs):

```go
package main

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/ccpool/sessionmeta"
)

func TestCollectPoolSessions_groupsByPoolAndExpandsMeta(t *testing.T) {
	ctx := context.Background()
	s, err := sessionmeta.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()
	// two pr-pool sessions + one foreign session that must be excluded.
	mustSet(t, s, "pr-pool-worker-zr-1-x", map[string]string{"prpool.pool": "pr-pool", "prpool.bead": "zr-1", "prpool.role": "worker"})
	mustSet(t, s, "pr-pool-feedback-zr-2-y", map[string]string{"prpool.pool": "pr-pool", "prpool.bead": "zr-2", "prpool.role": "feedback"})
	mustSet(t, s, "other-tool-sess", map[string]string{"prpool.pool": "something-else", "prpool.bead": "zz-9"})

	rows, err := collectPoolSessions(ctx, s)
	if err != nil {
		t.Fatalf("collectPoolSessions: %v", err)
	}
	want := []sessionRow{
		{ExternalID: "pr-pool-feedback-zr-2-y", Bead: "zr-2", Role: "feedback"},
		{ExternalID: "pr-pool-worker-zr-1-x", Bead: "zr-1", Role: "worker"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v (sorted, foreign excluded)", rows, want)
	}
}

func mustSet(t *testing.T, s *sessionmeta.Store, ext string, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		if err := s.Set(context.Background(), ext, k, v); err != nil {
			t.Fatalf("Set(%s,%s): %v", ext, k, err)
		}
	}
}

func TestRenderSessions_format(t *testing.T) {
	var b bytes.Buffer
	renderSessions(&b, []sessionRow{{ExternalID: "pr-pool-worker-zr-1-x", Bead: "zr-1", Role: "worker"}})
	got := b.String()
	if want := "pool sessions (1):"; !contains(got, want) {
		t.Errorf("missing header %q in %q", want, got)
	}
	for _, want := range []string{"pr-pool-worker-zr-1-x", "bead=zr-1", "role=worker"} {
		if !contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
```

- [ ] **Step 2: Run to verify failure**

Run: `cd packages/pr-pool && go test ./cmd/pr-pool/ -run 'CollectPoolSessions|RenderSessions'`
Expected: compile error — `undefined: collectPoolSessions / sessionRow / renderSessions`.

- [ ] **Step 3: Create `cmd/pr-pool/sessions_cmd.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/ccpool/sessionmeta"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
)

// sessionRow is one pr-pool session resolved from session metadata.
type sessionRow struct {
	ExternalID string
	Bead       string
	Role       string
}

// metaReader is the read subset of sessionmeta.Store used by the sessions view.
type metaReader interface {
	ListByMeta(ctx context.Context, filters map[string]string) ([]string, error)
	Meta(ctx context.Context, externalID string) (map[string]string, error)
}

// collectPoolSessions finds this pool's sessions (ListByMeta on prpool.pool) and
// expands each one's bead/role (Meta). external_ids come back sorted.
func collectPoolSessions(ctx context.Context, r metaReader) ([]sessionRow, error) {
	ids, err := r.ListByMeta(ctx, map[string]string{ccpool.MetaKeyPool: ccpool.PoolName})
	if err != nil {
		return nil, err
	}
	rows := make([]sessionRow, 0, len(ids))
	for _, id := range ids {
		m, err := r.Meta(ctx, id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, sessionRow{ExternalID: id, Bead: m[ccpool.MetaKeyBead], Role: m[ccpool.MetaKeyRole]})
	}
	return rows, nil
}

// renderSessions writes the human-readable session list.
func renderSessions(w io.Writer, rows []sessionRow) {
	fmt.Fprintf(w, "pool sessions (%d):\n", len(rows))
	for _, s := range rows {
		fmt.Fprintf(w, "  - %-40s bead=%-12s role=%s\n", s.ExternalID, s.Bead, s.Role)
	}
}

// runSessions implements `pr-pool sessions`: list this pool's live sessions with
// bead/role resolved from ccpool session metadata. Read-only; opens the SAME pool
// `ccpool new` writes to (CCPOOL_POOL-honoring, matching ccpool's own Load()).
func runSessions() int {
	store, err := sessionmeta.OpenPool(os.Getenv("CCPOOL_POOL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		return exitGeneric
	}
	defer func() { _ = store.Close() }()
	rows, err := collectPoolSessions(context.Background(), store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		return exitGeneric
	}
	renderSessions(os.Stdout, rows)
	return exitOK
}
```

- [ ] **Step 4: Route the subcommand**

In `cmd/pr-pool/args.go`: add `routeSessions` to the `routeKind` const block (after
`routeConfig`):

```go
	routeSessions // list this pool's sessions from metadata (read-only)
```

Add a case in `route` (after the `config` case):

```go
	case "sessions":
		return parseSessionsArgs(args[1:])
```

Add the parser:

```go
// parseSessionsArgs validates `sessions` (no args, read-only).
func parseSessionsArgs(args []string) routeResult {
	if len(args) > 0 {
		return routeResult{kind: routeUsageErr, msg: "sessions: unexpected argument: " + args[0]}
	}
	return routeResult{kind: routeSessions}
}
```

Update `usageLine` to include `| sessions` and add a help line under Subcommands:

```
  sessions                list this pool's sessions (bead/role) from metadata (read-only)
```

- [ ] **Step 5: Dispatch in `main.go`**

Add to the `switch r.kind` in `cmd/pr-pool/main.go`:

```go
	case routeSessions:
		os.Exit(runSessions())
```

- [ ] **Step 6: Run the tests**

Run: `cd packages/pr-pool && go test ./cmd/pr-pool/ -run 'CollectPoolSessions|RenderSessions|Route'`
Expected: PASS. (If `args_test.go` has a route table test, add a `sessions` case there.)

- [ ] **Step 7: Document in README**

In `packages/pr-pool/README.md`, add `sessions` to the subcommand list with a one-line
description matching the help text. Note the `prpool.*` metadata namespace.

- [ ] **Step 8: Commit**

```bash
git add cmd/pr-pool/sessions_cmd.go cmd/pr-pool/sessions_cmd_test.go cmd/pr-pool/args.go cmd/pr-pool/main.go README.md
git commit -m "feat(pr-pool): add 'pr-pool sessions' — read pool sessions via sessionmeta (pg2-5o5i)"
```

---

## Task 3: Gate — full suite, lint, flake check

- [ ] **Step 1:** `cd packages/pr-pool && go test ./...` → PASS.
- [ ] **Step 2:** (repo root) `prek run --all-files` → all hooks pass.
- [ ] **Step 3:** (repo root) `nix flake check` → all checks passed.

---

## Self-review notes

- **Spec coverage:** write `prpool.*` at dispatch via `--meta` (Task 1: DispatchMeta +
  Ensure param + CLIRunner emission); read via BOTH `ListByMeta` and `Meta` (Task 2:
  collectPoolSessions); namespace documented (meta.go consts + README + spec);
  round-trip test through the real `sessionmeta.Store` (Task 2 test); pool resolution
  honors `CCPOOL_POOL` (runSessions uses `OpenPool(os.Getenv("CCPOOL_POOL"))`); no
  regression (Task 3).
- **Type consistency:** `Ensure(..., env, meta map[string]string)` is identical across
  the interface, `CLIRunner`, and both fakes; `DispatchMeta`/`MetaKey*`/`PoolName` are
  the single source of the keys, used by the executor and the reader alike.
- Teardown/single-session lookups and `teardownAll`'s prefix reaping are intentionally
  untouched (spec "out of scope").
