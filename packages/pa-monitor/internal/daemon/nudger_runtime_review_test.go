package daemon

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// TestSendFailureCounterAttrs_TimedOutBounded pins that the timed_out label is a
// bounded 2-valued attribute ("true"/"false") for every failure class, so it can
// never inflate the send_failures_total counter's cardinality — the analogue of
// the reason/session_id boundedness guard for the label pg2-zixk added.
// (pg2-gweng, from the pg2-zixk review.)
func TestSendFailureCounterAttrs_TimedOutBounded(t *testing.T) {
	errs := []string{
		"cmux enumerate: cmux --json top --processes: signal: killed",
		"cmux enumerate failed: exit status 1",
		"cmux send-key: deadline exceeded",
		"cmux send-key failed: exit status 1",
		"cmux send: signal: killed",
		"cmux send: connection refused",
		"no cmux surface found for pid 42",
		"context deadline exceeded",
		"some entirely unrecognized failure",
		"",
	}
	for _, e := range errs {
		got := sendFailureCounterAttrs(string(transcript.ErrServerError), e)["timed_out"]
		if got != "true" && got != "false" {
			t.Errorf("timed_out for %q = %q, want a bounded \"true\"/\"false\"", e, got)
		}
	}
}
