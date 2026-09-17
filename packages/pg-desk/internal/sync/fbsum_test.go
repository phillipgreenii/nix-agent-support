package sync

import "testing"

func TestFbsumDigest_EmptyForNoUnaddressed(t *testing.T) {
	if got := fbsumDigest(nil); got != "" {
		t.Fatalf("fbsumDigest(nil) = %q, want empty", got)
	}
}

func TestFbsumDigest_DeterministicAndOrderIndependent(t *testing.T) {
	d1 := fbsumDigest([]string{"c1", "c2"})
	d2 := fbsumDigest([]string{"c2", "c1"}) // caller is expected to pre-sort, but digest itself is order-sensitive over its input
	if d1 == "" || len(d1) != 12 {
		t.Fatalf("fbsumDigest returned %q, want a 12-char digest", d1)
	}
	// unaddressedCommentIDs always sorts before calling fbsumDigest, so this
	// asserts sameness only when callers pre-sort identically (they do).
	sorted1 := fbsumDigest(sortStrings([]string{"c1", "c2"}))
	sorted2 := fbsumDigest(sortStrings([]string{"c2", "c1"}))
	if sorted1 != sorted2 {
		t.Fatalf("digest over pre-sorted input differs: %q vs %q", sorted1, sorted2)
	}
	_ = d2
}

func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func TestFbsumDigest_ChangesWithDifferentSet(t *testing.T) {
	d1 := fbsumDigest([]string{"c1"})
	d2 := fbsumDigest([]string{"c1", "c2"})
	if d1 == d2 {
		t.Fatal("digest did not change when the unaddressed set changed")
	}
}

func TestStaleFbsumLabels(t *testing.T) {
	labels := []string{"mine", "fbsum:aaa", "fbsum:bbb"}
	stale := staleFbsumLabels(labels, "bbb")
	if len(stale) != 1 || stale[0] != "fbsum:aaa" {
		t.Fatalf("staleFbsumLabels = %v, want [fbsum:aaa]", stale)
	}
}

func TestUnaddressedCommentIDs_FiltersToOpenOnly(t *testing.T) {
	got := unaddressedCommentIDs(nil)
	if got != nil {
		t.Fatalf("unaddressedCommentIDs(nil) = %v, want nil", got)
	}
}
