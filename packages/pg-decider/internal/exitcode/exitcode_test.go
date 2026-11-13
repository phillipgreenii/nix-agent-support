package exitcode

import "testing"

func TestCodes(t *testing.T) {
	want := map[string][2]int{
		"OK": {OK, 0}, "Failure": {Failure, 1}, "Usage": {Usage, 2}, "ViewUnreadable": {ViewUnreadable, 3},
		"Partial": {Partial, 5}, "LookupUntrusted": {LookupUntrusted, 20}, "PartialPermanent": {PartialPermanent, 21},
	}
	for name, c := range want {
		if c[0] != c[1] {
			t.Errorf("exit code %s drifted: got %d, want %d", name, c[0], c[1])
		}
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
