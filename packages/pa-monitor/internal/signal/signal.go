package signal

// Signaler classifies the terminal environment hosting a process. Nudges are
// never delivered through it: delivery goes through the control mod.
type Signaler interface {
	Name() string
	// Detect returns true if pid is running inside this terminal environment.
	Detect(pid int) bool
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
// otherwise fails silently — Detect swallows the exec error — which
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
	&TmuxSignaler{Procs: defaultProcTable},
	&CmuxSignaler{Procs: defaultProcTable},
}

// DefaultSignalers returns the standard ordered list of Signalers.
// TmuxSignaler is tried first.
func DefaultSignalers() []Signaler { return defaultSignalers }
