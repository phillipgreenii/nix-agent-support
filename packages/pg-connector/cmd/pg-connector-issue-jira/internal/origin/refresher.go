package origin

import (
	"context"
	"errors"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
)

// Why a Refresher is asked to refresh the token.
const (
	// ReasonUnauthenticated: pjira answered 401 (or auth-status failed).
	ReasonUnauthenticated = "unauthenticated"
	// ReasonTokenFileMissing: pjira could not read its token file.
	ReasonTokenFileMissing = "token_file_missing"
)

// tokenFileMissingText is pjira's fixed text when its token file cannot be
// read (the file was deleted underneath the daemon).
const tokenFileMissingText = "pjira: read token file"

// ErrTokenDegraded is the cause of a call refused because the Refresher is
// degraded. Its text deliberately carries neither "status 401" nor
// ": unauthenticated", so the event log does not classify the refusal as an
// auth failure.
var ErrTokenDegraded = errors.New("jira call refused: the daemon's credential guard is degraded, pjira was not launched")

// IsDegradedRefusal reports whether an error message (a wire error's text)
// is the refusal of a call while the guard is degraded. The event log uses it
// to keep such a refusal out of failure_class=auth: the triggering 401 already
// fired the auth-failing alert, and the degraded gauge alert covers the rest.
func IsDegradedRefusal(msg string) bool {
	return strings.Contains(msg, ErrTokenDegraded.Error())
}

// Refresher is the credential guard the Runner consults (a Guard/Decorator
// seam; tokenguard.Guard implements it).
type Refresher interface {
	// Degraded reports whether calls must be refused without launching pjira.
	Degraded() bool
	// Generation is the count of token file writes so far.
	Generation() uint64
	// Refresh is called after a failed call that launched under generation
	// used. It reports whether the call should be retried once.
	Refresh(ctx context.Context, used uint64, reason string) bool
	// RejectedAfterRetry is called when the retried call failed with a 401
	// again: the token now in the file was rejected.
	RejectedAfterRetry()
}

// refreshReason returns why a failed call should make the Refresher refresh,
// or "" when it should not. Only a 401, a failed auth-status (UNAUTHENTICATED
// state or a 401), or pjira's missing-token-file text qualify; a 403 never
// does.
func refreshReason(args []string, out, text string) string {
	switch {
	case strings.Contains(text, tokenFileMissingText):
		return ReasonTokenFileMissing
	case internal.IsPjira401(text):
		return ReasonUnauthenticated
	case len(args) > 0 && args[0] == "auth-status" &&
		eventlog.NormalizeAuthState(out, true) == "UNAUTHENTICATED":
		return ReasonUnauthenticated
	}
	return ""
}
