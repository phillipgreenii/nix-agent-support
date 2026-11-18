package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderBodyShape(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "something happened", Evidence: "raw=1"}
	when := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	body := renderBody(f, when, "", "")

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
	body := renderBody(f, time.Now(), "binary-hash: not configured", "")
	if !strings.Contains(body, "Note: this run's other sub-check(s) were skipped/degraded: binary-hash: not configured") {
		t.Fatalf("got %q", body)
	}
}

func TestRenderBodyNoNoteWhenNothingSkipped(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "", "")
	if strings.Contains(body, "Note:") {
		t.Fatalf("did not expect a Note: line, got %q", body)
	}
}

func TestRenderBodyReferencesClosedPredecessor(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "s", Evidence: "e"}
	body := renderBody(f, time.Now(), "", "pg2-pqw1u")
	if !strings.Contains(body, "Predecessor: pg2-pqw1u (closed") {
		t.Fatalf("got %q", body)
	}
	if strings.Contains(renderBody(f, time.Now(), "", ""), "Predecessor:") {
		t.Fatalf("no Predecessor line expected without a closed match")
	}
}

func TestRenderBodyRemediationForEscalationHuman(t *testing.T) {
	f := finding{
		Fingerprint: "fp1", Summary: "s", Evidence: "e", Escalation: escalationHuman,
		AlertSummary: "origin down", AlertDescription: "renew the cert",
	}
	body := renderBody(f, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), "", "")
	want := "\nRemediation:\norigin down\nrenew the cert\n" +
		"Close this bead once the alert clears after remediation (pg-router-probe never closes beads).\n"
	if !strings.HasSuffix(body, want) {
		t.Fatalf("body = %q, want suffix %q", body, want)
	}
}

func TestRenderBodyNoRemediationByDefault(t *testing.T) {
	f := finding{Fingerprint: "fp1", Summary: "s", Evidence: "e", AlertSummary: "x", AlertDescription: "y"}
	if body := renderBody(f, time.Now(), "", ""); strings.Contains(body, "Remediation") {
		t.Fatalf("default body must not render annotations: %q", body)
	}
}
