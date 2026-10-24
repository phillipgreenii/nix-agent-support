package prchange

import (
	"fmt"
	"regexp"
	"strings"
)

// idRe is the accepted PR reference: <owner>/<repo>#<number>.
var idRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[0-9]+$`)

// retryWindowSuffix is the router's retry-window id suffix: the source adapter
// gives every item under a retry window the id `<entity>@<12 hex>`.
var retryWindowSuffix = regexp.MustCompile(`@[0-9a-fA-F]{12}$`)

// NormalizeID returns the normalized PR id of raw, which is either
// `<repo>#<n>` (`<owner>/<repo>#<n>`) or the router's `<entity>@<12 hex>`
// form. The suffix is dropped, so both forms of one PR give the same id, and
// so the same lock. Any other reference form is an error: the lock key MUST
// be one string per PR, and a URL or a bare number cannot be normalized
// without the pg-desk configuration.
func NormalizeID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	id = retryWindowSuffix.ReplaceAllString(id, "")
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("%q is not a PR id: want <owner>/<repo>#<number>, optionally followed by @<12 hex digits>", raw)
	}
	return id, nil
}
