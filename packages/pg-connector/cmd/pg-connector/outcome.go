// outcome.go: pg-connector's own outcome-reporting helper.
//
// This is where pg-connector's CLI exit code is computed. A capability's
// own CLI verb group (e.g. a sibling packet's pr verb group) supplies this
// helper with the raw per-call result or error (including which sentinel
// it was, if any); it does not independently decide 0/4/8/3/2/1 or 0/5/3 itself
// — that classification logic lives exactly once, here, so every
// capability's verbs get identical exit-code behaviour for identical
// outcomes.
//
// These are pg-connector's OWN CLI exit codes, a different layer from the
// wire protocol's per-backend 0/1 exec exit codes (pkg/scriptout), and MUST
// NOT be confused with or built from them.
package main

import (
	"errors"

	"github.com/phillipgreenii/x/exitcode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The fan-out outcome types are defined once, in the published client
// package; the umbrella encodes with the same types a consumer decodes.
type (
	// SourceStatus is one row's status in a fan-out response's sources[] array.
	SourceStatus = client.SourceStatus
	// SourceResult is one row in a fan-out response's sources[] array.
	SourceResult = client.SourceResult
	// FanOutOutcome is the response envelope for a fan-out op; its ExitCode
	// method (0/5/3) lives with the type.
	FanOutOutcome = client.FanOutOutcome
)

const (
	SourceSucceeded = client.SourceSucceeded
	SourceDegraded  = client.SourceDegraded
	SourceDisabled  = client.SourceDisabled
)

// configError marks a failure to load or validate the pg-connector config
// (no file found, unreadable, unparseable, an unrecognised key, or a failed
// Validate). It classifies as exitcode.Config (7) in TargetedExitCode and in
// run(); dispatch-level failures (no backend registered, ambiguous
// registration) are not config load/validation failures and stay generic.
type configError struct{ err error }

func (e *configError) Error() string { return e.err.Error() }
func (e *configError) Unwrap() error { return e.err }

// asConfigError wraps err as a *configError (nil stays nil).
func asConfigError(err error) error {
	if err == nil {
		return nil
	}
	return &configError{err: err}
}

// TargetedExitCode computes pg-connector's own CLI exit code for a targeted
// op (one that resolves to a single registered backend) from the raw
// per-call error: 0 (success), 4 NotFound (not_found, a well-formed negative
// answer), 8 Denied (unauthenticated), 3 Unavailable (unavailable), 2 Usage
// (invalid_argument, query_not_recognized), 7 Config (config load or
// validation failure), or 1 (any other error, or a
// CLI-level failure before a well-formed response was produced). The wire
// error names are unchanged; only the process exit code is classified.
func TargetedExitCode(err error) int {
	switch {
	case err == nil:
		return exitcode.OK
	case isConfigError(err):
		return exitcode.Config
	case errors.Is(err, scriptout.ErrNotFound):
		return exitcode.NotFound
	case errors.Is(err, scriptout.ErrUnauthenticated):
		return exitcode.Denied
	case errors.Is(err, scriptout.ErrUnavailable):
		return exitcode.Unavailable
	case errors.Is(err, scriptout.ErrInvalidArgument), errors.Is(err, scriptout.ErrQueryNotRecognized):
		return exitcode.Usage
	}
	return exitcode.Error
}

// isConfigError reports whether err is (or wraps) a config load/validation
// failure.
func isConfigError(err error) bool {
	var ce *configError
	return errors.As(err, &ce)
}
