# pr-pool XDG-global budget config layer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Bead:** `pg2-wgg0` (P3, label `pr-pool`) — re-groomed 2026-06-23 (D4). Most of the original AC already shipped; this plan covers ONLY the remaining delta: the XDG-global budget layer.

**Goal:** Read an optional global config file at `$XDG_CONFIG_HOME/pr-pool/config.toml` and apply its `[pool].budget.{tokens,cost,time}` as a new lowest-precedence FILE layer — beneath the existing repo-local `<RepoRoot>/.pr-pool/config.toml`, above the `PR_POOL_*` env overlay.

**Architecture:** `Load()` already overlays `Default()` → `PR_POOL_*` env → repo-local file (`decodeRoleSet`, which calls `overlayConfigBudget` for `[pool].budget`). We insert ONE new step between the env overlay and the repo-local decode: stat-and-decode the XDG-global file's `[pool].budget` only, reusing the existing `budgetTOML` / `fileShape` / `overlayConfigBudget` machinery. The global file contributes budget ONLY — any `[[role]]` or other `[pool]` keys in it are ignored (roles/scalars remain repo-local + built-in per shipped spec C). A new `configHome()` XDG helper mirrors `stateHome()`. Absent/empty global file = today's behavior byte-for-byte.

**Tech Stack:** Go (stdlib `os`/`path/filepath`, `github.com/BurntSushi/toml` v1.6.0 — already a dep). Repo: `phillipgreenii-nix-agent-support/packages/pr-pool`. Test harness: `t.Setenv` + `t.TempDir` (existing `config_test.go` pattern; `absentConfig`/`writeCfg` helpers).

**Final precedence (low → high):** `Default()` < `PR_POOL_*` env < **XDG-global file** < repo-local file. Files win over env (keeps shipped behavior; do NOT flip it). Repo-local is most specific.

**Branch:** `pr-pool-xdg-global-budget` (off `main`).

---

### Task 1: `configHome()` XDG helper (mirrors `stateHome()`)

**Files:**
- Modify: `packages/pr-pool/internal/config/config.go` (add helper next to `stateHome()` at `:200-205`)
- Test: `packages/pr-pool/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestConfigHome_xdgWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	if got := configHome(); got != "/xdg/config" {
		t.Errorf("configHome() = %q, want /xdg/config (XDG_CONFIG_HOME must win)", got)
	}
}

func TestConfigHome_defaultsToHomeDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/me")
	if got := configHome(); got != "/home/me/.config" {
		t.Errorf("configHome() = %q, want /home/me/.config (default ~/.config)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestConfigHome -v`
Expected: FAIL — `undefined: configHome` (compile error).

- [ ] **Step 3: Add the helper**

In `internal/config/config.go`, immediately after the `stateHome()` function (ends at `:205`), add:

```go
// configHome resolves the XDG config base dir for the pr-pool global config file,
// mirroring stateHome(). XDG_CONFIG_HOME wins; otherwise ~/.config.
func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return os.Getenv("HOME") + "/.config"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestConfigHome -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(pr-pool): add configHome() XDG helper for global config"
```

---

### Task 2: `decodeGlobalBudget` — budget-only decode of the XDG-global file

**Files:**
- Modify: `packages/pr-pool/internal/config/registry.go` (add method after `decodeRoleSet`, before `buildRole` at `:145`)
- Test: `packages/pr-pool/internal/config/registry_test.go` (new file)

The global file must contribute ONLY `[pool].budget` (scope is budget-only; roles/scalars stay repo-local + built-in). We add a focused method that decodes the same `fileShape`, then applies `overlayConfigBudget` and nothing else. A malformed global file is a hard error (consistent with the repo-local file's `decodeRoleSet`).

- [ ] **Step 1: Write the failing test**

Create `internal/config/registry_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDecodeGlobalBudget_overlaysBudgetOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	body := "[pool]\nself_login = \"ignored\"\n[pool.budget]\ntokens = 500000\ntime = \"40m\"\n" +
		"[[role]]\nname = \"ignored-role\"\ntype = \"command\"\n[role.command]\nargv = [\"x\"]\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err != nil {
		t.Fatal(err)
	}
	if c.BudgetTokens != 500000 {
		t.Errorf("BudgetTokens = %d, want 500000", c.BudgetTokens)
	}
	if c.BudgetTime != 40*time.Minute {
		t.Errorf("BudgetTime = %v, want 40m", c.BudgetTime)
	}
	// Cost omitted in file => Default() (0/unlimited) preserved.
	if c.BudgetCost != 0 {
		t.Errorf("BudgetCost = %d, want 0 (unchanged)", c.BudgetCost)
	}
	// self_login and [[role]] must be IGNORED by the global layer (budget-only scope).
	if c.SelfLogin != "" {
		t.Errorf("SelfLogin = %q, want empty (global file must not set non-budget scalars)", c.SelfLogin)
	}
}

func TestDecodeGlobalBudget_malformedIsHardError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("this is = not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := NewRegistry().decodeGlobalBudget(p, &c); err == nil {
		t.Fatal("malformed global config must be a hard error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestDecodeGlobalBudget -v`
Expected: FAIL — `c.decodeGlobalBudget undefined` / `NewRegistry().decodeGlobalBudget undefined` (compile error).

- [ ] **Step 3: Add the method**

In `internal/config/registry.go`, add this method directly after `decodeRoleSet` (which ends at `:143`) and before `buildRole` (`:145`):

```go
// decodeGlobalBudget reads the XDG-global config file and applies ONLY its
// [pool].budget over c (budget-only scope: self_login, worktree_dir, [[role]] and
// every other key are intentionally ignored — roles/scalars stay repo-local +
// built-in per spec C). A present-but-malformed file is a hard error, matching
// decodeRoleSet. Caller stats the path first; this assumes the file exists.
func (r *Registry) decodeGlobalBudget(path string, c *Config) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var shape fileShape
	if _, err := toml.Decode(string(body), &shape); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	overlayConfigBudget(c, shape.Pool.Budget)
	return nil
}
```

(`fileShape`, `poolTOML`, `budgetTOML`, and `overlayConfigBudget` already exist in this file at `:33-48,:263-277`; `os`, `fmt`, and `toml` are already imported at `:4-15`. No new imports.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pr-pool && go test ./internal/config/ -run TestDecodeGlobalBudget -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add internal/config/registry.go internal/config/registry_test.go
git commit -m "feat(pr-pool): add decodeGlobalBudget (budget-only XDG-global overlay)"
```

---

### Task 3: Wire the XDG-global layer into `Load()` (between env and repo-local)

**Files:**
- Modify: `packages/pr-pool/internal/config/config.go:119-121` (insert the global-file step right after the env overlay block, before the repo-local path is resolved at `:120`)
- Test: covered by Task 4's integration tests

The env overlay block ends at `:118` (`c.LogDir = envStr(...)`). The repo-local path is resolved/decoded starting at `:120` (`path := envStr("PR_POOL_CONFIG", ...)`). Insert the global-file decode between them so the order is: env → XDG-global budget → repo-local file (each later layer overlays the earlier). Make the global path overridable via `PR_POOL_GLOBAL_CONFIG` so tests can point it at a temp file without touching the real `~/.config`.

- [ ] **Step 1: Insert the global-file step in `Load()`**

In `internal/config/config.go`, after the `c.LogDir = envStr("PR_POOL_LOG_DIR", c.LogDir)` line (`:118`) and before the `path := envStr("PR_POOL_CONFIG", ...)` line (`:120`), insert:

```go
	// XDG-global budget layer: sits BENEATH the repo-local file but ABOVE env.
	// Contributes [pool].budget only; absent/empty file = no change. The path is
	// overridable via PR_POOL_GLOBAL_CONFIG (test seam, mirrors PR_POOL_CONFIG).
	globalReg := NewRegistry()
	globalPath := envStr("PR_POOL_GLOBAL_CONFIG", filepath.Join(configHome(), "pr-pool", "config.toml"))
	if _, statErr := os.Stat(globalPath); statErr == nil {
		if err := globalReg.decodeGlobalBudget(globalPath, &c); err != nil {
			return Config{}, err
		}
		slog.Info("loaded pr-pool global budget config", "path", globalPath)
	} else if !os.IsNotExist(statErr) {
		return Config{}, fmt.Errorf("stat %s: %w", globalPath, statErr)
	}
```

(`filepath`, `os`, `fmt`, `slog`, `NewRegistry`, `configHome` are all already in scope.)

- [ ] **Step 2: Verify the package still compiles + all existing tests pass**

Run: `cd packages/pr-pool && go test ./internal/config/ -v`
Expected: PASS — every existing test still passes. In particular `TestLoad_envOverrides`, `TestWorkerBudget_envOverrides`, `TestLoad_noFile_builtinRoleSet`, and `TestLoad_tomlReplacesBuiltins` are unchanged (none set `PR_POOL_GLOBAL_CONFIG`, so `globalPath` resolves under `configHome()`; a real `~/.config/pr-pool/config.toml` could in theory exist on a dev machine — see Task 4 Step 1, which neutralizes that for the new tests; the existing tests are unaffected on CI where no such file exists).

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(pr-pool): read XDG-global budget layer in Load() (env < global < repo-local)"
```

---

### Task 4: Integration tests — precedence across all four layers

**Files:**
- Test: `packages/pr-pool/internal/config/config_test.go` (add a test helper + four tests)

Add a `writeGlobalCfg` helper mirroring the existing `writeCfg` (`config_test.go:17-25`), but pointing `PR_POOL_GLOBAL_CONFIG`. To keep tests hermetic (so a stray real `~/.config/pr-pool/config.toml` can't leak in), every new test that should see NO global file sets `PR_POOL_GLOBAL_CONFIG` to an absent temp path — add a `absentGlobalConfig` helper too. Tests use `Load()` + `WorkerBudget()` to assert end-to-end precedence.

- [ ] **Step 1: Add the test helpers**

Add to `internal/config/config_test.go` (after `writeCfg`, around `:25`):

```go
// writeGlobalCfg writes a global config.toml and points PR_POOL_GLOBAL_CONFIG at it.
func writeGlobalCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_POOL_GLOBAL_CONFIG", p)
}

// absentGlobalConfig points PR_POOL_GLOBAL_CONFIG at a non-existent path so Load()
// never picks up a real ~/.config/pr-pool/config.toml on the dev machine.
func absentGlobalConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PR_POOL_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "absent-global.toml"))
}
```

- [ ] **Step 2: Write the four failing precedence tests**

Add to `internal/config/config_test.go`:

```go
// XDG-global-only: budget defaults come from the global file when no repo-local
// file and no env override are present.
func TestLoad_globalBudget_appliesWhenAlone(t *testing.T) {
	absentConfig(t) // no repo-local file => built-in roles
	writeGlobalCfg(t, "[pool.budget]\ntokens = 750000\ncost = 1200\ntime = \"45m\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := c.WorkerBudget()
	if int64(b.Tokens) != 750000 || int64(b.Cost) != 1200 || b.Time != 45*time.Minute {
		t.Errorf("global-only budget = %+v, want tokens=750000 cost=1200 time=45m", b)
	}
}

// Repo-local overrides XDG-global (repo-local is most specific).
func TestLoad_repoLocalBudget_overridesGlobal(t *testing.T) {
	writeGlobalCfg(t, "[pool.budget]\ntokens = 100\ntime = \"10m\"\n")
	writeCfg(t, "[pool.budget]\ntokens = 999\n") // repo-local sets tokens only
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := c.WorkerBudget()
	if int64(b.Tokens) != 999 {
		t.Errorf("Tokens = %d, want 999 (repo-local overrides global)", int64(b.Tokens))
	}
	// time: repo-local omits it, so the global value survives (global < repo-local,
	// each overlay is field-by-field).
	if b.Time != 10*time.Minute {
		t.Errorf("Time = %v, want 10m (global time survives when repo-local omits it)", b.Time)
	}
}

// Either file overrides env (shipped file > env precedence; do NOT flip it).
func TestLoad_globalBudget_overridesEnv(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_BUDGET_TOKENS", "111")
	t.Setenv("PR_POOL_BUDGET_TIME", "120") // 120s
	writeGlobalCfg(t, "[pool.budget]\ntokens = 222\ntime = \"30m\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := c.WorkerBudget()
	if int64(b.Tokens) != 222 || b.Time != 30*time.Minute {
		t.Errorf("budget = %+v, want tokens=222 time=30m (file wins over env)", b)
	}
}

// Absent files = unchanged defaults (today's behavior byte-for-byte).
func TestLoad_noFiles_unchangedDefaults(t *testing.T) {
	absentConfig(t)
	absentGlobalConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := c.WorkerBudget()
	if !b.Tokens.Unlimited() || !b.Cost.Unlimited() {
		t.Error("absent files: token/cost must stay unlimited (default)")
	}
	if b.Time != 25*time.Minute {
		t.Errorf("absent files: Time = %v, want 25m (default)", b.Time)
	}
}
```

- [ ] **Step 3: Run the tests to verify they pass**

Run: `cd packages/pr-pool && go test ./internal/config/ -run 'TestLoad_globalBudget|TestLoad_repoLocalBudget|TestLoad_noFiles' -v`
Expected: PASS — all four. (If `TestLoad_repoLocalBudget_overridesGlobal` fails on `Time`, the field-by-field overlay order in `Load()` is wrong: global must be applied BEFORE the repo-local `decodeRoleSet`.)

- [ ] **Step 4: Commit**

```bash
git add internal/config/config_test.go
git commit -m "test(pr-pool): cover XDG-global budget precedence (env < global < repo-local)"
```

---

### Task 5: Full verification + bead close

- [ ] **Step 1: Full package test + vet**

Run: `cd packages/pr-pool && go test ./... && go vet ./...`
Expected: all PASS.

- [ ] **Step 2: Confirm no new dependency (gomod2nix unchanged)**

Run: `cd packages/pr-pool && go mod tidy && git diff --exit-code go.mod go.sum gomod2nix.toml`
Expected: no diff — `BurntSushi/toml` was already a dep; no new modules added. (If `gomod2nix.toml` does diff, regenerate per repo CLAUDE.md: `nix run github:nix-community/gomod2nix -- generate` and commit it.)

- [ ] **Step 3: Repo checks required before "complete" (per agent-support CLAUDE.md)**

Run (from repo root `phillipgreenii-nix-agent-support`):
```bash
prek run --all-files || pre-commit run --all-files
nix flake check
```
Expected: both PASS.

- [ ] **Step 4: Close the bead**

```bash
bd update pg2-wgg0 --claim         # if not already claimed
bd comment pg2-wgg0 "Implemented the XDG-global budget layer: configHome() XDG helper (config.go), decodeGlobalBudget budget-only overlay (registry.go), wired into Load() between the env overlay and the repo-local decode. Precedence low->high: Default() < PR_POOL_* env < XDG-global file < repo-local file (files win over env, repo-local most specific). Global path overridable via PR_POOL_GLOBAL_CONFIG (test seam). Honors [pool].budget.{tokens,cost,time}; absent/empty global file preserves prior behavior. Tests: global-only defaults, repo-local overrides global, file overrides env, absent files unchanged."
bd close pg2-wgg0
```

---

## Self-review checklist (done while writing)

- **Spec coverage (4 AC bullets):**
  1. Global file at `$XDG_CONFIG_HOME/pr-pool/config.toml` read + `configHome()` helper defaulting to `~/.config` — Task 1 (helper) + Task 3 (path resolution via `filepath.Join(configHome(), "pr-pool", "config.toml")`).
  2. `Load()` reads it as a NEW lowest-precedence FILE layer BENEATH repo-local; order `Default() < env < XDG-global < repo-local` — Task 3 (inserted between env block `:118` and repo-local decode `:120`); proven by Tasks 4 Steps 2-3.
  3. Honors the same `[pool].budget.{tokens,cost,time}` keys (reuses `overlayConfigBudget`); absent/empty global file preserves behavior — Task 2 (`decodeGlobalBudget` reuses `overlayConfigBudget`) + `TestLoad_noFiles_unchangedDefaults`.
  4. Tests: global-only defaults / repo-local overrides global / env overridden by either file / absent files unchanged — Task 4 (all four).
- **Out of scope respected:** no `[role.*]` budget changes (already shipped); file>env NOT flipped (kept shipped order); only budget keys consumed from the global file (`decodeGlobalBudget` ignores `self_login`/`worktree_dir`/`[[role]]` — asserted in `TestDecodeGlobalBudget_overlaysBudgetOnly`).
- **Type/name consistency:** `configHome()` (matches `stateHome()` shape); `decodeGlobalBudget(path string, c *Config) error` used identically in Task 2 test and Task 3 caller; env seam `PR_POOL_GLOBAL_CONFIG` used identically in `Load()` and `writeGlobalCfg`/`absentGlobalConfig`; reuses existing `fileShape`/`poolTOML`/`budgetTOML`/`overlayConfigBudget` (verified present at registry.go `:33-48,:263-277`).
- **No placeholders:** every code step shows the exact edit; every command has expected output.
- **Precedence proof:** field-by-field overlays mean repo-local omitting a key lets the global value survive — covered explicitly by `TestLoad_repoLocalBudget_overridesGlobal` (tokens overridden, time inherited from global).
