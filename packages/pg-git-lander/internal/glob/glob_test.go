package glob_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/glob"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"*.lock", "a.lock", true},
		{"*.lock", "d/a.lock", false}, // * stays within one segment
		{"**/*.lock", "d/e/a.lock", true},
		{"**/*.lock", "a.lock", true},
		{"packages/*/go.sum", "packages/x/go.sum", true},
		{"packages/*/go.sum", "packages/x/y/go.sum", false},
		{"packages/**", "packages/x/y/go.sum", true},
		{"packages/**/go.sum", "packages/go.sum", true},
		{"go.sum", "go.sum", true},
		{"go.sum", "x/go.sum", false},
		{"a/**/b/**/c", "a/q/b/r/s/c", true},
		{"a/**/b/**/c", "a/q/c", false},
	}
	for _, c := range cases {
		if got := glob.Match(c.glob, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}
