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

// IsAuthFailure is the exported form of isAuthFailure, for the sites that build
// their own gh command via CLI.Command and therefore classify the exit
// themselves (internal/auth, internal/branch, internal/worktree,
// pkg/provider/cicd/ghactions). Sharing the classifier keeps
// errors.Is(err, vcs.ErrAuthInvalid) true for the same gh failures everywhere,
// which is what the daemon's fail-fast preflight and poll-side escalation key
// on.
func IsAuthFailure(exitCode int, stderr string) bool { return isAuthFailure(exitCode, stderr) }

// isAuthFailure classifies a gh failure as an auth problem from its exit code
// and stderr. gh 2.93 surfaces: exit 4 + "gh auth login" (no token); exit 1 +
// "Bad credentials (HTTP 401)" (invalid token); "Requires authentication
// (HTTP 401)" (unauthenticated request).
//
// A token that authenticates but can no longer act — an expired/
// un-reauthorized SSO session, or a token missing required OAuth scopes —
// surfaces as HTTP 403 rather than 401: "Resource protected by organization
// SAML enforcement. You must grant your personal access token access to
// this organization." (SSO), "Resource not accessible by integration"
// (insufficient scopes for an App-style token), or a GraphQL "...has not
// been granted the required scopes..." message. gh formats all of these the
// same way the 401 messages above are formatted, with a trailing
// "(HTTP 403)" — the bare "http 403" check below already covers them, and
// the specific phrases are matched too, redundantly, in case gh ever omits
// the HTTP-code suffix (ported from pg-connector-pr-github, bead pg2-y23d4
// #32 — these previously fell through to a generic/unavailable
// classification instead of auth). Match defensively.
func isAuthFailure(exitCode int, stderr string) bool {
	if exitCode == 4 {
		return true
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "http 401") ||
		strings.Contains(s, "bad credentials") ||
		strings.Contains(s, "requires authentication") ||
		strings.Contains(s, "http 403") ||
		strings.Contains(s, "saml enforcement") ||
		strings.Contains(s, "not accessible by integration") ||
		strings.Contains(s, "required scopes")
}
