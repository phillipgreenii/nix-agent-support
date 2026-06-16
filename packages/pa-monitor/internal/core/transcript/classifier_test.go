package transcript

import (
	"fmt"
	"testing"
	"time"
)

// apiErrorEvent returns a JSONL line for a synthetic isApiErrorMessage
// assistant event with the given error kind and message text. Shared by the
// snapshot tests (the classifier itself is tested in the claude-transcript
// module; this file only covers pa-monitor's Retryable policy + test helpers).
func apiErrorEvent(ts time.Time, kind ErrorKind, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":"%s","error":%q,"isApiErrorMessage":true,`+
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`,
		ts.UTC().Format(time.RFC3339Nano), string(kind), text)
}

// rateEvent returns a JSONL line for a legacy rate_limit_error api_error event.
func rateEvent(ts time.Time, retryInMs int64) string {
	return `{"type":"system","subtype":"api_error","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","retryInMs":` + fmt.Sprintf("%d", retryInMs) +
		`,"error":{"status":429,"error":{"type":"error","error":{"type":"rate_limit_error","message":"limit exceeded"}}}}`
}

// TestRetryable_resumesOnlyTransientClasses pins pa-monitor's auto-resume
// policy to the two transient classes and documents the deliberate tightening:
// an opaque non-network `unknown` (ClassTerminal) is NOT auto-resumed, whereas a
// network-drop `unknown` (ClassTransientNetwork) and a server_error
// (ClassTransientServer) are. rate_limit and the terminal kinds hand back.
func TestRetryable_resumesOnlyTransientClasses(t *testing.T) {
	cases := []struct {
		name string
		kind ErrorKind
		text string
		want bool
	}{
		{"server_error → resume", ErrServerError, "API Error: 500 Internal server error", true},
		{"unknown network drop → resume", ErrUnknown, "API Error: The socket connection was closed unexpectedly", true},
		{"unknown stream idle timeout → resume", ErrUnknown, "API Error: Stream idle timeout - partial response received", true},
		{"unknown opaque non-network → hand back (tightening)", ErrUnknown, "something completely unrecognized happened", false},
		{"rate_limit → hand back", ErrRateLimit, "You've hit your limit · resets 3:30pm (America/New_York)", false},
		{"authentication_failed → hand back", ErrAuthFailed, "Please run /login", false},
		{"model_not_found → hand back", ErrModelNotFound, "selected model may not exist", false},
		{"invalid_request → hand back", ErrInvalidRequest, "Prompt is too long", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &ErrorRecord{Kind: tc.kind, Text: tc.text}
			if got := Retryable(rec); got != tc.want {
				t.Errorf("Retryable(kind=%q text=%q) = %v, want %v (class=%v)", tc.kind, tc.text, got, tc.want, rec.RetryClass())
			}
		})
	}
}

// TestRetryable_nilIsFalse guards the nil-record path.
func TestRetryable_nilIsFalse(t *testing.T) {
	if Retryable(nil) {
		t.Error("Retryable(nil) = true, want false")
	}
}
