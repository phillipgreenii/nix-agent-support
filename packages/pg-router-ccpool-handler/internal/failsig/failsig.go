// Package failsig turns a failure's text into one failure signature from a
// closed set, plus a short evidence snippet that is redacted before it is
// cut.
//
// # Why one package
//
// A failed handler session, or a check the handler runs before launching one,
// leaves only text behind: a git stderr, the tail of a session transcript.
// More than one caller in this module needs to turn that text into a cause.
// One records why a dispatched session failed. Another decides whether a
// route's git origin is reachable. Those callers MUST agree on what a given
// text means, so the patterns live in ONE table (table.go) and every caller
// goes through [Classify]. A caller MUST NOT keep a second table of its own.
//
// Naming a failure's cause is a judgment about the work, not an observed
// session fact, so it lives here on the handler side and never in ccpool
// (`phillipgreenii-nix-agent-support` ADR 0015's "Decision": ccpool reports
// observed session facts only).
//
// # The table
//
// table.go holds one ordered slice of rows, each a {signature, name, regexp}
// triple. [Classify] tries the rows from top to bottom against the text, and
// the FIRST row that matches anywhere in the text wins. Row order is therefore
// precedence, not position in the text. Text that no row matches is
// [Unknown], never the empty string. That includes text that shows success
// even though the process exited non-zero: the classifier sees only text, and
// a success message matches no failure row.
//
// Every row matches within a single line; none may span a newline. The
// evidence is taken from the line the match starts on (see Redaction below).
//
// The patterns match English git, ssh, and Go error text. A localized message
// falls through to [Unknown], which is the same result as having no
// classifier at all, so a missed locale never makes things worse.
//
// # Adding a signature
//
//  1. Add the constant to the Signature block below, and add it to the
//     signatures slice as well. The set is closed: TestSignaturesIsClosedSet
//     pins its exact contents.
//  2. Add its row or rows to table.go in precedence position. Read the ordering
//     notes there first. A broad row placed above a specific one silently
//     changes the answer for text that matches both.
//  3. In failsig_test.go, add at least one positive case and one negative
//     (near-miss) case for EACH new row, keyed by the row's name.
//     TestEveryRowHasPositiveAndNegativeCases fails if a row lacks either.
//  4. Update the closed set named in docs/behavior/invariants.md's INV-CCH-9.
//
// Adding a row to an existing signature is steps 2 and 3 only.
//
// # Redaction
//
// [Redact] masks credentials: URL userinfo, Bearer tokens, provider-prefixed
// tokens, generic 32+ character hex/base64 runs, and PEM/OpenSSH private-key
// blocks. [Classify] redacts the WHOLE input before it picks or cuts any line.
// Cutting first could split a credential into a fragment that no pattern
// recognizes any more, for example a 20-character tail of a hex token, and
// that fragment would then leak. Redaction keeps every newline, so the line a
// raw match starts on is the same line number in the redacted text.
// Evidence is that redacted line, windowed around the match and cut to at
// most [MaxEvidenceLen] bytes.
//
// # Exits
//
// [ClassifyExit] is [Classify] for a session that exited before completing.
// It also takes the ccpool session facts observed at exit ([ExitFacts]), and
// the session-exit rows at the end of the table name the exit from them when
// no transcript text did. Its evidence is never empty (see exit.go).
//
// Redaction fails closed. A long path or identifier that is lexically a
// 32+ character run is masked too. That over-redaction is deliberate: the
// Signature is the fact that matters, the Evidence is only a debugging aid, and
// a caller that needs the path already has it from its own configuration.
//
// Nothing in this package logs, and [Result] carries no field that holds the
// input. The raw text is only ever read, and never returned.
package failsig

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Signature is a failure's cause, drawn from a closed set. Its value is the
// wire and log spelling (e.g. "git-auth").
type Signature string

// The closed set. [Unknown] is the fallthrough for text that no table row
// matches. [Classify] never returns an empty Signature.
const (
	GitAuth     Signature = "git-auth"
	GitNetwork  Signature = "git-network"
	MountOrPath Signature = "mount-or-path"
	Budget      Signature = "budget"
	IndexLock   Signature = "index-lock"
	Unknown     Signature = "unknown"

	// The API-error family: the session's own model API call failed, and
	// Claude Code wrote the error into the transcript (StopFailure).
	APITransient Signature = "api-transient" // 5xx, 529 Overloaded, socket/stream drop
	APIRateLimit Signature = "api-rate-limit"
	APIAuth      Signature = "api-auth" // 401, "Please run /login"
	ContextLimit Signature = "context-limit"

	// The session-exit family: no transcript text explained the exit, so the
	// ccpool session facts observed at exit name its shape (see [ExitFacts]).
	SessionErrored Signature = "session-errored" // ccpool state errored, cause unstated
	SessionIdle    Signature = "session-idle"    // turn ended; bead not completed
	SessionGone    Signature = "session-gone"    // pane dead or row absent
)

// signatures is the closed set, in declaration order.
var signatures = []Signature{
	GitAuth, GitNetwork, MountOrPath, Budget, IndexLock,
	APITransient, APIRateLimit, APIAuth, ContextLimit,
	SessionErrored, SessionIdle, SessionGone,
	Unknown,
}

// Signatures returns every member of the closed set. The caller owns the
// returned slice.
func Signatures() []Signature { return slices.Clone(signatures) }

// Valid reports whether s is a member of the closed set.
func (s Signature) Valid() bool { return slices.Contains(signatures, s) }

// String returns the wire and log spelling.
func (s Signature) String() string { return string(s) }

// MaxEvidenceLen is the upper bound on [Result.Evidence], in bytes. The bound
// is in bytes, not runes, so it also holds for a caller that stores the value
// by length. Because each character is at least one byte, it is also a bound
// on characters. A cut never splits a UTF-8 sequence.
const MaxEvidenceLen = 300

// evidenceLead is how many bytes of the redacted line to keep BEFORE the match
// when the match starts far into a long line. It keeps the matched text
// inside the evidence window instead of cutting it off.
const evidenceLead = 100

// Result is the outcome of [Classify] and [ClassifyExit].
type Result struct {
	// Signature is never empty. It is [Unknown] when no row matched.
	Signature Signature
	// Evidence is the redacted line the match was on, at most
	// MaxEvidenceLen bytes. [Classify] leaves it empty for Unknown;
	// [ClassifyExit] never does (see there).
	Evidence string
}

// Classify names text's failure signature using the ordered table in
// table.go, where the first matching row wins. It returns redacted evidence
// for that match. Text that no row matches is [Unknown] with empty evidence.
func Classify(text string) Result {
	r, start, ok := firstMatch(text)
	if !ok {
		return Result{Signature: Unknown}
	}
	return Result{Signature: r.signature, Evidence: evidence(text, r, start)}
}

// firstMatch returns the first table row that matches text, and the byte
// offset in text where that match starts.
func firstMatch(text string) (rule, int, bool) {
	for _, r := range table {
		if loc := r.re.FindStringIndex(text); loc != nil {
			return r, loc[0], true
		}
	}
	return rule{}, 0, false
}

// evidence builds the Evidence for a match starting at rawStart in raw.
// The ORDER below is the invariant: redact the whole text, then pick the
// line, then cut.
func evidence(raw string, r rule, rawStart int) string {
	redacted := Redact(raw)
	// Redact keeps every newline, so the raw match's line number is valid in
	// the redacted text. Counting newlines reads raw but returns nothing from
	// it.
	line := nthLine(redacted, strings.Count(raw[:rawStart], "\n"))
	line = strings.TrimRight(line, "\r")

	start := 0
	// Re-find the match in the redacted line to center the window on it. If
	// redaction removed part of the match, the rule no longer matches, and
	// the window falls back to the start of the line.
	if loc := r.re.FindStringIndex(line); loc != nil && loc[0] > evidenceLead {
		start = loc[0] - evidenceLead
	}
	return strings.TrimSpace(cut(line, start, MaxEvidenceLen))
}

// nthLine returns line n of s, counting from 0 and without its trailing
// newline. It returns "" when s has fewer lines.
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

// cut returns at most maxLen bytes of s starting at byte offset start. Both
// ends are moved to UTF-8 rune boundaries: start moves forward and the end
// moves backward, so a multi-byte character is never split.
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
