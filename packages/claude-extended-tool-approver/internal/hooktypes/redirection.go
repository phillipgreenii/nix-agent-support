// Package hooktypes holds value types shared across the hook boundary, the
// shell parser, and the effect-graph spike — without any of those three
// needing to import one of the others to name them.
//
// Redirection (and its RedirectionKind) is a pure value type that internal/hookio
// never constructs or inspects; it exists purely so cmdparse.ParsedCommand has
// somewhere to record a leaf's I/O redirections, and internal/engine and the
// effect-graph spike (internal/effectgraph) both need to read that value back
// out. Before this package existed, Redirection lived in internal/hookio, so
// cmdparse importing it for the type pulled hookio in transitively, and in turn
// so did every spike package that imports cmdparse for ParsedCommand — even
// though the spike's own import guard (internal/effectpolicy's
// TestNoDirectHookioImport) forbids importing hookio directly. Moving the value
// type here (slice 3r of the effect-graph spike, tc-lc8f) breaks that chain:
// hooktypes imports nothing internal, so any package may depend on it without
// reaching hookio at all.
//
// internal/hookio keeps compatibility aliases (Redirection = hooktypes.Redirection,
// etc.) so existing call sites that still say `hookio.Redirection` keep compiling;
// new code should import this package directly instead.
package hooktypes

import "regexp"

// RedirectionKind classifies the type of I/O redirection.
type RedirectionKind int

const (
	RedirectStdin  RedirectionKind = iota // <
	RedirectStdout                        // >, >>, >|, 1>, 1>>, 1>|
	RedirectStderr                        // 2>, 2>>, 2>|
	RedirectAll                           // &>, &>>, >& FILE
	// RedirectOtherFD is a write to a PATH on a descriptor that is neither stdout
	// nor stderr: `9> f`, `3>> f`, `{fd}> f`. It is a file write like any other —
	// every write-direction consumer must treat it as one — but it captures no
	// stdout, so cmdparse.CapturesStdout deliberately does NOT count it.
	RedirectOtherFD
	// RedirectReadWrite is bash's `<>` open: the target is opened for reading AND
	// writing, and may be created. It is classified as a WRITE (it is checked for
	// writability, not readability) because creating/modifying the target is the
	// direction that matters to a permission gate.
	RedirectReadWrite
)

// IsWrite reports whether the redirection can CREATE OR MODIFY its target.
// Everything that is not a pure read (`<`) is a write, so a kind added later
// fails closed rather than silently becoming read-only.
func (k RedirectionKind) IsWrite() bool { return k != RedirectStdin }

// IsReadWrite reports whether the redirection opens its target for BOTH
// reading and writing (bash's `<>`), as opposed to a pure read or a pure
// write. It exists so a consumer that needs to test for this one kind (e.g.
// effectgraph's redirectionEffect, deciding Modify vs Truncate) has a named
// predicate rather than a bare `== RedirectReadWrite` comparison — a plain
// convenience, not an import-cycle workaround: this package's constants are
// importable from anywhere, spike packages included.
func (k RedirectionKind) IsReadWrite() bool { return k == RedirectReadWrite }

// Redirection represents a parsed I/O redirection.
type Redirection struct {
	// Operator is the operator text AS WRITTEN, including any file-descriptor
	// prefix: "<", ">", ">>", ">|", ">&", "<>", "1>", "2>>", "9>", "{fd}>", "&>",
	// "&>>". A consumer MUST classify by Kind, never by matching this string.
	Operator string
	Path     string          // target file path
	Kind     RedirectionKind // classification

	// LiveExpansion reports whether Path contains a shell expansion the
	// runtime would actually evaluate — a parameter expansion, command or
	// process substitution, or arithmetic expansion, OUTSIDE single quotes —
	// computed by cmdparse's wordHasLiveExpansion over the SAME target word
	// ParsedCommand.ArgLiveExpansion uses for ordinary arguments (pg2-pui5w),
	// so a redirect target and an argument can never drift on what counts as
	// "live". A consumer that needs "is this target dynamic" MUST read this
	// field rather than re-deriving it with a `$`/backtick substring test:
	// that heuristic is wrong in both directions — a target that is ONLY a
	// process substitution (`>(cmd)`) contains neither byte and is a false
	// NEGATIVE, while a single-quoted `'$x'` or a backslash-escaped `\$x`
	// contains the byte but bash never expands it, a false POSITIVE.
	//
	// Heredocs and herestrings (`<<`, `<<-`, `<<<`) never populate a
	// Redirection at all — see attachRedir in shellparse.go — so this field
	// says nothing about a heredoc BODY's own liveness; a here-string's body
	// can be live (`<<< "$x"`) with no path target to judge here.
	LiveExpansion bool

	// Append reports whether the redirection operator is one of bash's
	// APPEND forms — `>>`, `&>>`, `n>>`, `{fd}>>` — as opposed to a
	// truncating write (`>`, `>|`, `n>`, `&>`). It is populated from the
	// PARSER'S OWN OPERATOR ENUM (syntax.AppOut / syntax.AppAll), never from
	// matching Operator's text, for the same reason Kind is: a consumer MUST
	// NOT re-derive a parser fact from the rendered string. Meaningless (and
	// always false) for a non-write Kind (RedirectStdin) and for
	// RedirectReadWrite (`<>` has no append form).
	Append bool
}

// devFdPattern matches /dev/fd/<n> for any file-descriptor number.
var devFdPattern = regexp.MustCompile(`^/dev/fd/[0-9]+$`)

// IsSafeRedirectTarget reports whether path is one of the standard special device
// files that are always safe as an I/O redirection target — for reading (stdin)
// and writing (stdout/stderr) alike: /dev/null, /dev/stdout, /dev/stderr,
// /dev/tty, and /dev/fd/<n>.
//
// TWO callers, for two different reasons, which is why it lives beside the
// Redirection type rather than inside either of them (the same relocation
// cmdparse.SkipGrepPattern got when a rule needed to share it):
//
//   - the engine's redirection evaluation, where the PathEvaluator does not model
//     these pseudo-files (it classifies them PathUnknown) and without the
//     short-circuit a redirect to one would demote an otherwise-approved command
//     to NoOpinion (pg2-9ctmb);
//   - the gitdir rule's copy-out detection, where an output redirection is what
//     turns a read of git metadata into a capture of it — but writing to a
//     terminal or discarding to /dev/null captures nothing, so `ls .git/hooks
//     2>/dev/null` must stay a plain read (tc-403c).
//
// Being a redirect-TARGET predicate is the whole of its meaning: it does NOT make
// these paths writable to the rest of the ruleset (e.g. `rm /dev/null` is
// unaffected).
func IsSafeRedirectTarget(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return devFdPattern.MatchString(path)
}
