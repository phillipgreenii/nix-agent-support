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

// TestGrafanaAlertFingerprintDistinguishesTypeLabel is pg2-0gn0u's own
// acceptance case: pg-router-queue-depth-growing fires separately for
// type=pr.reconcile and type=pr.changed, and each SHOULD get its own
// fingerprint (so a matching bead's episode_count increments per-type
// instead of every type colliding on one bead), while two firings of the
// SAME rule+type must still produce the SAME fingerprint (so a genuine
// repeat is recognized as "nothing new" rather than filed again).
// grafanaAlertFingerprint already hashes every label it is given -- this
// test locks that in; the actual bug pg2-0gn0u fixes is downstream, in
// how a multi-label fingerprint value survives the pg-connector CLI
// boundary (see connector.go's metadataArgs and
// connector_test.go's TestMetadataArgsRoundTripsThroughPflagStringToString).
func TestGrafanaAlertFingerprintDistinguishesTypeLabel(t *testing.T) {
	labelsFor := func(typ string) map[string]string {
		return map[string]string{
			"__alert_rule_uid__": "pg-router-queue-depth-growing",
			"alertname":          "pg-router queue depth is growing (by type)",
			"severity":           "warning",
			"type":               typ,
		}
	}

	reconcile := grafanaAlertFingerprint("pg-router-queue-depth-growing", labelsFor("pr.reconcile"))
	changed := grafanaAlertFingerprint("pg-router-queue-depth-growing", labelsFor("pr.changed"))
	if reconcile == changed {
		t.Fatalf("different type labels must not collapse to the same fingerprint, got %q for both", reconcile)
	}

	reconcileAgain := grafanaAlertFingerprint("pg-router-queue-depth-growing", labelsFor("pr.reconcile"))
	if reconcile != reconcileAgain {
		t.Fatalf("two firings of the same rule+type must produce the same fingerprint: %q vs %q", reconcile, reconcileAgain)
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
