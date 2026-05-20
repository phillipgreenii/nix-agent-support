package proto

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestSessionEnvRoundTrip confirms that the known env-key subset
// (CMUX_WORKSPACE_ID, TMUX, GC_RIG, GC_AGENT, WORKSPACE) survives
// FromTree → ToTree without loss. Arbitrary other keys must NOT be
// exposed on the wire — verifies the privacy guard.
func TestSessionEnvRoundTrip(t *testing.T) {
	in := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Sessions: []*aggregate.SessionView{
					{
						Session: &session.Session{
							SessionID: "s1",
							PID:       42,
							Env: map[string]string{
								"CMUX_WORKSPACE_ID": "ws-7",
								"TMUX":              "/tmp/tmux-501/default,1,0",
								"GC_RIG":            "beads",
								"GC_AGENT":          "polecat",
								"WORKSPACE":         "ws-1",
								"AWS_SECRET":        "should-not-leak",
								"OAUTH_TOKEN":       "still-not-leaking",
							},
						},
					},
				},
			},
		},
	}
	wire := FromTree(in)
	out := ToTree(wire)

	if len(out.Dirs) != 1 || len(out.Dirs[0].Sessions) != 1 {
		t.Fatal("shape lost")
	}
	got := out.Dirs[0].Sessions[0].Env
	if got["CMUX_WORKSPACE_ID"] != "ws-7" {
		t.Errorf("CMUX_WORKSPACE_ID lost: %+v", got)
	}
	if got["TMUX"] != "/tmp/tmux-501/default,1,0" {
		t.Errorf("TMUX lost: %+v", got)
	}
	if got["GC_RIG"] != "beads" {
		t.Errorf("GC_RIG lost: %+v", got)
	}
	if got["GC_AGENT"] != "polecat" {
		t.Errorf("GC_AGENT lost: %+v", got)
	}
	if got["WORKSPACE"] != "ws-1" {
		t.Errorf("WORKSPACE lost: %+v", got)
	}
	// Privacy guard: arbitrary keys must NOT round-trip.
	if _, ok := got["AWS_SECRET"]; ok {
		t.Errorf("AWS_SECRET leaked across the wire: %+v", got)
	}
	if _, ok := got["OAUTH_TOKEN"]; ok {
		t.Errorf("OAUTH_TOKEN leaked across the wire: %+v", got)
	}
}
