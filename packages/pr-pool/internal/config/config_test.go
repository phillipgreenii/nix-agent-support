package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// prefixLocator is the package-wide test CommandLocator: every command resolves
// EXCEPT one whose (base) name is prefixed "absent-". Unit tests MUST be isolated,
// and the built-in / example role and query sets declare real backing commands
// (bd, ccpool), so resolving them against the host's PATH would make this
// package's tests pass or fail on what happens to be installed. Pinning this stub
// keeps every existing Load() test hermetic while still letting a test say "this
// command is missing" by naming it absent-*.
type prefixLocator struct{}

func (prefixLocator) Locate(name string) error {
	if strings.HasPrefix(filepath.Base(name), "absent-") {
		return fmt.Errorf("stub locator: %q not found", name)
	}
	return nil
}

// stubLocator resolves exactly the commands it was given, so a test states its
// own command environment explicitly.
type stubLocator struct{ present map[string]bool }

func (s stubLocator) Locate(name string) error {
	if s.present[name] {
		return nil
	}
	return fmt.Errorf("stub locator: %q not found", name)
}

func TestMain(m *testing.M) {
	defaultLocator = prefixLocator{}
	os.Exit(m.Run())
}

// --- wiring fixtures (INV-WORKFLOW-1 / USECASE-VALIDATE-CONFIG) ---

// cmdRole is a handler bound to binds, backed by a command that resolves under
// prefixLocator.
func cmdRole(name string, binds ...string) roles.Role {
	return roles.Role{
		Name: name, Type: "command", Enabled: true, Binds: binds,
		Command: &roles.CommandConfig{Argv: []string{"present-tool"}},
	}
}

// eventSource is a period-triggered source emitting emits, backed by a command
// ("present-tool") that resolves under prefixLocator — matching cmdRole's own
// backing command — so a wiring fixture built from it never trips check 5.
func eventSource(name string, emits ...string) query.Source {
	return query.Source{Name: name, Query: query.CommandQuery{
		Meta:   query.Meta{EmitTypes: emits},
		Argv:   []string{"present-tool"},
		Format: query.FormatJSONL,
	}}
}

// thresholdSource is a source whose trigger binds trigBinds with the given count
// — the only config-visible re-entry edge (type -> source).
func thresholdSource(name string, count int, trigBinds []string, emits ...string) query.Source {
	return query.Source{Name: name, Query: query.CommandQuery{
		Meta:   query.Meta{EmitTypes: emits, Trig: query.ThresholdTrigger{Binds: trigBinds, Count: count}},
		Argv:   []string{"present-tool"},
		Format: query.FormatJSONL,
	}}
}

func wiring(qs query.SourceSet, rs roles.RoleSet) Config {
	c := Default()
	c.Queries = qs
	c.Roles = rs
	return c
}

// findingsContain reports whether some finding contains want.
func findingsContain(errs []error, want string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return true
		}
	}
	return false
}

func warningsContain(warns []string, want string) bool {
	for _, w := range warns {
		if strings.Contains(w, want) {
			return true
		}
	}
	return false
}

// A fully wired config has no findings at all — no error and no warning.
func TestValidate_wiredConfigHasNoFindings(t *testing.T) {
	c := wiring(
		query.SourceSet{eventSource("s", "a.ready")},
		roles.RoleSet{cmdRole("r", "a.ready")},
	)
	errs, warns := c.diagnose()
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("wired config must produce no findings; errs=%v warns=%v", errs, warns)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Check 3 — a handler no binding can reach. In the config model a binding is the
// handler's own Binds list, so "no binding reaches it" is an empty Binds.
func TestValidate_disconnectedHandler(t *testing.T) {
	c := wiring(nil, roles.RoleSet{cmdRole("lonely")})
	errs, warns := c.diagnose()
	if !findingsContain(errs, "disconnected handler") {
		t.Fatalf("a role with no binds must be a disconnected-handler error; got %v", errs)
	}
	// It must NOT also be reported as check 4: that names a BOUND handler.
	if findingsContain(errs, "no events to listen for") {
		t.Fatalf("an unbound handler must not also be reported as check 4; got %v", errs)
	}
	if len(warns) != 0 {
		t.Fatalf("a disconnected handler is an error, never a warning; warns=%v", warns)
	}
}

// Check 4 — a BOUND handler whose reachable event set is empty. The docs require
// it reported BOTH ways: check 1 names the type, check 4 names the handler.
func TestValidate_handlerWithNoEventsToListenFor(t *testing.T) {
	c := wiring(
		query.SourceSet{eventSource("s", "b.ready")},
		roles.RoleSet{cmdRole("deaf", "a.ready"), cmdRole("hears", "b.ready")},
	)
	errs, _ := c.diagnose()
	if !findingsContain(errs, "no events to listen for") {
		t.Fatalf("a handler bound only to unemitted types must be a check-4 error; got %v", errs)
	}
	if !findingsContain(errs, "orphan consumer") {
		t.Fatalf("the same config must also report the orphan event TYPE (check 1); got %v", errs)
	}
	if findingsContain(errs, "disconnected handler") {
		t.Fatalf("a bound handler must not be reported as disconnected; got %v", errs)
	}
}

// Check 4 must NOT fire when at least one bound type is emitted.
func TestValidate_boundHandlerWithSomeEmittedTypeIsValid(t *testing.T) {
	c := wiring(
		query.SourceSet{eventSource("s1", "a.ready"), eventSource("s2", "b.ready")},
		roles.RoleSet{cmdRole("r", "a.ready", "b.ready")},
	)
	errs, warns := c.diagnose()
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("a handler with reachable events must be valid; errs=%v warns=%v", errs, warns)
	}
}

// Check 5 — a configured source or handler whose backing command is absent.
func TestValidate_absentBackingCommand(t *testing.T) {
	c := wiring(
		query.SourceSet{{Name: "cmd-source", Query: query.CommandQuery{
			Meta: query.Meta{EmitTypes: []string{"a.ready"}}, Argv: []string{"absent-lister"}, Format: query.FormatJSONL,
		}}},
		roles.RoleSet{{
			Name: "cmd-role", Type: "command", Enabled: true, Binds: []string{"a.ready"},
			Command: &roles.CommandConfig{Argv: []string{"absent-handler"}},
		}},
	)
	errs, warns := c.diagnose()
	if !findingsContain(errs, `handler "cmd-role" backing command "absent-handler"`) {
		t.Fatalf("an absent handler backing command must error; got %v", errs)
	}
	if !findingsContain(errs, `source "cmd-source" backing command "absent-lister"`) {
		t.Fatalf("an absent source backing command must error; got %v", errs)
	}
	if len(warns) != 0 {
		t.Fatalf("an absent backing command is an error, never a warning; warns=%v", warns)
	}
	// Present commands: the same wiring with a locator that resolves both is valid.
	c.Locator = stubLocator{present: map[string]bool{"absent-lister": true, "absent-handler": true}}
	if errs, warns := c.diagnose(); len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("present backing commands must be valid; errs=%v warns=%v", errs, warns)
	}
}

// Every participant kind's backing command is probed, including the fixed
// integration binaries (bd for a beads-backed source, ccpool for a ccpool handler).
func TestValidate_backingCommandCoversFixedIntegrationBinaries(t *testing.T) {
	c := wiring(
		query.SourceSet{{Name: "beads-source", Query: query.BeadsReady{
			Meta: query.Meta{EmitTypes: []string{"a.ready"}},
		}}},
		roles.RoleSet{{
			Name: "ccpool-role", Type: "ccpool", Enabled: true, Binds: []string{"a.ready"},
			CCPool: &roles.CCPoolConfig{Actor: "a"},
		}},
	)
	c.Locator = stubLocator{present: map[string]bool{}} // nothing installed
	errs, _ := c.diagnose()
	if !findingsContain(errs, `backing command "bd"`) {
		t.Fatalf("a beads-backed source's backing command is bd; got %v", errs)
	}
	if !findingsContain(errs, `backing command "ccpool"`) {
		t.Fatalf("a ccpool handler's backing command is ccpool; got %v", errs)
	}
}

// Check 6 — a re-entry cycle the declared graph shows CANNOT terminate: a
// threshold gate satisfied by zero events (count <= 0) re-fires unconditionally.
func TestValidate_determinablyNonTerminatingCycleIsError(t *testing.T) {
	c := wiring(
		query.SourceSet{thresholdSource("loop", 0, []string{"loop.ready"}, "loop.ready")},
		roles.RoleSet{cmdRole("r", "loop.ready")},
	)
	errs, warns := c.diagnose()
	if !findingsContain(errs, "cannot terminate") {
		t.Fatalf("a cycle whose gate needs no events must be a blocking error; got %v", errs)
	}
	if len(warns) != 0 {
		t.Fatalf("a determinably non-terminating cycle must NOT warn as well; warns=%v", warns)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() must block a determinably non-terminating re-entry cycle")
	}
}

// The set's ONE warning — a re-entry cycle whose termination is not determinable.
// It MUST be reported and MUST NOT block the run.
func TestValidate_undeterminableCycleIsWarningNotError(t *testing.T) {
	c := wiring(
		query.SourceSet{thresholdSource("loop", 1, []string{"loop.ready"}, "loop.ready")},
		roles.RoleSet{cmdRole("r", "loop.ready")},
	)
	errs, warns := c.diagnose()
	if len(errs) != 0 {
		t.Fatalf("a cycle whose termination is not determinable must not block; errs=%v", errs)
	}
	if !warningsContain(warns, "cannot determine whether it terminates") {
		t.Fatalf("the cycle must be reported as the one warning; warns=%v", warns)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (the warning must not become an error)", err)
	}
}

// A multi-hop cycle (source -> type -> source -> type -> source) is detected too.
func TestValidate_multiHopCycleWarns(t *testing.T) {
	c := wiring(
		query.SourceSet{
			thresholdSource("up", 2, []string{"down.ready"}, "up.ready"),
			thresholdSource("down", 2, []string{"up.ready"}, "down.ready"),
		},
		roles.RoleSet{cmdRole("ur", "up.ready"), cmdRole("dr", "down.ready")},
	)
	errs, warns := c.diagnose()
	if len(errs) != 0 {
		t.Fatalf("a multi-hop undeterminable cycle must not block; errs=%v", errs)
	}
	if len(warns) != 1 {
		t.Fatalf("one cycle must yield exactly one warning; warns=%v", warns)
	}
}

// Run-scoping is NOT a config defect: a handler disabled for the run leaves the
// configuration valid — validity is judged against the CONFIG, never the run's
// active subset. Neither an error nor the warning.
func TestValidate_runScopedDisabledBindingStaysValid(t *testing.T) {
	disabled := cmdRole("paused", "a.ready")
	disabled.Enabled = false
	c := wiring(
		query.SourceSet{eventSource("s", "a.ready")},
		roles.RoleSet{disabled},
	)
	errs, warns := c.diagnose()
	if len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("a disabled binding must leave the config valid; errs=%v warns=%v", errs, warns)
	}
}

// Findings AGGREGATE: a config breaking several checks reports all of them.
func TestValidate_aggregatesEveryFinding(t *testing.T) {
	c := wiring(
		query.SourceSet{
			eventSource("unheard", "nobody.binds.this"),
			thresholdSource("loop", 1, []string{"loop.ready"}, "loop.ready"),
		},
		roles.RoleSet{
			cmdRole("unbound"),
			cmdRole("deaf", "nobody.emits.this"),
			{
				Name: "no-tool", Type: "command", Enabled: true, Binds: []string{"loop.ready"},
				Command: &roles.CommandConfig{Argv: []string{"absent-tool"}},
			},
		},
	)
	c.PermissionMode = "nonsense"
	errs, warns := c.diagnose()
	for _, want := range []string{
		"invalid PR_POOL_PERMISSION_MODE",
		"orphan consumer",
		"orphan producer",
		"disconnected handler",
		"no events to listen for",
		"backing command",
	} {
		if !findingsContain(errs, want) {
			t.Errorf("aggregated findings missing %q; got %v", want, errs)
		}
	}
	if !warningsContain(warns, "cannot determine whether it terminates") {
		t.Errorf("aggregated run must still report the cycle warning; warns=%v", warns)
	}
	joined := c.Validate()
	if joined == nil {
		t.Fatal("Validate() must return the aggregated error")
	}
	for _, want := range []string{"orphan consumer", "disconnected handler", "backing command"} {
		if !strings.Contains(joined.Error(), want) {
			t.Errorf("errors.Join output missing %q: %v", want, joined)
		}
	}
}

// The Load() path applies check 5 (the seam's default is the package locator).
func TestLoad_absentBackingCommandIsError(t *testing.T) {
	writeCfg(t, `
[[query]]
name = "s"
emits = ["a.ready"]
type = "command"
[query.command]
argv = ["present-tool"]
format = "jsonl"

[[role]]
name = "r"
type = "command"
cap = 1
binds = ["a.ready"]
[role.command]
argv = ["absent-handler"]
`)
	_, err := Load()
	if err == nil {
		t.Fatal("a role whose backing command is absent must fail Load (absent backing command)")
	}
	if !strings.Contains(err.Error(), "backing command") {
		t.Fatalf("Load error must name the absent backing command; got %v", err)
	}
}

// PathLocator is the production strategy: it resolves a real executable and
// rejects a name that is not installed. The executable is created in a temp dir
// the test owns, so the assertion never depends on the host's PATH.
func TestPathLocator_resolvesExecutableAndRejectsAbsent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pr-pool-test-cmd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (PathLocator{}).Locate(bin); err != nil {
		t.Errorf("PathLocator must resolve an executable path: %v", err)
	}
	if err := (PathLocator{}).Locate(filepath.Join(dir, "pr-pool-test-missing")); err == nil {
		t.Error("PathLocator must reject a path that does not exist")
	}
	if err := (PathLocator{}).Locate("pr-pool-no-such-command-4f2a9c"); err == nil {
		t.Error("PathLocator must reject a command that is not on PATH")
	}
}

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

// PR_POOL_MAX_WORKER and the other role env vars are dropped (spec C): setting
// them must have NO effect. Per-role capacity is no longer a declarable concept
// at all (bead pg2-f3mcb.2, INV-CONC-1) — there is no `cap` left to be a no-op
// on, so this only locks that the built-in role SET itself is unaffected.
func TestLoad_roleEnvVarsAreNoOps(t *testing.T) {
	absentConfig(t)
	t.Setenv("PR_POOL_MAX_WORKER", "3")
	t.Setenv("PR_POOL_MAX_FEEDBACK", "5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 3 || c.Roles[1].Name != "worker" {
		t.Errorf("PR_POOL_MAX_WORKER must be a no-op; roles = %+v", c.Roles)
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
type = "command"
[query.command]
argv = ["worker-lister"]
format = "jsonl"

[[role]]
name = "solo"
type = "ccpool"
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
	if len(c.Roles) != 1 || c.Roles[0].Name != "solo" {
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
type = "command"
[query.command]
argv = ["x"]
format = "jsonl"

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
type = "command"
[query.command]
argv = ["y"]
format = "jsonl"
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
type = "command"
[query.command]
argv = ["a"]
format = "jsonl"

[[query]]
name = "down"
emits = ["down.ready"]
type = "command"
[query.trigger]
kind = "threshold"
count = 2
binds = ["up.ready"]
[query.command]
argv = ["b"]
format = "jsonl"

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

// LogDir() resolves the state directory ALONE — no config.toml load — so a
// manager→core callback can find a running core's socket even when the
// repo-local config is missing or broken. It must agree with Load()'s LogDir.
func TestLogDir_resolvesWithoutLoadingConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := LogDir(), "/xdg/state/pr-pool"; got != want {
		t.Errorf("LogDir() = %q, want %q", got, want)
	}
	t.Setenv("PR_POOL_LOG_DIR", "/override/dir")
	if got, want := LogDir(), "/override/dir"; got != want {
		t.Errorf("LogDir() with PR_POOL_LOG_DIR = %q, want %q", got, want)
	}
	// It must not depend on a readable/valid config file: point PR_POOL_CONFIG at a
	// deliberately broken one and prove LogDir() still answers while Load() fails.
	bad := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bad, []byte("this is not = valid = toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_POOL_CONFIG", bad)
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded on a broken config; the premise of this test is wrong")
	}
	if got, want := LogDir(), "/override/dir"; got != want {
		t.Errorf("LogDir() with a broken config = %q, want %q", got, want)
	}
}
