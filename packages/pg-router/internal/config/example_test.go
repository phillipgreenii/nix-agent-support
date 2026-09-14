package config

import (
	"regexp"
	"strings"
	"testing"
)

// TestExampleTOML_roundTrips guarantees the generated example config actually
// loads and decodes the one illustrative [[query]]/[[role]] pair it prints —
// so 'config --print-defaults' output is always a valid, copy-pasteable
// starting point. As of docket pg2-oju6w's Task 5.8, ExampleTOML() no longer
// mirrors a built-in default set (there is none) — it just needs to be a
// loadable, self-consistent example.
func TestExampleTOML_roundTrips(t *testing.T) {
	writeCfg(t, ExampleTOML())
	c, err := Load()
	if err != nil {
		t.Fatalf("example config must load: %v\n---\n%s", err, ExampleTOML())
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "example-role" {
		t.Fatalf("example must decode its one illustrative role: %+v", c.Roles)
	}
	r := c.Roles[0]
	if !r.Enabled || len(r.Binds) != 1 || r.Binds[0] != "example.ready" {
		t.Fatalf("example role did not round-trip: %+v", r)
	}
	if len(c.Queries) != 1 || c.Queries[0].Name != "example-source" {
		t.Fatalf("example must decode its one illustrative query: %+v", c.Queries)
	}
	if got := c.Queries[0].Query.Emits(); len(got) != 1 || got[0] != "example.ready" {
		t.Fatalf("example query did not round-trip its emits: %+v", got)
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
		"# operator_paused_path =",
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
