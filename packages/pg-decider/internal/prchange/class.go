// Package prchange is the composed command a `pr-change` role runs for one
// PR id (PR lane refactor design 6.1, "Hydrate then decide"): it takes a
// per-PR lock, hydrates the PR with `pg-desk pr refresh <id>`, refuses an
// unhydrated view, and then runs `pg-decider plan|apply pr <id>`, in
// sequence, so the ordering is guaranteed by construction and pg-desk stays
// independent of the decider.
//
// The package execs pg-desk, pg-decider and pg-connector as subprocesses
// (never imports pg-desk or pg-connector). pg-router owns every clock (ADR
// 0077, S30), so nothing here sleeps and retries: a retryable condition
// exits non-zero with a failure line that carries the router's `unavailable`
// class, and the router re-runs the delivery.
package prchange

import (
	"fmt"
	"regexp"
	"strings"
)

// The two modes of the composed command, the same words the decider's
// subcommands use.
const (
	ModePlan  = "plan"
	ModeApply = "apply"
)

// The stages a failure names. `lock` is the wait for the per-PR lock, before
// any work.
const (
	StageLock    = "lock"
	StageHydrate = "hydrate"
	StageDecide  = "decide"
)

// The failure classes (`error_class`). The first five are pg-desk's refresh
// classes (docs/behavior/pg-desk/refresh.md), `unhydrated` is set by this
// command's fail-closed check, and the last two classify the decider's exit.
const (
	ClassNotFound        = "not_found"
	ClassTransient       = "transient"
	ClassDegraded        = "degraded"
	ClassUnhydrated      = "unhydrated"
	ClassUnauthenticated = "unauthenticated"
	ClassHydrateError    = "hydrate_error"
	ClassDecidePartial   = "decide_partial"
	ClassDecideFailed    = "decide_failed"
)

// Exit codes of the composed command (published in
// docs/behavior/pg-decider/pr-change.md). None is 1 (reserved for unexpected
// errors) or 9 (the router's "busy, retry" code); the failing stage and class
// are named by the code.
const (
	ExitOK = 0
	// ExitUsage: bad arguments or an id that is neither accepted form.
	ExitUsage = 64

	// Stage lock.
	ExitLockTimeout = 40 // transient: the per-PR lock was not free within the wait
	ExitLockError   = 41 // hydrate_error shape: the lock file cannot be created or opened

	// Stage hydrate.
	ExitHydrateTransient       = 20
	ExitHydrateDegraded        = 21
	ExitHydrateNotFound        = 22
	ExitHydrateUnauthenticated = 23
	ExitHydrateError           = 24
	ExitUnhydrated             = 25

	// Stage decide.
	ExitDecidePartial = 30
	ExitDecideFailed  = 31
)

// FailureSpec is one row of the published exit-code table.
type FailureSpec struct {
	Code      int
	Stage     string
	Class     string
	Retryable bool
}

// Table is the exit-code table of the failures, in code order within each
// stage. The documentation page carries the same rows (a test compares them).
var Table = []FailureSpec{
	{ExitHydrateTransient, StageHydrate, ClassTransient, true},
	{ExitHydrateDegraded, StageHydrate, ClassDegraded, true},
	{ExitHydrateNotFound, StageHydrate, ClassNotFound, false},
	{ExitHydrateUnauthenticated, StageHydrate, ClassUnauthenticated, false},
	{ExitHydrateError, StageHydrate, ClassHydrateError, false},
	{ExitUnhydrated, StageHydrate, ClassUnhydrated, false},
	{ExitDecidePartial, StageDecide, ClassDecidePartial, true},
	{ExitDecideFailed, StageDecide, ClassDecideFailed, false},
	{ExitLockTimeout, StageLock, ClassTransient, true},
	{ExitLockError, StageLock, ClassHydrateError, false},
}

// specFor returns the table row of (stage, class).
func specFor(stage, class string) FailureSpec {
	for _, s := range Table {
		if s.Stage == stage && s.Class == class {
			return s
		}
	}
	// Unreachable for the callers in this package; a safe non-retryable row.
	return FailureSpec{Code: ExitHydrateError, Stage: stage, Class: class}
}

// The router classifies a failure by a case-insensitive substring match on its
// text (pg-router orchestrator/retry.go): any of these makes a delivery
// retryable. A retryable failure line carries the first, and a non-retryable
// line carries none of them, including when it re-wraps upstream text.
const retryWord = "unavailable"

var retryWords = []string{"unavailable", "deadline exceeded", "signal: killed"}

var retryWordReplacer = func() *regexp.Regexp {
	parts := make([]string, len(retryWords))
	for i, w := range retryWords {
		parts[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(parts, "|"))
}()

// scrub rewrites every router retry word in upstream text to a neutral
// phrase, so a non-retryable failure that re-wraps upstream text is never
// retried by the router.
func scrub(s string) string {
	return retryWordReplacer.ReplaceAllStringFunc(s, func(m string) string {
		switch strings.ToLower(m) {
		case "unavailable":
			return "not reachable"
		case "deadline exceeded":
			return "timed out"
		default:
			return "stopped by a signal"
		}
	})
}

// oneLine folds s to a single line of at most max runes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "..."
	}
	return s
}

// failureLine renders the single stderr line of a failure:
//
//	pg-pr-change: <what> (stage=<stage>, class=<class>): <detail>: run <next command>
//
// A retryable class puts the router's `unavailable` word in front of the
// detail; a non-retryable one has upstream text scrubbed of every router
// retry word. The line always ends with the next command to run.
func failureLine(spec FailureSpec, what, detail, next string) string {
	detail = oneLine(detail, 300)
	if spec.Retryable {
		detail = retryWord + ", the router re-runs this delivery: " + detail
	} else {
		detail = scrub(detail)
	}
	detail = strings.TrimRight(strings.TrimSpace(detail), ":")
	return fmt.Sprintf("pg-pr-change: %s (stage=%s, class=%s): %s: run %s", scrub2(spec, what), spec.Stage, spec.Class, detail, scrub2(spec, next))
}

// scrub2 scrubs s only for a non-retryable row.
func scrub2(spec FailureSpec, s string) string {
	if spec.Retryable {
		return s
	}
	return scrub(s)
}
