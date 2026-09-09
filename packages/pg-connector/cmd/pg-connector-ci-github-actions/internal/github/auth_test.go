package github

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsAuthFailure exercises this backend's own auth.go classifier. Unlike
// the pr-github sibling copy — which exports IsAuthFailure as a thin wrapper
// around a package-private isAuthFailure — this copy's IsAuthFailure IS the
// classifier (auth.go has no unexported twin), so it is asserted on
// directly.
func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		stderr string
		want   bool
	}{
		{"exit 4 no token", 4, "To get started with GitHub CLI, please run:  gh auth login", true},
		{"exit 4 with empty stderr still classifies", 4, "", true},
		{"bad credentials 401", 1, "HTTP 401: Bad credentials (https://api.github.com/...)", true},
		{"requires authentication 401", 1, "Requires authentication (HTTP 401)", true},
		{"could not resolve host", 1, "dial tcp: lookup api.github.com: could not resolve host", false},
		{"repo not found", 1, "GraphQL: Could not resolve to a Repository with the name 'x/y'.", false},
		{"saml enforcement 403", 1, "HTTP 403: Resource protected by organization SAML enforcement. You must grant your personal access token access to this organization.", true},
		{"insufficient scopes 403", 1, "HTTP 403: Resource not accessible by integration", true},
		{"graphql required scopes", 1, "Your token has not been granted the required scopes to execute this query.", true},
		{"bare http 403", 1, "some error (HTTP 403)", true},
		// The stderr substring check runs for ANY exit code other than 4 —
		// including 0 — since the implementation only special-cases
		// exitCode==4 before falling through to the text match. This case
		// pins that actual behavior rather than an assumed "only non-zero
		// exits classify" rule the code does not implement.
		{"stderr match wins even on a zero exit code", 0, "Bad credentials (HTTP 401)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAuthFailure(tc.exit, tc.stderr); got != tc.want {
				t.Errorf("IsAuthFailure(%d, %q) = %v, want %v", tc.exit, tc.stderr, got, tc.want)
			}
		})
	}
}

// TestIsAuthFailure_MatchesCaseInsensitively proves the strings.ToLower step
// in auth.go's implementation: a gh version whose stderr capitalizes the
// matched phrases differently must still classify as an auth failure.
func TestIsAuthFailure_MatchesCaseInsensitively(t *testing.T) {
	if !IsAuthFailure(1, "BAD CREDENTIALS (HTTP 401)") {
		t.Error("IsAuthFailure did not match an upper-cased \"Bad credentials\" stderr")
	}
	if !IsAuthFailure(1, "Requires Authentication (Http 401)") {
		t.Error("IsAuthFailure did not match a mixed-case \"HTTP 401\"/\"Requires authentication\" stderr")
	}
}

// TestErrGHAuthInvalid_IsUsableSentinel guards this copy's deliberate
// divergence from the pr-github sibling's auth.go: here ErrGHAuthInvalid is
// a plain local sentinel (errors.New), not an alias of pkg/provider/vcs's
// ErrAuthInvalid — this module has no dependency on packages/pg-pr. It must
// still behave like a normal sentinel: non-nil, with a non-empty message,
// and detectable via errors.Is once wrapped exactly as ghexec.go's command()
// wraps it (errors.Join(ErrGHAuthInvalid, err), then fmt.Errorf("%w", ...)).
func TestErrGHAuthInvalid_IsUsableSentinel(t *testing.T) {
	if ErrGHAuthInvalid == nil {
		t.Fatal("ErrGHAuthInvalid is nil")
	}
	if ErrGHAuthInvalid.Error() == "" {
		t.Error("ErrGHAuthInvalid.Error() is empty")
	}

	wrapped := fmt.Errorf("gh pr view: no usable GitHub credential: %w",
		errors.Join(ErrGHAuthInvalid, errors.New("keychain unavailable")))
	if !errors.Is(wrapped, ErrGHAuthInvalid) {
		t.Errorf("errors.Is(wrapped, ErrGHAuthInvalid) = false, want true (wrapped=%v)", wrapped)
	}

	if errors.Is(errors.New("some other error"), ErrGHAuthInvalid) {
		t.Error("errors.Is matched an unrelated error against ErrGHAuthInvalid")
	}
}
