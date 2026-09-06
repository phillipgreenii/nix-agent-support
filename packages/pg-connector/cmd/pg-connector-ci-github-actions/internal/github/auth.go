// Package github is this backend's token-protected `gh` CLI gateway — a
// per-backend copy of the identical mechanism in
// cmd/pg-connector-pr-github/internal/github's auth.go/token.go/ghexec.go
// trio, carried over per this packet's "same env-then-gh auth token chain
// the pg-connector-pr-github backend already uses, since both are
// GitHub-backed" binding decision [design: §4.6]. It is duplicated rather
// than imported because Go's internal/ visibility rule makes the sibling
// backend's copy unreachable from here, and because
// packages/pg-connector/go.mod MUST NOT depend on packages/pg-pr — the
// original source of both copies [design: §5.2].
//
// Only the slice pg-connector-ci-github-actions.Provider actually needs is
// ported: the token-first `gh` exec choke point (CLI/Command/Run/RunStdin)
// and the auth-failure classifier. The much larger PR-specific VCS surface
// (GetPR/ListComments/CreatePR/…) in the sibling backend's own copy has no
// analogue here and is not carried over.
package github

import (
	"errors"
	"strings"
)

// ErrGHAuthInvalid signals that gh could not authenticate (missing/invalid
// token). Callers detect it via errors.Is. Unlike the sibling backend's
// copy, this is a plain local sentinel rather than an alias of
// pkg/provider/vcs.ErrAuthInvalid — that package is pg-pr-only and this
// module does not depend on pg-pr.
var ErrGHAuthInvalid = errors.New("pg-connector-ci-github-actions: github auth invalid")

// IsAuthFailure classifies a gh failure as an auth problem from its exit
// code and stderr, ported from the sibling backend's copy. gh surfaces:
// exit 4 + "gh auth login" (no token); exit 1 + "Bad credentials
// (HTTP 401)" (invalid token); "Requires authentication (HTTP 401)"
// (unauthenticated request).
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
// the HTTP-code suffix (bead pg2-y23d4 #32; these previously fell through to
// a generic/unavailable classification instead of auth). Match defensively.
func IsAuthFailure(exitCode int, stderr string) bool {
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
