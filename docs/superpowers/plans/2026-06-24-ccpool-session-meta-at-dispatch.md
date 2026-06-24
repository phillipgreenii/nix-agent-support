# ccpool: atomic session metadata at dispatch (`new --meta`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-87ly` (P2, feature, label `ccpool`). Blocks `pg2-5o5i`.
**Spec:** `docs/superpowers/specs/2026-06-24-session-metadata-at-dispatch-design.md`

**Goal:** Let a caller attach session metadata atomically as part of `ccpool new`
(`--meta key=value`, repeatable) instead of a separate `ccpool meta set` call, and
guarantee a fresh Claude session under a reused `external_id` starts with no stale
metadata.

**Architecture:** Add a `Meta map[string]string` to `session.EnsureOpts`; the
`Service.Ensure` wrapper upserts it through the store **inside the per-external_id
lock, after `ensureLocked` returns** — which is after the existing path-4 phantom-row
prune (`store.Delete` already cascade-deletes metadata), so reuse ⇒ new is free.
`cmd/ccpool/new.go` gains a repeatable `--meta` flag (mirroring `--env`) wired into
`EnsureOpts.Meta`.

**Tech Stack:** Go 1.25, `modernc.org/sqlite`, stdlib `flag`. Repo:
`packages/ccpool`, module `github.com/phillipgreenii/ccpool`. Run all `go` commands
from `packages/ccpool`.

---

## File structure

- `internal/session/session.go` — add `Meta` to `EnsureOpts`; add `SetMeta` to the
  `Store` interface; add `applyMeta`; call it in `Ensure`. (Already has the prune
  path that clears stale metadata.)
- `internal/session/session_test.go` — three new `Ensure` tests (real `newMemStore`).
- `cmd/ccpool/new.go` — `metaFlag` type + `--meta` flag + wire to `EnsureOpts.Meta`;
  update the usage string.
- `cmd/ccpool/new_test.go` — `metaFlag` parse unit test.
- `cmd/ccpool/new_integration_test.go` — end-to-end `new --meta` → `meta get`
  round-trip (tmux-gated, mirrors the existing integration harness).

---

## Task 1: `EnsureOpts.Meta` + `Store.SetMeta` + `applyMeta` in `Ensure`

**Files:**

- Modify: `internal/session/session.go` (Store interface ~`:31-37`; `EnsureOpts`
  ~`:149-176`; `Ensure` ~`:203-211`)
- Test: `internal/session/session_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/session/session_test.go` (the harness mirrors
`TestEnsure_brandNewWhenNoRow` / `TestEnsure_mergesCallerEnv`; assertions read back
through the real in-memory store via `st.Meta`):

```go
// TestEnsure_setsDispatchMetadataOnBrandNew: --meta supplied at dispatch is upserted
// onto the brand-new session, addressable by external_id (no separate meta set call).
func TestEnsure_setsDispatchMetadataOnBrandNew(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	ft := &fakeTmux{live: map[string]bool{}}
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-1" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	meta := map[string]string{"prpool.bead": "zr-1", "prpool.role": "worker"}
	if _, err := s.Ensure(ctx, "ext-meta", "/tmp/proj", "", EnsureOpts{Meta: meta}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, err := st.Meta(ctx, "ext-meta")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if !reflect.DeepEqual(got, meta) {
		t.Errorf("Meta = %v, want %v", got, meta)
	}
}

// TestEnsure_reuseLivePreservesAndUpsertsMetadata: on the reuse-live path (tmux
// already up) the session is NOT relaunched, prior metadata is preserved, and the
// dispatch's --meta keys are upserted on top.
func TestEnsure_reuseLivePreservesAndUpsertsMetadata(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	if err := st.Insert(ctx, store.Session{
		ExternalID: "ext-live", ClaudeSessionID: "csid-x", State: store.Ready, TmuxSession: "cc-ext-live",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := st.SetMeta(ctx, "ext-live", "prpool.role", "old"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{"cc-ext-live": true}} // tmux already alive
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Prefix: "cc-",
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if _, err := s.Ensure(ctx, "ext-live", "/tmp/proj", "", EnsureOpts{
		Meta: map[string]string{"prpool.bead": "zr-2"},
	}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 0 {
		t.Fatalf("reuse-live must not relaunch; got %d NewSession calls", len(ft.newCalls))
	}
	got, _ := st.Meta(ctx, "ext-live")
	want := map[string]string{"prpool.role": "old", "prpool.bead": "zr-2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Meta = %v, want %v (preserve old + upsert new)", got, want)
	}
}

// TestEnsure_freshLaunchUnderReusedExternalIDClearsPriorMetadata: when the row's
// Claude session is gone (not resumable, not fresh-starting), Ensure prunes the
// phantom row — store.Delete cascade-deletes the metadata — then launches brand-new,
// so ONLY this dispatch's metadata remains. (Reuse of an external_id whose Claude
// session is gone is "new"; no stale cross-session metadata.)
func TestEnsure_freshLaunchUnderReusedExternalIDClearsPriorMetadata(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	// A row with no resumable Claude session (empty TranscriptPath, nil Exister =>
	// not resumable) and State != Starting (=> not fresh-starting => prunable).
	if err := st.Insert(ctx, store.Session{
		ExternalID: "ext-reuse", ClaudeSessionID: "csid-old", State: store.Ready, TmuxSession: "cc-ext-reuse",
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := st.SetMeta(ctx, "ext-reuse", "prpool.bead", "zr-OLD"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	ft := &fakeTmux{live: map[string]bool{}} // tmux NOT alive => not reuse-live
	waiter := waitFunc(func(_ context.Context, externalID string, since int64) (wait.Outcome, error) {
		_, _ = st.Transition(ctx, externalID, store.Ready, "", "/p/t.jsonl")
		return wait.Outcome{State: store.Ready}, nil
	})
	s := New(Deps{
		Tmux: ft, Trust: &fakeTrust{}, Store: st, Wait: waiter,
		Socket: "ccpool", Prefix: "cc-", PluginDir: "/plugin", ClaudeBin: "claude",
		NewUUID: func() string { return "csid-new" },
		Now:     func() time.Time { return time.Unix(100, 0) },
	})
	if _, err := s.Ensure(ctx, "ext-reuse", "/tmp/proj", "", EnsureOpts{
		Meta: map[string]string{"prpool.bead": "zr-NEW"},
	}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(ft.newCalls) != 1 {
		t.Fatalf("expected a brand-new launch after prune; got %d NewSession calls", len(ft.newCalls))
	}
	got, _ := st.Meta(ctx, "ext-reuse")
	want := map[string]string{"prpool.bead": "zr-NEW"} // zr-OLD cleared by the prune cascade
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Meta = %v, want %v (prior metadata cleared on fresh launch)", got, want)
	}
}
```

Ensure `reflect` is imported in `session_test.go` (add it to the import block if
absent).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd packages/ccpool && go test ./internal/session/ -run 'TestEnsure_(setsDispatchMetadataOnBrandNew|reuseLivePreservesAndUpsertsMetadata|freshLaunchUnderReusedExternalIDClearsPriorMetadata)' -v`
Expected: compile error — `EnsureOpts` has no field `Meta` (and, once that's added,
the metadata assertions fail because nothing upserts it).

- [ ] **Step 3: Add `SetMeta` to the `Store` interface**

In `internal/session/session.go`, extend the `Store` interface (currently
`GetByExternalID`/`Insert`/`Transition`/`Delete`/`List`) with:

```go
type Store interface {
	GetByExternalID(ctx context.Context, externalID string) (store.Session, bool, error)
	Insert(ctx context.Context, s store.Session) error
	Transition(ctx context.Context, externalID string, to store.State, claudeSessionID, transcriptPath string) (store.State, error)
	Delete(ctx context.Context, externalID string) error
	List(ctx context.Context) ([]store.Session, error)
	// SetMeta upserts caller-supplied session metadata (single autocommit UPSERT).
	SetMeta(ctx context.Context, externalID, key, value string) error
}
```

(`*store.Store` already implements `SetMeta`, so the real wiring and `newMemStore`
satisfy this with no fake changes.)

- [ ] **Step 4: Add `Meta` to `EnsureOpts`**

In `internal/session/session.go`, add to the `EnsureOpts` struct (after `Autonomous`):

```go
	// Meta is caller-supplied session metadata upserted atomically as part of this
	// dispatch (e.g. pr-pool's prpool.bead/role/pool). Addressed by external_id and
	// tied to the Claude session lifecycle: preserved across reuse-live/resume,
	// cleared when a phantom row is pruned (reuse => new). Empty/nil is a no-op.
	Meta map[string]string
```

- [ ] **Step 5: Upsert metadata in `Ensure` + add `applyMeta`**

In `internal/session/session.go`, replace the `Ensure` method body so the metadata
upsert runs inside the lock, after `ensureLocked` succeeds:

```go
// Ensure returns a live, ready handle for externalID, launching/resuming/pruning
// as needed (ADR 0015). tmux session name = tmuxName(Prefix, externalID).
func (s *Service) Ensure(ctx context.Context, externalID, cwd, model string, opts EnsureOpts) (Handle, error) {
	var h Handle
	err := s.withLock(externalID, func() error {
		var e error
		h, e = s.ensureLocked(ctx, externalID, cwd, model, opts)
		if e != nil {
			return e
		}
		// Upsert dispatch metadata atomically within the per-external_id lock, AFTER
		// ensureLocked has launched/resumed/reused the session. Running here (not inside
		// ensureLocked) means it lands AFTER the path-4 phantom-row prune, whose
		// store.Delete cascade already cleared any prior session's metadata — so a fresh
		// Claude session under a reused external_id keeps only this dispatch's metadata
		// (reuse => new), while reuse-live/resume preserve prior keys and upsert on top.
		return s.applyMeta(ctx, externalID, opts.Meta)
	})
	return h, err
}

// applyMeta upserts each (key,value) of meta onto externalID. Empty meta is a no-op.
// A write error is returned: the dispatch metadata is part of the creation contract,
// and SetMeta is a single autocommit UPSERT on the DB the row was just written to.
func (s *Service) applyMeta(ctx context.Context, externalID string, meta map[string]string) error {
	for k, v := range meta {
		if err := s.d.Store.SetMeta(ctx, externalID, k, v); err != nil {
			return fmt.Errorf("set dispatch metadata %q: %w", k, err)
		}
	}
	return nil
}
```

(`fmt` is already imported in `session.go`.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd packages/ccpool && go test ./internal/session/ -run 'TestEnsure_(setsDispatchMetadataOnBrandNew|reuseLivePreservesAndUpsertsMetadata|freshLaunchUnderReusedExternalIDClearsPriorMetadata)' -v`
Expected: PASS (all three).

- [ ] **Step 7: Run the full session package to check no regression**

Run: `cd packages/ccpool && go test ./internal/session/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/session/session.go internal/session/session_test.go
git commit -m "feat(ccpool): EnsureOpts.Meta — atomic session metadata at dispatch (pg2-87ly)"
```

---

## Task 2: `ccpool new --meta key=value` flag

**Files:**

- Modify: `cmd/ccpool/new.go` (`runNew` ~`:20-89`)
- Test: `cmd/ccpool/new_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/ccpool/new_test.go`:

```go
func TestMetaFlag_collectsRepeatedPairs(t *testing.T) {
	m := metaFlag{}
	for _, kv := range []string{"prpool.bead=zr-1", "prpool.role=worker"} {
		if err := m.Set(kv); err != nil {
			t.Fatalf("Set(%q): %v", kv, err)
		}
	}
	want := metaFlag{"prpool.bead": "zr-1", "prpool.role": "worker"}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("metaFlag = %v, want %v", m, want)
	}
}

func TestMetaFlag_rejectsMissingEquals(t *testing.T) {
	if err := (metaFlag{}).Set("noequals"); err == nil {
		t.Fatal("metaFlag.Set without '=' must error")
	}
}

func TestMetaFlag_allowsEmptyValue(t *testing.T) {
	m := metaFlag{}
	if err := m.Set("prpool.pinned="); err != nil {
		t.Fatalf("Set bare tag: %v", err)
	}
	if v, ok := m["prpool.pinned"]; !ok || v != "" {
		t.Errorf("bare tag = (%q,%v), want (\"\",true)", v, ok)
	}
}
```

Ensure `reflect` is imported in `new_test.go` (add if absent).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestMetaFlag -v`
Expected: compile error — `undefined: metaFlag`.

- [ ] **Step 3: Add the `metaFlag` type**

In `cmd/ccpool/new.go`, add next to `envFlag` (after its `Set` method, ~`:104`):

```go
// metaFlag collects repeated `--meta KEY=VAL` into a map (mirrors envFlag). An empty
// value (`--meta k=`) is a valid bare tag. Wired into EnsureOpts.Meta so metadata is
// set atomically as part of `ccpool new`, not a separate `ccpool meta set` call.
type metaFlag map[string]string

func (m metaFlag) String() string { return "" }

func (m metaFlag) Set(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("invalid --meta %q, want KEY=VAL", kv)
	}
	m[k] = v
	return nil
}
```

- [ ] **Step 4: Register the flag and wire it into `EnsureOpts`**

In `runNew`, register the flag next to `--env` (after the `env` `fs.Var`, ~`:26`):

```go
	meta := metaFlag{}
	fs.Var(meta, "meta", "session metadata KEY=VAL upserted at dispatch (repeatable)")
```

Add `Meta: meta` to the `EnsureOpts` literal passed to `svc.Ensure` (~`:74-81`):

```go
	h, err := svc.Ensure(context.Background(), externalID, dir, m, session.EnsureOpts{
		Env:            env,
		Name:           *displayName,
		PermissionMode: launch.PermissionMode(*permMode),
		AllowedTools:   *allowedTools,
		Effort:         *effort,
		Autonomous:     *autonomous,
		Meta:           meta,
	})
```

And add `[--meta KEY=VAL ...]` to the usage string (~`:33`):

```go
		fmt.Fprintln(os.Stderr, "usage: ccpool new <external_id> [--name label] [--cwd dir] [--model m] [--env KEY=VAL ...] [--meta KEY=VAL ...] [--permission-mode m] [--allowed-tools list] [--effort v] [--autonomous]")
```

- [ ] **Step 5: Run the test + vet to verify it passes clean**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestMetaFlag -v && go vet ./cmd/ccpool/`
Expected: PASS; vet clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/ccpool/new.go cmd/ccpool/new_test.go
git commit -m "feat(ccpool): add 'ccpool new --meta KEY=VAL' (atomic dispatch metadata)"
```

---

## Task 3: end-to-end `new --meta` → `meta get` round-trip (integration)

Proves the flag plumbs through the real CLI: `ccpool new --meta` sets metadata with
NO separate `meta set` call, queryable via `ccpool meta get`. Mirrors the setup in
`TestNew_launchesFakeClaude_reachesReadyAndLive` (same file), tmux-gated.

**Files:**

- Test: `cmd/ccpool/new_integration_test.go`

- [ ] **Step 1: Write the test**

Add to `cmd/ccpool/new_integration_test.go`:

```go
func TestNew_metaFlag_setsMetadataAtomically(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ccpool := filepath.Join(bin, "ccpool")
	if out, err := exec.Command("go", "build", "-o", ccpool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, err := os.ReadFile("testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fakeClaude := filepath.Join(bin, "fake-claude")
	if err := os.WriteFile(fakeClaude, src, 0o755); err != nil {
		t.Fatal(err)
	}
	socket := "ccpool-metatest"
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	cfgDir := filepath.Join(base, "cfg", "ccpool")
	_ = os.MkdirAll(cfgDir, 0o700)
	cfg := `
[tmux]
socket = "` + socket + `"
[claude]
bin = "` + fakeClaude + `"
plugin_dir = "/unused-in-fake"
[wait]
timeout = "10s"
`
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600)
	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+filepath.Join(base, "cfg"),
		"XDG_DATA_HOME="+filepath.Join(base, "data"),
		"XDG_STATE_HOME="+filepath.Join(base, "state"),
		"HOME="+base,
		"CCPOOL_BIN="+ccpool,
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	proj := filepath.Join(base, "proj")
	_ = os.MkdirAll(proj, 0o755)

	// new WITH --meta, no separate `meta set`.
	newCmd := exec.Command(ccpool, "new", "ext-meta", "--cwd", proj,
		"--meta", "prpool.bead=zr-1", "--meta", "prpool.role=worker")
	newCmd.Env = env
	if out, err := newCmd.CombinedOutput(); err != nil {
		t.Fatalf("new --meta: %v\n%s", err, out)
	}

	// meta get must return the values set by `new`.
	getCmd := exec.Command(ccpool, "meta", "get", "ext-meta", "prpool.bead")
	getCmd.Env = env
	out, err := getCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("meta get: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "zr-1" {
		t.Errorf("meta get prpool.bead = %q, want zr-1", got)
	}
}
```

(`os`, `os/exec`, `path/filepath`, `strings`, `testing` are already imported in this
file.)

- [ ] **Step 2: Run the test**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestNew_metaFlag_setsMetadataAtomically -v`
Expected: PASS (or SKIP if tmux is unavailable in the sandbox).

- [ ] **Step 3: Commit**

```bash
git add cmd/ccpool/new_integration_test.go
git commit -m "test(ccpool): integration round-trip for 'new --meta' -> 'meta get'"
```

---

## Task 4: Gate — full suite, lint, flake check

- [ ] **Step 1: ccpool package tests**

Run: `cd packages/ccpool && go test ./...`
Expected: PASS (no regression).

- [ ] **Step 2: Repo pre-commit gate**

Run (repo root): `prek run --all-files` (or `pre-commit run --all-files`)
Expected: all hooks pass (gofmt/golangci-lint/treefmt clean).

- [ ] **Step 3: Flake check**

Run (repo root): `nix flake check`
Expected: PASS.

- [ ] **Step 4: Update ccpool docs if the `new` flag set is documented**

Check `packages/ccpool/README.md` (and any `docs/`) for a list of `ccpool new` flags;
if present, add `--meta KEY=VAL` (repeatable; atomic dispatch metadata; bare tag when
value omitted). If no such list exists, the usage string update in Task 2 is the
documentation. Commit any doc change:

```bash
git add packages/ccpool/README.md
git commit -m "docs(ccpool): document 'new --meta' flag"
```

---

## Self-review notes

- **Spec coverage:** atomic `--meta` (Tasks 1-2), upsert on all Ensure paths
  (Task 1: brand-new, reuse-live; resume shares the post-`ensureLocked` upsert path),
  reuse ⇒ new clears prior metadata (Task 1 prune test), unchanged `meta`/`sessionmeta`
  surface + still cleared on purge (existing cascade, untouched), gate (Task 4).
- **No new types beyond `metaFlag` + `EnsureOpts.Meta` + `Store.SetMeta`** — all
  defined in the tasks that use them.
- The resume path (`session.go:246-262`, `:274-290`) is not separately tested:
  metadata upsert there is the SAME `applyMeta` call exercised by the brand-new and
  reuse-live tests, and resume preserves prior metadata because it does not prune.
