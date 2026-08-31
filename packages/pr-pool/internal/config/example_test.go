package config

import (
	"regexp"
	"strings"
	"testing"
)

// TestExampleTOML_roundTrips guarantees the generated example config actually loads
// and reproduces the built-in feedback + worker + review roles — so 'config
// --print-defaults' output is always a valid, copy-pasteable starting point.
func TestExampleTOML_roundTrips(t *testing.T) {
	writeCfg(t, ExampleTOML())
	c, err := Load()
	if err != nil {
		t.Fatalf("example config must load: %v\n---\n%s", err, ExampleTOML())
	}
	if len(c.Roles) != 3 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" || c.Roles[2].Name != "review" {
		t.Fatalf("example must reproduce built-in feedback+worker+review: %+v", c.Roles)
	}
	// The worker's authorship guard and completion mode must survive the round-trip.
	if c.Roles[1].CCPool == nil || !c.Roles[1].CCPool.AuthorshipGuard || c.Roles[1].CCPool.Completion != "close-or-handback" {
		t.Fatalf("worker ccpool config did not round-trip: %+v", c.Roles[1].CCPool)
	}
	// The review role reviews teammate PRs too, so its authorship guard MUST be
	// off (a guard asserting "author is me + my branch" would block team reviews).
	rv := c.Roles[2].CCPool
	if rv == nil || rv.AuthorshipGuard || rv.Completion != "close-or-handback" {
		t.Fatalf("review ccpool config did not round-trip (authorship_guard must be false): %+v", rv)
	}
	// The feedback role carries budget.Budget{} (fully unlimited => NO watchdog) in
	// roles.BuiltinRoleSet. Without an emitted [role.ccpool.budget] table, buildCCPool
	// seeds it from the pool default (Time=25m) and the example reload silently adds a
	// 25m watchdog. Assert the exact "fully unlimited" triple executor.budgetUnlimited
	// checks: Tokens<=0 && Cost<=0 && Time<=0.
	fb := c.Roles[0].CCPool
	if fb == nil {
		t.Fatalf("feedback ccpool config missing after round-trip: %+v", c.Roles[0])
	}
	if !fb.Budget.Tokens.Unlimited() || !fb.Budget.Cost.Unlimited() || fb.Budget.Time > 0 {
		t.Fatalf("feedback budget did not round-trip to unlimited (got Tokens=%d Cost=%d Time=%v); "+
			"print-defaults reload added a watchdog", int64(fb.Budget.Tokens), int64(fb.Budget.Cost), fb.Budget.Time)
	}
}

// TestExampleTOML_gateKeysDocumented is Task 1.2b's Step 1(b): the gate keys
// must be present as a commented-out, absolute-path example (not a bracketed
// placeholder — those default paths vary per environment/XDG_STATE_HOME, so a
// literal <LogDir> value would be actively wrong if pasted in live), with
// prose pointing at `config --show` for the real resolved paths.
func TestExampleTOML_gateKeysDocumented(t *testing.T) {
	out := ExampleTOML()
	for _, want := range []string{
		"config --show",
		"# quota_paused_path =",
		"# cicd_down_path =",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ExampleTOML() missing %q in:\n%s", want, out)
		}
	}
}

// tomlKeyLine matches a single-line `key = value` TOML assignment (a bare or
// quoted key immediately followed by '='). It deliberately does NOT match a
// continuation line inside a multi-line triple-quoted string (e.g. a ccpool
// role's prompt body, which is free-form prose and legitimately contains
// '<', '=', and anything else) — only the FIRST line of such an assignment
// starts with the key itself.
var tomlKeyLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)

// TestExampleTOML_noPlaceholderBracketsInValues is Task 1.2b's Step 1(b): no
// line TOML would parse as the FIRST line of a key=value assignment (i.e. not
// a '#'-prefixed comment, and not prose inside a multi-line string value) may
// contain '<' — a placeholder token like <LogDir> is fine in PROSE (the
// pre-existing "<RepoRoot>" in the header is a comment; a role's prompt body
// is free text), but would be actively wrong if it ever appeared as a live,
// uncommented scalar value an operator could copy-paste without editing.
func TestExampleTOML_noPlaceholderBracketsInValues(t *testing.T) {
	for _, line := range strings.Split(ExampleTOML(), "\n") {
		trimmed := strings.TrimSpace(line)
		if !tomlKeyLine.MatchString(trimmed) {
			continue
		}
		if strings.Contains(trimmed, "<") {
			t.Errorf("uncommented TOML key=value line contains a placeholder bracket: %q", line)
		}
	}
}
