package github

import "testing"

func TestIsAuthFailure(t *testing.T) {
	cases := []struct {
		name   string
		exit   int
		stderr string
		want   bool
	}{
		{"exit 4 no token", 4, "To get started with GitHub CLI, please run:  gh auth login", true},
		{"bad credentials 401", 1, "HTTP 401: Bad credentials (https://api.github.com/...)", true},
		{"requires authentication 401", 1, "Requires authentication (HTTP 401)", true},
		{"could not resolve host", 1, "dial tcp: lookup api.github.com: could not resolve host", false},
		{"repo not found", 1, "GraphQL: Could not resolve to a Repository with the name 'x/y'.", false},
		{"saml enforcement 403", 1, "HTTP 403: Resource protected by organization SAML enforcement. You must grant your personal access token access to this organization.", true},
		{"insufficient scopes 403", 1, "HTTP 403: Resource not accessible by integration", true},
		{"graphql required scopes", 1, "Your token has not been granted the required scopes to execute this query.", true},
		{"bare http 403", 1, "some error (HTTP 403)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAuthFailure(tc.exit, tc.stderr); got != tc.want {
				t.Errorf("IsAuthFailure(%d, %q) = %v, want %v", tc.exit, tc.stderr, got, tc.want)
			}
		})
	}
}
