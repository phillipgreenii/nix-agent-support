package config

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/roles"
)

// max_dispatch_retries (bead pg2-yu5y2) is a per-role opt-in: absent means 0
// (no re-run), a value within the cap rides through to roles.Role.
func TestLoad_maxDispatchRetries(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "src"
type = "timer"
emits = ["pr.changed", "e", "f"]

[[role]]
name = "desk-pr"
binds = ["pr.changed"]
max_dispatch_retries = 2

[[role]]
name = "plain"
binds = ["e"]

[[role]]
name = "at-cap"
binds = ["f"]
max_dispatch_retries = 3
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i, want := range []int{2, 0, roles.MaxDispatchRetriesCap} {
		if got := c.Roles[i].MaxDispatchRetries; got != want {
			t.Errorf("role %q MaxDispatchRetries = %d, want %d", c.Roles[i].Name, got, want)
		}
	}
}

// A value above the hard cap, or negative, is rejected at load rather than
// silently clamped, so an operator never believes a larger retry budget is in
// force.
func TestLoad_maxDispatchRetriesOutOfRangeRejected(t *testing.T) {
	for _, v := range []string{"4", "100", "-1"} {
		t.Run(v, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, `
[[query]]
name = "src"
type = "timer"
emits = ["pr.changed"]

[[role]]
name = "desk-pr"
binds = ["pr.changed"]
max_dispatch_retries = `+v+`
`)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "max_dispatch_retries") || strings.Contains(err.Error(), "orphan") {
				t.Fatalf("Load() error = %v, want a max_dispatch_retries range error", err)
			}
		})
	}
}
