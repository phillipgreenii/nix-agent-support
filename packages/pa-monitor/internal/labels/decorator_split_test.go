package labels

import (
	"reflect"
	"testing"
)

// TestSplitArgs covers the shell-words splitter that lets a [[decorator]]
// command carry flags (bead pg2-r1f1j.10). It is sandbox-safe (no exec), so it
// runs under the default `go test ./...` gate.
func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t ", nil},
		{"single token", "/nix/store/x/bin/tool", []string{"/nix/store/x/bin/tool"}},
		{"tool with flags", "/nix/store/x/bin/tool -rule x", []string{"/nix/store/x/bin/tool", "-rule", "x"}},
		{"collapses runs of whitespace", "a   b\t c", []string{"a", "b", "c"}},
		{"single quotes group spaces", "tool -m 'a b c'", []string{"tool", "-m", "a b c"}},
		{"double quotes group spaces", `tool -m "a b c"`, []string{"tool", "-m", "a b c"}},
		{"double-quote backslash escape", `tool "a\"b"`, []string{"tool", `a"b`}},
		{"backslash escapes space", `tool a\ b`, []string{"tool", "a b"}},
		{"empty quoted string is a token", `tool ""`, []string{"tool", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := splitArgs(c.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("splitArgs(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestSplitArgs_Errors(t *testing.T) {
	for _, in := range []string{`tool "unterminated`, `tool 'unterminated`, `tool trailing\`} {
		if _, err := splitArgs(in); err == nil {
			t.Errorf("splitArgs(%q): expected error, got nil", in)
		}
	}
}

// TestNewDecorator_SplitsCommandArgs verifies a command with flags is split so
// argv[0] is the binary and the rest are args passed through to exec.
func TestNewDecorator_SplitsCommandArgs(t *testing.T) {
	d, err := NewDecorator(DecoratorConfig{
		Name:    "with-args",
		Command: "/nix/store/abc/bin/decorator -rule scope 'two words'",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/nix/store/abc/bin/decorator", "-rule", "scope", "two words"}
	if !reflect.DeepEqual(d.argv, want) {
		t.Errorf("argv = %#v, want %#v", d.argv, want)
	}
}

// TestNewDecorator_ForwardsConfigEnv verifies config-provided env vars are
// merged onto the minimal base env so a generic decorator gets its config
// without a writeShellScriptBin wrapper.
func TestNewDecorator_ForwardsConfigEnv(t *testing.T) {
	d, err := NewDecorator(DecoratorConfig{
		Name:    "with-env",
		Command: "/nix/store/abc/bin/decorator",
		Env:     map[string]string{"PA_MONITOR_SCOPE_RULES": "zr-rules"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Fixed base env first, then additional config keys sorted.
	want := []string{
		"PA_MONITOR_DECORATE=1",
		"PATH=/usr/bin:/bin",
		"PA_MONITOR_SCOPE_RULES=zr-rules",
	}
	if !reflect.DeepEqual(d.env, want) {
		t.Errorf("env = %#v, want %#v", d.env, want)
	}
}

// TestNewDecorator_ConfigEnvOverridesBase confirms a config env entry wins over
// the base value for the same key (merge is config-last).
func TestNewDecorator_ConfigEnvOverridesBase(t *testing.T) {
	d, err := NewDecorator(DecoratorConfig{
		Name:    "override-path",
		Command: "/nix/store/abc/bin/decorator",
		Env:     map[string]string{"PATH": "/custom/bin"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"PA_MONITOR_DECORATE=1", "PATH=/custom/bin"}
	if !reflect.DeepEqual(d.env, want) {
		t.Errorf("env = %#v, want %#v", d.env, want)
	}
}

// TestNewDecorator_NoArgsNoEnvUnchanged is the backward-compat guard: an
// existing single-path decorator with no args and no env must produce exactly
// the argv and stripped env the runner used before this change.
func TestNewDecorator_NoArgsNoEnvUnchanged(t *testing.T) {
	d, err := NewDecorator(DecoratorConfig{
		Name:    "legacy",
		Command: "/nix/store/abc/bin/decorator",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"/nix/store/abc/bin/decorator"}; !reflect.DeepEqual(d.argv, want) {
		t.Errorf("argv = %#v, want %#v", d.argv, want)
	}
	if want := []string{"PA_MONITOR_DECORATE=1", "PATH=/usr/bin:/bin"}; !reflect.DeepEqual(d.env, want) {
		t.Errorf("env = %#v, want %#v", d.env, want)
	}
}

// TestNewDecorator_RejectsUnbalancedQuotes confirms a malformed command surfaces
// as a construction error (not a silent single-token path).
func TestNewDecorator_RejectsUnbalancedQuotes(t *testing.T) {
	if _, err := NewDecorator(DecoratorConfig{Name: "bad", Command: `/nix/store/x/bin/t "oops`}); err == nil {
		t.Fatal("expected error for unbalanced quotes")
	}
}

// TestNewDecorator_ValidatesBinaryAfterSplit confirms the /nix/store/ boundary
// is enforced against the split-out binary (argv[0]), so flags cannot smuggle a
// non-store path past validation.
func TestNewDecorator_ValidatesBinaryAfterSplit(t *testing.T) {
	if _, err := NewDecorator(DecoratorConfig{Name: "bad", Command: "/tmp/evil -rule x"}); err == nil {
		t.Fatal("expected rejection of non-/nix/store binary with args")
	}
}
