package transcript

import "time"

// ErrorKind enumerates the `error` field values seen on synthetic
// isApiErrorMessage events emitted by Claude Code. Kept as a closed
// allowlist so retryability is unambiguous.
type ErrorKind string

const (
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrUnknown        ErrorKind = "unknown"
	ErrServerError    ErrorKind = "server_error"
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthFailed     ErrorKind = "authentication_failed"
)

// IsRetryable reports whether the disrupt producer treats this kind as
// auto-nudgeable. Only transport-level (unknown) and transient-server
// (server_error) kinds qualify.
func (k ErrorKind) IsRetryable() bool {
	return k == ErrUnknown || k == ErrServerError
}

// ErrorRecord is the most recent isApiErrorMessage observed in a session
// transcript. IsTerminal is true iff no non-synthetic user/assistant
// event follows in the JSONL.
type ErrorRecord struct {
	Kind        ErrorKind
	Text        string
	At          time.Time
	IsTerminal  bool
	IsRetryable bool
}
