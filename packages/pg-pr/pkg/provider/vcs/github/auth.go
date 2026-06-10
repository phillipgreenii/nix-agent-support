package github

import (
	"errors"
	"strings"
)

// ErrGHAuthInvalid signals that gh could not authenticate (missing/invalid
// token). Callers detect it via errors.Is to trigger the daemon's restart-to-
// refresh escalation.
var ErrGHAuthInvalid = errors.New("github: gh authentication failed")

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
