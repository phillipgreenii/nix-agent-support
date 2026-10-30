package lifecycle

import "testing"

func TestLegalPairs(t *testing.T) {
	for _, st := range Stages {
		if got, want := LegalPair(StateOpen, st), st == StageLive; got != want {
			t.Errorf("open+%s = %v", st, got)
		}
		if got, want := LegalPair(StateClosed, st), st != StageLive; got != want {
			t.Errorf("closed+%s = %v", st, got)
		}
	}
	if LegalPair("other", StageQuiet) {
		t.Error("unknown state is legal")
	}
}
