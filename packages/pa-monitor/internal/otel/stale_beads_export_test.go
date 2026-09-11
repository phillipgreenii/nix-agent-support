package otel

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRecordStaleBeadsExport_EmitsErrorLog is the pg2-zjopv top-level-alert
// contract: RecordStaleBeadsExport must emit an ERROR-severity log record (a
// severity a dashboard/alert rule would actually surface), with the path
// attribute reaching the record so the alert names the exact file found.
func TestRecordStaleBeadsExport_EmitsErrorLog(t *testing.T) {
	cl := &capturingLogger{}
	e := &Emitter{logger: cl} // no SDK counter; RecordStaleBeadsExport is nil-safe on it

	e.RecordStaleBeadsExport(map[string]string{"path": "/ws/.beads/issues.jsonl"})

	if len(cl.records) != 1 {
		t.Fatalf("records = %d, want 1", len(cl.records))
	}
	rec := cl.records[0]
	if rec.Severity() != otellog.SeverityError {
		t.Errorf("severity = %v, want Error (a stray issues.jsonl is a data-corruption footgun)", rec.Severity())
	}
	if rec.Body().AsString() != "beads.stale_export_found" {
		t.Errorf("body = %q, want beads.stale_export_found", rec.Body().AsString())
	}
	got := map[string]string{}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.AsString()
		return true
	})
	if got["event_name"] != "beads.stale_export_found" {
		t.Errorf("event_name attr = %q, want beads.stale_export_found", got["event_name"])
	}
	if got["path"] != "/ws/.beads/issues.jsonl" {
		t.Errorf("path attr = %q, want /ws/.beads/issues.jsonl", got["path"])
	}
}

// TestRecordStaleBeadsExport_IncrementsCounter proves the metric side is ALSO
// wired (the bead's "not just a metric point" requirement is about the log
// event being the thing that surfaces — it does not forbid a companion
// counter for dashboarding), with the path attribute on the data point too.
func TestRecordStaleBeadsExport_IncrementsCounter(t *testing.T) {
	e, reader := newTestEmitter(t)

	e.RecordStaleBeadsExport(map[string]string{"path": "/ws/.beads/issues.jsonl"})
	e.RecordStaleBeadsExport(map[string]string{"path": "/ws/repo/.beads/issues.jsonl"})

	m, ok := collectMetric(t, reader, "pa_monitor.beads.stale_export_found_total")
	if !ok {
		t.Fatal("pa_monitor.beads.stale_export_found_total not emitted")
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("stale_export_found_total is %T, want metricdata.Sum[int64]", m.Data)
	}
	if len(sum.DataPoints) != 2 {
		t.Fatalf("data points = %d, want 2 (one per distinct path)", len(sum.DataPoints))
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
}

// TestRecordStaleBeadsExport_NilSafe: a nil *Emitter (OTel disabled) must not
// panic — the same nil-safety contract every other Record* method carries.
func TestRecordStaleBeadsExport_NilSafe(t *testing.T) {
	var e *Emitter
	e.RecordStaleBeadsExport(map[string]string{"path": "/ws/.beads/issues.jsonl"})
}
