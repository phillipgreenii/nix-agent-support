// Package transient classifies a handler error as a transient failure and names
// its class. It is the one place that decides what "transient" means for
// pg-router: internal/orchestrator re-runs a failed dispatch on it (bead
// pg2-yu5y2) and internal/metrics labels the failure with it (bead pg2-8o8x9d).
// It is a leaf package so both can share the test without importing each other
// (orchestrator imports core, and core imports metrics).
package transient

import (
	"context"
	"errors"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Transient handler-failure classes (bead pg2-yu5y2). They are the ONLY
// failures a role with MaxDispatchRetries > 0 re-runs; anything else (a
// deterministic failure such as a validation error or a bad exit code 1 with
// a reasoned message) is never retried, because running it again cannot help
// and would only add load. The class string is the `class` label of
// pg_router_dispatch_retries_total.
const (
	// ClassKilled: the handler subprocess was killed by a signal (os/exec
	// reports exit -1), or the handler reported that one of ITS children was
	// ("signal: killed" / "exit -1") — the shape a 30s connector timeout under
	// host overload produces.
	ClassKilled = "killed"
	// ClassDeadline: a deadline expired (context.DeadlineExceeded, or the
	// handler said so in its error text).
	ClassDeadline = "deadline"
	// ClassUnavailable: the handler or a backend it drives reported itself
	// unavailable (the connector wire taxonomy's retryable "unavailable" code).
	ClassUnavailable = "unavailable"
)

// Classify reports whether err is a transient handler failure worth one more
// bounded attempt, and which class. A busy decline never reaches here (Offer
// maps it before), and a cancelled run context is excluded by the caller.
// Matching mirrors how the failure actually surfaces: the typed
// *wireclient.ExitError for a killed handler (-1), context.DeadlineExceeded
// structurally, and the handler's own error text for what it flattened into
// the reply (its child "signal: killed" / "exit -1:", "deadline exceeded",
// pg-desk's own `"error_class":"killed"|"deadline"` stderr line the command
// handler appends to its error, the connector's "unavailable" code).
func Classify(err error) (class string, ok bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) {
		return "", false
	}
	var ee *wireclient.ExitError
	if errors.As(err, &ee) && ee.Code == -1 {
		return ClassKilled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClassDeadline, true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "signal: killed"), strings.Contains(msg, "exit -1"),
		strings.Contains(msg, `"error_class":"killed"`):
		return ClassKilled, true
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, `"error_class":"deadline"`):
		return ClassDeadline, true
	case strings.Contains(msg, "unavailable"):
		return ClassUnavailable, true
	}
	return "", false
}
