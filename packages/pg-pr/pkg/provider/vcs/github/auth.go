package github

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ErrGHAuthInvalid signals that gh could not authenticate (missing/invalid
// token). Callers detect it via errors.Is to trigger the daemon's restart-to-
// refresh escalation. It is an alias for the provider-agnostic
// vcs.ErrAuthInvalid sentinel so internal/sync can match on the latter without
// importing this concrete provider (errors.Is(err, vcs.ErrAuthInvalid) holds
// for any error wrapping ErrGHAuthInvalid).
var ErrGHAuthInvalid = vcs.ErrAuthInvalid

// isAuthFailure classifies a gh failure as an auth problem from its exit code
// and stderr. gh 2.93 surfaces: exit 4 + "gh auth login" (no token); exit 1 +
// "Bad credentials (HTTP 401)" (invalid token); "Requires authentication
// (HTTP 401)" (unauthenticated request). Match defensively.
func isAuthFailure(exitCode int, stderr string) bool {
	if exitCode == 4 {
		return true
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "http 401") ||
		strings.Contains(s, "bad credentials") ||
		strings.Contains(s, "requires authentication")
}
