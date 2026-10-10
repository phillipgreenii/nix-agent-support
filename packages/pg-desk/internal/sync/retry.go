package sync

import (
	"errors"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Automatic retry of a recorded sync_error (bead pg2-xb6fs; operator ruling
// 2026-09-30). Before this, a recorded sync_error was re-driven by every
// `pg-desk reconcile` pass forever, whatever the failure was, and nothing
// told an operator whether a row was still being retried. The policy is:
//
//   - Classify decides whether a failure is transient (environmental: a
//     missing path, an unmounted volume, a network blip) or needs a person
//     (authentication, validation, a config or version problem). Only a
//     transient failure is retried automatically.
//   - A transient failure is retried with exponential backoff (see
//     RetryPolicy.Backoff) for at most RetryPolicy.MaxRetries automatic
//     retries. After that the row is exhausted: it stays a sync_error and is
//     not retried again automatically.
//   - The resulting store.SyncRetry is the retry indicator status, doctor
//     and serve's /metrics read.

// ErrorClass is Classify's verdict.
type ErrorClass string

const (
	// ClassTransient failures are retried automatically, within the bound.
	ClassTransient ErrorClass = "transient"
	// ClassNonTransient failures need a person and are never retried
	// automatically.
	ClassNonTransient ErrorClass = "non-transient"
)

// errUnknownMode is wrapped by Sync's error for an unrecognized sync.mode: a
// config problem, so non-transient. Its text is the message prefix Sync has
// always used.
var errUnknownMode = errors.New("sync: unknown sync.mode")

// nonTransientCodes are the pg-connector wire error codes that no retry can
// fix without a person changing credentials, input, config or the deployed
// binaries: authentication (unauthenticated), validation (invalid_argument),
// a verb or query name the backend does not know (unknown_op,
// query_not_recognized), and binary version skew (version_mismatch).
var nonTransientCodes = map[string]bool{
	"unauthenticated":      true,
	"invalid_argument":     true,
	"unknown_op":           true,
	"version_mismatch":     true,
	"query_not_recognized": true,
}

// isOpenChildRefusal reports whether detail is bd >= 1.3.1's refusal to close
// a parent that still has open children ("cannot close <id>: <N> open child
// issue(s); close children first or use --force to override"). pg-connector's
// beads backend wraps that refusal as wire code "unavailable", so the code
// alone cannot tell it from an environmental failure; the message is the
// stable discriminator. Both fragments are required so an unrelated failure
// that merely mentions children, or a different close failure, stays
// transient. Retrying cannot clear it: a child exists that the closure did not
// close, and only a person (or a fix to the closure's child discovery) lifts
// it (bead pg2-ubvmh: it burned the whole 10-retry backoff budget).
func isOpenChildRefusal(detail string) bool {
	return strings.Contains(detail, "cannot close") && strings.Contains(detail, "open child issue")
}

// Classify returns ClassNonTransient when err's chain carries a
// *ConnectorError whose wire code is in nonTransientCodes or whose detail is
// bd's open-child close refusal (isOpenChildRefusal), or is Sync's own
// unknown-sync.mode config error, and ClassTransient for everything else.
//
// Transient therefore covers pg-connector's "unavailable" code (its code for
// "this backend cannot currently be used", which is what a beads workspace
// on an unmounted volume produces: the bd exec fails with "chdir <dir>: no
// such file or directory"), a targeted "not_found" (seen when the tracker
// answers from the wrong or an empty database), a failure to exec
// pg-connector at all, a timeout, undecodable output, a store error, and a
// failure in an earlier pipeline stage (gather) during a retry. Defaulting
// the unrecognized to transient is deliberate: the retry bound caps its
// cost, while defaulting it to non-transient would stop self-healing for
// environmental failures nobody anticipated here.
func Classify(err error) ErrorClass {
	if errors.Is(err, errUnknownMode) {
		return ClassNonTransient
	}
	var ce *ConnectorError
	if errors.As(err, &ce) && (nonTransientCodes[ce.Code] || isOpenChildRefusal(ce.Detail)) {
		return ClassNonTransient
	}
	return ClassTransient
}

// RetryPolicy bounds automatic retries. Build it with RetryPolicyFor.
type RetryPolicy struct {
	// MaxRetries is the number of automatic retries after the original
	// failure; 0 disables automatic retry.
	MaxRetries int
	// InitialBackoff is the wait before the first retry; each later retry
	// waits twice the previous one, capped at MaxBackoff.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// RetryPolicyFor resolves cfg's sync.retry block, falling back to the
// defaults (config.DefaultSyncRetry*) for a nil config or an invalid block —
// config load rejects an invalid block, so that fallback is reached only by
// a Config built in code.
func RetryPolicyFor(cfg *config.Config) RetryPolicy {
	def := RetryPolicy{
		MaxRetries:     config.DefaultSyncRetryMaxRetries,
		InitialBackoff: config.DefaultSyncRetryInitialBackoff,
		MaxBackoff:     config.DefaultSyncRetryMaxBackoff,
	}
	if cfg == nil {
		return def
	}
	maxRetries, initialBackoff, maxBackoff, err := cfg.Sync.Retry.Resolve()
	if err != nil {
		return def
	}
	return RetryPolicy{MaxRetries: maxRetries, InitialBackoff: initialBackoff, MaxBackoff: maxBackoff}
}

// Backoff is the wait after the attempts-th consecutive failure before the
// next retry: InitialBackoff * 2^(attempts-1), capped at MaxBackoff. With the
// defaults: 1m, 2m, 4m, 8m, 16m, then 30m.
func (p RetryPolicy) Backoff(attempts int) time.Duration {
	d := p.InitialBackoff
	for i := 1; i < attempts; i++ {
		if d >= p.MaxBackoff/2 { // doubling would reach the cap (and cannot overflow)
			d = p.MaxBackoff
			break
		}
		d *= 2
	}
	if d > p.MaxBackoff {
		d = p.MaxBackoff
	}
	return d
}

// NextState is the retry state after one more failed run, given the state
// recorded before it (the zero value when none was). It counts the attempt,
// classifies err, and either schedules the next retry or records why there
// will be none.
func (p RetryPolicy) NextState(prev store.SyncRetry, err error, now time.Time) store.SyncRetry {
	now = now.UTC()
	next := store.SyncRetry{
		Attempts:     prev.Attempts + 1,
		MaxRetries:   p.MaxRetries,
		LastFailedAt: now.Format(time.RFC3339),
	}
	switch {
	case Classify(err) == ClassNonTransient:
		next.State = store.SyncRetryNonTransient
	case next.Retries() >= p.MaxRetries:
		next.State = store.SyncRetryExhausted
	default:
		next.State = store.SyncRetryRetrying
		next.NextRetryAt = now.Add(p.Backoff(next.Attempts)).Format(time.RFC3339)
	}
	return next
}

// RetryDue reports whether reconcile should re-drive a sync_error row now.
// A row with no recorded state (found=false) is due, so a row recorded
// before retry state existed gets one re-drive, which then records proper
// state. A retrying row is due once NextRetryAt has passed (or when it cannot
// be parsed); an exhausted or non-transient row is never due.
func RetryDue(r store.SyncRetry, found bool, now time.Time) bool {
	if !found {
		return true
	}
	if r.EffectiveState() != store.SyncRetryRetrying {
		return false
	}
	if r.NextRetryAt == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, r.NextRetryAt)
	if err != nil {
		return true
	}
	return !now.Before(at)
}
