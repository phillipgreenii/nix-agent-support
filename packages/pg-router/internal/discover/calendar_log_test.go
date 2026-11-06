package discover

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// captureLogs routes the default slog logger into a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}

func wantLog(t *testing.T, logs *bytes.Buffer, parts ...string) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		ok := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("no log line with %q in:\n%s", parts, logs.String())
}

func TestCalendarLogs_firedOnTimeAndLate(t *testing.T) {
	logs := captureLogs(t)
	h, _ := primed(t, 10, "run-now")
	h.pass(slotAt(t, 10).Add(3 * time.Second))
	wantLog(t, logs, "level=INFO", `msg="calendar tick fired"`, "source=weekly", "reason=\"on time\"", "policy=run-now")
	if strings.Contains(logs.String(), "late by") {
		t.Errorf("an on-time fire must not log lateness:\n%s", logs.String())
	}

	logs.Reset()
	h2, _ := primed(t, 10, "run-now")
	h2.pass(slotAt(t, 10).Add(2 * time.Minute))
	wantLog(t, logs, "level=INFO", "calendar tick late by more than one poll interval", "late_by=2m0s")
}

func TestCalendarLogs_skippedAndSuperseded(t *testing.T) {
	logs := captureLogs(t)
	h, _ := primed(t, 10, "next-slot")
	h.start = slotAt(t, 10).Add(time.Hour) // the process started after the slot
	h.pass(slotAt(t, 10).Add(2 * time.Hour))
	wantLog(t, logs, "level=INFO", `msg="calendar slot skipped"`, "reason=\"router not running\"", "policy=next-slot")

	logs.Reset()
	h2, _ := primed(t, 10, "run-now")
	h2.start = slotAt(t, 13).Add(time.Hour)
	h2.pass(slotAt(t, 13).Add(2 * time.Hour))
	wantLog(t, logs, "level=WARN", "calendar slots superseded", "count=3")
	wantLog(t, logs, "level=INFO", `msg="calendar tick fired"`, "reason=\"router not running\"")
}

func TestCalendarLogs_clockClamp(t *testing.T) {
	logs := captureLogs(t)
	h, _ := primed(t, 10, "run-now")
	rec := h.slots["weekly"]
	rec.LastSlot = slotAt(t, 20)
	h.slots["weekly"] = rec
	h.pass(slotAt(t, 10).Add(time.Hour))
	wantLog(t, logs, "level=WARN", "calendar clock clamp")
}

func TestCalendarLogs_movedByDSTGap(t *testing.T) {
	logs := captureLogs(t)
	cs := newCalSrc(t, "weekly", "run-now", query.ClockTime{Hour: 2, Minute: 30}, ny(t))
	slot := time.Date(2027, 3, 14, 3, 0, 0, 0, ny(t)) // 02:30 does not exist: resolves to 03:00 EDT
	first := slot.Add(-30 * time.Hour)
	h := newHarness(t, first, first.Add(-time.Hour), cs)
	h.pass(first)
	h.pass(slot.Add(time.Second))
	wantLog(t, logs, "level=INFO", "calendar slot moved by DST gap", "configured=02:30")
}

func TestCalendarLogs_enqueueFailedSkipped(t *testing.T) {
	logs := captureLogs(t)
	h, cs := primed(t, 10, "run-now")
	*cs.fail = true
	h.pass(slotAt(t, 10).Add(time.Second))
	*cs.fail = false
	h.pass(slotAt(t, 11).Add(time.Second))
	wantLog(t, logs, "level=WARN", `msg="calendar slot skipped"`, "reason=\"enqueue failed\"")
}
