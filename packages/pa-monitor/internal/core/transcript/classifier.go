package transcript

import (
	ct "github.com/phillipgreenii/claude-transcript"
)

// The error-classification primitives now live in the shared claude-transcript
// module (the single source of truth across pa-monitor, ccpool, and pr-pool).
// This file re-exports them under the local `transcript` package so the existing
// pa-monitor call sites (transcript.ErrorRecord, transcript.ErrUnknown, …) keep
// compiling unchanged, and adds pa-monitor's own retry POLICY on top of the
// library's neutral RetryClass.

// ErrorKind and ErrorRecord alias the shared types. ErrorRecord NO LONGER
// carries an IsRetryable field: retryability is now a derived policy (Retryable
// below) plus a separately-tracked escalation flag (Snapshot.LastErrorRetryable
// / SessionEnrichment.LastErrorRetryable), since the daemon flips it on
// escalation independently of the record's intrinsic class.
type (
	ErrorKind   = ct.ErrorKind
	ErrorRecord = ct.ErrorRecord
	RetryClass  = ct.RetryClass
)

const (
	ErrRateLimit      = ct.ErrRateLimit
	ErrUnknown        = ct.ErrUnknown
	ErrServerError    = ct.ErrServerError
	ErrInvalidRequest = ct.ErrInvalidRequest
	ErrAuthFailed     = ct.ErrAuthFailed
	ErrModelNotFound  = ct.ErrModelNotFound
)

const (
	ClassTerminal         = ct.ClassTerminal
	ClassTransientServer  = ct.ClassTransientServer
	ClassTransientNetwork = ct.ClassTransientNetwork
	ClassRateLimited      = ct.ClassRateLimited
)

// LastAPIError, LastSubagentError, and RateLimitPause are re-exported as-is.
var (
	LastAPIError      = ct.LastAPIError
	LastSubagentError = ct.LastSubagentError
	RateLimitPause    = ct.RateLimitPause
)

// Retryable is pa-monitor's auto-resume POLICY over the library's neutral
// RetryClass: pa-monitor's nudger resumes the two transient classes
// (ClassTransientServer, ClassTransientNetwork) and hands back everything else.
//
// This is a slight, deliberate TIGHTENING of the previous predicate (which
// retried every `unknown` regardless of text): an opaque non-network `unknown`
// — one whose text matches no connection-drop pattern → ClassTerminal — is no
// longer auto-resumed. The rate-limit pause is handled separately via
// RateLimitPause, unchanged.
func Retryable(rec *ErrorRecord) bool {
	if rec == nil {
		return false
	}
	switch rec.RetryClass() {
	case ClassTransientServer, ClassTransientNetwork:
		return true
	default:
		return false
	}
}
