package main

import "testing"

func TestPickSubcommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCmd  string
		wantRest []string
	}{
		{"no args", []string{"pa-monitor"}, "tui", nil},
		{"daemon", []string{"pa-monitor", "daemon"}, "daemon", []string{}},
		{"daemon with flag", []string{"pa-monitor", "daemon", "--socket=/tmp/x"}, "daemon", []string{"--socket=/tmp/x"}},
		{"status", []string{"pa-monitor", "status"}, "status", []string{}},
		{"agents-busy-check", []string{"pa-monitor", "agents-busy-check"}, "agents-busy-check", []string{}},
		{"agents-busy-check with flag", []string{"pa-monitor", "agents-busy-check", "--consider-daemon-down-as-busy"}, "agents-busy-check", []string{"--consider-daemon-down-as-busy"}},
		{"flag-first preserves tui", []string{"pa-monitor", "--wait-until-idle"}, "tui", []string{"--wait-until-idle"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCmd, gotRest := pickSubcommand(c.args)
			if gotCmd != c.wantCmd {
				t.Errorf("cmd: got %q, want %q", gotCmd, c.wantCmd)
			}
			if len(gotRest) != len(c.wantRest) {
				t.Fatalf("rest len: got %d, want %d", len(gotRest), len(c.wantRest))
			}
			for i := range gotRest {
				if gotRest[i] != c.wantRest[i] {
					t.Errorf("rest[%d]: got %q, want %q", i, gotRest[i], c.wantRest[i])
				}
			}
		})
	}
}
