package claudetranscript

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// scheduleWakeupEvent renders an assistant event whose content includes a
// ScheduleWakeup tool_use with the given delaySeconds.
func scheduleWakeupEvent(ts time.Time, delaySeconds int) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":`+
			`[{"type":"text","text":"scheduling"},`+
			`{"type":"tool_use","id":"toolu_1","name":"ScheduleWakeup","input":{"delaySeconds":%d,"prompt":"<<autonomous-loop-dynamic>>","reason":"pace"}}]}}`,
		ts.Format(time.RFC3339Nano), delaySeconds)
}

func TestPendingScheduledResume_found(t *testing.T) {
	ts := time.Date(2026, 5, 26, 17, 1, 16, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	// ScheduleWakeup followed only by trailing metadata (no real turn) => pending.
	body := scheduleWakeupEvent(ts, 120) + "\n" +
		`{"type":"system","subtype":"turn_duration","timestamp":"2026-05-26T17:01:17Z"}` + "\n" +
		`{"type":"last-prompt","lastPrompt":"x"}` + "\n" +
		`{"type":"agent-name","name":"a"}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, ok := PendingScheduledResume(path)
	if !ok {
		t.Fatal("ok = false, want true (trailing ScheduleWakeup, no turn follows)")
	}
	want := ts.Add(120 * time.Second)
	if !got.Equal(want) {
		t.Errorf("resumeAt = %v, want %v (ts + delaySeconds)", got, want)
	}
}

func TestPendingScheduledResume_resumedAfter(t *testing.T) {
	ts := time.Date(2026, 5, 26, 17, 1, 16, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	// A real user turn follows the ScheduleWakeup => the session resumed.
	body := scheduleWakeupEvent(ts, 120) + "\n" +
		`{"type":"system","subtype":"turn_duration","timestamp":"2026-05-26T17:01:17Z"}` + "\n" +
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"<<autonomous-loop-dynamic>>"}}`, ts.Add(2*time.Minute).Format(time.RFC3339Nano)) + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	if _, ok := PendingScheduledResume(path); ok {
		t.Error("ok = true, want false (session resumed after ScheduleWakeup)")
	}
}

// TestPendingScheduledResume_resumedThenRescheduled verifies the LAST
// ScheduleWakeup wins: a wakeup, a resume, then a second wakeup with nothing
// after => pending again, using the second wakeup's time + delay.
func TestPendingScheduledResume_lastWakeupWins(t *testing.T) {
	t1 := time.Date(2026, 5, 26, 17, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 26, 18, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := scheduleWakeupEvent(t1, 60) + "\n" +
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"resumed"}}`, t1.Add(time.Minute).Format(time.RFC3339Nano)) + "\n" +
		scheduleWakeupEvent(t2, 300) + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, ok := PendingScheduledResume(path)
	if !ok {
		t.Fatal("ok = false, want true (second wakeup is trailing)")
	}
	want := t2.Add(300 * time.Second)
	if !got.Equal(want) {
		t.Errorf("resumeAt = %v, want %v (second wakeup ts + delay)", got, want)
	}
}

func TestPendingScheduledResume_none(t *testing.T) {
	ts := time.Date(2026, 5, 26, 17, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`, ts.Format(time.RFC3339Nano)) + "\n" +
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"thanks"}}`, ts.Add(time.Minute).Format(time.RFC3339Nano)) + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	if _, ok := PendingScheduledResume(path); ok {
		t.Error("ok = true, want false (no ScheduleWakeup present)")
	}
}

// TestPendingScheduledResume_apiErrorDoesNotCountAsResume verifies a synthetic
// api-error assistant event after the ScheduleWakeup does NOT count as a resume
// (it is not a real turn) — mirrors LastAPIError's IsTerminal logic.
func TestPendingScheduledResume_apiErrorDoesNotCountAsResume(t *testing.T) {
	ts := time.Date(2026, 5, 26, 17, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := scheduleWakeupEvent(ts, 90) + "\n" +
		apiErrorEvent(ts.Add(30*time.Second), ErrUnknown, "API Error: socket closed") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, ok := PendingScheduledResume(path)
	if !ok {
		t.Fatal("ok = false, want true (api-error is not a real resume turn)")
	}
	if want := ts.Add(90 * time.Second); !got.Equal(want) {
		t.Errorf("resumeAt = %v, want %v", got, want)
	}
}
