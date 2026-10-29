package asklog

import (
	"path/filepath"
	"testing"
)

func TestTrackerLabels_RoundTripAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "asks.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	k1 := CallKey{SessionID: "s1", ToolUseID: "t1"}
	k2 := CallKey{SessionID: "s1", ToolUseID: "t2"}
	if err := s.SetTrackerCorrectDecision([]CallKey{k1, k2}, "deny", "because"); err != nil {
		t.Fatal(err)
	}
	// excluding keeps the correct-decision label; relabelling keeps the exclusion
	if err := s.MarkTrackerExcluded([]CallKey{k1}, "noise"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTrackerCorrectDecision([]CallKey{k1}, "allow", "changed my mind"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	ro, err := NewReadOnlyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	got, err := ro.TrackerLabels()
	if err != nil {
		t.Fatal(err)
	}
	l1 := got[k1]
	if l1.CorrectDec == nil || *l1.CorrectDec != "allow" || l1.CorrectDecExplanation != "changed my mind" || !l1.Excluded || l1.ExcludedReason != "noise" {
		t.Errorf("k1 = %+v", l1)
	}
	l2 := got[k2]
	if l2.CorrectDec == nil || *l2.CorrectDec != "deny" || l2.Excluded {
		t.Errorf("k2 = %+v", l2)
	}
}

func TestTrackerLabels_AbsentTableMeansNoLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "asks.db")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP TABLE tracker_labels`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	ro, err := NewReadOnlyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	got, err := ro.TrackerLabels()
	if err != nil || len(got) != 0 {
		t.Errorf("got %v %v", got, err)
	}
}
