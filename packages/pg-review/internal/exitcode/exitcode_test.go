package exitcode

import "testing"

func TestTableValues(t *testing.T) {
	want := map[string]struct{ got, want int }{
		"OK": {OK, 0}, "Failure": {Failure, 1}, "Usage": {Usage, 2}, "Skipped": {Skipped, 3},
		"ValidationRejected": {ValidationRejected, 4}, "Busy": {Busy, 5}, "Ledger": {Ledger, 6},
		"Provider": {Provider, 7}, "Reserved": {Reserved, 8}, "Unused": {Unused, 9},
	}
	for name, c := range want {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", name, c.got, c.want)
		}
	}
}
