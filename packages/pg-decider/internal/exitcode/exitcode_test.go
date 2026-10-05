package exitcode

import "testing"

func TestCodes(t *testing.T) {
	if OK != 0 || Failure != 1 || Partial != 2 || ViewUnreadable != 3 {
		t.Fatalf("exit codes drifted: OK=%d Failure=%d Partial=%d ViewUnreadable=%d",
			OK, Failure, Partial, ViewUnreadable)
	}
}

func TestExitCallsOsExitWithCode(t *testing.T) {
	orig := osExit
	t.Cleanup(func() { osExit = orig })
	got := -1
	osExit = func(c int) { got = c }
	Exit(Partial)
	if got != Partial {
		t.Fatalf("Exit(Partial) called os.Exit(%d)", got)
	}
}
