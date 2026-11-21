package poller

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// The daemon wires the real Emitter through SetPhaseRecorder. The Emitter's own
// RecordPhase takes a context, so the bare Emitter fails the PhaseRecorder
// assertion and every producer phase was silently dropped; the ScanRecorder
// adapter is what the daemon passes.
var _ PhaseRecorder = otel.ScanRecorder{}

func TestBareEmitterIsNotAPhaseRecorder(t *testing.T) {
	var e any = &otel.Emitter{}
	if _, ok := e.(PhaseRecorder); ok {
		t.Fatal("the bare Emitter now satisfies PhaseRecorder; the ScanRecorder adapter is no longer needed")
	}
}

// TestProducerPhasesReachTheRealEmitter drives one real Assemble and one real
// refresh into a real Emitter (manual metric reader) and asserts the producer
// phases, including assemble, are exported.
func TestProducerPhasesReachTheRealEmitter(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)

	reader := metric.NewManualReader()
	em, err := otel.NewWithReader(reader)
	if err != nil {
		t.Fatalf("NewWithReader: %v", err)
	}
	p := newMonitorPoller(t, sessionsDir, home, pidAlive, now)
	p.SetPhaseRecorder(otel.ScanRecorder{Emitter: em})

	p.Producer().refresh(context.Background())

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "pa_monitor.poll.phase.duration" {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("phase duration is %T", m.Data)
			}
			for _, dp := range h.DataPoints {
				if v, ok := dp.Attributes.Value("phase"); ok && dp.Count > 0 {
					got[v.AsString()] = true
				}
			}
		}
	}
	for _, ph := range []string{"discover", "pricer", "limits", "weekly", "assemble"} {
		if !got[ph] {
			t.Errorf("phase %q not exported by the real Emitter; got %v", ph, got)
		}
	}
}
