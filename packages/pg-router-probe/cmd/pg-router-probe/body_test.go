package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderBodyShape(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "something happened", Evidence: "raw=1"}
	when := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	body := renderBody(f, when, "")

	want := "Pg-Router-Escalation-Fingerprint: fp1\n\n" +
		"Source: pg-router-probe\n" +
		"Finding: something happened\n" +
		"Since: 2026-09-22T12:00:00Z\n" +
		"Evidence:\n" +
		"raw=1\n"
	if body != want {
		t.Fatalf("got:\n%q\nwant:\n%q", body, want)
	}
}

func TestRenderBodyAppendsSkippedNote(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "binary-hash: not configured")
	if !strings.Contains(body, "Note: this run's other sub-check(s) were skipped/degraded: binary-hash: not configured") {
		t.Fatalf("got %q", body)
	}
}

func TestRenderBodyNoNoteWhenNothingSkipped(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "")
	if strings.Contains(body, "Note:") {
		t.Fatalf("did not expect a Note: line, got %q", body)
	}
}
