package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderBodyMatchesTemplate(t *testing.T) {
	f := finding{Fingerprint: "needs-input:sess-1", Summary: "ccpool session sess-1 is stuck in needs_input", Evidence: "external_id=sess-1"}
	when := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	body := renderBody(f, when, "")

	wantLines := []string{
		"Pg-Router-Escalation-Fingerprint: needs-input:sess-1",
		"Source: ccpool-probe",
		"Finding: ccpool session sess-1 is stuck in needs_input",
		"Since: 2026-09-22T12:00:00Z",
		"Evidence:",
		"external_id=sess-1",
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got:\n%s", want, body)
		}
	}
}

func TestRenderBodyAppendsSkippedNote(t *testing.T) {
	f := finding{Fingerprint: "fp", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "zombie-drift: unreachable")
	if !strings.Contains(body, "zombie-drift: unreachable") {
		t.Fatalf("expected skipped note in body, got:\n%s", body)
	}
}

func TestRenderBodyNoNoteWhenNothingSkipped(t *testing.T) {
	f := finding{Fingerprint: "fp", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "")
	if strings.Contains(body, "Note:") {
		t.Fatalf("expected no Note: line when nothing was skipped, got:\n%s", body)
	}
}
