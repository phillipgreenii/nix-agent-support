package otel

import (
	"context"
	"time"
)

// ScanRecorder adapts an Emitter to the context-free recorder interfaces of the
// poller, corpus and provider packages (RecordPhase(phase, d), RecordScan,
// RecordSubprocess, RecordScanDirtyFiles). The Emitter's own RecordPhase takes a
// context first (the emit tick records with its context), so the Emitter alone
// does not satisfy those interfaces: handing it to SetPhaseRecorder silently
// dropped every producer-side phase (discover, pricer, limits, weekly, assemble)
// because the interface assertion there fails without any error. Wrap the
// Emitter in a ScanRecorder when wiring a recorder into the poller.
//
// A ScanRecorder over a nil Emitter is safe: every Emitter recorder is nil-safe.
type ScanRecorder struct{ *Emitter }

// RecordPhase records one phase duration without a caller context.
func (r ScanRecorder) RecordPhase(phase string, d time.Duration) {
	r.Emitter.RecordPhase(context.Background(), phase, d)
}
