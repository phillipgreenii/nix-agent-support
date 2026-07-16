package signal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// This file is the cmux-command Gateway: the single place that runs a cmux
// subprocess command and turns its failure into a TYPED value instead of a bare
// error string. Diagnosing cmux failures used to mean substring-matching the
// combined error text far downstream in the daemon (classifySendFailure) — a
// brittle, invisible coupling where changing how cmux.go wrapped an error could
// silently reclassify a metric (pg2-qxo5). The typed failures below make the
// failing command PATH and the TIMEOUT nature first-class fields, so a consumer
// classifies via a type switch (ClassifyCmuxFailure) rather than re-parsing text.
//
// The daemon still classifies from text because cmux delivery is executed by the
// cmux-bridge and its error crosses a gRPC boundary as a plain string (ADR 0022:
// the daemon MUST NOT execute cmux). ClassifyCmuxFailure therefore keeps a
// substring fallback for such reconstructed errors — but that taxonomy now lives
// here, in ONE place, rather than being duplicated in the daemon.

// CmuxCommand names the cmux subcommand a Gateway invocation ran, so a failure
// records which PATH failed without parsing the message.
type CmuxCommand string

const (
	// CmuxEnumerate is `cmux --json top --processes` (surface enumeration).
	CmuxEnumerate CmuxCommand = "enumerate"
	// CmuxSend is `cmux send ...` (inject text).
	CmuxSend CmuxCommand = "send"
	// CmuxSendKey is `cmux send-key ...` (inject the Enter key).
	CmuxSendKey CmuxCommand = "send-key"
)

// CmuxFailureReason is a bounded, low-cardinality label for a cmux command
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

// CmuxError is the typed failure a cmux subprocess command produces. It carries
// the failing command PATH, whether the run TIMED OUT, the process exit status,
// and captured stderr — so classification is a type switch over these fields
// rather than substring matching. Its Error() is deliberately transparent
// (it returns the underlying error's message unchanged) so callers that wrap it
// with fmt.Errorf("cmux send: %w", err) produce byte-identical strings to the
// pre-Gateway code; the structured fields are additive metadata.
type CmuxError struct {
	Command    CmuxCommand
	TimedOut   bool
	ExitStatus int
	Stderr     string
	Underlying error
}

func (e *CmuxError) Error() string { return e.Underlying.Error() }
func (e *CmuxError) Unwrap() error { return e.Underlying }

// WireCmuxError reconstructs a cmux failure that crossed the cmux-bridge gRPC
// boundary carrying its TYPED classification (DeliverResult.reason/timed_out,
// pg2-p1q00). The bridge holds the real *CmuxError and classifies it there;
// the daemon rebuilds this from the wire fields so ClassifyCmuxFailure reads
// Reason/TimedOut directly instead of re-parsing the transported text. Its
// Error() returns the transported message unchanged, so log/telemetry error
// strings are byte-identical to the pre-typed path.
type WireCmuxError struct {
	Reason   CmuxFailureReason
	TimedOut bool
	Msg      string
}

func (e *WireCmuxError) Error() string { return e.Msg }

// NoCmuxSurfaceError is returned by CmuxSignaler.Send when no cmux surface hosts
// the target pid (or any ancestor). It is a logical delivery failure, not a
// subprocess failure, so it never carries a timeout.
type NoCmuxSurfaceError struct{ PID int }

func (e *NoCmuxSurfaceError) Error() string {
	return fmt.Sprintf("signal: no cmux surface found for pid %d", e.PID)
}

// runCmux runs a `cmux` subprocess command through the injectable seam and, on
// failure, wraps the (stderr-enriched) error in a *CmuxError tagged with cmd.
// This is the Gateway entry point for the send/enumerate delivery path.
func (c *CmuxSignaler) runCmux(ctx context.Context, cmd CmuxCommand, args ...string) ([]byte, error) {
	out, err := c.run(ctx, "cmux", args...)
	if err == nil {
		return out, nil
	}
	return out, newCmuxError(ctx, cmd, err)
}

// newCmuxError builds a *CmuxError from a failed cmux run. It extracts the exit
// status and stderr structurally from any *exec.ExitError, and determines
// TimedOut from the run context (DeadlineExceeded) — with a string-signature
// fallback for the injectable test seam, which returns synthetic errors without
// a cancelled context or a real ProcessState. Determining timeout-ness ONCE
// here, at the exec boundary, is exactly the point: downstream consumers read a
// bool field instead of re-parsing "signal: killed"/"deadline exceeded" text.
func newCmuxError(ctx context.Context, cmd CmuxCommand, err error) *CmuxError {
	ce := &CmuxError{Command: cmd, Underlying: err}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ce.ExitStatus = ee.ExitCode()
		ce.Stderr = string(bytes.TrimSpace(ee.Stderr))
	}
	ce.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) ||
		hasTimeoutSignature(strings.ToLower(err.Error()))
	return ce
}

// ClassifyCmuxFailure maps a cmux command failure onto a bounded reason label
// plus a timed_out flag. When err is a typed Gateway failure (*NoCmuxSurfaceError
// or *CmuxError, possibly wrapped) it classifies via a TYPE SWITCH over the
// Command + TimedOut fields; the enumerate/send-key path wins over the generic
// timeout reason (pg2-qxo5) while timed_out is reported orthogonally (pg2-zixk).
// Otherwise — an error that did not come through the Gateway as a typed value,
// e.g. one reconstructed from a string that crossed the cmux-bridge gRPC
// boundary — it falls back to substring matching, the documented degraded path.
func ClassifyCmuxFailure(err error) (CmuxFailureReason, bool) {
	if err == nil {
		return ReasonUnknown, false
	}
	// A failure reconstructed from the cmux-bridge wire (DeliverResult.reason/
	// timed_out) already carries the bridge's typed classification — use it
	// directly (pg2-p1q00).
	var we *WireCmuxError
	if errors.As(err, &we) {
		return we.Reason, we.TimedOut
	}
	var ns *NoCmuxSurfaceError
	if errors.As(err, &ns) {
		return ReasonNoSurface, false
	}
	var ce *CmuxError
	if errors.As(err, &ce) {
		switch ce.Command {
		case CmuxSendKey:
			return ReasonSendKey, ce.TimedOut
		case CmuxEnumerate:
			return ReasonEnumerate, ce.TimedOut
		default: // CmuxSend (and any future non-path command)
			if ce.TimedOut {
				return ReasonTimeout, true
			}
			if containsAny(strings.ToLower(ce.Error()), "connection", "connect", "broken pipe", "refused") {
				return ReasonConnection, false
			}
			return ReasonOther, false
		}
	}
	return classifyCmuxText(err.Error())
}

// classifyCmuxText is the substring fallback and the single source of the reason
// taxonomy for errors that arrive as text. Its logic is byte-for-byte the pre-
// refactor daemon classifySendFailure + sendFailureTimedOut, preserved so the
// send_failures_total / nudge.send_failed labels do not change for the gRPC-
// transported cmux failures the daemon actually sees.
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

// hasTimeoutSignature reports whether lowercased error text carries a cmux
// timeout signature. exec.CommandContext SIGKILLs a subprocess whose context
// deadline expires; the resulting ExitError renders as "signal: killed" — the
// same root cause as "deadline exceeded" and the more common of the two in
// practice (pg2-qxo5). s MUST already be lowercased by the caller.
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
