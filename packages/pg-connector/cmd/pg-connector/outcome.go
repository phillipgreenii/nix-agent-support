// outcome.go: pg-connector's own outcome-reporting helper.
//
// This is where pg-connector's CLI exit code is computed. A capability's
// own CLI verb group (e.g. a sibling packet's pr verb group) supplies this
// helper with the raw per-call result or error (including which sentinel
// it was, if any); it does not independently decide 0/4/1 or 0/2/3 itself
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

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// SourceStatus is one row's status in a fan-out response's sources[] array.
type SourceStatus string

const (
	// SourceSucceeded: the source answered normally.
	SourceSucceeded SourceStatus = "succeeded"
	// SourceDegraded: the source answered, but with a failure/bad state.
	SourceDegraded SourceStatus = "degraded"
	// SourceDisabled: the source does not implement/support this op at all
	// (a well-formed "not applicable," not a failure).
	SourceDisabled SourceStatus = "disabled"
)

// SourceResult is one row in a fan-out response's sources[] array: one row
// per source queried, never collapsed. Count is that source's own raw
// pre-merge count; for a fan-out op with no natural item count of its own
// (auth_status/capabilities), Count MAY be 0 — an implementer choice, not a
// design-pinned value.
type SourceResult struct {
	Source string       `json:"source"`
	Status SourceStatus `json:"status"`
	Count  int          `json:"count"`
	Reason string       `json:"reason,omitempty"`
}

// FanOutOutcome is the response envelope for a fan-out op: one that queries
// every registered backend of a type/capability at once (e.g. auth status
// or config validate).
type FanOutOutcome struct {
	Sources []SourceResult `json:"sources"`
}

// ExitCode returns this outcome's pg-connector CLI exit code: 0 (all
// succeeded), 2 (degraded/partial — at least one source did not succeed,
// but at least one did), or 3 (total failure — no source succeeded,
// including the case of zero sources queried). 1 is never returned here —
// it is reserved for the CLI's own generic/unexpected-failure path outside
// this helper.
func (o FanOutOutcome) ExitCode() int {
	succeeded, other := 0, 0
	for _, s := range o.Sources {
		if s.Status == SourceSucceeded {
			succeeded++
		} else {
			other++
		}
	}
	switch {
	case other == 0 && succeeded > 0:
		return 0
	case succeeded == 0:
		return 3
	default:
		return 2
	}
}

// TargetedExitCode computes pg-connector's own CLI exit code for a targeted
// op (one that resolves to a single registered backend) from the raw
// per-call error: 0 (success), 4 (not_found — a well-formed negative
// answer), or 1 (any other error, or a CLI-level failure before a
// well-formed response was produced).
func TargetedExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		return 4
	}
	return 1
}
