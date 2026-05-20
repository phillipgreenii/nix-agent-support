package main

import "testing"

func TestPickSubcommand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCmd  string
		wantRest []string
	}{
		{"no args", []string{"claude-agents-tui"}, "tui", nil},
		{"daemon", []string{"claude-agents-tui", "daemon"}, "daemon", []string{}},
		{"daemon with flag", []string{"claude-agents-tui", "daemon", "--socket=/tmp/x"}, "daemon", []string{"--socket=/tmp/x"}},
		{"flag-first preserves tui", []string{"claude-agents-tui", "--wait-until-idle"}, "tui", []string{"--wait-until-idle"}},
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
