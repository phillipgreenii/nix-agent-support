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
// code and stderr, ported unchanged from the sibling backend's copy. gh
// surfaces: exit 4 + "gh auth login" (no token); exit 1 + "Bad credentials
// (HTTP 401)" (invalid token); "Requires authentication (HTTP 401)"
// (unauthenticated request). Match defensively.
func IsAuthFailure(exitCode int, stderr string) bool {
	if exitCode == 4 {
		return true
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "http 401") ||
		strings.Contains(s, "bad credentials") ||
		strings.Contains(s, "requires authentication")
}
