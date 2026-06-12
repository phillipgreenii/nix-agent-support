# ccpool Pool Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let ccpool run multiple fully-isolated pools, each a directory selected via `--pool <dir>` or `CCPOOL_POOL`, with zero behavior change for existing single-pool installs.

**Architecture:** A pool is a canonical directory holding `config.toml` (optional), `store.db`, per-session `*.lock` files, and `hook.log`; its tmux server uses a named socket `cc-<hash(canonical path)>`. `main()` normalizes the `--pool` flag into the `CCPOOL_POOL` env var, so a single resolver in `internal/config` (`ResolvePool`) is the one source of truth — no per-command signature changes. Sessions carry `CCPOOL_POOL` in their tmux env so claude hooks re-resolve the same pool. Spec: `docs/superpowers/specs/2026-06-12-ccpool-pool-isolation-design.md`. This plan refines the spec's "parameterize `Load` across 13 sites" Components note with the lower-churn env-normalization approach (same behavior).

**Tech Stack:** Go (stdlib `crypto/sha256`, `encoding/hex`, `path/filepath`, `os`), SQLite store, tmux, `github.com/BurntSushi/toml`. TDD with `go test`; hermetic gate `nix build .#ccpool`.

---

## File Structure

- `internal/config/pool.go` (**create**) — `PoolContext`, `resolvePaths` (pure, no I/O side effects beyond canonicalize), `ResolvePool` (validate + leaf-create), `socketFor`. One responsibility: turn `CCPOOL_POOL` (or empty) into resolved paths + socket.
- `internal/config/pool_test.go` (**create**) — unit tests for the above.
- `internal/config/config.go` (**modify**) — add resolved `Config.PoolRoot`; `Load()` and `StateDirPath()` consult the resolver.
- `internal/config/config_test.go` (**modify**) — extend default-mode + add pool-mode `Load` cases.
- `cmd/ccpool/main.go` (**modify**) — strip `--pool <dir>` into `CCPOOL_POOL` before `pickSubcommand`.
- `cmd/ccpool/pool_args_test.go` (**create**) — tests for the strip/position-contract.
- `internal/session/session.go` (**modify**) — `Deps.PoolPath`; inject `CCPOOL_POOL` in `launchAndWait`.
- `internal/session/pool_env_test.go` (**create**) — env-injection tests (new + resume).
- `cmd/ccpool/{cancel.go,new.go,reply.go}` (**modify**) — set `PoolPath: cfg.PoolRoot` in each `session.New(session.Deps{…})`.
- `cmd/ccpool/doctor.go` (**modify**) — print pool context.
- `cmd/ccpool/pool_integration_test.go` (**create**) — fake-claude two-pool isolation + validation refusal.

---

## Task 1: PoolContext + default-mode resolution

**Files:**

- Create: `internal/config/pool.go`
- Create: `internal/config/pool_test.go`

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"path/filepath"
	"testing"
)

func TestResolvePool_defaultMode(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	pc, err := ResolvePool("")
	if err != nil {
		t.Fatalf("ResolvePool: %v", err)
	}
	if pc.Root != "" {
		t.Errorf("default mode Root = %q, want empty", pc.Root)
	}
	if pc.DBPath != filepath.Join(home, ".local/share", "ccpool", "store.db") {
		t.Errorf("DBPath = %q", pc.DBPath)
	}
	if pc.StateDir != filepath.Join(home, ".local/state", "ccpool") {
		t.Errorf("StateDir = %q", pc.StateDir)
	}
	if pc.Socket != "" {
		t.Errorf("default mode Socket = %q, want empty (config.toml supplies it)", pc.Socket)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestResolvePool_defaultMode`
Expected: FAIL — `undefined: ResolvePool` / `PoolContext`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package config — pool.go: resolve the active pool (CCPOOL_POOL) into paths.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// PoolContext is the resolved location of the active pool. In default mode Root
// is "" and Socket is "" (config.toml's [tmux] supplies the socket); in pool-dir
// mode every field is derived from the canonical pool directory.
type PoolContext struct {
	Root       string // canonical pool dir; "" in default mode
	ConfigPath string // <root>/config.toml, or the XDG config path
	DBPath     string
	StateDir   string // holds hook.log
	RuntimeDir string // holds the per-session *.lock files
	Socket     string // tmux -L name; "" in default mode
	Prefix     string // "" in default mode (config.toml supplies it)
}

// resolvePaths computes a PoolContext from the CCPOOL_POOL value WITHOUT validating
// or creating anything (safe for the hook's log-dir lookup). Empty → default mode.
func resolvePaths(poolEnv string) PoolContext {
	if poolEnv == "" {
		return PoolContext{
			ConfigPath: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "ccpool", "config.toml"),
			DBPath:     filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "ccpool", "store.db"),
			StateDir:   StateDirPath(),
			RuntimeDir: defaultRuntimeDir(),
		}
	}
	root := canonicalize(poolEnv)
	return PoolContext{
		Root:       root,
		ConfigPath: filepath.Join(root, "config.toml"),
		DBPath:     filepath.Join(root, "store.db"),
		StateDir:   root,
		RuntimeDir: root,
		Socket:     socketFor(root),
		Prefix:     "cc-",
	}
}

// ResolvePool resolves AND validates the pool dir, creating a missing leaf.
func ResolvePool(poolEnv string) (PoolContext, error) {
	pc := resolvePaths(poolEnv)
	if pc.Root == "" {
		return pc, nil // default mode: nothing to validate/create
	}
	if err := ensurePoolDir(pc.Root); err != nil {
		return PoolContext{}, err
	}
	return pc, nil
}

func defaultRuntimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "ccpool")
	}
	return filepath.Join(os.TempDir(), "ccpool")
}

// socketFor derives a short, collision-resistant tmux -L name from the canonical
// path: "cc-" + first 16 hex of sha256. ~19 chars, far under the socket-path limit.
func socketFor(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return "cc-" + hex.EncodeToString(sum[:8])
}
```

Add stubs so the package compiles (filled in Task 3):

```go
func canonicalize(p string) string { abs, _ := filepath.Abs(p); return filepath.Clean(abs) }
func ensurePoolDir(root string) error { return nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestResolvePool_defaultMode`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/pool.go internal/config/pool_test.go
git commit -m "feat(ccpool): PoolContext + default-mode pool resolution"
```

---

## Task 2: Socket derivation guarantees

**Files:**

- Modify: `internal/config/pool_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestSocketFor(t *testing.T) {
	a := socketFor("/Users/x/pools/alpha")
	b := socketFor("/Users/x/pools/beta")
	if a == b {
		t.Error("distinct paths must yield distinct sockets")
	}
	if a != socketFor("/Users/x/pools/alpha") {
		t.Error("socketFor must be deterministic")
	}
	deep := socketFor("/Users/x/" + filepath.Join(makeLong(40)...))
	if len(deep) != len("cc-")+16 {
		t.Errorf("socket basename = %d chars, want 19 regardless of path depth", len(deep))
	}
}

func makeLong(n int) []string {
	s := make([]string, n)
	for i := range s {
		s[i] = "deeply-nested-segment"
	}
	return s
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestSocketFor`
Expected: FAIL until `makeLong` import (`path/filepath` already imported) — if it compiles, it should PASS immediately since `socketFor` exists. If PASS, that is acceptable (the test pins the guarantee). If the test references `filepath` and it's unused elsewhere, ensure the import remains.

- [ ] **Step 3: (implementation already exists from Task 1 — no code change)**

- [ ] **Step 4: Run to verify it passes**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestSocketFor`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/pool_test.go
git commit -m "test(ccpool): pin socket derivation determinism + length"
```

---

## Task 3: Canonicalization, validation allowlist, leaf creation

**Files:**

- Modify: `internal/config/pool.go` (replace the `canonicalize`/`ensurePoolDir` stubs)
- Modify: `internal/config/pool_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestEnsurePoolDir_allowlist(t *testing.T) {
	dir := t.TempDir()
	// allowlist-only contents → OK
	for _, f := range []string{"config.toml", "store.db", "store.db-wal", "store.db-shm", "store.db-journal", "alpha.lock", "beta.lock", "hook.log"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensurePoolDir(dir); err != nil {
		t.Fatalf("allowlist-only dir must be accepted: %v", err)
	}
	// a foreign file → refuse
	foreign := filepath.Join(dir, "README.md")
	_ = os.WriteFile(foreign, nil, 0o600)
	if err := ensurePoolDir(dir); err == nil {
		t.Error("foreign file must be refused")
	}
	_ = os.Remove(foreign)
	// a foreign subdirectory → refuse
	_ = os.Mkdir(filepath.Join(dir, "src"), 0o700)
	if err := ensurePoolDir(dir); err == nil {
		t.Error("foreign subdirectory must be refused")
	}
}

func TestEnsurePoolDir_create(t *testing.T) {
	parent := t.TempDir()
	leaf := filepath.Join(parent, "newpool")
	if err := ensurePoolDir(leaf); err != nil {
		t.Fatalf("leaf with existing parent must be created: %v", err)
	}
	info, err := os.Stat(leaf)
	if err != nil || !info.IsDir() {
		t.Fatalf("leaf not created: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("leaf mode = %o, want 0700", info.Mode().Perm())
	}
	// missing parent → error (no mkdir -p)
	if err := ensurePoolDir(filepath.Join(parent, "missing", "deep")); err == nil {
		t.Error("missing parent must error (no mkdir -p)")
	}
}

func TestResolvePool_canonicalIdentity(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	parent := t.TempDir()
	real := filepath.Join(parent, "pool")
	_ = os.Mkdir(real, 0o700)
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	a, _ := ResolvePool(real)
	b, _ := ResolvePool(link)         // via symlink
	c, _ := ResolvePool(real + "/")   // trailing slash
	if a.Root != b.Root || a.Root != c.Root {
		t.Errorf("same dir mapped to different roots: %q %q %q", a.Root, b.Root, c.Root)
	}
	if a.Socket != b.Socket {
		t.Errorf("same dir mapped to different sockets: %q %q", a.Socket, b.Socket)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd packages/ccpool && go test ./internal/config/ -run 'TestEnsurePoolDir|TestResolvePool_canonical'`
Expected: FAIL — stub `ensurePoolDir` accepts everything; stub `canonicalize` doesn't resolve symlinks so the identity test fails.

- [ ] **Step 3: Replace the stubs with real implementations**

```go
import "fmt" // add to pool.go imports alongside the existing ones

// canonicalize resolves symlinks + makes absolute + cleans. EvalSymlinks errors on
// a non-existent leaf, so fall back to canonicalizing the PARENT and rejoining the
// leaf (the dir may not exist yet — it is created by ensurePoolDir).
func canonicalize(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent, leaf := filepath.Split(abs)
	if rp, err := filepath.EvalSymlinks(filepath.Clean(parent)); err == nil {
		return filepath.Join(rp, leaf)
	}
	return abs
}

// poolFileOK reports whether a dir entry name is one ccpool writes.
func poolFileOK(name string) bool {
	switch {
	case name == "config.toml", name == "hook.log":
		return true
	case strings.HasPrefix(name, "store.db"): // store.db, -wal, -shm, -journal
		return true
	case strings.HasSuffix(name, ".lock"): // per-session <name>.lock
		return true
	}
	return false
}

// ensurePoolDir validates an existing pool dir's contents against the allowlist, or
// creates a missing leaf (parent must exist; mode 0700; never mkdir -p).
func ensurePoolDir(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		if _, perr := os.Stat(filepath.Dir(root)); perr != nil {
			return fmt.Errorf("pool parent does not exist: %s", filepath.Dir(root))
		}
		return os.Mkdir(root, 0o700)
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !poolFileOK(e.Name()) {
			return fmt.Errorf("not a ccpool pool dir: %s contains %s", root, e.Name())
		}
	}
	return nil
}
```

Add `"strings"` to the `pool.go` import block.

- [ ] **Step 4: Run to verify they pass**

Run: `cd packages/ccpool && go test ./internal/config/ -run 'TestEnsurePoolDir|TestResolvePool'`
Expected: PASS (all).

- [ ] **Step 5: Commit**

```bash
git add internal/config/pool.go internal/config/pool_test.go
git commit -m "feat(ccpool): pool-dir canonicalize + allowlist validation + leaf-create"
```

---

## Task 4: Wire the resolver into Load() and StateDirPath()

**Files:**

- Modify: `internal/config/config.go:13-25` (add `PoolRoot`), `:87-92` (StateDirPath), `:94-116` (Load)
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLoad_poolMode(t *testing.T) {
	pool := t.TempDir()
	t.Setenv("CCPOOL_POOL", pool)
	// no config.toml in the pool → built-in defaults
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolRoot != pool {
		t.Errorf("PoolRoot = %q, want %q", c.PoolRoot, pool)
	}
	if c.DBPath != filepath.Join(pool, "store.db") {
		t.Errorf("DBPath = %q", c.DBPath)
	}
	if c.StateDir != pool {
		t.Errorf("StateDir = %q, want pool root (hook.log lives here)", c.StateDir)
	}
	if c.Pool.MaxSessions != 6 || c.Tmux.Prefix != "cc-" {
		t.Errorf("no-config pool must use built-in defaults: max=%d prefix=%q", c.Pool.MaxSessions, c.Tmux.Prefix)
	}
	if c.Tmux.Socket == "ccpool" || c.Tmux.Socket == "" {
		t.Errorf("pool-mode socket must be derived, got %q", c.Tmux.Socket)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd packages/ccpool && go test ./internal/config/ -run TestLoad_poolMode`
Expected: FAIL — `c.PoolRoot` undefined; `Load` ignores `CCPOOL_POOL`.

- [ ] **Step 3: Implement**

In `config.go`, add to the resolved block of `Config` (after `RuntimeDir string \`toml:"-"\``):

```go
	PoolRoot string `toml:"-"` // canonical pool dir; "" in default mode
```

Replace `StateDirPath` (lines 87-92) with a pool-aware version (still no `config.Load`, so the hook keeps logging through a bad config):

```go
// StateDirPath resolves the active pool's state dir (holding hook.log) from
// CCPOOL_POOL using only env/fs — no config.toml read — so diagnostics logging
// survives a malformed config. Default mode → $XDG_STATE_HOME/ccpool.
func StateDirPath() string {
	if pool := os.Getenv("CCPOOL_POOL"); pool != "" {
		return canonicalize(pool)
	}
	return filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "ccpool")
}
```

Replace `Load` (lines 94-116):

```go
// Load reads the active pool's config.toml (if present) over the defaults and
// resolves paths. The active pool comes from CCPOOL_POOL (set by --pool in main).
func Load() (Config, error) {
	pc, err := ResolvePool(os.Getenv("CCPOOL_POOL"))
	if err != nil {
		return Config{}, err
	}
	c := defaults()
	if _, err := os.Stat(pc.ConfigPath); err == nil {
		if _, err := toml.DecodeFile(pc.ConfigPath, &c); err != nil {
			return Config{}, fmt.Errorf("decode %s: %w", pc.ConfigPath, err)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("stat %s: %w", pc.ConfigPath, err)
	}
	c.DBPath = pc.DBPath
	c.StateDir = pc.StateDir
	c.RuntimeDir = pc.RuntimeDir
	c.PoolRoot = pc.Root
	if pc.Root != "" { // pool-dir mode: derived socket + constant prefix override config
		c.Tmux.Socket = pc.Socket
		c.Tmux.Prefix = pc.Prefix
	}
	return c, nil
}
```

Note: `StateDirPath` no longer takes the XDG-only path in pool mode, so the duplicate runtime-dir logic now lives only in `defaultRuntimeDir` (Task 1). Delete the old `XDG_RUNTIME_DIR` block that was inside `Load`.

- [ ] **Step 4: Run to verify it passes + no regression**

Run: `cd packages/ccpool && go test ./internal/config/`
Expected: PASS (incl. the existing default-path test in `config_test.go`).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(ccpool): Load + StateDirPath resolve the active pool from CCPOOL_POOL"
```

---

## Task 5: Normalize `--pool` into CCPOOL_POOL in main()

**Files:**

- Modify: `cmd/ccpool/main.go:38` (before `pickSubcommand`)
- Create: `cmd/ccpool/pool_args_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"os"
	"reflect"
	"testing"
)

func TestStripPoolFlag(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantArgv []string
		wantPool string
		wantErr  bool
	}{
		{"none", []string{"ccpool", "list"}, []string{"ccpool", "list"}, "", false},
		{"before cmd", []string{"ccpool", "--pool", "/p", "new", "a"}, []string{"ccpool", "new", "a"}, "/p", false},
		{"equals form", []string{"ccpool", "--pool=/p", "new"}, []string{"ccpool", "new"}, "/p", false},
		{"after cmd rejected", []string{"ccpool", "new", "--pool", "/p"}, nil, "", true},
		{"missing value", []string{"ccpool", "--pool"}, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgv, gotPool, err := stripPoolFlag(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(gotArgv, tc.wantArgv) || gotPool != tc.wantPool {
				t.Errorf("got (%v,%q), want (%v,%q)", gotArgv, gotPool, tc.wantArgv, tc.wantPool)
			}
		})
	}
	_ = os.Args
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestStripPoolFlag`
Expected: FAIL — `undefined: stripPoolFlag`.

- [ ] **Step 3: Implement in main.go**

Add the helper and call it at the top of `main()` (before `pickSubcommand`):

```go
// stripPoolFlag removes a leading "--pool <dir>" (or "--pool=<dir>") that appears
// BEFORE the subcommand, returning the cleaned argv + the pool dir. A --pool after
// the subcommand, or a missing value, is an error (the subcommand flagsets are
// ExitOnError and would mishandle it). Position contract: ccpool --pool <dir> <cmd>.
func stripPoolFlag(argv []string) (clean []string, pool string, err error) {
	if len(argv) < 2 {
		return argv, "", nil
	}
	a := argv[1]
	switch {
	case a == "--pool":
		if len(argv) < 3 {
			return nil, "", fmt.Errorf("--pool requires a directory argument")
		}
		return append([]string{argv[0]}, argv[3:]...), argv[2], nil
	case strings.HasPrefix(a, "--pool="):
		return append([]string{argv[0]}, argv[2:]...), strings.TrimPrefix(a, "--pool="), nil
	}
	// reject a --pool anywhere after the subcommand
	for _, x := range argv[1:] {
		if x == "--pool" || strings.HasPrefix(x, "--pool=") {
			return nil, "", fmt.Errorf("--pool must come before the subcommand: ccpool --pool <dir> <command>")
		}
	}
	return argv, "", nil
}
```

In `main()`, replace `cmd, rest := pickSubcommand(os.Args)` with:

```go
	argv, pool, err := stripPoolFlag(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if pool != "" {
		_ = os.Setenv("CCPOOL_POOL", pool) // --pool overrides any inherited CCPOOL_POOL
	}
	cmd, rest := pickSubcommand(argv)
```

Add `"strings"` to `main.go` imports.

- [ ] **Step 4: Run to verify it passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestStripPoolFlag`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add cmd/ccpool/main.go cmd/ccpool/pool_args_test.go
git commit -m "feat(ccpool): --pool flag normalized into CCPOOL_POOL before dispatch"
```

---

## Task 6: Inject CCPOOL_POOL into launched sessions (new + resume)

**Files:**

- Modify: `internal/session/session.go:75-91` (`Deps`), `:212-217` (`launchAndWait`)
- Modify: `cmd/ccpool/cancel.go:72-88`, and the `session.New(session.Deps{…})` literals in `cmd/ccpool/new.go` and `cmd/ccpool/reply.go`
- Create: `internal/session/pool_env_test.go`

- [ ] **Step 1: Write the failing test**

```go
package session

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// envCapTmux records the env passed to NewSession.
type envCapTmux struct {
	env map[string]string
}

func (c *envCapTmux) HasSession(string) bool { return false }
func (c *envCapTmux) NewSession(_, _ string, env map[string]string, _ []string) error {
	c.env = env
	return nil
}
func (c *envCapTmux) SendKeys(string, ...string) error      { return nil }
func (c *envCapTmux) Paste(string, string) error            { return nil }
func (c *envCapTmux) KillSession(string) error              { return nil }
func (c *envCapTmux) CapturePane(string) (string, error)    { return "", nil }

func TestLaunchAndWait_injectsPool(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Starting, TmuxSession: "cc-a"})
	tm := &envCapTmux{}
	s := New(Deps{
		Tmux: tm, Store: st, Prefix: "cc-", PoolPath: "/pools/alpha",
		Wait: waitFunc(func(context.Context, string, int64) (wait.Outcome, error) {
			return wait.Outcome{State: store.Ready}, nil
		}),
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	if _, err := s.launchAndWait(ctx, "a", "cc-a", "u", "/cwd", 0, []string{"claude"}, nil); err != nil {
		t.Fatalf("launchAndWait: %v", err)
	}
	if tm.env["CCPOOL_POOL"] != "/pools/alpha" {
		t.Errorf("CCPOOL_POOL = %q, want /pools/alpha", tm.env["CCPOOL_POOL"])
	}
}

func TestLaunchAndWait_defaultModeNoPool(t *testing.T) {
	ctx := context.Background()
	st := newMemStore(t)
	_ = st.Insert(ctx, store.Session{Name: "a", UUID: "u", State: store.Starting, TmuxSession: "cc-a"})
	tm := &envCapTmux{}
	s := New(Deps{
		Tmux: tm, Store: st, Prefix: "cc-", PoolPath: "", // default mode
		Wait: waitFunc(func(context.Context, string, int64) (wait.Outcome, error) {
			return wait.Outcome{State: store.Ready}, nil
		}),
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	_, _ = s.launchAndWait(ctx, "a", "cc-a", "u", "/cwd", 0, []string{"claude"}, nil)
	if _, ok := tm.env["CCPOOL_POOL"]; ok {
		t.Error("default mode must NOT set CCPOOL_POOL")
	}
}
```

Add the `wait` import (`github.com/phillipgreenii/ccpool/internal/wait`) to the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `cd packages/ccpool && go test ./internal/session/ -run TestLaunchAndWait_injects`
Expected: FAIL — `Deps` has no `PoolPath` field.

- [ ] **Step 3: Implement**

In `session.go` `Deps` (after `Prefix string`):

```go
	PoolPath  string // canonical pool dir; injected as CCPOOL_POOL into sessions; "" = default mode
```

In `launchAndWait`, after `env["CCPOOL_UUID"] = uuid`:

```go
	if s.d.PoolPath != "" {
		env["CCPOOL_POOL"] = s.d.PoolPath
	}
```

In each `session.New(session.Deps{…})` builder — `buildService` (`cancel.go`), and the inline ones in `new.go` and `reply.go` — add the field (find them with `grep -rn 'session.New(session.Deps{' cmd/ccpool`):

```go
		PoolPath: cfg.PoolRoot,
```

- [ ] **Step 4: Run to verify it passes + full session suite**

Run: `cd packages/ccpool && go test ./internal/session/`
Expected: PASS (new + existing).

- [ ] **Step 5: Commit**

```bash
git add internal/session/session.go internal/session/pool_env_test.go cmd/ccpool/cancel.go cmd/ccpool/new.go cmd/ccpool/reply.go
git commit -m "feat(ccpool): inject CCPOOL_POOL into launched + resumed sessions"
```

---

## Task 7: doctor prints pool context (hook is already pool-aware)

**Files:**

- Modify: `cmd/ccpool/doctor.go`
- Modify: `cmd/ccpool/pool_args_test.go` (add a doctor-output assertion via the built binary, or a unit test if doctor exposes a render func)

The hook needs no change: `runHook` calls `config.StateDirPath()` (now pool-aware) for `hook.log` and `config.Load()` (now pool-aware) for the db — both resolve `CCPOOL_POOL`, which the session carries from Task 6.

- [ ] **Step 1: Write the failing test**

Add to `cmd/ccpool/pool_integration_test.go` (created in Task 8 — if doing Task 7 first, create the file here) a check that `doctor` names the pool. Prefer a small render helper so it is unit-testable:

```go
func TestDoctorHeader_poolContext(t *testing.T) {
	got := doctorPoolHeader(config.Config{PoolRoot: "/pools/alpha", DBPath: "/pools/alpha/store.db", StateDir: "/pools/alpha", Tmux: config.Tmux{Socket: "cc-abc123"}})
	for _, want := range []string{"/pools/alpha", "store.db", "cc-abc123", "hook.log"} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor header missing %q:\n%s", want, got)
		}
	}
	def := doctorPoolHeader(config.Config{PoolRoot: "", DBPath: "/xdg/store.db", Tmux: config.Tmux{Socket: "ccpool"}})
	if !strings.Contains(def, "default") {
		t.Errorf("default mode header should say 'default':\n%s", def)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestDoctorHeader`
Expected: FAIL — `undefined: doctorPoolHeader`.

- [ ] **Step 3: Implement in doctor.go**

```go
// doctorPoolHeader renders the active pool's context for `ccpool doctor`.
func doctorPoolHeader(cfg config.Config) string {
	root := cfg.PoolRoot
	if root == "" {
		root = "default (XDG)"
	}
	return fmt.Sprintf("pool: %s\n  db:     %s\n  socket: %s\n  hook.log: %s\n",
		root, cfg.DBPath, cfg.Tmux.Socket, filepath.Join(cfg.StateDir, "hook.log"))
}
```

Print it at the start of `runDoctor`'s output (after `config.Load()` succeeds): `fmt.Print(doctorPoolHeader(cfg))`. Ensure `fmt` and `path/filepath` are imported in `doctor.go`.

- [ ] **Step 4: Run to verify it passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestDoctorHeader`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ccpool/doctor.go cmd/ccpool/pool_integration_test.go
git commit -m "feat(ccpool): doctor prints active pool context"
```

---

## Task 8: Integration test — two-pool isolation (fake-claude)

**Files:**

- Modify/Create: `cmd/ccpool/pool_integration_test.go`

This mirrors the existing `TestReap_closesOverCap` integration style (build the binary, fake-claude, real tmux, isolated XDG). Token-free, runs in the normal suite.

- [ ] **Step 1: Write the test**

```go
func TestPools_isolated(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base := t.TempDir()
	bin := filepath.Join(base, "ccpool")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	src, _ := os.ReadFile("testdata/fake-claude")
	fake := filepath.Join(base, "fake-claude")
	_ = os.WriteFile(fake, src, 0o755)
	poolA := filepath.Join(base, "A")
	poolB := filepath.Join(base, "B")
	_ = os.MkdirAll(poolA, 0o700)
	_ = os.MkdirAll(poolB, 0o700)
	cfg := "[claude]\nbin = \"" + fake + "\"\nplugin_dir = \"/unused\"\n[wait]\ntimeout = \"10s\"\n"
	_ = os.WriteFile(filepath.Join(poolA, "config.toml"), []byte(cfg), 0o600)
	_ = os.WriteFile(filepath.Join(poolB, "config.toml"), []byte(cfg), 0o600)
	env := append(os.Environ(), "HOME="+base, "CCPOOL_BIN="+bin, "PATH="+base+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		// kill each pool's derived tmux server (best-effort)
		for _, p := range []string{poolA, poolB} {
			abs, _ := filepath.EvalSymlinks(p)
			_ = exec.Command("tmux", "-L", config.SocketForTest(abs), "kill-server").Run()
		}
	})
	run := func(pool string, args ...string) (string, int) {
		full := append([]string{"--pool", pool}, args...)
		cmd := exec.Command(bin, full...)
		cmd.Env = env
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v\n%s", full, err, out)
		}
		return string(out), code
	}

	run(poolA, "new", "alpha")
	run(poolB, "new", "beta")
	outA, _ := run(poolA, "list", "--all")
	outB, _ := run(poolB, "list", "--all")
	if !strings.Contains(outA, "alpha") || strings.Contains(outA, "beta") {
		t.Errorf("pool A list leaked: %s", outA)
	}
	if !strings.Contains(outB, "beta") || strings.Contains(outB, "alpha") {
		t.Errorf("pool B list leaked: %s", outB)
	}
	// foreign content → exit 2
	foreign := filepath.Join(base, "C")
	_ = os.MkdirAll(foreign, 0o700)
	_ = os.WriteFile(filepath.Join(foreign, "README.md"), nil, 0o600)
	if _, code := run(foreign, "list"); code != 2 {
		t.Errorf("foreign pool dir should exit 2, got %d", code)
	}
}
```

Add a tiny exported test shim in `internal/config/pool.go` so the integration test can compute the expected socket: `func SocketForTest(canonicalRoot string) string { return socketFor(canonicalRoot) }` (or make `socketFor` exported as `SocketFor`). Prefer exporting `SocketFor` and updating Task 1/2 references.

- [ ] **Step 2: Run to verify it passes**

Run: `cd packages/ccpool && go test ./cmd/ccpool/ -run TestPools_isolated -v`
Expected: PASS (or SKIP if tmux absent).

- [ ] **Step 3: Commit**

```bash
git add cmd/ccpool/pool_integration_test.go internal/config/pool.go
git commit -m "test(ccpool): two-pool isolation + validation refusal (fake-claude)"
```

---

## Task 9: Full gate

- [ ] **Step 1: vet + test both tags**

Run: `cd packages/ccpool && gofmt -l . && go vet ./... && go vet -tags contract ./cmd/ccpool/ && go test ./...`
Expected: gofmt prints nothing; vet clean; all packages `ok`.

- [ ] **Step 2: hermetic build (runs go test in the sandbox)**

Run: `cd .. && nix build .#ccpool`
Expected: exit 0, `result` symlink created.

- [ ] **Step 3: Commit (if any formatting/fixups)**

```bash
git add -A && git commit -m "chore(ccpool): gate fixups for pool isolation"
```

---

## Self-Review

**Spec coverage:** resolution precedence (Task 5 `--pool`→env + Task 4 env→default) ✔; pool-dir layout + socket (Tasks 1-3) ✔; allowlist incl. `*.lock`/`store.db-journal`/foreign-subdir (Task 3) ✔; leaf 0700 + missing-parent (Task 3) ✔; no-config defaults (Task 4) ✔; hook re-entry: db via `Load`, hook.log via `StateDirPath`, both pool-aware, session carries `CCPOOL_POOL` via new + resume (Tasks 4, 6) ✔; `--pool` strip + position contract + exit 2 (Task 5) ✔; doctor context (Task 7) ✔; isolation integration + refusal (Task 8) ✔; gate (Task 9) ✔. **Non-isolation boundaries (trust/notify) and the reap-timer limitation are documentation/`pg2-4zvg`, no code task here — intentional.** `reap` honoring `--pool` is covered automatically: `reap` calls `config.Load()` which resolves `CCPOOL_POOL` (no extra task needed); a regression test asserting this can be added to Task 8 if desired.

**Placeholder scan:** no TBD/TODO; every code step has concrete code; commands have expected output.

**Type consistency:** `PoolContext`/`ResolvePool`/`resolvePaths`/`socketFor`(→exported `SocketFor` per Task 8)/`ensurePoolDir`/`canonicalize`/`poolFileOK` in `internal/config`; `Config.PoolRoot`; `Deps.PoolPath`; `stripPoolFlag`; `doctorPoolHeader` — names used consistently across tasks. **Action for implementer:** export `socketFor` as `SocketFor` from the start (Task 1) since Task 8 needs it cross-package; update Tasks 1-2 test references accordingly.
