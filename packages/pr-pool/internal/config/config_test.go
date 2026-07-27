package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/query"
)

// absentConfig points PR_POOL_CONFIG at a non-existent path so Load() resolves to
// the built-in role set deterministically (independent of the test's cwd).
func absentConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PR_POOL_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
}

// writeCfg writes a config.toml into a temp dir and points PR_POOL_CONFIG at it.
func writeCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_POOL_CONFIG", p)
}

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
	if d.PermissionMode != "dontAsk" {
		t.Errorf("PermissionMode = %q, want dontAsk (deny-by-default: auto-deny un-allowlisted tools, non-interactive)", d.PermissionMode)
	}
	if d.SessionPrefix != "pr-pool-" {
		t.Errorf("SessionPrefix = %q, want pr-pool-", d.SessionPrefix)
	}
}

func TestDefault_allowedTools(t *testing.T) {
	d := Default()
	if d.AllowedTools == "" {
		t.Fatal("AllowedTools default must be a non-empty allowlist (deny-by-default needs an allowlist to be useful)")
	}
	// Sanity: the conservative default must grant the worker its core verbs and
	// must NOT be a blanket "Bash" (which would re-open arbitrary RCE).
	// Bash(pg-pr: is required so the review role can post reviews back through
	// pg-pr (its only completion action) under dontAsk deny-by-default; see
	// pg2-vmbn7. Scope of per-role access is revisited in pg2-f9vcg.
	for _, must := range []string{"Read", "Edit", "Write", "Bash(git ", "Bash(pg-pr:"} {
		if !strings.Contains(d.AllowedTools, must) {
			t.Errorf("AllowedTools default %q missing required entry %q", d.AllowedTools, must)
		}
	}
	if strings.Contains(d.AllowedTools, "Bash(*)") || strings.Contains(d.AllowedTools, ",Bash,") ||
		strings.HasSuffix(d.AllowedTools, ",Bash") || d.AllowedTools == "Bash" {
		t.Errorf("AllowedTools must not grant unrestricted Bash: %q", d.AllowedTools)
	}
}

func TestLoad_allowedToolsEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_ALLOWED_TOOLS", "Read,Edit")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowedTools != "Read,Edit" {
		t.Errorf("AllowedTools = %q, want Read,Edit (PR_POOL_ALLOWED_TOOLS overlay)", c.AllowedTools)
	}
}

func TestValidate_permissionMode(t *testing.T) {
	valid := []string{"", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}
	for _, m := range valid {
		c := Default()
		c.PermissionMode = m
		if err := c.Validate(); err != nil {
			t.Errorf("Validate() with PermissionMode=%q = %v, want nil", m, err)
		}
	}
	for _, m := range []string{"bypass", "Plan", "yolo", "skip-permissions"} {
		c := Default()
		c.PermissionMode = m
		if err := c.Validate(); err == nil {
			t.Errorf("Validate() with PermissionMode=%q = nil, want error", m)
		}
	}
}

func TestLoad_envOverrides(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_MAX_WAIT", "60")
	t.Setenv("PR_POOL_BEADS_PREFIX", "pg2")
	t.Setenv("PR_POOL_MODEL", "claude-opus-4-8")
	t.Setenv("PR_POOL_PERMISSION_MODE", "plan")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
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
	if c.PermissionMode != "plan" {
		t.Errorf("PR_POOL_PERMISSION_MODE = %q, want plan", c.PermissionMode)
	}
}

// WorktreeDir layers Default() -> PR_POOL_WORKTREE_DIR (env, global) ->
// [pool].worktree_dir (config, repo). Config is the highest priority.
func TestLoad_worktreeDir_configWinsOverEnv(t *testing.T) {
	t.Setenv("PR_POOL_WORKTREE_DIR", "/env/wt")
	writeCfg(t, "[pool]\nworktree_dir = \"/config/wt\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeDir != "/config/wt" {
		t.Errorf("WorktreeDir = %q, want /config/wt ([pool].worktree_dir must override the env var)", c.WorktreeDir)
	}
}

// A [pool] table that omits worktree_dir must NOT clobber the env value.
func TestLoad_worktreeDir_envWhenConfigOmitsKey(t *testing.T) {
	t.Setenv("PR_POOL_WORKTREE_DIR", "/env/wt")
	writeCfg(t, "[pool]\nself_login = \"someone\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeDir != "/env/wt" {
		t.Errorf("WorktreeDir = %q, want /env/wt (absent [pool].worktree_dir must not override the env var)", c.WorktreeDir)
	}
}

// PR_POOL_MAX_WORKER and the other role env vars are dropped (spec C): setting them
// must have NO effect (role caps live in config / built-in defaults only).
func TestLoad_roleEnvVarsAreNoOps(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_MAX_WORKER", "3")
	t.Setenv("PR_POOL_MAX_FEEDBACK", "5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Built-in roles use the Default() caps (1/1); the dropped env vars do nothing.
	if len(c.Roles) != 3 || c.Roles[1].Cap != 1 {
		t.Errorf("PR_POOL_MAX_WORKER must be a no-op; worker cap = %d, want 1", c.Roles[1].Cap)
	}
}

func TestLoad_badIntFallsBackToDefault(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_MAX_WAIT", "notanint")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxWait != 1800*time.Second {
		t.Errorf("bad int should fall back to default 1800s, got %v", c.MaxWait)
	}
}

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
	absentConfig(t)
	absentGlobalConfig(t) // the XDG-global budget layer sits above env; neutralize a real ~/.config so it can't override these env values
	t.Setenv("PR_POOL_BUDGET_TOKENS", "1000000")
	t.Setenv("PR_POOL_BUDGET_TIME", "600")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := c.WorkerBudget()
	if int64(b.Tokens) != 1000000 || b.Time != 600*time.Second {
		t.Errorf("env overrides not applied: %+v", b)
	}
}

func TestLoad_logDirIsStandardPath(t *testing.T) {
	absentConfig(t)
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.LogDir, "/xdg/state/pr-pool"; got != want {
		t.Errorf("LogDir = %q, want %q (standard path, no /log subdir)", got, want)
	}
}

func TestLoad_noFile_builtinRoleSet(t *testing.T) {
	absentConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 3 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" || c.Roles[2].Name != "review" {
		t.Fatalf("no-file must yield built-in feedback+worker+review: %+v", c.Roles)
	}
}

func TestLoad_tomlReplacesBuiltins(t *testing.T) {
	writeCfg(t, `
[[query]]
name = "solo-source"
emits = ["work.ready"]
type = "beads-ready"
[query.beads-ready]
labels = ["worker-ready"]

[[role]]
name = "solo"
type = "ccpool"
cap = 2
enabled = true
binds = ["work.ready"]
[role.ccpool]
actor = "a"
completion = "close-or-handback"
on_failure = "add-human"
on_dispatch_fail = "leave"
prompt = "do {{.BeadID}}"
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "solo" || c.Roles[0].Cap != 2 {
		t.Fatalf("toml must replace built-ins: %+v", c.Roles)
	}
	if len(c.Roles[0].Binds) != 1 || c.Roles[0].Binds[0] != "work.ready" {
		t.Fatalf("role binds not decoded: %+v", c.Roles[0].Binds)
	}
	if len(c.Queries) != 1 || c.Queries[0].Name != "solo-source" {
		t.Fatalf("[[query]] not decoded: %+v", c.Queries)
	}
	if c.Roles[0].CCPool == nil || c.Roles[0].CCPool.Completion != "close-or-handback" {
		t.Fatalf("ccpool config not decoded: %+v", c.Roles[0].CCPool)
	}
}

// A role binding an event type that no query emits is an orphan consumer (M3).
func TestLoad_orphanBindIsError(t *testing.T) {
	writeCfg(t, `
[[role]]
name = "lonely"
type = "command"
cap = 1
binds = ["nobody.emits.this"]
[role.command]
argv = ["x"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("a role binding an unemitted event type must error (orphan consumer)")
	}
}

// A query emitting an event type that no role binds is an orphan producer (M3).
func TestLoad_orphanEmitIsError(t *testing.T) {
	writeCfg(t, `
[[query]]
name = "shouting-into-void"
emits = ["heard.by.none"]
type = "beads-ready"
[query.beads-ready]
labels = ["x"]

[[role]]
name = "r"
type = "command"
cap = 1
binds = ["heard.by.none"]
[role.command]
argv = ["x"]

[[query]]
name = "orphan"
emits = ["orphan.type"]
type = "beads-ready"
[query.beads-ready]
labels = ["y"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("a query emitting an unbound event type must error (orphan producer)")
	}
}

// A threshold-triggered query fires off an upstream event type (Q1); it decodes
// and wires without error.
func TestLoad_thresholdTriggerDecodes(t *testing.T) {
	writeCfg(t, `
[[query]]
name = "up"
emits = ["up.ready"]
type = "beads-ready"
[query.beads-ready]
labels = ["a"]

[[query]]
name = "down"
emits = ["down.ready"]
type = "beads-ready"
[query.trigger]
kind = "threshold"
count = 2
binds = ["up.ready"]
[query.beads-ready]
labels = ["b"]

[[role]]
name = "ur"
type = "command"
cap = 1
binds = ["up.ready"]
[role.command]
argv = ["x"]

[[role]]
name = "dr"
type = "command"
cap = 1
binds = ["down.ready"]
[role.command]
argv = ["y"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("threshold config must load: %v", err)
	}
	var down query.Source
	for _, s := range c.Queries {
		if s.Name == "down" {
			down = s
		}
	}
	tt, ok := query.Threshold(down.Query.Trigger())
	if !ok || tt.Count != 2 || len(tt.Binds) != 1 || tt.Binds[0] != "up.ready" {
		t.Fatalf("threshold trigger not decoded: %#v", down.Query.Trigger())
	}
}

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

func TestLoad_malformedIsHardError(t *testing.T) {
	writeCfg(t, "this is = not valid toml [[[")
	if _, err := Load(); err == nil {
		t.Fatal("malformed config must be a hard error, not a silent fallback")
	}
}

func TestLoad_singleBracketRoleTypoIsError(t *testing.T) {
	writeCfg(t, "[role]\nname = \"x\"\n") // single bracket = the classic [[role]] typo
	if _, err := Load(); err == nil {
		t.Fatal("[role] single-bracket table must error, not fall back to built-ins")
	}
}

func TestLoad_unknownTypeIsError(t *testing.T) {
	writeCfg(t, `
[[role]]
name = "x"
type = "weird"
cap = 1
binds = ["a.ready"]
[role.weird]
foo = "bar"
`)
	if _, err := Load(); err == nil {
		t.Fatal("unknown role type must error")
	}
}

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

func TestDefault_autonomousTrue(t *testing.T) {
	if !Default().Autonomous {
		t.Error("Default().Autonomous should be true (workers are human-less by default)")
	}
}

func TestLoad_autonomousEnvOverlay(t *testing.T) {
	absentConfig(t)
	absentGlobalConfig(t)
	t.Setenv("PR_POOL_AUTONOMOUS", "false")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Autonomous {
		t.Error("PR_POOL_AUTONOMOUS=false should disable autonomous")
	}
}

func TestLoad_promptXorPromptFile(t *testing.T) {
	writeCfg(t, `
[[role]]
name = "x"
type = "ccpool"
cap = 1
binds = ["a.ready"]
[role.ccpool]
actor = "a"
completion = "close-only"
on_failure = "unclaim"
on_dispatch_fail = "unclaim"
prompt = "hi"
prompt_file = "x.md"
`)
	if _, err := Load(); err == nil {
		t.Fatal("prompt AND prompt_file must error (XOR)")
	}
}
