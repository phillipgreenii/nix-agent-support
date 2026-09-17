package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
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

// cmdRole is a handler bound to binds. As of docket pg2-oju6w's Task 5.4 a
// role carries no backing command of its own (that is the registered
// handler participant's own concern, reached over the wire) — the name is
// kept for its many existing callers' sake.
func cmdRole(name string, binds ...string) roles.Role {
	return roles.Role{Name: name, Enabled: true, Binds: binds}
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

// Check 5 — a configured source whose backing command is absent. As of
// docket pg2-oju6w's Task 5.4 (ADR 0065's "Open question resolved" section)
// a ROLE no longer declares a backing command at all — that check narrowed
// to sources only (config.go's absentBackingCommands doc comment) — so this
// test now covers the source half alone.
func TestValidate_absentBackingCommand(t *testing.T) {
	c := wiring(
		query.SourceSet{{Name: "cmd-source", Query: query.CommandQuery{
			Meta: query.Meta{EmitTypes: []string{"a.ready"}}, Argv: []string{"absent-lister"}, Format: query.FormatJSONL,
		}}},
		roles.RoleSet{cmdRole("cmd-role", "a.ready")},
	)
	errs, warns := c.diagnose()
	if !findingsContain(errs, `source "cmd-source" backing command "absent-lister"`) {
		t.Fatalf("an absent source backing command must error; got %v", errs)
	}
	if len(warns) != 0 {
		t.Fatalf("an absent backing command is an error, never a warning; warns=%v", warns)
	}
	// A present command: the same wiring with a locator that resolves it is valid.
	c.Locator = stubLocator{present: map[string]bool{"absent-lister": true}}
	if errs, warns := c.diagnose(); len(errs) != 0 || len(warns) != 0 {
		t.Fatalf("present backing commands must be valid; errs=%v warns=%v", errs, warns)
	}
}

// A registered source PARTICIPANT's own invoked command (query.
// ParticipantQuery, docket pg2-oju6w's Task 5.8 — the wire-client
// replacement for the retired query.BeadsReady, which used to back this
// same check with the fixed "bd" binary) is probed like any other source
// backing command. The former ccpool-handler half of this test (asserting
// the fixed "ccpool" binary for a ccpool-typed role) has no successor: a
// role no longer declares any backing command (see
// TestValidate_absentBackingCommand's updated doc comment).
func TestValidate_backingCommandCoversFixedIntegrationBinaries(t *testing.T) {
	c := wiring(
		query.SourceSet{{Name: "participant-source", Query: query.ParticipantQuery{
			Meta: query.Meta{EmitTypes: []string{"a.ready"}}, Command: []string{"pg-router-ccpool-handler"},
		}}},
		roles.RoleSet{cmdRole("r", "a.ready")},
	)
	c.Locator = stubLocator{present: map[string]bool{}} // nothing installed
	errs, _ := c.diagnose()
	if !findingsContain(errs, `backing command "pg-router-ccpool-handler"`) {
		t.Fatalf("a registered source participant's backing command is checked; got %v", errs)
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
			// A role no longer declares its own backing command (Task 5.4), so the
			// "backing command" finding this test aggregates now comes from a
			// source instead — bound to the same "loop.ready" type loop already
			// emits/binds, so it introduces no NEW orphan-producer/-consumer finding.
			{Name: "no-tool", Query: query.CommandQuery{
				Meta: query.Meta{EmitTypes: []string{"loop.ready"}}, Argv: []string{"absent-tool"}, Format: query.FormatJSONL,
			}},
		},
		roles.RoleSet{
			cmdRole("unbound"),
			cmdRole("deaf", "nobody.emits.this"),
			cmdRole("no-tool-consumer", "loop.ready"),
		},
	)
	errs, warns := c.diagnose()
	for _, want := range []string{
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
argv = ["absent-lister"]
format = "jsonl"

[[role]]
name = "r"
binds = ["a.ready"]
`)
	_, err := Load()
	if err == nil {
		t.Fatal("a source whose backing command is absent must fail Load (absent backing command)")
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
	bin := filepath.Join(dir, "pg-router-test-cmd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (PathLocator{}).Locate(bin); err != nil {
		t.Errorf("PathLocator must resolve an executable path: %v", err)
	}
	if err := (PathLocator{}).Locate(filepath.Join(dir, "pg-router-test-missing")); err == nil {
		t.Error("PathLocator must reject a path that does not exist")
	}
	if err := (PathLocator{}).Locate("pg-router-no-such-command-4f2a9c"); err == nil {
		t.Error("PathLocator must reject a command that is not on PATH")
	}
}

// absentConfig points PG_ROUTER_CONFIG at a non-existent path so Load() resolves to
// the built-in role set deterministically (independent of the test's cwd).
func absentConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
}

// writeCfg writes a config.toml into a temp dir and points PG_ROUTER_CONFIG at it.
func writeCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", p)
}

// writeGlobalCfg writes a global config.toml and points PG_ROUTER_GLOBAL_CONFIG at it.
func writeGlobalCfg(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "global.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", p)
}

// absentGlobalConfig points PG_ROUTER_GLOBAL_CONFIG at a non-existent path so Load()
// never picks up a real ~/.config/pg-router/config.toml on the dev machine.
func absentGlobalConfig(t *testing.T) {
	t.Helper()
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "absent-global.toml"))
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
	if d.SessionPrefix != "pg-router-" {
		t.Errorf("SessionPrefix = %q, want pg-router-", d.SessionPrefix)
	}
}

func TestDefault_allowedTools(t *testing.T) {
	d := Default()
	if d.AllowedTools == "" {
		t.Fatal("AllowedTools default must be a non-empty allowlist (deny-by-default needs an allowlist to be useful)")
	}
	// Sanity: the conservative default must grant the worker its core verbs and
	// must NOT be a blanket "Bash" (which would re-open arbitrary RCE). PRTool
	// defaults to empty, so no external review-post tool grant is baked in —
	// see TestDefaultAllowedTools_prToolGrant below for that behavior.
	for _, must := range []string{"Read", "Edit", "Write", "Bash(git "} {
		if !strings.Contains(d.AllowedTools, must) {
			t.Errorf("AllowedTools default %q missing required entry %q", d.AllowedTools, must)
		}
	}
	if strings.Contains(d.AllowedTools, "Bash(*)") || strings.Contains(d.AllowedTools, ",Bash,") ||
		strings.HasSuffix(d.AllowedTools, ",Bash") || d.AllowedTools == "Bash" {
		t.Errorf("AllowedTools must not grant unrestricted Bash: %q", d.AllowedTools)
	}
}

// TestDefaultAllowedTools_prToolGrant proves defaultAllowedTools folds a
// configured PRTool into a Bash(<tool>:*) grant (docket pg2-oju6w's Task
// 5.13 register-catch-down): a review role's only completion action is to
// post its review back through that external tool, so under dontAsk
// deny-by-default the grant MUST be present once PRTool names one — see
// pg2-vmbn7 — but pg-router's own compiled-in default no longer names any
// concrete tool itself (GOAL-MIN-1's Floor).
func TestDefaultAllowedTools_prToolGrant(t *testing.T) {
	if got := defaultAllowedTools(""); got != baseAllowedTools {
		t.Errorf("defaultAllowedTools(\"\") = %q, want the base allowlist unchanged: %q", got, baseAllowedTools)
	}
	got := defaultAllowedTools("review-tool")
	if !strings.Contains(got, "Bash(review-tool:*)") {
		t.Errorf("defaultAllowedTools(%q) = %q, want it to contain Bash(review-tool:*)", "review-tool", got)
	}
	if !strings.Contains(got, "Bash(git ") {
		t.Errorf("defaultAllowedTools(%q) = %q, must still contain the base allowlist", "review-tool", got)
	}
}

func TestLoad_allowedToolsEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_ALLOWED_TOOLS", "Read,Edit")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowedTools != "Read,Edit" {
		t.Errorf("AllowedTools = %q, want Read,Edit (PG_ROUTER_ALLOWED_TOOLS overlay)", c.AllowedTools)
	}
}

// TestLoad_prToolEnvOverride proves PG_ROUTER_PR_TOOL folds into the
// built-in AllowedTools default (when PG_ROUTER_ALLOWED_TOOLS is not itself
// set) rather than requiring an operator to spell out the whole allowlist
// just to grant one more tool.
func TestLoad_prToolEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_PR_TOOL", "review-tool")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PRTool != "review-tool" {
		t.Errorf("PRTool = %q, want review-tool (PG_ROUTER_PR_TOOL overlay)", c.PRTool)
	}
	if !strings.Contains(c.AllowedTools, "Bash(review-tool:*)") {
		t.Errorf("AllowedTools = %q, want it to contain Bash(review-tool:*)", c.AllowedTools)
	}
}

// TestLoad_prToolDoesNotOverrideExplicitAllowedTools proves an explicit
// PG_ROUTER_ALLOWED_TOOLS always wins outright — PRTool only fills the
// built-in default, never appends onto an operator-supplied allowlist.
func TestLoad_prToolDoesNotOverrideExplicitAllowedTools(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_PR_TOOL", "review-tool")
	t.Setenv("PG_ROUTER_ALLOWED_TOOLS", "Read,Edit")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowedTools != "Read,Edit" {
		t.Errorf("AllowedTools = %q, want Read,Edit (explicit PG_ROUTER_ALLOWED_TOOLS must win over PRTool)", c.AllowedTools)
	}
}

// TestDefault_handlerCommandIsEmpty locks GOAL-MIN-1's Floor (ADR 0065's
// Register row R16, bead pg2-g068j's own doc comment on HandlerCommand):
// pg-router's own built-in defaults MUST name no concrete tool, so an
// unconfigured deployment gets HandlerCommand == "" — never a hardcoded
// participant binary name.
func TestDefault_handlerCommandIsEmpty(t *testing.T) {
	if got := Default().HandlerCommand; got != "" {
		t.Errorf("Default().HandlerCommand = %q, want empty (GOAL-MIN-1's Floor: no baked-in tool name)", got)
	}
}

// TestLoad_handlerCommandEnvOverride proves PG_ROUTER_HANDLER_COMMAND folds
// into Config.HandlerCommand — the wireclient.CommandFor seam bootCore/
// runRunRole (cmd/pg-router) resolve every enabled role's registered handler
// participant command through (bead pg2-g068j).
func TestLoad_handlerCommandEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_HANDLER_COMMAND", "/usr/local/bin/pg-router-ccpool-handler")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HandlerCommand != "/usr/local/bin/pg-router-ccpool-handler" {
		t.Errorf("HandlerCommand = %q, want /usr/local/bin/pg-router-ccpool-handler (PG_ROUTER_HANDLER_COMMAND overlay)", c.HandlerCommand)
	}
}

// TestDefault_handlerCommandDirIsEmpty mirrors TestDefault_handlerCommandIsEmpty:
// HandlerCommandDir carries no baked-in default either.
func TestDefault_handlerCommandDirIsEmpty(t *testing.T) {
	if got := Default().HandlerCommandDir; got != "" {
		t.Errorf("Default().HandlerCommandDir = %q, want empty", got)
	}
}

// TestLoad_handlerCommandDirEnvOverride proves PG_ROUTER_HANDLER_COMMAND_DIR
// folds into Config.HandlerCommandDir (this bead, pg2-ymb3v) — the per-role
// JSON directory cmd/pg-router/run.go's handlerCommandFor consults to
// differentiate roles sharing one HandlerCommand.
func TestLoad_handlerCommandDirEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_HANDLER_COMMAND_DIR", "/etc/pg-router/roles")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HandlerCommandDir != "/etc/pg-router/roles" {
		t.Errorf("HandlerCommandDir = %q, want /etc/pg-router/roles (PG_ROUTER_HANDLER_COMMAND_DIR overlay)", c.HandlerCommandDir)
	}
}

// PermissionMode validation MOVED to the new module's own config validation
// (packages/pg-router-ccpool-handler/internal/config), docket pg2-oju6w Task
// 5.7 — pg-router itself no longer rejects an invalid PermissionMode value at
// its own config-load time; see that package's TestValidate_permissionMode.

func TestLoad_envOverrides(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_MAX_WAIT", "60")
	t.Setenv("PG_ROUTER_BEADS_PREFIX", "pg2")
	t.Setenv("PG_ROUTER_MODEL", "claude-opus-4-8")
	t.Setenv("PG_ROUTER_PERMISSION_MODE", "plan")
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
		t.Errorf("PG_ROUTER_PERMISSION_MODE = %q, want plan", c.PermissionMode)
	}
}

// WorktreeDir layers Default() -> PG_ROUTER_WORKTREE_DIR (env, global) ->
// [pool].worktree_dir (config, repo). Config is the highest priority.
func TestLoad_worktreeDir_configWinsOverEnv(t *testing.T) {
	t.Setenv("PG_ROUTER_WORKTREE_DIR", "/env/wt")
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
	t.Setenv("PG_ROUTER_WORKTREE_DIR", "/env/wt")
	writeCfg(t, "[pool]\nself_login = \"someone\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.WorktreeDir != "/env/wt" {
		t.Errorf("WorktreeDir = %q, want /env/wt (absent [pool].worktree_dir must not override the env var)", c.WorktreeDir)
	}
}

// OperatorPaused/CICDDown (INV-LIFE-2, Task 1.2b) layer Default() ("") ->
// PG_ROUTER_OPERATOR_PAUSED/PG_ROUTER_CICD_DOWN (env) -> [pool].operator_paused_path/
// cicd_down_path (config, repo), filled AFTER the repo-TOML layer. Config is
// the highest priority, mirroring WorktreeDir's own precedence.
func TestLoad_gatePaths_configWinsOverEnv(t *testing.T) {
	t.Setenv("PG_ROUTER_OPERATOR_PAUSED", "/env/operator-paused")
	t.Setenv("PG_ROUTER_CICD_DOWN", "/env/cicd-down")
	writeCfg(t, "[pool]\noperator_paused_path = \"/config/operator-paused\"\ncicd_down_path = \"/config/cicd-down\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OperatorPaused != "/config/operator-paused" {
		t.Errorf("OperatorPaused = %q, want /config/operator-paused ([pool].operator_paused_path must override the env var)", c.OperatorPaused)
	}
	if c.CICDDown != "/config/cicd-down" {
		t.Errorf("CICDDown = %q, want /config/cicd-down ([pool].cicd_down_path must override the env var)", c.CICDDown)
	}
}

// A [pool] table that omits the gate keys must NOT clobber the env values.
func TestLoad_gatePaths_envWhenConfigOmitsKeys(t *testing.T) {
	t.Setenv("PG_ROUTER_OPERATOR_PAUSED", "/env/operator-paused")
	t.Setenv("PG_ROUTER_CICD_DOWN", "/env/cicd-down")
	writeCfg(t, "[pool]\nself_login = \"someone\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OperatorPaused != "/env/operator-paused" {
		t.Errorf("OperatorPaused = %q, want /env/operator-paused (absent [pool].operator_paused_path must not override the env var)", c.OperatorPaused)
	}
	if c.CICDDown != "/env/cicd-down" {
		t.Errorf("CICDDown = %q, want /env/cicd-down (absent [pool].cicd_down_path must not override the env var)", c.CICDDown)
	}
}

// With neither env nor [pool] key set, both gate paths default to
// <LogDir>/gates/{operator-paused,cicd-down} — filled AFTER the repo-TOML layer,
// so PG_ROUTER_LOG_DIR moves them exactly the way it moves LogDir itself.
func TestLoad_gatePaths_defaultUnderLogDir(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_LOG_DIR", "/override/dir")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/override/dir/gates/operator-paused"; c.OperatorPaused != want {
		t.Errorf("OperatorPaused = %q, want %q", c.OperatorPaused, want)
	}
	if want := "/override/dir/gates/cicd-down"; c.CICDDown != want {
		t.Errorf("CICDDown = %q, want %q", c.CICDDown, want)
	}
}

// PG_ROUTER_MAX_WORKER and the other role env vars are dropped (spec C): setting
// them must have NO effect. Per-role capacity is no longer a declarable concept
// at all (bead pg2-f3mcb.2, INV-CONC-1) — there is no `cap` left to be a no-op
// on, so this only locks that the (now zero, docket pg2-oju6w's Task 5.8) role
// set is unaffected — no env var conjures a role into existence.
func TestLoad_roleEnvVarsAreNoOps(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_MAX_WORKER", "3")
	t.Setenv("PG_ROUTER_MAX_FEEDBACK", "5")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 0 {
		t.Errorf("PG_ROUTER_MAX_WORKER must be a no-op; roles = %+v", c.Roles)
	}
}

// Default() carries no opinion on the ring size (0 = "let internal/activity
// pick its own default"); PG_ROUTER_ACTIVITY_RING overlays it (Task 3.4).
func TestDefault_activityRingSizeIsUnset(t *testing.T) {
	if Default().ActivityRingSize != 0 {
		t.Errorf("ActivityRingSize default = %d, want 0 (package-default sentinel)", Default().ActivityRingSize)
	}
}

func TestLoad_activityRingSizeEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_ACTIVITY_RING", "128")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ActivityRingSize != 128 {
		t.Errorf("ActivityRingSize = %d, want 128 (PG_ROUTER_ACTIVITY_RING overlay)", c.ActivityRingSize)
	}
}

// Default() carries no opinion on the metrics listen address (empty means
// disabled — no listener is opened); PG_ROUTER_METRICS_ADDR overlays it
// (design decision D2), the same nil/zero-means-package-default idiom
// ActivityRingSize above already uses.
func TestDefault_metricsAddrIsUnset(t *testing.T) {
	if Default().MetricsAddr != "" {
		t.Errorf("MetricsAddr default = %q, want \"\" (disabled)", Default().MetricsAddr)
	}
}

func TestLoad_metricsAddrEnvOverride(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_METRICS_ADDR", "127.0.0.1:9820")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MetricsAddr != "127.0.0.1:9820" {
		t.Errorf("MetricsAddr = %q, want 127.0.0.1:9820 (PG_ROUTER_METRICS_ADDR overlay)", c.MetricsAddr)
	}
}

func TestLoad_badIntFallsBackToDefault(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_MAX_WAIT", "notanint")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxWait != 1800*time.Second {
		t.Errorf("bad int should fall back to default 1800s, got %v", c.MaxWait)
	}
}

// The budget SCALARS (BudgetTokens/BudgetCost/BudgetTime/Reminder|Cancel|
// HardPct) are checked directly on Config now: Config.WorkerBudget()
// (which converted them into a budget.Budget) had no successor once
// "budget" moved out of pg-router entirely (docket pg2-oju6w's Task 5.2's
// package move) — that conversion's only consumer was
// roles.CCPoolConfig.Budget, itself deleted by Task 5.4. The scalars
// themselves are plain fields, independent of the moved package, so they
// stay.
func TestWorkerBudget_defaults(t *testing.T) {
	c := Default()
	if c.BudgetTokens > 0 || c.BudgetCost > 0 {
		t.Error("token/cost default must be unlimited (<= 0)")
	}
	if c.BudgetTime != 25*time.Minute {
		t.Errorf("time default = %v, want 25m (< MaxWait 30m)", c.BudgetTime)
	}
	if c.ReminderPct != 0.725 || c.CancelPct != 0.90 || c.HardPct != 1.0 {
		t.Errorf("thresholds = reminder=%v cancel=%v hard=%v", c.ReminderPct, c.CancelPct, c.HardPct)
	}
	if c.BudgetTime >= c.MaxWait {
		t.Errorf("budget time %v must be < MaxWait %v", c.BudgetTime, c.MaxWait)
	}
}

func TestWorkerBudget_envOverrides(t *testing.T) {
	absentConfig(t)
	absentGlobalConfig(t) // the XDG-global budget layer sits above env; neutralize a real ~/.config so it can't override these env values
	t.Setenv("PG_ROUTER_BUDGET_TOKENS", "1000000")
	t.Setenv("PG_ROUTER_BUDGET_TIME", "600")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BudgetTokens != 1000000 || c.BudgetTime != 600*time.Second {
		t.Errorf("env overrides not applied: tokens=%d time=%v", c.BudgetTokens, c.BudgetTime)
	}
}

func TestLoad_logDirIsStandardPath(t *testing.T) {
	absentConfig(t)
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.LogDir, "/xdg/state/pg-router"; got != want {
		t.Errorf("LogDir = %q, want %q (standard path, no /log subdir)", got, want)
	}
}

// TestLoad_noFile_zeroRolesAndQueries is the acceptance test for docket
// pg2-oju6w's Task 5.8 (ADR 0065's "Source-side boundary" section, closing
// register row USECASE-CREATE-SOURCE / bead pg2-u7rzl): with no config file
// (config.toml absent), an unconfigured pg-router core now runs with ZERO
// roles and ZERO queries — the former built-in feedback+worker+review
// fallback (roles.BuiltinRoleSet/BuiltinQuerySet) is deleted outright, not
// narrowed. It does nothing until an operator configures [[role]]/[[query]].
func TestLoad_noFile_zeroRolesAndQueries(t *testing.T) {
	absentConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 0 {
		t.Fatalf("no-file must yield zero roles, got: %+v", c.Roles)
	}
	if len(c.Queries) != 0 {
		t.Fatalf("no-file must yield zero queries, got: %+v", c.Queries)
	}
}

func TestLoad_tomlDefinesRoles(t *testing.T) {
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
enabled = true
binds = ["work.ready"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "solo" {
		t.Fatalf("toml [[role]] must decode: %+v", c.Roles)
	}
	if len(c.Roles[0].Binds) != 1 || c.Roles[0].Binds[0] != "work.ready" {
		t.Fatalf("role binds not decoded: %+v", c.Roles[0].Binds)
	}
	if len(c.Queries) != 1 || c.Queries[0].Name != "solo-source" {
		t.Fatalf("[[query]] not decoded: %+v", c.Queries)
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
	if c.BudgetTokens != 750000 || c.BudgetCost != 1200 || c.BudgetTime != 45*time.Minute {
		t.Errorf("global-only budget = tokens=%d cost=%d time=%v, want tokens=750000 cost=1200 time=45m", c.BudgetTokens, c.BudgetCost, c.BudgetTime)
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
	if c.BudgetTokens != 999 {
		t.Errorf("Tokens = %d, want 999 (repo-local overrides global)", c.BudgetTokens)
	}
	// time: repo-local omits it, so the global value survives (global < repo-local,
	// each overlay is field-by-field).
	if c.BudgetTime != 10*time.Minute {
		t.Errorf("Time = %v, want 10m (global time survives when repo-local omits it)", c.BudgetTime)
	}
}

// Either file overrides env (shipped file > env precedence; do NOT flip it).
func TestLoad_globalBudget_overridesEnv(t *testing.T) {
	absentConfig(t)
	t.Setenv("PG_ROUTER_BUDGET_TOKENS", "111")
	t.Setenv("PG_ROUTER_BUDGET_TIME", "120") // 120s
	writeGlobalCfg(t, "[pool.budget]\ntokens = 222\ntime = \"30m\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BudgetTokens != 222 || c.BudgetTime != 30*time.Minute {
		t.Errorf("budget = tokens=%d time=%v, want tokens=222 time=30m (file wins over env)", c.BudgetTokens, c.BudgetTime)
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
	if c.BudgetTokens > 0 || c.BudgetCost > 0 {
		t.Error("absent files: token/cost must stay unlimited (default, <= 0)")
	}
	if c.BudgetTime != 25*time.Minute {
		t.Errorf("absent files: Time = %v, want 25m (default)", c.BudgetTime)
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
	t.Setenv("PG_ROUTER_AUTONOMOUS", "false")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Autonomous {
		t.Error("PG_ROUTER_AUTONOMOUS=false should disable autonomous")
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
	if got, want := LogDir(), "/xdg/state/pg-router"; got != want {
		t.Errorf("LogDir() = %q, want %q", got, want)
	}
	t.Setenv("PG_ROUTER_LOG_DIR", "/override/dir")
	if got, want := LogDir(), "/override/dir"; got != want {
		t.Errorf("LogDir() with PG_ROUTER_LOG_DIR = %q, want %q", got, want)
	}
	// It must not depend on a readable/valid config file: point PG_ROUTER_CONFIG at a
	// deliberately broken one and prove LogDir() still answers while Load() fails.
	bad := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bad, []byte("this is not = valid = toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", bad)
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded on a broken config; the premise of this test is wrong")
	}
	if got, want := LogDir(), "/override/dir"; got != want {
		t.Errorf("LogDir() with a broken config = %q, want %q", got, want)
	}
}

// stubMeterProvider is a distinguishable MeterProvider double: it is not the
// real no-op provider, so a test can prove Config.Meter() returned exactly
// the value the deployment configured (mirrors locator()'s own defaulting
// test posture for CommandLocator).
type stubMeterProvider struct{ noop.MeterProvider }

// Task 3.3 binding decision: the MeterProvider config default is unset =>
// noop.NewMeterProvider(), CHOSEN BY CONFIG, not hardcoded in cmd/pg-router.
func TestConfig_Meter_defaultsToNoop(t *testing.T) {
	var c Config
	mp := c.Meter()
	if mp == nil {
		t.Fatal("Meter() = nil, want the package default (noop) provider")
	}
	// noop.NewMeterProvider()'s Meter() always returns the same embedded no-op
	// meter type regardless of scope name — proving the default is actually the
	// no-op provider, not merely non-nil.
	if _, ok := mp.Meter("any").(noop.Meter); !ok {
		t.Fatalf("Meter() default = %T, want the OTel no-op provider", mp)
	}
}

func TestConfig_Meter_returnsConfiguredProvider(t *testing.T) {
	want := stubMeterProvider{}
	c := Config{MeterProvider: want}
	if got := c.Meter(); got != metric.MeterProvider(want) {
		t.Fatalf("Meter() = %v, want the configured provider %v unchanged", got, want)
	}
}

// GatePaths() must resolve the SAME precedence Load() fills the gate paths
// with — [pool] key > PG_ROUTER_* env > <LogDir>/gates/... — when the config
// file parses cleanly.
func TestGatePaths_agreesWithLoad(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, "[pool]\noperator_paused_path = \"/config/operator-paused\"\n")
	t.Setenv("PG_ROUTER_CICD_DOWN", "/env/cicd-down")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	qp, cd := GatePaths()
	if qp != c.OperatorPaused {
		t.Errorf("GatePaths operatorPaused = %q, Load = %q, want equal", qp, c.OperatorPaused)
	}
	if cd != c.CICDDown {
		t.Errorf("GatePaths cicdDown = %q, Load = %q, want equal", cd, c.CICDDown)
	}
}

// GatePaths() is validation-free: it must resolve even when Load() would
// hard-fail on an absent backing command (INV-WORKFLOW-1 check 5) — the whole
// reason `pause`/`resume` call GatePaths() and never Load() (interfaces.md's
// "Operator pause/resume" MUST: succeed even with no core running, acting on
// gate-file state directly).
func TestGatePaths_worksWhenLoadWouldFail(t *testing.T) {
	writeCfg(t, `
[[role]]
name = "r"
binds = ["e"]

[[query]]
name = "s"
emits = ["e"]
type = "command"
[query.command]
argv = ["absent-lister"]
format = "jsonl"
`)
	if _, err := Load(); err == nil {
		t.Fatal("premise: this config must fail Load() (absent backing command)")
	}
	t.Setenv("PG_ROUTER_LOG_DIR", "/override/dir")
	qp, cd := GatePaths()
	if want := "/override/dir/gates/operator-paused"; qp != want {
		t.Errorf("GatePaths operatorPaused = %q, want %q (must resolve even though Load() fails)", qp, want)
	}
	if want := "/override/dir/gates/cicd-down"; cd != want {
		t.Errorf("GatePaths cicdDown = %q, want %q", cd, want)
	}
}

// GatePaths() must also resolve silently when the config file is malformed
// TOML — Load() hard-errors on that too, but GatePaths() falls back to the
// env/default resolution (mirrors LogDir()'s own "must not depend on a
// readable/valid config file" contract).
func TestGatePaths_worksWhenConfigIsMalformed(t *testing.T) {
	writeCfg(t, "this is = not valid toml [[[")
	if _, err := Load(); err == nil {
		t.Fatal("premise: malformed config must fail Load()")
	}
	t.Setenv("PG_ROUTER_LOG_DIR", "/override/dir")
	qp, cd := GatePaths()
	if want := "/override/dir/gates/operator-paused"; qp != want {
		t.Errorf("GatePaths operatorPaused = %q, want %q", qp, want)
	}
	if want := "/override/dir/gates/cicd-down"; cd != want {
		t.Errorf("GatePaths cicdDown = %q, want %q", cd, want)
	}
}

// PG_ROUTER_LOG_DIR moves both gate defaults, same as LogDir() itself.
func TestGatePaths_respectsLogDirEnv(t *testing.T) {
	absentConfig(t)
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	qp, cd := GatePaths()
	if want := "/xdg/state/pg-router/gates/operator-paused"; qp != want {
		t.Errorf("operatorPaused = %q, want %q", qp, want)
	}
	if want := "/xdg/state/pg-router/gates/cicd-down"; cd != want {
		t.Errorf("cicdDown = %q, want %q", cd, want)
	}
}

// TestDefault_monitorSubsetsIsUnset proves the built-in default (no [[monitor]]
// declared) leaves MonitorSubsets nil, matching every other nil-means-package-
// default field (Locator, MeterProvider, ActivityRingSize's own zero) — an
// unconfigured deployment resolves every mon.read id to no subset, unchanged
// from before pg2-nhvdo.
func TestDefault_monitorSubsetsIsUnset(t *testing.T) {
	if d := Default(); d.MonitorSubsets != nil {
		t.Errorf("MonitorSubsets = %v, want nil", d.MonitorSubsets)
	}
}

// TestLoad_monitorSubsets_decodes is pg2-nhvdo's core acceptance: a [[monitor]]
// array in config.toml populates Config.MonitorSubsets by id -> subset,
// mirroring how [[role]]/[[query]] populate Roles/Queries.
func TestLoad_monitorSubsets_decodes(t *testing.T) {
	writeCfg(t, `
[[monitor]]
id = "mon-1"
subset = ["queue_depth", "unconsumed_expired"]

[[monitor]]
id = "mon-2"
subset = ["source_failures"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MonitorSubsets) != 2 {
		t.Fatalf("MonitorSubsets = %v, want 2 entries", c.MonitorSubsets)
	}
	if got := c.MonitorSubsets["mon-1"]; len(got) != 2 || got[0] != "queue_depth" || got[1] != "unconsumed_expired" {
		t.Errorf("MonitorSubsets[mon-1] = %v, want [queue_depth unconsumed_expired]", got)
	}
	if got := c.MonitorSubsets["mon-2"]; len(got) != 1 || got[0] != "source_failures" {
		t.Errorf("MonitorSubsets[mon-2] = %v, want [source_failures]", got)
	}
	if _, ok := c.MonitorSubsets["unconfigured-id"]; ok {
		t.Errorf("MonitorSubsets must not carry an id nobody declared")
	}
}

// TestLoad_monitorSubsets_appliesWithoutRolesOrQueries proves [[monitor]] is a
// pool-level key like [pool].worktree_dir/operator_paused_path — it applies
// even when the file declares no [[role]]/[[query]] (which, since docket
// pg2-oju6w's Task 5.8 deleted the former built-in role+query fallback,
// leaves Roles/Queries at zero — decodeRoleSet's "pool-only / empty => nil"
// early return). MonitorSubsets must NOT be swallowed by that.
func TestLoad_monitorSubsets_appliesWithoutRolesOrQueries(t *testing.T) {
	writeCfg(t, `
[[monitor]]
id = "mon-1"
subset = ["queue_depth"]
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 0 {
		t.Fatalf("a monitor-only config must still yield zero roles: %+v", c.Roles)
	}
	if got := c.MonitorSubsets["mon-1"]; len(got) != 1 || got[0] != "queue_depth" {
		t.Errorf("MonitorSubsets[mon-1] = %v, want [queue_depth] even with no [[role]]/[[query]]", got)
	}
}

// A [[monitor]] entry with no id is rejected — mirroring buildQueries'/
// buildRole's own required-field checks (name is required).
func TestLoad_monitorSubsets_emptyIdIsError(t *testing.T) {
	writeCfg(t, `
[[monitor]]
subset = ["queue_depth"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("a [[monitor]] entry with no id must error")
	}
}

// Two [[monitor]] entries sharing an id are rejected — mirroring buildRole's/
// buildQueries' own duplicate-name checks.
func TestLoad_monitorSubsets_duplicateIdIsError(t *testing.T) {
	writeCfg(t, `
[[monitor]]
id = "mon-1"
subset = ["queue_depth"]

[[monitor]]
id = "mon-1"
subset = ["source_failures"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("duplicate [[monitor]] id must error")
	}
}

// No [[monitor]] declared at all leaves MonitorSubsets nil, same as Default() —
// a config file that only sets other pool keys must not spuriously initialize
// an empty (non-nil) map.
func TestLoad_monitorSubsets_absentLeavesNil(t *testing.T) {
	writeCfg(t, "[pool]\nself_login = \"someone\"\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MonitorSubsets != nil {
		t.Errorf("MonitorSubsets = %v, want nil when no [[monitor]] is declared", c.MonitorSubsets)
	}
}

// TestLoad_connectorCIHealthRecipeLoads pins MIGRATION.md's "per-connector CI
// health (pg-connector) as a command source" worked example (bead pg2-h410q)
// to an actual Load(): the recipe is documentation, not built-in code (like
// the github-issues/jira-issues examples it sits alongside), so nothing else
// exercises it — a doc that silently rotted as the config surface evolved
// would otherwise go unnoticed until an operator copy-pasted it. This is the
// EXACT TOML MIGRATION.md prints; keep the two in sync.
func TestLoad_connectorCIHealthRecipeLoads(t *testing.T) {
	writeCfg(t, `
[[query]]
name = "connector-ci-github-actions"
emits = ["ci.health"]
type = "command"
[query.command]
argv = [
  "sh", "-c",
  "set -o pipefail; pg-connector ci list 'my-org/my-repo#123' | jq -c --arg provider github-actions '(.runs // [] | map(select(.provider == $provider))) as $runs | (.sources // [] | map(select(.source == $provider))) as $srcs | if ($runs | any(.stale)) or ($srcs | any(.status == \"degraded\")) then error(\"pg-connector ci: \" + $provider + \" is stale or degraded\") else [] end'"
]
format = "json"

[[role]]
name = "ci-health-sink"
type = "command"
enabled = false
binds = ["ci.health"]
[role.command]
argv = ["true"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("the documented connector-ci-health recipe must Load() cleanly: %v", err)
	}
	if len(c.Queries) != 1 || c.Queries[0].Name != "connector-ci-github-actions" {
		t.Fatalf("connector-ci query not decoded: %+v", c.Queries)
	}
	cq, ok := c.Queries[0].Query.(query.CommandQuery)
	if !ok {
		t.Fatalf("connector-ci query decoded as %T, want query.CommandQuery", c.Queries[0].Query)
	}
	if len(cq.Argv) != 3 || cq.Argv[0] != "sh" || cq.Argv[1] != "-c" {
		t.Fatalf("connector-ci argv not decoded as sh -c <pipeline>: %+v", cq.Argv)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "ci-health-sink" || c.Roles[0].Enabled {
		t.Fatalf("ci-health-sink role not decoded as a disabled sink: %+v", c.Roles)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the documented recipe must also pass Validate(): %v", err)
	}
}

// --- pg2-xl659: git-common-dir read-through config resolution + fallback WARN ---

// minimalRoleCfg is the smallest TOML that decodes to one wired role+query
// pair (registry_test.go's own minimal shape), so a Load() from it exercises
// the full decode+Validate path rather than an empty/trivial config.
func minimalRoleCfg(roleName string) string {
	return "[[query]]\n" +
		"name = \"q\"\n" +
		"emits = [\"e\"]\n" +
		"type = \"command\"\n" +
		"[query.command]\n" +
		"argv = [\"x\"]\n" +
		"format = \"jsonl\"\n" +
		"\n" +
		"[[role]]\n" +
		"name = \"" + roleName + "\"\n" +
		"type = \"command\"\n" +
		"binds = [\"e\"]\n" +
		"[role.command]\n" +
		"argv = [\"x\"]\n"
}

// TestLoad_worktreeReadsThroughToCanonicalConfigViaGitCommonDir is
// acceptance criterion (a): a LINKED WORKTREE with no .pg-router/ of its own
// still resolves the CANONICAL clone's .pg-router/config.toml, via `git
// rev-parse --git-common-dir` from the worktree's RepoRoot -- the
// read-through design decision (operator, 2026-09-10), not the
// .pre-commit-config.yaml symlink-in precedent this repo's own CLAUDE.md
// documents for a different problem. Mirrors x/gitclient's own
// TestToplevelAndCommonDirDifferBetweenALinkedWorktreeAndItsCanonicalClone
// anchoring setup.
func TestLoad_worktreeReadsThroughToCanonicalConfigViaGitCommonDir(t *testing.T) {
	absentGlobalConfig(t)
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pg2-xl659-readthrough"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := repo.WriteFile(".pg-router/config.toml", minimalRoleCfg("canonical-role")); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wtPath := filepath.Join(t.TempDir(), "linked-wt")
	if err := repo.Client.CreateWorktree(ctx, wtPath, "feature", gitclient.CreateWorktreeOptions{}); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	// The linked worktree has NO .pg-router/ of its own -- proving a resolution
	// that found the role below did not just read a naive
	// RepoRoot-relative path, which would find nothing here.
	if _, err := os.Stat(filepath.Join(wtPath, ".pg-router")); !os.IsNotExist(err) {
		t.Fatalf(".pg-router must not exist in the linked worktree fixture, stat err=%v", err)
	}

	t.Setenv("PG_ROUTER_REPO_ROOT", wtPath)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() from linked worktree error = %v", err)
	}

	wantPath := filepath.Join(repo.Dir, ".pg-router", "config.toml")
	if c.ConfigPath != wantPath {
		t.Errorf("ConfigPath = %q, want %q (the canonical clone's config, via git-common-dir)", c.ConfigPath, wantPath)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "canonical-role" {
		t.Fatalf("Roles = %+v, want the canonical clone's own [[role]] (\"canonical-role\"), not a built-in fallback", c.Roles)
	}
}

// TestLoad_noFile_logsAtWarnByDefault is acceptance criterion (b): the
// missing-config fallback (config.go's former slog.Info) now logs at WARN,
// naming the resolved path it looked in (design decision's second clause).
func TestLoad_noFile_logsAtWarnByDefault(t *testing.T) {
	absentGlobalConfig(t)
	missing := filepath.Join(t.TempDir(), "absent.toml")
	t.Setenv("PG_ROUTER_CONFIG", missing)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "no pg-router config found") {
		t.Errorf("missing-config fallback did not log at WARN (or above):\n%s", got)
	}
	if !strings.Contains(got, missing) {
		t.Errorf("WARN must name the resolved path it looked in (%s), got:\n%s", missing, got)
	}
}

// TestLoad_noFile_optOutSuppressesWarn is acceptance criterion (c):
// PG_ROUTER_NO_CONFIG_WARN is the explicit opt-out for intentional
// built-in-role usage. It suppresses the WARN -- demoting back to this
// package's pre-pg2-xl659 INFO, not going fully silent.
func TestLoad_noFile_optOutSuppressesWarn(t *testing.T) {
	absentGlobalConfig(t)
	missing := filepath.Join(t.TempDir(), "absent.toml")
	t.Setenv("PG_ROUTER_CONFIG", missing)
	t.Setenv("PG_ROUTER_NO_CONFIG_WARN", "true")

	var warnBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(warnBuf.String(), "no pg-router config found") {
		t.Errorf("PG_ROUTER_NO_CONFIG_WARN=true must suppress the WARN, got:\n%s", warnBuf.String())
	}

	var infoBuf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(infoBuf.String(), "no pg-router config found") {
		t.Errorf("PG_ROUTER_NO_CONFIG_WARN=true must still log at INFO (not go fully silent), got:\n%s", infoBuf.String())
	}
}
