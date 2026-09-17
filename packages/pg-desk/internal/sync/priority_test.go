package sync

import "testing"

func TestPriorityDelta_FirstConflictingTick_StashesAndNudges(t *testing.T) {
	addLabels, removeLabels, priority, setPriority := priorityDelta(2, nil, true, true)
	if len(addLabels) != 1 || addLabels[0] != "pbase:2" {
		t.Fatalf("addLabels = %v, want [pbase:2]", addLabels)
	}
	if removeLabels != nil {
		t.Fatalf("removeLabels = %v, want nil", removeLabels)
	}
	if !setPriority || priority != 1 {
		t.Fatalf("priority=%d setPriority=%v, want priority=1 setPriority=true (mine/co-owned raises toward 0)", priority, setPriority)
	}
}

func TestPriorityDelta_FirstConflictingTick_TeamLowers(t *testing.T) {
	_, _, priority, setPriority := priorityDelta(2, nil, false, true)
	if !setPriority || priority != 3 {
		t.Fatalf("priority=%d setPriority=%v, want priority=3 setPriority=true (team lowers toward 4)", priority, setPriority)
	}
}

func TestPriorityDelta_RepeatedConflict_NoOp(t *testing.T) {
	addLabels, removeLabels, priority, setPriority := priorityDelta(1, []string{"pbase:2"}, true, true)
	if addLabels != nil || removeLabels != nil || setPriority || priority != 0 {
		t.Fatalf("repeated conflicting tick should be a no-op, got addLabels=%v removeLabels=%v priority=%d setPriority=%v",
			addLabels, removeLabels, priority, setPriority)
	}
}

func TestPriorityDelta_ConflictCleared_RestoresBaseline(t *testing.T) {
	addLabels, removeLabels, priority, setPriority := priorityDelta(1, []string{"pbase:2"}, true, false)
	if addLabels != nil {
		t.Fatalf("addLabels = %v, want nil", addLabels)
	}
	if len(removeLabels) != 1 || removeLabels[0] != "pbase:2" {
		t.Fatalf("removeLabels = %v, want [pbase:2]", removeLabels)
	}
	if !setPriority || priority != 2 {
		t.Fatalf("priority=%d setPriority=%v, want priority=2 setPriority=true (restore baseline)", priority, setPriority)
	}
}

func TestPriorityDelta_NoConflictNoBaseline_NoOp(t *testing.T) {
	addLabels, removeLabels, priority, setPriority := priorityDelta(2, nil, true, false)
	if addLabels != nil || removeLabels != nil || setPriority || priority != 0 {
		t.Fatalf("no conflict, no baseline should be a no-op, got addLabels=%v removeLabels=%v priority=%d setPriority=%v",
			addLabels, removeLabels, priority, setPriority)
	}
}

func TestNudgedPriority_ClampsAtBoundaries(t *testing.T) {
	if got := nudgedPriority(0, true); got != 0 {
		t.Fatalf("nudgedPriority(0, mine) = %d, want 0 (clamped)", got)
	}
	if got := nudgedPriority(4, false); got != 4 {
		t.Fatalf("nudgedPriority(4, team) = %d, want 4 (clamped)", got)
	}
}

func TestParseFormatPriority(t *testing.T) {
	for n := 0; n <= 4; n++ {
		s := formatPriority(n)
		got, ok := parsePriority(s)
		if !ok || got != n {
			t.Fatalf("round-trip %d -> %q -> (%d, %v), want (%d, true)", n, s, got, ok, n)
		}
	}
	if _, ok := parsePriority("High"); ok {
		t.Fatal("parsePriority(\"High\") should not parse (not a P0..P4 string)")
	}
	if got := formatPriority(9); got != "P4" {
		t.Fatalf("formatPriority(9) = %q, want P4 (clamped)", got)
	}
}
