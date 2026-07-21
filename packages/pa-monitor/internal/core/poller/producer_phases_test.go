package poller

import (
	"context"
	"testing"
	"time"
)

// TestProducer_Assemble_FiresProducerPhases is the Phase-3 step-6 re-homing +
// pg2-sewtz metric-parity assertion: the discover/pricer/limits/weekly phases
// now fire from the PRODUCER (Assemble), not the emit tick. The recorder is wired
// once via SetPhaseRecorder, which fans out to the Monitor and the producer.
func TestProducer_Assemble_FiresProducerPhases(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	rec := newFakeRec()
	p := newMonitorPoller(sessionsDir, home, pidAlive, now)
	p.SetPhaseRecorder(rec)

	if _, err := p.Producer().Assemble(ctx, now); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, ph := range []string{"discover", "pricer", "limits", "weekly"} {
		if rec.phases[ph] == 0 {
			t.Errorf("producer Assemble did not fire phase %q; phases=%v", ph, rec.phases)
		}
	}
}
