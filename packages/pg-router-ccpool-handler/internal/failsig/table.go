package failsig

import "regexp"

// rule is one row of the classification table.
type rule struct {
	signature Signature
	// name identifies the row in tests and review. It is never returned
	// to a caller.
	name string
	// re MUST match within a single line. The evidence line is taken from
	// the line the match starts on.
	re *regexp.Regexp
}

// table is THE classification table. There is exactly one, and every caller
// goes through Classify (see the package doc, "Why one package"). Rows are
// tried from top to bottom and the first match wins, so ORDER IS
// PRECEDENCE:
//
//   - budget comes first. "session budget exceeded" is this handler's own
//     hard stop, so when it is present it explains the stop outright,
//     whatever else the text contains.
//   - Specific git-auth markers come next: the credential helper's oauth/OIDC
//     failures and ssh's publickey rejection. Each one names the credential
//     as the cause.
//   - git-network comes BEFORE the generic git-auth fallback. git prints
//     "Could not read from remote repository" after EVERY failed ssh
//     transport, network failures included ("ssh: Could not resolve
//     hostname ..." followed by that line). If the fallback came first, an
//     unresolvable host would be mislabeled as an auth failure.
//   - The generic "Could not read from remote repository" git-auth fallback
//     comes after network, for the case where no more specific marker is
//     present.
//   - mount-or-path and index-lock come last. Their markers do not overlap
//     any row above them.
//
// Every pattern is case-insensitive where the tool's capitalization varies,
// and anchored on word boundaries, so a marker inside unrelated prose (the
// word "oauth", say) does not match.
var table = []rule{
	{Budget, "session-budget-exceeded", regexp.MustCompile(`(?i)\bsession budget exceeded\b`)},

	{GitAuth, "oauth-command-timed-out", regexp.MustCompile(`(?i)\boauth command timed out\b`)},
	{GitAuth, "oidc-token-error", regexp.MustCompile(`(?i)\berror generating OIDC token\b`)},
	{GitAuth, "step-oauth-exec-failed", regexp.MustCompile(`(?i)\bexec "?step oauth"? failed\b`)},
	{GitAuth, "ssh-publickey-denied", regexp.MustCompile(`(?i)\bpermission denied \(publickey\b`)},

	{GitNetwork, "could-not-resolve-host", regexp.MustCompile(`(?i)\bcould not resolve host`)},
	{GitNetwork, "connection-timed-out", regexp.MustCompile(`(?i)\bconnection timed out\b`)},
	{GitNetwork, "connection-refused", regexp.MustCompile(`(?i)\bconnection refused\b`)},

	{GitAuth, "could-not-read-from-remote", regexp.MustCompile(`(?i)\bcould not read from remote repository\b`)},

	// mkdir on an unmounted or unwritable mount point, e.g. Go's os.MkdirAll
	// reporting "mkdir /Volumes/gitrepos: permission denied".
	{MountOrPath, "mkdir-permission-denied", regexp.MustCompile(`(?i)\bmkdir\b[^\n]*: permission denied\b`)},
	// A missing directory on a path the handler changes into or creates a
	// worktree on: Go's "chdir <p>: no such file or directory", git's
	// "cannot change to '<p>': No such file or directory", a failed
	// "worktree add", or mkdir under a missing parent. A bare "no such file
	// or directory" with none of these verbs (a missing config file, a
	// missing binary) is NOT a mount/path failure, and it stays unknown.
	{MountOrPath, "path-no-such-file", regexp.MustCompile(`(?i)\b(?:chdir|cannot change to|worktree add|mkdir)\b[^\n]*\bno such file or directory\b`)},

	// A leftover git index lock ("Unable to create '<repo>/.git/index.lock':
	// File exists."). Any mention of the lock file in failure text is a lock
	// problem.
	{IndexLock, "index-lock", regexp.MustCompile(`\bindex\.lock\b`)},
}
