package appcode

import (
	"testing"

	"github.com/phillipgreenii/x/exitcode"
)

// TestValuesPinned fails on any renumbering: the router adapter and the docs
// branch on these numbers, so a change must be a visible diff.
func TestValuesPinned(t *testing.T) {
	for name, c := range map[string]struct{ got, want int }{
		"Skipped":    {Skipped, 20},
		"Validation": {Validation, 21},
		"Ledger":     {Ledger, 22},
		"Provider":   {Provider, 23},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", name, c.got, c.want)
		}
		if !exitcode.IsAppSpecific(c.got) {
			t.Errorf("%s = %d is outside the app range", name, c.got)
		}
	}
}
