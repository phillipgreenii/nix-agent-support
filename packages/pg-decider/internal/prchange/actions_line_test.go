package prchange

import (
	"context"
	"strings"
	"testing"
)

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// TestApplyReportsDecidedActionsOnTheLastStdoutLine is the regression guard for
// bead pg2-4ye8pn: pg-router's command role reads the actions a run decided from
// the last stdout line, so an apply that wrote work must end its stdout with one
// {"actions":[...]} object listing each planned action by kind, and a run that
// decided nothing, or any plan-mode run, must not print one.
func TestApplyReportsDecidedActionsOnTheLastStdoutLine(t *testing.T) {
	cases := []struct {
		name string
		mode string
		plan string
		want string // the exact last line, or "" for no actions line
	}{
		{
			"apply with work", ModeApply, planWithWork,
			`{"actions":[{"verb":"anchor"},{"verb":"review-pr"},{"verb":"annotation"}]}`,
		},
		{"apply with nothing to do", ModeApply, planEmpty, ""},
		{"plan decides nothing", ModePlan, planWithWork, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := (&script{}).
				on("pg-desk pr refresh", ok("refreshed pr acme/api#7: no change (version 3)\n")).
				on("pg-desk pr show", ok(hydratedView(3, false, ""))).
				on("pg-decider plan pr acme/api#7 --json", ok(c.plan)).
				on("pg-decider apply pr acme/api#7", ok("applied\n")).
				on("pg-decider plan pr acme/api#7", ok("the plan\n"))
			o, out, errb := opts(t, c.mode, s)
			if got := Run(context.Background(), o); got != ExitOK {
				t.Fatalf("exit %d: %s", got, errb)
			}
			got := lastLine(out.String())
			if c.want == "" {
				if strings.Contains(out.String(), `"actions"`) {
					t.Fatalf("unexpected actions line in stdout: %q", out)
				}
				return
			}
			if got != c.want {
				t.Fatalf("last stdout line = %q, want %q", got, c.want)
			}
		})
	}
}
