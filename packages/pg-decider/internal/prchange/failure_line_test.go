package prchange

import (
	"strings"
	"testing"
)

func TestFailureLineKeepsASourceReasonPastTheTruncationPoint(t *testing.T) {
	pad := strings.Repeat(`{"source":"pg-connector-issue-beads-zr","status":"degraded","count":0},`, 8)
	reason := "bd list timed out after 120s: dolt server not reachable"
	detail := "pg-connector issue list: exit 3:\n{\"sources\":[" + pad +
		`{"source":"x","status":"degraded","reason":"` + reason + `"}]}`
	if i := strings.Index(detail, reason); i < 300 {
		t.Fatalf("fixture reason sits at %d, want past 300", i)
	}
	line := failureLine(specFor(StageDecide, ClassTransient), "the decide failed", detail, "pg-decider plan pr x")
	if !strings.Contains(line, reason) {
		t.Errorf("reason missing from line: %s", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("line is not single-line: %q", line)
	}
	if n := len([]rune(line)); n > 1000 {
		t.Errorf("line is unbounded: %d runes", n)
	}
	if !strings.Contains(line, "unavailable") {
		t.Errorf("retry word lost: %s", line)
	}
}

func TestFailureLineBoundsManyLongReasons(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("x ", 200))
	for i := 0; i < 10; i++ {
		b.WriteString(`{"reason":"` + strings.Repeat("r", 500) + `"}`)
	}
	line := failureLine(specFor(StageDecide, ClassTransient), "w", b.String(), "n")
	if n := len([]rune(line)); n > 1500 {
		t.Errorf("line is unbounded: %d runes", n)
	}
}
