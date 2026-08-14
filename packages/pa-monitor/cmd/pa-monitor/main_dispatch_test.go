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
		// Generic leading-flag routing, NOT an endorsement of any particular
		// flag: a first arg starting with "-" (other than -h/--help) goes to
		// tui because no TUI flag collides with a subcommand name. The example
		// MUST stay a flag runTUI actually defines -- it was `--wait-until-idle`
		// until bead pg2-3fv9l, which read as blessing a route for a flag ADR
		// 0011 had already REMOVED in favour of the `wait-until-agents-finished`
		// subcommand (and which runTUI's flag set rejects with exit 2).
		{"flag-first preserves tui", []string{"pa-monitor", "--version"}, "tui", []string{"--version"}},
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
