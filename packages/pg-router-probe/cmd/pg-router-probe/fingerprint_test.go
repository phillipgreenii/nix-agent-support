package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAlertServer serves body as the Alertmanager v2 alerts response.
func newAlertServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

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

// pg2-3tt2e: startsAt and __values__ identify an EPISODE and a reading, not
// the alert. They must never reach Labels (and so the fingerprint), or a
// re-firing alert could never be matched to its predecessor.
func TestFingerprintIgnoresStartsAtAndValues(t *testing.T) {
	const body = `[{
	  "labels": {"__alert_rule_uid__": "pg-router-failure-rate", "class": "handler-error"},
	  "annotations": {"__values__": "{\"B\":1.5,\"A\":0}", "__value_string__": "ignored", "summary": "s"},
	  "startsAt": %q,
	  "status": {"state": "active"}
	}]`
	fingerprintFor := func(startsAt string) (string, grafanaAlert) {
		srv := newAlertServer(t, strings.Replace(body, "%q", `"`+startsAt+`"`, 1))
		defer srv.Close()
		client := newGrafanaClient(srv.URL, "", srv.Client())
		alerts, err := client.firingAlerts(context.Background(), registeredRuleUIDs)
		if err != nil || len(alerts) != 1 {
			t.Fatalf("firingAlerts: %v %+v", err, alerts)
		}
		a := alerts[0]
		return grafanaAlertFingerprint(a.RuleUID, a.Labels), a
	}
	fp1, a1 := fingerprintFor("2026-10-05T09:00:00Z")
	fp2, a2 := fingerprintFor("2026-10-05T10:30:00.123Z")
	if fp1 != fp2 {
		t.Fatalf("startsAt changed the fingerprint: %q vs %q", fp1, fp2)
	}
	if a1.StartsAt.Equal(a2.StartsAt) {
		t.Fatalf("startsAt must decode distinctly: %v vs %v", a1.StartsAt, a2.StartsAt)
	}
	for k := range a1.Labels {
		if strings.HasPrefix(k, "__values") || k == "startsAt" || k == "summary" {
			t.Fatalf("annotation/startsAt leaked into Labels: %v", a1.Labels)
		}
	}
	want := "pg-router-failure-rate|__alert_rule_uid__=pg-router-failure-rate,class=handler-error"
	if fp1 != want {
		t.Fatalf("fingerprint = %q, want %q", fp1, want)
	}
	if a1.Values != "A=0, B=1.5" {
		t.Fatalf("Values = %q", a1.Values)
	}
}

// pg2-x7ie2: the `escalation` label is routing metadata, not identity.
func TestGrafanaAlertFingerprintExcludesEscalationLabel(t *testing.T) {
	base := map[string]string{"__alert_rule_uid__": "r", "severity": "warning"}
	with := map[string]string{"__alert_rule_uid__": "r", "severity": "warning", "escalation": "human"}
	a, b := grafanaAlertFingerprint("r", base), grafanaAlertFingerprint("r", with)
	if a != b {
		t.Fatalf("escalation label changed the fingerprint: %q vs %q", a, b)
	}
	if strings.Contains(b, "escalation") {
		t.Fatalf("fingerprint %q must not mention escalation", b)
	}
}

// Annotations (summary/description) decode into the alert but never reach
// the fingerprint.
func TestFingerprintIgnoresAnnotations(t *testing.T) {
	fp := func(summary string) string {
		srv := newAlertServer(t, `[{"labels":{"__alert_rule_uid__":"pg-router-failure-rate","escalation":"human"},"annotations":{"summary":"`+summary+`","description":"d"},"startsAt":"2026-10-05T09:00:00Z","status":{"state":"active"}}]`)
		defer srv.Close()
		alerts, err := newGrafanaClient(srv.URL, "", srv.Client()).firingAlerts(context.Background(), registeredRuleUIDs)
		if err != nil || len(alerts) != 1 {
			t.Fatalf("firingAlerts: %v %+v", err, alerts)
		}
		return checkGrafanaAlerts(alerts)[0].Fingerprint
	}
	if a, b := fp("one"), fp("two"); a != b {
		t.Fatalf("annotations changed the fingerprint: %q vs %q", a, b)
	}
}
