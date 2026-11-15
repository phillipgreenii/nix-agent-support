// Package tokenguard owns the Jira token lifecycle and the degraded flag of
// the daemon. The Guard is a Guard/Decorator collaborator of origin.Runner
// (it implements origin.Refresher): it reads the login keychain once at
// Start, hands the token to pjira through the tokenfile Store, re-pulls it
// only when pjira answers 401 (or the token file vanished), and degrades the
// daemon when the keychain holds the token Jira already rejected.
//
// Recovery from degraded token_rejected is a daemon restart only: there is no
// recheck timer. A keychain read error degrades with token_unavailable and is
// retried after 1, 2 and 5 minutes, then waits for a restart.
//
// The Guard keeps only SHA-256 digests of the token (last written, last
// rejected), never the token itself. Neither the token nor a digest is ever
// logged, returned, recorded or passed in argv.
package tokenguard
