package signal

import "strings"

// This file holds the single source of the cmux failure-reason taxonomy. A
// delivery failure reaches the daemon as plain error text (the control mod
// deliverer's message), and the daemon classifies it into the bounded reason
// labels the send_failures_total counter and the nudge.send_failed log carry.
// Keeping the substring matching here, in ONE place, stops it being duplicated
// in the daemon.

// CmuxFailureReason is a bounded, low-cardinality label for a delivery
// failure. It is the single source of truth for the reason taxonomy the
// send_failures_total counter and nudge.send_failed log carry.
type CmuxFailureReason string

const (
	ReasonUnknown    CmuxFailureReason = "unknown"
	ReasonNoSurface  CmuxFailureReason = "no_surface"
	ReasonSendKey    CmuxFailureReason = "send_key"
	ReasonEnumerate  CmuxFailureReason = "enumerate"
	ReasonTimeout    CmuxFailureReason = "timeout"
	ReasonConnection CmuxFailureReason = "connection"
	ReasonOther      CmuxFailureReason = "other"
)

// ClassifyCmuxFailure maps a delivery failure onto a bounded reason label plus
// a timed_out flag by substring matching on the error text (the error arrives
// as text, so no typed classification is possible).
func ClassifyCmuxFailure(err error) (CmuxFailureReason, bool) {
	if err == nil {
		return ReasonUnknown, false
	}
	return classifyCmuxText(err.Error())
}

// classifyCmuxText is the substring classifier and the single source of the
// reason taxonomy for errors that arrive as text. Its logic is byte-for-byte
// the pre-refactor daemon classifySendFailure + sendFailureTimedOut, preserved
// so the send_failures_total / nudge.send_failed labels do not change.
func classifyCmuxText(errText string) (CmuxFailureReason, bool) {
	s := strings.ToLower(errText)
	timedOut := hasTimeoutSignature(s)
	switch {
	case s == "":
		return ReasonUnknown, false
	case containsAny(s, "no cmux surface", "no surface", "surface not found"):
		return ReasonNoSurface, timedOut
	case containsAny(s, "send-key", "send key"):
		return ReasonSendKey, timedOut
	case strings.Contains(s, "enumerate"):
		return ReasonEnumerate, timedOut
	case timedOut:
		return ReasonTimeout, true
	case containsAny(s, "connection", "connect", "broken pipe", "refused"):
		return ReasonConnection, timedOut
	default:
		return ReasonOther, timedOut
	}
}

// hasTimeoutSignature reports whether lowercased error text carries a timeout
// signature. exec.CommandContext SIGKILLs a subprocess whose context deadline
// expires; the resulting ExitError renders as "signal: killed" — the same root
// cause as "deadline exceeded" and the more common of the two in practice.
// s MUST already be lowercased by the caller.
func hasTimeoutSignature(s string) bool {
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "signal: killed")
}

// containsAny reports whether s contains any of subs. s is expected lowercased.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
