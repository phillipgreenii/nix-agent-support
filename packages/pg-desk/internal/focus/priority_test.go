package focus

import "testing"

func TestPriorityResolve(t *testing.T) {
	table := newPriorityTable(map[string]string{"Highest": "P0", "High": "P1", "Medium": "P2", "Low": "P3", "Lowest": "P4", "Odd": "P9"})
	cases := []struct {
		raw      string
		rank     int
		present  bool
		unmapped bool
	}{
		{"", 0, false, false},
		{"   ", 0, false, false},
		{"Highest", 0, true, false},
		{"high", 1, true, false},
		{" MEDIUM ", 2, true, false},
		{"Low", 3, true, false},
		{"lowest", 4, true, false},
		{"P0", 0, true, false},
		{"p3", 3, true, false},
		{"P4", 4, true, false},
		{"P5", priorityUnmapped, true, true},
		{"P", priorityUnmapped, true, true},
		{"P10", priorityUnmapped, true, true},
		{"Urgent", priorityUnmapped, true, true},
		{"Odd", priorityUnmapped, true, true}, // a map value outside P0..P4 is skipped
		{"Q1", priorityUnmapped, true, true},  // the letter must be P
		{"P/", priorityUnmapped, true, true},  // the digit before 0
		{"P:", priorityUnmapped, true, true},  // the digit after 9
		{"P+1", priorityUnmapped, true, true}, // a signed number is not a priority
		{"P-0", priorityUnmapped, true, true},
		{"Pa", priorityUnmapped, true, true},
		{"pP", priorityUnmapped, true, true},
		{"0", priorityUnmapped, true, true}, // no letter
		{"1P", priorityUnmapped, true, true},
		{"q1", priorityUnmapped, true, true}, // letters on both sides of P and p
		{"A1", priorityUnmapped, true, true},
		{"a1", priorityUnmapped, true, true},
		{" P2 ", 2, true, false},
	}
	for _, c := range cases {
		rank, present, unmapped := table.resolve(c.raw)
		if rank != c.rank || present != c.present || unmapped != c.unmapped {
			t.Errorf("resolve(%q) = %d %v %v, want %d %v %v", c.raw, rank, present, unmapped, c.rank, c.present, c.unmapped)
		}
	}
}

// The explicit table wins over the direct form.
func TestPriorityTableBeatsTheDirectForm(t *testing.T) {
	table := newPriorityTable(map[string]string{"P1": "P4"})
	if rank, _, _ := table.resolve("P1"); rank != 4 {
		t.Errorf("a table entry for P1 must win: rank %d, want 4", rank)
	}
}

func TestPriorityLabel(t *testing.T) {
	for _, c := range []struct {
		rank  int
		known bool
		want  string
	}{{0, true, "P0"}, {4, true, "P4"}, {5, true, ""}, {-1, true, ""}, {2, false, ""}} {
		if got := priorityLabel(c.rank, c.known); got != c.want {
			t.Errorf("priorityLabel(%d, %v) = %q, want %q", c.rank, c.known, got, c.want)
		}
	}
}
