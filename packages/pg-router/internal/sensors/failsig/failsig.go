// Package failsig is a trimmed COPY of the pg-failsig module (the sensors
// module cannot import it: pg-failsig requires a newer Go than this module's
// toolchain). It keeps only what the git origin probe needs: Redact (verbatim,
// see redact.go) and Classify over the git rows of the shared table
// (git-auth, git-network, mount-or-path). Text that no row matches is Unknown.
//
// The classification rows are copied verbatim from pg-failsig's table.go;
// when that table gains a git row, copy it here.
package failsig

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Signature is a failure's cause.
type Signature string

// The signatures this copy can return.
const (
	GitAuth     Signature = "git-auth"
	GitNetwork  Signature = "git-network"
	MountOrPath Signature = "mount-or-path"
	Unknown     Signature = "unknown"
)

// MaxEvidenceLen bounds Result.Evidence, in bytes.
const MaxEvidenceLen = 300

const evidenceLead = 100

// Result is the outcome of Classify.
type Result struct {
	Signature Signature
	Evidence  string
}

type rule struct {
	signature Signature
	name      string
	re        *regexp.Regexp
}

// table is the ordered classification table; the first match wins. git-network
// stays above the generic git-auth fallback because git prints "Could not read
// from remote repository" after every failed ssh transport.
var table = []rule{
	{GitAuth, "oauth-command-timed-out", regexp.MustCompile(`(?i)\boauth command timed out\b`)},
	{GitAuth, "oidc-token-error", regexp.MustCompile(`(?i)\berror generating OIDC token\b`)},
	{GitAuth, "step-oauth-exec-failed", regexp.MustCompile(`(?i)\bexec "?step oauth"? failed\b`)},
	{GitAuth, "ssh-publickey-denied", regexp.MustCompile(`(?i)\bpermission denied \(publickey\b`)},

	{GitNetwork, "could-not-resolve-host", regexp.MustCompile(`(?i)\bcould not resolve host`)},
	{GitNetwork, "connection-timed-out", regexp.MustCompile(`(?i)\bconnection timed out\b`)},
	{GitNetwork, "connection-refused", regexp.MustCompile(`(?i)\bconnection refused\b`)},

	{GitAuth, "could-not-read-from-remote", regexp.MustCompile(`(?i)\bcould not read from remote repository\b`)},

	{MountOrPath, "mkdir-permission-denied", regexp.MustCompile(`(?i)\bmkdir\b[^\n]*: permission denied\b`)},
	{MountOrPath, "path-no-such-file", regexp.MustCompile(`(?i)\b(?:chdir|cannot change to|worktree add|mkdir)\b[^\n]*\bno such file or directory\b`)},
}

// Classify names text's failure signature; the first matching row wins.
func Classify(text string) Result {
	for _, r := range table {
		if loc := r.re.FindStringIndex(text); loc != nil {
			return Result{Signature: r.signature, Evidence: evidence(text, r, loc[0])}
		}
	}
	return Result{Signature: Unknown}
}

func evidence(raw string, r rule, rawStart int) string {
	redacted := Redact(raw)
	line := strings.TrimRight(nthLine(redacted, strings.Count(raw[:rawStart], "\n")), "\r")
	start := 0
	if loc := r.re.FindStringIndex(line); loc != nil && loc[0] > evidenceLead {
		start = loc[0] - evidenceLead
	}
	return strings.TrimSpace(cut(line, start, MaxEvidenceLen))
}

func nthLine(s string, n int) string {
	for range n {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return ""
		}
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func cut(s string, start, maxLen int) string {
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	s = s[start:]
	if len(s) <= maxLen {
		return s
	}
	end := maxLen
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
