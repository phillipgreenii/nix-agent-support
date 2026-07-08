package signal

// Signaler injects keyboard input into the terminal session hosting a process.
type Signaler interface {
	Name() string
	// Detect returns true if pid is running inside this terminal environment.
	Detect(pid int) bool
	// Send injects text followed by Enter into the terminal hosting pid.
	Send(pid int, text string) error
}

// ResolveSignaler returns the first Signaler whose Detect returns true for pid,
// or nil if none match.
func ResolveSignaler(signalers []Signaler, pid int) Signaler {
	for _, s := range signalers {
		if s.Detect(pid) {
			return s
		}
	}
	return nil
}

// BinaryRequirer is implemented by Signalers that shell out to an external
// executable for detection and/or delivery. The daemon checks these at
// startup: a missing binary (e.g. tmux/cmux absent from the launchd PATH)
// otherwise fails silently — Detect/Send swallow the exec error — which
// classifies the terminal as "unknown" and drops its auto-resume nudges with
// no signal anywhere.
type BinaryRequirer interface {
	RequiredBinaries() []string
}

// MissingBinary names an executable a Signaler needs that is not resolvable
// on PATH.
type MissingBinary struct {
	Signaler string
	Binary   string
}

// MissingBinaries returns every required executable, across the given
// signalers, that lookPath cannot resolve. Signalers that do not implement
// BinaryRequirer are skipped. lookPath is injected for testability; callers
// pass exec.LookPath.
func MissingBinaries(signalers []Signaler, lookPath func(string) (string, error)) []MissingBinary {
	var missing []MissingBinary
	for _, s := range signalers {
		br, ok := s.(BinaryRequirer)
		if !ok {
			continue
		}
		for _, bin := range br.RequiredBinaries() {
			if _, err := lookPath(bin); err != nil {
				missing = append(missing, MissingBinary{Signaler: s.Name(), Binary: bin})
			}
		}
	}
	return missing
}

var defaultSignalers = []Signaler{
	&TmuxSignaler{},
	&CmuxSignaler{},
}

// DefaultSignalers returns the standard ordered list of Signalers.
// TmuxSignaler is tried first.
func DefaultSignalers() []Signaler { return defaultSignalers }

// WithoutCmux returns a new slice containing every signaler except any
// *CmuxSignaler, preserving order and leaving the input slice unmodified.
//
// The in-daemon delivery path (SignalerAdapter over the daemon's configured
// signalers) uses this so a CmuxSignaler can never be resolved there: per ADR
// 0022 the daemon MUST NOT execute cmux — cmux-hosted targets are routed to the
// bridge instead. Excluding the type from the delivery slice — rather than
// relying on CmuxSignaler.Detect happening to return false for in-daemon
// targets in the shipped config — makes "the daemon never execs cmux" a
// structural guarantee of the delivery path rather than emergent coupling.
//
// Callers that legitimately need cmux (e.g. the D5 keep-awake predicate, which
// only calls Detect and never Send) MUST keep the unfiltered slice.
func WithoutCmux(signalers []Signaler) []Signaler {
	out := make([]Signaler, 0, len(signalers))
	for _, s := range signalers {
		if _, isCmux := s.(*CmuxSignaler); isCmux {
			continue
		}
		out = append(out, s)
	}
	return out
}
