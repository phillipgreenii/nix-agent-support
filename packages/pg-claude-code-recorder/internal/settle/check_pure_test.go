package settle

import (
	"testing"
	"time"
)

// pureStep is one second: the smallest probe far above clock resolution that
// still sits one unit across a duration boundary. pureAfter and pureRecheck
// (10m, 3m) MUST differ so a swapped argument changes a verdict.
const (
	pureStep    = time.Second
	pureAfter   = 10 * time.Minute
	pureRecheck = 3 * time.Minute
)

var pureNow = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func TestQuietAgeReached(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want bool
	}{
		{"one under", pureAfter - pureStep, false},
		{"exactly", pureAfter, true},
		{"one over", pureAfter + pureStep, true},
		{"written in the future", -pureStep, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := quietAgeReached(pureNow, pureNow.Add(-tc.ago), pureAfter); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHeldHasNewWrites(t *testing.T) {
	since := pureNow.UnixNano()
	tests := []struct {
		name   string
		newest time.Time
		want   bool
	}{
		{"one under the hold", pureNow.Add(-pureStep), false},
		{"exactly at the hold", pureNow, false},
		{"one over the hold", pureNow.Add(pureStep), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := heldHasNewWrites(tc.newest, since); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHeldRecheckNotDue(t *testing.T) {
	tests := []struct {
		name  string
		force bool
		ago   time.Duration
		never bool
		want  bool
	}{
		{"one under the recheck", false, pureRecheck - pureStep, false, true},
		{"exactly the recheck", false, pureRecheck, false, false},
		{"one over the recheck", false, pureRecheck + pureStep, false, false},
		{"forced skips the wait", true, 0, false, false},
		{"never checked", false, 0, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := pureNow.Add(-tc.ago)
			if tc.never {
				at = time.Time{}
			}
			if got := heldRecheckNotDue(tc.force, at, pureNow, pureRecheck); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSnapshotFailure(t *testing.T) {
	tests := []struct {
		name       string
		size, stSz int64
		hash, stH  uint64
		want       string
	}{
		{"agree", 10, 10, 1, 1, ""},
		{"size one under", 9, 10, 1, 1, ReasonCopyOffset},
		{"size one over", 11, 10, 1, 1, ReasonCopyOffset},
		{"hash differs", 10, 10, 1, 2, ReasonCopyTail},
		{"both differ reports the size", 9, 10, 1, 2, ReasonCopyOffset},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, detail := snapshotFailure(tc.size, tc.hash, tc.stSz, tc.stH)
			if reason != tc.want || (reason != "") != (detail != "") {
				t.Fatalf("got (%q, %q), want reason %q with a detail iff failing", reason, detail, tc.want)
			}
		})
	}
}

func TestAppendOriginalFailure(t *testing.T) {
	tests := []struct {
		name         string
		orig, offset int64
		tailLen      int
		tailOK       bool
		want         string
		wantTailRead bool
	}{
		{"agree, no tail", 100, 100, 0, false, "", false},
		{"original one under", 99, 100, 5, true, ReasonCopyOffset, false},
		{"original one over", 101, 100, 5, true, ReasonCopyOffset, false},
		{"agree, tail matches", 100, 100, 1, true, "", true},
		{"agree, tail differs", 100, 100, 1, false, ReasonCopyTail, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			read := false
			reason, detail := appendOriginalFailure(tc.orig, tc.offset, tc.tailLen, func() bool { read = true; return tc.tailOK })
			if reason != tc.want || (reason != "") != (detail != "") {
				t.Fatalf("got (%q, %q), want reason %q", reason, detail, tc.want)
			}
			if read != tc.wantTailRead {
				t.Fatalf("tail read = %v, want %v (lazy)", read, tc.wantTailRead)
			}
		})
	}
}

func TestTornFactStale(t *testing.T) {
	// copy offset 100, newline at 90, so a fresh fact is (90, 10).
	tests := []struct {
		name    string
		off, ln int64
		want    bool
	}{
		{"fresh", 90, 10, false},
		{"starts one before the newline", 89, 11, true},
		{"starts one after the newline", 91, 9, true},
		{"ends one short of the offset", 90, 9, true},
		{"ends one past the offset", 90, 11, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tornFactStale(tc.off, tc.ln, 90, 100); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// maxLine 50 is above every tail length probed except the boundary ones, and
// copy offset 100 leaves room for a parse offset one under and over.
func TestTornTailVerdict(t *testing.T) {
	const maxLine, copyOff = 50, 100
	tests := []struct {
		name            string
		tornLen, parsed int64
		quiet           bool
		want            tornVerdict
	}{
		{"no tail", 0, 0, true, tornVerdict{}},
		{"tail but parser caught up", 10, copyOff, true, tornVerdict{}},
		{"tail, parser one behind, not quiet", 10, copyOff - 1, false, tornVerdict{pending: true}},
		{"tail, parser one behind, quiet", 10, copyOff - 1, true, tornVerdict{pending: true, tryParse: true}},
		{"one under max, quiet", maxLine - 1, 0, true, tornVerdict{pending: true, tryParse: true}},
		{"exactly max", maxLine, 0, true, tornVerdict{pending: true, tooLong: true}},
		{"one over max, not quiet", maxLine + 1, 0, false, tornVerdict{pending: true, tooLong: true}},
		{"parser past the copy offset", 10, copyOff + 1, true, tornVerdict{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tornTail(tc.tornLen, tc.parsed, copyOff, maxLine, tc.quiet); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseBehindFailure(t *testing.T) {
	// copy offset 100, last newline 90, so tornLen is 10 for the short cases.
	const maxLine, copyOff, nl = 50, 100, 90
	tests := []struct {
		name            string
		parsed, tornLen int64
		want            bool
	}{
		{"parsed to the newline", nl, 10, false},
		{"parsed to the copy offset", copyOff, 10, false},
		{"parsed one before the newline", nl - 1, 10, true},
		{"parsed one past the newline", nl + 1, 10, true},
		{"parsed one before the copy offset", copyOff - 1, 10, true},
		{"tail too long and parser behind is not asked", nl - 1, maxLine, false},
		{"tail one under max is asked", nl - 1, maxLine - 1, true},
		{"tail too long but parser at the copy offset is asked, and fine", copyOff, maxLine, false},
		{"tail too long, parser past the copy offset is asked", copyOff + 1, maxLine, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseBehindFailure(tc.parsed, copyOff, nl, tc.tornLen, maxLine); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
