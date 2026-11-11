package settle

import "testing"

// The caps 5 (per run) and 7 (per hour) are distinct, so a swapped argument
// order changes a verdict; every probe is the cap minus one, the cap, or the
// cap plus one. Both caps MUST be at least 1 for "one under" to exist.
func TestRemovalCap(t *testing.T) {
	const runCap, hourCap = 5, 7
	tests := []struct {
		name           string
		run, hour      int
		want           string
		wantHourCalled bool
	}{
		{"both one under", runCap - 1, hourCap - 1, "", true},
		{"run at cap", runCap, 0, BlockRunCap, false},
		{"run over cap", runCap + 1, 0, BlockRunCap, false},
		{"hour at cap", 0, hourCap, BlockHourCap, true},
		{"hour over cap", 0, hourCap + 1, BlockHourCap, true},
		{"run cap wins when both are reached", runCap, hourCap, BlockRunCap, false},
		{"hour one under with run one under", runCap - 1, hourCap - 1, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got := removalCap(tc.run, runCap, func() int { called = true; return tc.hour }, hourCap)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if called != tc.wantHourCalled {
				t.Fatalf("hourCount called = %v, want %v (it must be read lazily)", called, tc.wantHourCalled)
			}
		})
	}
}
