package cirollup

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestClassify(t *testing.T) {
	excl := NewExcluder([]string{"^policy-bot"})
	cases := []struct {
		name string
		run  api.CIRun
		want Disposition
	}{
		{"success", api.CIRun{Status: "completed", Conclusion: "success"}, Passed},
		{"skipped is passed", api.CIRun{Status: "completed", Conclusion: "skipped"}, Passed},
		{"neutral is passed", api.CIRun{Status: "completed", Conclusion: "neutral"}, Passed},
		{"failure", api.CIRun{Status: "completed", Conclusion: "failure"}, Failed},
		{"error is failed", api.CIRun{Status: "completed", Conclusion: "error"}, Failed},
		{"cancelled is failed", api.CIRun{Status: "completed", Conclusion: "cancelled"}, Failed},
		{"timed_out is failed", api.CIRun{Status: "completed", Conclusion: "timed_out"}, Failed},
		{"in_progress is pending", api.CIRun{Status: "in_progress", Conclusion: ""}, Pending},
		{"queued is pending", api.CIRun{Status: "queued", Conclusion: ""}, Pending},
		{"empty conclusion is pending", api.CIRun{Status: "completed", Conclusion: ""}, Pending},
		// StatusContext hardcodes Status="completed"; in-flight commit-status states must be Pending.
		{"statuscontext pending", api.CIRun{Status: "completed", Conclusion: "pending"}, Pending},
		{"statuscontext expected", api.CIRun{Status: "completed", Conclusion: "expected"}, Pending},
		// Excluded short-circuits in ALL states.
		{"policy-bot failure excluded", api.CIRun{Name: "policy-bot: approval required (click for details): main", Status: "completed", Conclusion: "failure"}, Excluded},
		{"policy-bot success excluded", api.CIRun{Name: "policy-bot: ok", Status: "completed", Conclusion: "success"}, Excluded},
		{"policy-bot pending excluded", api.CIRun{Name: "policy-bot: x", Status: "completed", Conclusion: "pending"}, Excluded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.run, excl); got != tc.want {
				t.Errorf("Classify(%+v) = %v, want %v", tc.run, got, tc.want)
			}
		})
	}
}

func TestClassifyNilExcluder(t *testing.T) {
	if got := Classify(api.CIRun{Name: "policy-bot", Status: "completed", Conclusion: "failure"}, nil); got != Failed {
		t.Errorf("nil excluder: got %v want Failed", got)
	}
}

func TestCompute(t *testing.T) {
	excl := NewExcluder([]string{"^policy-bot"})
	cases := []struct {
		name                             string
		runs                             []api.CIRun
		wantState                        string
		wantPassed, wantFailed, wantPend int
	}{
		{"empty is none", nil, "none", 0, 0, 0},
		{"all success", []api.CIRun{{Status: "completed", Conclusion: "success"}, {Status: "completed", Conclusion: "success"}}, "success", 2, 0, 0},
		{"any failure wins", []api.CIRun{{Status: "completed", Conclusion: "success"}, {Status: "completed", Conclusion: "failure"}}, "failure", 1, 1, 0},
		{"pending when no failure", []api.CIRun{{Status: "completed", Conclusion: "success"}, {Status: "in_progress"}}, "pending", 1, 0, 1},
		// The core bead case: real checks pass, policy-bot "fails" → excluded → success.
		{"policy-bot only failure excluded → success", []api.CIRun{
			{Name: "build", Status: "completed", Conclusion: "success"},
			{Name: "policy-bot: approval required (click for details): main", Status: "completed", Conclusion: "failure"},
		}, "success", 1, 0, 0},
		// A PR whose ONLY check is an excluded one rolls up to none.
		{"only excluded → none", []api.CIRun{
			{Name: "policy-bot: x", Status: "completed", Conclusion: "failure"},
		}, "none", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.runs, excl)
			if got.State != tc.wantState || got.Passed != tc.wantPassed || got.Failed != tc.wantFailed || got.Pending != tc.wantPend {
				t.Errorf("Compute = %+v, want state=%q p=%d f=%d pend=%d", got, tc.wantState, tc.wantPassed, tc.wantFailed, tc.wantPend)
			}
		})
	}
}

func TestNewExcluderSkipsInvalid(t *testing.T) {
	// An invalid regex is skipped (warn-and-skip), not fatal; valid ones still match.
	excl := NewExcluder([]string{"(unclosed", "^policy-bot"})
	if !excl.Match("policy-bot: x") {
		t.Errorf("valid pattern should still match after invalid one skipped")
	}
}
