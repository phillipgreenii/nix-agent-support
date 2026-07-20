package corpus

import "time"

// Recorder receives the scan/phase metrics the Monitor emits. It matches the
// poller.PhaseRecorder shape structurally and is defined HERE (not imported from
// internal/otel or internal/core/poller) so corpus has no dependency on either —
// poller imports corpus, not the reverse. A nil Recorder disables recording;
// *otel.Emitter satisfies it at wiring time.
type Recorder interface {
	RecordScan(mode string, d time.Duration, bytes int64)
	RecordPhase(phase string, d time.Duration)
}

func recordScan(rec Recorder, mode string, d time.Duration, bytes int64) {
	if rec != nil {
		rec.RecordScan(mode, d, bytes)
	}
}

func recordPhase(rec Recorder, phase string, d time.Duration) {
	if rec != nil {
		rec.RecordPhase(phase, d)
	}
}
