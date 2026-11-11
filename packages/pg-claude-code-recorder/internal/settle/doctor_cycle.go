package settle

import "time"

// cycleDueAt reports whether a new doctor cycle starts. lastEndNS is the end of
// the last completed cycle (0 when none is known), lastDurationNS that cycle's
// length. The cycle is due when it ended at least interval minus the expected
// cycle length ago; the expected length is the previous duration, minExpectedCycle
// at least. A disabled interval (<= 0) is never due; an unknown last end is
// always due.
func cycleDueAt(interval time.Duration, lastEndNS, lastDurationNS int64, now time.Time) bool {
	if interval <= 0 {
		return false
	}
	if lastEndNS == 0 {
		return true
	}
	expected := time.Duration(lastDurationNS)
	if expected < minExpectedCycle {
		expected = minExpectedCycle
	}
	return now.Sub(time.Unix(0, lastEndNS)) >= interval-expected
}
