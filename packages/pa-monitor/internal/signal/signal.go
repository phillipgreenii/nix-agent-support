package signal

import "strings"

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

// hostClassifier is implemented by a Signaler whose Detect is exactly the
// ancestry predicate that classifies a session's terminal host (CmuxSignaler:
// "a cmux server is an ancestor of pid"). For such a signaler a cached host of
// its Name() already proves Detect, so ResolveSignalerForHost skips the walk.
// A signaler whose Detect consults live state beyond the classification (tmux:
// the current pane map) MUST NOT implement it.
type hostClassifier interface {
	DetectIsHostClassification() bool
}

// BareHost strips the poller's bridge-status refinement from a terminal-host
// string: "cmux (no bridge)" -> "cmux". Any other value is returned unchanged.
func BareHost(host string) string {
	if i := strings.IndexByte(host, ' '); i >= 0 {
		return host[:i]
	}
	return host
}

// signalerForHost returns the Signaler whose Name() is host's bare name, or nil
// when host is empty, "unknown" or names no configured signaler (the caller
// then falls back to the full first-match walk).
func signalerForHost(signalers []Signaler, host string) Signaler {
	h := BareHost(host)
	if h == "" || h == "unknown" {
		return nil
	}
	for _, s := range signalers {
		if s.Name() == h {
			return s
		}
	}
	return nil
}

// ResolveSignalerForHost is the host-aware ResolveSignaler: host is the
// session's terminal host already cached by the poller (provider.Cache). When
// it names a configured signaler, ONLY that signaler is consulted, so a
// cmux-hosted session never pays the tmux ancestry walk (spike pg2-57blt: 105
// of 231 ps execs per tick). A signaler that is its own host classification
// (hostClassifier) is returned without any walk; others still run Detect, and
// a miss means "no surface" (the session is not re-tried against the other
// signalers). An empty or "unknown" host, or one naming no signaler, behaves
// exactly like ResolveSignaler.
func ResolveSignalerForHost(signalers []Signaler, host string, pid int) Signaler {
	s := signalerForHost(signalers, host)
	if s == nil {
		return ResolveSignaler(signalers, pid)
	}
	if hc, ok := s.(hostClassifier); ok && hc.DetectIsHostClassification() {
		return s
	}
	if s.Detect(pid) {
		return s
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
