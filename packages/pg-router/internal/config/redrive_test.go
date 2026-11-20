package config

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/roles"
)

const redriveRole = `
[[query]]
name = "src"
type = "timer"
emits = ["pr.changed", "e"]

[[role]]
name = "desk-pr"
handler = "/bin/h"
binds = ["pr.changed"]
%s
[[role]]
name = "plain"
handler = "/bin/h"
binds = ["e"]
`

func TestLoad_maxRedrives(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, strings.Replace(redriveRole, "%s", "max_redrives = 2\nredrive_delay = \"30m\"", 1))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r := c.Roles[0]; r.MaxRedrives != 2 || r.RedriveDelay != 30*time.Minute {
		t.Errorf("desk-pr = %d / %v, want 2 / 30m", r.MaxRedrives, r.RedriveDelay)
	}
	if r := c.Roles[1]; r.MaxRedrives != 0 || r.RedriveDelay != roles.DefaultRedriveDelay {
		t.Errorf("plain = %d / %v, want 0 / 1h (off by default)", r.MaxRedrives, r.RedriveDelay)
	}
}

// Above the hard cap of 2, or negative, is rejected rather than clamped; so is a
// non-positive delay.
func TestLoad_maxRedrivesOutOfRangeRejected(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"max_redrives = 3", "max_redrives"},
		{"max_redrives = 100", "max_redrives"},
		{"max_redrives = -1", "max_redrives"},
		{"max_redrives = 1\nredrive_delay = \"0s\"", "redrive_delay"},
		{"max_redrives = 1\nredrive_delay = \"-1h\"", "redrive_delay"},
	} {
		t.Run(c.body, func(t *testing.T) {
			absentGlobalConfig(t)
			writeCfg(t, strings.Replace(redriveRole, "%s", c.body, 1))
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load() error = %v, want one naming %s", err, c.want)
			}
		})
	}
}
