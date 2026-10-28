package signal

import "strings"

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

// DetectHost returns the Name() of the first Signaler that detects pid, or
// "unknown" when none match. This is the bare terminal-host name; the
// cmux/bridge refinement is applied separately (in the poller). Shared by the
// poller's terminal-host provider fetch boundary.
func DetectHost(signalers []Signaler, pid int) string {
	if s := ResolveSignaler(signalers, pid); s != nil {
		return s.Name()
	}
	return "unknown"
}

// PaneInputInspector is implemented by Signalers that can inspect whether
// their target's terminal surface currently shows composed-but-not-yet-
// submitted input (a half-typed prompt). Optional: a Signaler that does not
// implement it has no inspection capability — see HasUnsubmittedInput for how
// that is handled conservatively.
type PaneInputInspector interface {
	// HasUnsubmittedInput reports whether pid's surface currently shows
	// non-empty composed-but-not-submitted input.
	HasUnsubmittedInput(pid int) bool
}

// HasUnsubmittedInput reports whether pid's resolved Signaler shows
// composed-but-not-yet-submitted input. Used by
// nudger.AutoSessionWrapUpProducer's unsubmitted-input guard (bead tc-m08w3,
// safety-critical): injecting a directive via send-keys+Enter while the user
// is mid-compose would splice into their half-typed line and force-submit a
// corrupted message.
//
// Conservative by design — fails closed (reports true = "assume unsubmitted
// input present, do not send") on any uncertainty, per the feature's own
// documented cost asymmetry: a missed nudge costs nothing extra, a
// force-submitted half-typed message is much worse than a missed nudge:
//   - no Signaler resolves for pid (surfaceless) -> true. (HasSurface already
//     reaps a surfaceless session from the producer's candidate set upstream
//     of this call, so in practice this path is defense-in-depth, not the
//     primary gate.)
//   - the resolved Signaler does not implement PaneInputInspector (e.g.
//     CmuxSignaler has no capture-pane equivalent today — cmux delivery is
//     routed through the cmux-bridge process per ADR 0022, which has no
//     existing RPC for inspecting composed-but-unsubmitted input) -> true.
//     This means a cmux-hosted session's AutoSessionWrapUp nudge never clears
//     the guard until CmuxSignaler grows an equivalent inspection capability
//     (real, separate follow-up work — see packages/pa-monitor/README.md).
//   - the resolved Signaler's own inspection is itself uncertain (a
//     capture-pane error, or no recognizable input-box line found) -> true.
//     See TmuxSignaler.HasUnsubmittedInput.
func HasUnsubmittedInput(signalers []Signaler, pid int) bool {
	return unsubmittedInput(ResolveSignaler(signalers, pid), pid)
}

// HasUnsubmittedInputForHost is HasUnsubmittedInput resolved through the
// session's cached terminal host: when host names a configured signaler only
// that signaler is asked (no Detect walk first; an inspector fails closed
// itself when it cannot find the pane). An empty or "unknown" host behaves
// exactly like HasUnsubmittedInput.
func HasUnsubmittedInputForHost(signalers []Signaler, host string, pid int) bool {
	if s := signalerForHost(signalers, host); s != nil {
		return unsubmittedInput(s, pid)
	}
	return HasUnsubmittedInput(signalers, pid)
}

func unsubmittedInput(s Signaler, pid int) bool {
	if s == nil {
		return true
	}
	insp, ok := s.(PaneInputInspector)
	if !ok {
		return true
	}
	return insp.HasUnsubmittedInput(pid)
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
