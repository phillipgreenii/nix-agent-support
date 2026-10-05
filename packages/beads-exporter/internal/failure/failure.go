// Package failure defines the closed enumeration of reasons a collection pass
// can fail, and the typed error that carries one. The reason is the only part
// of a failure that ever becomes a metric label; the wrapped error text is for
// logs and never leaves the process as a label.
package failure

import "errors"

// Reason is a member of the closed failure-reason enumeration.
type Reason string

// The full reason enumeration. TranscriptError is first emitted by the
// stranded-claim pass, which is not part of the main and throughput passes.
const (
	StaleIssuesJSONL Reason = "stale_issues_jsonl"
	Timeout          Reason = "timeout"
	BDError          Reason = "bd_error"
	SchemaSkew       Reason = "schema_skew"
	ParseError       Reason = "parse_error"
	TranscriptError  Reason = "transcript_error"
)

// All returns every reason in declaration order.
func All() []Reason {
	return []Reason{StaleIssuesJSONL, Timeout, BDError, SchemaSkew, ParseError, TranscriptError}
}

// Valid reports whether r is a member of the enumeration.
func (r Reason) Valid() bool {
	for _, k := range All() {
		if r == k {
			return true
		}
	}
	return false
}

// Error is an error classified with a Reason.
type Error struct {
	Reason Reason
	Op     string
	Err    error
}

// Error implements error. The text is for logs only.
func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Reason) + ": " + e.Op
	}
	return string(e.Reason) + ": " + e.Op + ": " + e.Err.Error()
}

// Unwrap exposes the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// New builds a classified error.
func New(reason Reason, op string, err error) *Error {
	return &Error{Reason: reason, Op: op, Err: err}
}

// ReasonOf extracts the reason from err. An error that carries no classified
// Error defaults to BDError so the label value always stays inside the enum.
func ReasonOf(err error) Reason {
	var fe *Error
	if errors.As(err, &fe) && fe.Reason.Valid() {
		return fe.Reason
	}
	return BDError
}
