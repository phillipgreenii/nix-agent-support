package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAlert_JSONRoundTripAndOmitempty(t *testing.T) {
	ack := true
	in := Alert{
		ID: "grafana:abc", Provider: "grafana", Title: "HighCPU", Severity: SeverityHigh,
		Acknowledged: &ack, Since: "2026-10-01T00:00:00Z",
		Attributes: map[string]string{"label.severity": "error"},
		Extensions: map[string]json.RawMessage{"grafana": json.RawMessage(`{"rule_uid":"r1"}`)},
		AsOf:       "2026-10-01T00:01:00Z",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Alert
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Severity != in.Severity || out.Acknowledged == nil || !*out.Acknowledged ||
		out.Attributes["label.severity"] != "error" || string(out.Extensions["grafana"]) != `{"rule_uid":"r1"}` {
		t.Fatalf("round-trip mismatch: %+v", out)
	}

	// Absent optional fields must be omitted, not zero-valued (INV-ALERT-2/4).
	bare, _ := json.Marshal(Alert{ID: "grafana:x", Provider: "grafana", Title: "t", Since: "s", AsOf: "a"})
	for _, k := range []string{"acknowledged", "severity", "description", "url", "attributes", "extensions"} {
		if strings.Contains(string(bare), `"`+k+`"`) {
			t.Errorf("%s must be omitted when unset: %s", k, bare)
		}
	}
	if !strings.Contains(string(bare), `"stale":false`) {
		t.Errorf("stale must always be present: %s", bare)
	}
}

func TestAlertEpisode_OmitsEndedAtWhenStillFiring(t *testing.T) {
	raw, _ := json.Marshal(AlertEpisode{RuleID: "r", Title: "t", StartedAt: "2026-10-01T00:00:00Z"})
	if strings.Contains(string(raw), "ended_at") || strings.Contains(string(raw), "alert_id") {
		t.Fatalf("ended_at/alert_id must be omitted: %s", raw)
	}
}

func TestCurrentSchemaVersions_IncludesAlert(t *testing.T) {
	if CurrentSchemaVersions["alert"] != AlertSchemaVersion {
		t.Fatal("alert missing from CurrentSchemaVersions")
	}
}
