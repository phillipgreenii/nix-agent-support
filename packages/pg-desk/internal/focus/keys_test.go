package focus

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

func keyCfg() *config.Config {
	return &config.Config{
		Repos:          []config.RepoConfig{{Remote: testRepo}},
		BeadIDPattern:  `^bd-`,
		TicketPatterns: []string{`[A-Z][A-Z0-9]+-[0-9]+`},
	}
}

func prK(n string) Key { return Key{Type: "pr", ID: testRepo + "#" + n} }

func TestParseKeyBareIntegerIsAPRKey(t *testing.T) {
	got, err := ParseKey("42", keyCfg())
	if err != nil {
		t.Fatal(err)
	}
	if got != prK("42") {
		t.Fatalf("ParseKey(42) = %v, want %v", got, prK("42"))
	}
}

func TestParseKeyForms(t *testing.T) {
	cases := []struct {
		raw  string
		want Key
	}{
		{"42", prK("42")},
		{" 42 ", prK("42")},
		{"007", prK("7")},
		{"#42", prK("42")},
		{"acme/widgets#42", prK("42")},
		{"ACME/Widgets#42", prK("42")},
		{"https://example.test/acme/widgets/pull/42", prK("42")},
		{"https://example.test/acme/widgets/pull/42/", prK("42")},
		{"bd-abc1", Key{"issue", "bd-abc1"}},
		{"PROJ-5", Key{"issue", "PROJ-5"}},
		{"proj-5", Key{"issue", "PROJ-5"}},
	}
	for _, tc := range cases {
		got, err := ParseKey(tc.raw, keyCfg())
		if err != nil {
			t.Errorf("ParseKey(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseKey(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseKeyErrors(t *testing.T) {
	cases := []struct {
		name, raw string
		cfg       *config.Config
		msg       string
	}{
		{"empty", "", keyCfg(), "empty key"},
		{"blank", "   ", keyCfg(), "empty key"},
		{"zero", "0", keyCfg(), "not a positive PR number"},
		{"zero hash", "#0", keyCfg(), "not a positive PR number"},
		{"another repository", "other/repo#4", keyCfg(), "only configured repository"},
		{"another repository URL", "https://example.test/other/repo/pull/4", keyCfg(), "only configured repository"},
		{"word", "hello", keyCfg(), "cannot resolve key"},
		{"signed number", "+4", keyCfg(), "cannot resolve key"},
		{"no config", "42", nil, "no repository is configured"},
		{"no repo", "42", &config.Config{}, "no repository is configured"},
		{"blank remote", "42", &config.Config{Repos: []config.RepoConfig{{Remote: " "}}}, "no repository is configured"},
		{"tracker key without config", "PROJ-5", nil, "cannot resolve key"},
		{"tracker key without pattern", "PROJ-5", &config.Config{Repos: []config.RepoConfig{{Remote: testRepo}}}, "cannot resolve key"},
		{"number too large", "99999999999999999999999", keyCfg(), "not a positive PR number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseKey(tc.raw, tc.cfg)
			if err == nil {
				t.Fatalf("ParseKey(%q) = %v, want an error", tc.raw, got)
			}
			if !errors.Is(err, ErrUsage) {
				t.Errorf("error %v is not ErrUsage", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q lacks %q", err, tc.msg)
			}
			if got != (Key{}) {
				t.Errorf("an error returned key %v", got)
			}
		})
	}
}

// The unanchored bead pattern and the anchored ticket shape both resolve; a
// ticket key the pattern only matches inside a longer text does not.
func TestParseKeyTicketShapeIsAnchored(t *testing.T) {
	if _, err := ParseKey("see PROJ-5 now", keyCfg()); err == nil {
		t.Fatal("a sentence containing a ticket key must not resolve")
	}
}
