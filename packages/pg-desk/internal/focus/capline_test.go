package focus

import "testing"

func TestCapLineTextWithFinishedRows(t *testing.T) {
	cases := []struct {
		name string
		in   CapLine
		want string
	}{
		{"finished clause", newCapLine(6, 4, 2), "cap 6: 4 in plan, 2 open slots; 2 finished, not counted"},
		{"finished clause omitted", newCapLine(6, 4, 0), "cap 6: 4 in plan, 2 open slots"},
		{"full", newCapLine(3, 3, 0), "cap 3: 3 in plan, 0 open slots"},
		{"over cap never negative", newCapLine(2, 5, 0), "cap 2: 5 in plan, 0 open slots"},
		{"over cap with finished", newCapLine(2, 5, 1), "cap 2: 5 in plan, 0 open slots; 1 finished, not counted"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String() = %q, want %q", c.name, got, c.want)
		}
	}
	if got := newCapLine(2, 5, 0).OpenSlots; got != 0 {
		t.Errorf("OpenSlots = %d, want 0 (never negative)", got)
	}
}
