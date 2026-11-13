// Package originguard is the library behind pg-origin-guard: a read-only
// Check of whether git authentication to a realm is available, plus the
// config, state store, certificate checker, presence reader and event writer
// that the CLI and the ProxyCommand wrapper share.
//
// The contract is docs/contract.md. This package covers its sections 4
// (Check and the library), 5 (state, config, events) and 6 (presence), and
// the certificate checker of rule W1. Check NEVER uses the network and NEVER
// starts a login. A failure to write state or events never changes a verdict.
package originguard

import (
	"context"
	"time"

	"github.com/phillipgreenii/x/exitcode"
)

// Exit codes of the CLI, the shared codes of the workspace x/exitcode
// package. A caller MUST treat only ExitOK as "go ahead" and MUST NOT count
// ExitUnavailable as a failure of its own work. ExitInternal (1) carries no
// branchable meaning.
const (
	ExitOK          = exitcode.OK
	ExitInternal    = exitcode.Error
	ExitUsage       = exitcode.Usage
	ExitUnavailable = exitcode.Unavailable
	ExitConfig      = exitcode.Config
)

// Reason is the closed set of verdict reasons.
type Reason string

// The closed set of reasons.
const (
	ReasonOK               Reason = "ok"
	ReasonIgnored          Reason = "ignored"
	ReasonCertExpired      Reason = "cert-expired"
	ReasonCertMissing      Reason = "cert-missing"
	ReasonAgentUnreachable Reason = "agent-unreachable"
	ReasonNoHuman          Reason = "no-human"
	ReasonBackoff          Reason = "backoff"
	ReasonAttempting       Reason = "attempting"
	ReasonAuthRejected     Reason = "auth-rejected"
	ReasonNetwork          Reason = "network"
	ReasonTimeout          Reason = "timeout"
	ReasonUnknown          Reason = "unknown"
	ReasonMountMissing     Reason = "mount-missing"
)

// Reasons returns the closed set, in contract order.
func Reasons() []Reason {
	return []Reason{
		ReasonOK, ReasonIgnored, ReasonCertExpired, ReasonCertMissing,
		ReasonAgentUnreachable, ReasonNoHuman, ReasonBackoff, ReasonAttempting,
		ReasonAuthRejected, ReasonNetwork, ReasonTimeout, ReasonUnknown,
		ReasonMountMissing,
	}
}

// Request names what to check. Exactly one of Realm, Remote and RepoPath is
// set. RepoPath also does the mount check.
type Request struct {
	Realm    string
	Remote   string
	RepoPath string
}

// Result is the answer of Check.
type Result struct {
	Realm     string
	Available bool
	Reason    Reason
	// Since is when the realm became unavailable. It is the zero time while
	// the realm is available.
	Since time.Time
	// LastError is redacted and at most MaxErrorLen bytes.
	LastError string
}

// Guard answers "is auth available now". The error is for a tool fault (or a
// usage error, see UsageError) only, never for "unavailable".
type Guard interface {
	Check(ctx context.Context, req Request) (Result, error)
}

// Clock is the time seam.
type Clock interface{ Now() time.Time }

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// IsTTY reports whether the caller has a terminal. The wrapper uses it.
type IsTTY func() bool

// UsageError marks a caller mistake (an unknown realm, no matching realm, a
// bad request). The CLI maps it to ExitUsage.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }
