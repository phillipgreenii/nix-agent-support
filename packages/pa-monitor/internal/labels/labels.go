// Package labels owns the contract for telemetry labels: stable keys,
// per-key cardinality caps, detector-driven derivation, and decorator
// shell-out integration. See docs/superpowers/specs/2026-05-20-pa-monitor-daemon-otel-design.md
// for the full taxonomy.
package labels

// Set is a label key -> value map. Empty values represent absence.
type Set map[string]string

// Merge combines two sets. The argument wins on conflict. Empty values
// in either operand are dropped from the result.
func (a Set) Merge(b Set) Set {
	out := Set{}
	for k, v := range a {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range b {
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// Session is the subset of session state a detector inspects. The poller
// builds this per-session and passes it in. Detectors must NOT mutate.
type Session struct {
	ID    string
	PID   int
	CWD   string
	Env   map[string]string
	Model string
}

// Detector contributes labels for a single session. Built-in detectors
// satisfy this; the shell-out Decorator also implements it.
type Detector interface {
	Name() string
	Detect(s Session) Set
}
