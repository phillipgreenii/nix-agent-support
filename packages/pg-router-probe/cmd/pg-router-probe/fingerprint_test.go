package main

import "testing"

func TestGrafanaAlertFingerprint(t *testing.T) {
	got := grafanaAlertFingerprint("pg-router-liveness-down", map[string]string{
		"instance": "host-a",
		"job":      "pg-router",
	})
	want := "pg-router-liveness-down|instance=host-a,job=pg-router"
	if got != want {
		t.Fatalf("grafanaAlertFingerprint: got %q, want %q", got, want)
	}
}

func TestGrafanaAlertFingerprintOrderIndependent(t *testing.T) {
	a := grafanaAlertFingerprint("rule", map[string]string{"b": "2", "a": "1"})
	b := grafanaAlertFingerprint("rule", map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatalf("fingerprint must be independent of map iteration order: %q vs %q", a, b)
	}
}

func TestGrafanaAlertFingerprintEmptyLabels(t *testing.T) {
	got := grafanaAlertFingerprint("rule-uid", map[string]string{})
	if got != "rule-uid|" {
		t.Fatalf("got %q, want %q", got, "rule-uid|")
	}
}

func TestQueueGrowthFingerprint(t *testing.T) {
	if got := queueGrowthFingerprint("queue-depth"); got != "queue-growth:queue-depth" {
		t.Fatalf("got %q", got)
	}
	if got := queueGrowthFingerprint("backlog"); got != "queue-growth:backlog" {
		t.Fatalf("got %q", got)
	}
}

func TestBinaryHashMismatchFingerprintIsFixed(t *testing.T) {
	if binaryHashMismatchFingerprint != "binary-hash-mismatch" {
		t.Fatalf("got %q", binaryHashMismatchFingerprint)
	}
}
