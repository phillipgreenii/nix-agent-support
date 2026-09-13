package cmddesc

import (
	"fmt"
	"strings"
)

// awkDialect is a deliberately small, honest CLASSIFIER for awk/gawk
// programs — not an awk parser. It tokenizes the program text (strings,
// regex literals, `#` comments, identifiers, numbers, punctuation) well
// enough to recognise the handful of constructs that touch the outside
// world, and treats everything else — assignments, arithmetic, control
// flow, user functions, field references — as inert. Anything it cannot
// classify with confidence (an unterminated string or regex, a non-literal
// redirect/pipe/system target, a two-way `|&` coprocess pipe, `@load`, an
// unrecognised `|` or `@` usage) makes the interpretation insufficient with
// a reason, exactly like dialect_sed.go. It errs toward insufficient: a
// false "inert" is the one mistake it must not make.
//
// Verified against GNU Awk 5.4.1 on this host (`gawk --version`,
// `gawk --help`): print/printf's output redirection (`>`, `>>`, `|`, the
// gawk extension `|&`) is a suffix of the statement, ending at `;`, a raw
// newline, or `}` — but ONLY at the statement's own paren-nesting depth, so
// `print (a > b)` is a comparison INSIDE the argument list, not a
// redirection, while `print (a, b) > "file"` (nesting closed before the
// `>`) is one; a bare `/` begins a regex constant exactly when the
// preceding token was an operator, a keyword, `(`, `,`, `;`, `{`, `!`, `~`,
// or the very start of the program — anywhere else (after an identifier,
// number, string, regex, `)` or `]`) it is division, matching this file's
// isValueContext/regexContext tracking.
type awkDialect struct{}

// InterpretProgram implements DialectInterpreter.
func (awkDialect) InterpretProgram(program string, _ Context) ProgramInterpretation {
	p := &awkParser{src: program, regexContext: true}
	p.run()
	return ProgramInterpretation{
		Effects:       p.effects,
		Children:      p.children,
		Sufficient:    p.insuff == "",
		Insufficiency: p.insuff,
	}
}

// awkKeywords are the reserved words that, as the MOST RECENTLY consumed
// token, put a following bare `/` in regex context rather than division
// context (a keyword cannot itself be divided). print/printf/getline/
// system/close/fflush are dispatched specially (see handleWord) and never
// reach this set.
var awkKeywords = map[string]bool{
	"BEGIN": true, "END": true, "function": true, "func": true,
	"if": true, "else": true, "while": true, "for": true, "do": true,
	"break": true, "continue": true, "next": true, "nextfile": true,
	"exit": true, "return": true, "delete": true, "in": true,
}

type awkParser struct {
	src      string
	i        int
	depth    int // nesting of ( and [ only — braces are not tracked
	effects  []Effect
	children []ChildInvocation
	insuff   string

	// regexContext: true when the NEXT bare `/` begins a regex constant
	// (the previous significant token was an operator/keyword/`(`/`,`/`;`/
	// `{`/`!`/`~`/start); false when it is division (previous token was a
	// value: identifier, number, string, regex, `)`, `]`).
	regexContext bool
	// lastString/lastWasString remember the immediately preceding complete
	// token when it was a string literal, for the `"cmd" | getline` form —
	// reset to false by every OTHER token so a stale literal several tokens
	// back can never be mistaken for the operand of a pipe it does not
	// belong to.
	lastString    string
	lastWasString bool
}

func (p *awkParser) fail(format string, a ...any) bool {
	if p.insuff == "" {
		p.insuff = fmt.Sprintf(format, a...)
	}
	return false
}

func (p *awkParser) eof() bool { return p.i >= len(p.src) }

func (p *awkParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *awkParser) peekAt(n int) byte {
	if p.i+n >= len(p.src) {
		return 0
	}
	return p.src[p.i+n]
}

// skip consumes spaces, tabs, carriage returns, `\`-newline line
// continuations, and `#` comments (to end of line, not consuming the
// newline itself). A bare newline is consumed too when allowNewline is
// true; when false, a bare newline is left for the caller to see — this is
// what lets printStatement (and the tighter operand-lookahead helpers)
// treat an unescaped newline as significant while every OTHER caller
// (the top-level scan) treats it as ordinary whitespace.
func (p *awkParser) skip(allowNewline bool) {
	for !p.eof() {
		switch c := p.peek(); {
		case c == ' ' || c == '\t' || c == '\r':
			p.i++
		case c == '\n':
			if !allowNewline {
				return
			}
			p.i++
		case c == '\\' && p.peekAt(1) == '\n':
			p.i += 2
		case c == '#':
			for !p.eof() && p.peek() != '\n' {
				p.i++
			}
		default:
			return
		}
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || isAwkDigit(c)
}

func isAwkDigit(c byte) bool { return c >= '0' && c <= '9' }

func (p *awkParser) ident() string {
	start := p.i
	for !p.eof() && isIdentCont(p.src[p.i]) {
		p.i++
	}
	return p.src[start:p.i]
}

// matchIdent reports whether word appears at the current position as a
// whole identifier (not a prefix of a longer one) — used to test for
// "getline" right after a pipe without committing to consuming anything
// else if it turns out not to match.
func (p *awkParser) matchIdent(word string) bool {
	if !strings.HasPrefix(p.src[p.i:], word) {
		return false
	}
	end := p.i + len(word)
	return end >= len(p.src) || !isIdentCont(p.src[end])
}

// number consumes a run of digits, at most one `.`, and an optional
// exponent — a best-effort lexical span; the classifier never evaluates a
// number's value.
func (p *awkParser) number() {
	for !p.eof() && (isAwkDigit(p.peek()) || p.peek() == '.') {
		p.i++
	}
	if p.peek() == 'e' || p.peek() == 'E' {
		save := p.i
		p.i++
		if p.peek() == '+' || p.peek() == '-' {
			p.i++
		}
		if isAwkDigit(p.peek()) {
			for !p.eof() && isAwkDigit(p.peek()) {
				p.i++
			}
		} else {
			p.i = save
		}
	}
}

// string consumes a double-quoted string literal (backslash escapes the
// next byte) and returns its raw content (escapes NOT interpreted, same
// convention as sedParser.part). Fails on a raw newline or EOF before the
// closing quote.
func (p *awkParser) string() (string, bool) {
	p.i++ // opening quote
	start := p.i
	for !p.eof() {
		switch c := p.peek(); c {
		case '\\':
			p.i += 2
		case '"':
			val := p.src[start:p.i]
			p.i++
			return val, true
		case '\n':
			return "", p.fail("newline inside string")
		default:
			p.i++
		}
	}
	return "", p.fail("unterminated string")
}

// regex consumes a `/`-delimited regular expression constant, honouring
// backslash escapes and `[...]` bracket expressions (including POSIX
// `[:class:]` character classes). Fails on a raw newline or EOF before the
// closing delimiter.
func (p *awkParser) regex() bool {
	p.i++ // opening /
	for !p.eof() {
		switch c := p.peek(); c {
		case '\\':
			p.i += 2
		case '[':
			if !p.regexBracket() {
				return false
			}
		case '/':
			p.i++
			return true
		case '\n':
			return p.fail("newline inside regex")
		default:
			p.i++
		}
	}
	return p.fail("unterminated regex")
}

func (p *awkParser) regexBracket() bool {
	p.i++ // [
	if p.peek() == '^' {
		p.i++
	}
	if p.peek() == ']' {
		p.i++
	}
	for !p.eof() {
		switch c := p.peek(); {
		case c == ']':
			p.i++
			return true
		case c == '\n':
			return p.fail("newline inside bracket expression")
		case c == '[' && p.i+1 < len(p.src) && strings.IndexByte(":.=", p.src[p.i+1]) >= 0:
			closer := string(p.src[p.i+1]) + "]"
			end := strings.Index(p.src[p.i+2:], closer)
			if end < 0 {
				return p.fail("unterminated character class")
			}
			p.i += 2 + end + 2
		default:
			p.i++
		}
	}
	return p.fail("unterminated bracket expression")
}

// consumeToCloseParen consumes text up to and including the `)` that
// matches ONE already-consumed `(` (honouring nested parens and string
// literals), using a LOCAL depth counter — it never touches p.depth, which
// tracks only the OUTER scan's nesting for print-statement boundary
// detection. Used by systemCall's non-literal fallback and inertCall.
func (p *awkParser) consumeToCloseParen() bool {
	depth := 1
	for depth > 0 {
		if p.eof() {
			return p.fail("unterminated (")
		}
		switch c := p.peek(); c {
		case '"':
			if _, ok := p.string(); !ok {
				return false
			}
		case '#':
			for !p.eof() && p.peek() != '\n' {
				p.i++
			}
		case '(':
			depth++
			p.i++
		case ')':
			depth--
			p.i++
		default:
			p.i++
		}
	}
	return true
}

// balanced consumes one `(...)` or `[...]` group starting AT the open
// bracket (honouring nested brackets of the SAME kind and string
// literals), using a local depth counter — used by getlineTail for an
// optional array-subscripted or parenthesised operand it does not need to
// understand beyond "skip past it".
func (p *awkParser) balanced() bool {
	open := p.peek()
	closeCh := byte(')')
	if open == '[' {
		closeCh = ']'
	}
	p.i++
	depth := 1
	for depth > 0 {
		if p.eof() {
			return p.fail("unterminated %q", string(open))
		}
		switch c := p.peek(); c {
		case '"':
			if _, ok := p.string(); !ok {
				return false
			}
		case '#':
			for !p.eof() && p.peek() != '\n' {
				p.i++
			}
		case open:
			depth++
			p.i++
		case closeCh:
			depth--
			p.i++
		default:
			p.i++
		}
	}
	return true
}

// run is the top-level scan: skip trivia (newlines included — nothing at
// this level cares about statement boundaries except print/printf, which
// dispatch to printStatement) and dispatch one token at a time. It stops
// the instant anything fails, exactly like sedParser.run.
func (p *awkParser) run() {
	for !p.eof() {
		p.skip(true)
		if p.eof() {
			return
		}
		if !p.step() {
			return
		}
	}
}

func (p *awkParser) step() bool {
	switch c := p.peek(); {
	case c == '"':
		val, ok := p.string()
		if !ok {
			return false
		}
		p.lastString, p.lastWasString = val, true
		p.regexContext = false
		return true
	case c == '/':
		if p.regexContext {
			if !p.regex() {
				return false
			}
			p.regexContext = false
		} else {
			p.i++
			p.regexContext = true
		}
		p.lastWasString = false
		return true
	case c == '@':
		return p.atDirective()
	case isIdentStart(c):
		return p.handleWord(p.ident())
	case isAwkDigit(c):
		p.number()
		p.regexContext = false
		p.lastWasString = false
		return true
	case c == '(' || c == '[':
		p.depth++
		p.i++
		p.regexContext = true
		p.lastWasString = false
		return true
	case c == ')' || c == ']':
		if p.depth > 0 {
			p.depth--
		}
		p.i++
		p.regexContext = false
		p.lastWasString = false
		return true
	case c == '|':
		return p.topLevelPipe()
	default:
		p.i++
		p.regexContext = true
		p.lastWasString = false
		return true
	}
}

// handleWord dispatches an already-read identifier/keyword token, shared by
// the top-level scan and printStatement's own argument scan (so a
// system()/getline/close/fflush call embedded INSIDE a print statement's
// argument list — `print system("x")` — is still recognised, not silently
// skipped as an ordinary identifier).
func (p *awkParser) handleWord(word string) bool {
	switch word {
	case "print", "printf":
		if !p.printStatement() {
			return false
		}
		p.regexContext = true
	case "getline":
		if !p.getlineTail() {
			return false
		}
		p.regexContext = false
	case "system":
		if !p.systemCall() {
			return false
		}
		p.regexContext = false
	case "close", "fflush":
		if !p.inertCall() {
			return false
		}
		p.regexContext = false
	default:
		p.regexContext = awkKeywords[word]
	}
	p.lastWasString = false
	return true
}

// atDirective handles gawk's `@include "file"` (a real file read) and
// `@load "ext"` (a native extension load, deliberately unmodeled).
func (p *awkParser) atDirective() bool {
	p.i++ // @
	if !isIdentStart(p.peek()) {
		return p.fail("unrecognised @ directive")
	}
	word := p.ident()
	p.regexContext = false
	p.lastWasString = false
	switch word {
	case "include":
		p.skip(false)
		if p.peek() != '"' {
			return p.fail("@include target is not a string literal")
		}
		val, ok := p.string()
		if !ok {
			return false
		}
		p.effects = append(p.effects, Effect{Kind: EffectPath, Path: val, Access: AccessRead, Source: "awk @include"})
		return true
	case "load":
		return p.fail("@load loads a native gawk extension (not modeled)")
	default:
		return p.fail("unrecognised @%s directive", word)
	}
}

// topLevelPipe handles a bare `|` OUTSIDE a print statement: `||` (logical
// or, inert), `|&` (coprocess, unmodeled) and `"cmd" | getline [var]` (the
// input-pipe form — the command must be the STRING LITERAL that was the
// immediately preceding token, mirroring print's "non-literal target is
// insufficient" rule). Any other bare `|` matches no construct awk actually
// has, so it fails closed rather than being silently skipped.
func (p *awkParser) topLevelPipe() bool {
	if p.peekAt(1) == '|' {
		p.i += 2
		p.regexContext = true
		p.lastWasString = false
		return true
	}
	if p.peekAt(1) == '&' {
		p.i += 2
		return p.fail("`|&` two-way pipe is not modeled")
	}
	literal, wasLiteral := p.lastString, p.lastWasString
	p.i++
	p.lastWasString = false
	p.skip(false)
	if !p.matchIdent("getline") {
		return p.fail("unrecognised `|` (only a print pipe and `cmd | getline` are modeled)")
	}
	p.i += len("getline")
	if !wasLiteral {
		return p.fail("command piped to getline is not a string literal")
	}
	p.children = append(p.children, ChildInvocation{Dialect: "shell", Program: literal})
	p.skip(false)
	switch {
	case isIdentStart(p.peek()):
		p.ident()
	case p.peek() == '$':
		p.i++
		p.skip(false)
		switch {
		case isIdentStart(p.peek()):
			p.ident()
		case isAwkDigit(p.peek()):
			p.number()
		}
	}
	p.regexContext = false
	return true
}

// getlineTail handles `getline`, `getline var`, `getline < "file"` and
// `getline var < "file"` — called right after the "getline" keyword itself
// was consumed. The `"cmd" | getline` form is handled entirely by
// topLevelPipe (which consumes "getline" itself and never reaches here).
func (p *awkParser) getlineTail() bool {
	p.skip(false)
	switch {
	case isIdentStart(p.peek()):
		p.ident()
		p.skip(false)
		if p.peek() == '[' {
			if !p.balanced() {
				return false
			}
			p.skip(false)
		}
	case p.peek() == '$':
		p.i++
		p.skip(false)
		switch {
		case isIdentStart(p.peek()):
			p.ident()
		case isAwkDigit(p.peek()):
			p.number()
		case p.peek() == '(':
			if !p.balanced() {
				return false
			}
		}
		p.skip(false)
	}
	if p.peek() == '<' && p.peekAt(1) != '=' {
		p.i++
		p.skip(false)
		if p.peek() != '"' {
			return p.fail("getline source is not a string literal")
		}
		val, ok := p.string()
		if !ok {
			return false
		}
		p.effects = append(p.effects, Effect{Kind: EffectPath, Path: val, Access: AccessRead, Source: "awk getline <"})
	}
	return true
}

// systemCall handles `system(...)`: a single string-literal argument is a
// shell child; anything else (a variable, a field reference, a
// concatenation) is insufficient — a program is still fully consumed to the
// matching `)` first so the scan position stays correct.
func (p *awkParser) systemCall() bool {
	p.skip(false)
	if p.peek() != '(' {
		return p.fail("`system` without `(`")
	}
	p.i++
	p.skip(false)
	if p.peek() == '"' {
		val, ok := p.string()
		if !ok {
			return false
		}
		p.skip(false)
		if p.peek() == ')' {
			p.i++
			p.children = append(p.children, ChildInvocation{Dialect: "shell", Program: val})
			return true
		}
		if !p.consumeToCloseParen() {
			return false
		}
		return p.fail("system() argument is not a single string literal")
	}
	if !p.consumeToCloseParen() {
		return false
	}
	return p.fail("system() argument is not a literal")
}

// inertCall consumes `close(...)`/`fflush(...)` (or a bare reference with no
// call at all) as opaque: neither builtin can itself write, read, or
// execute anything the classifier needs to see.
func (p *awkParser) inertCall() bool {
	p.skip(false)
	if p.peek() != '(' {
		return true
	}
	p.i++
	return p.consumeToCloseParen()
}

// printStatement scans a print/printf statement's argument list from
// immediately after the keyword to `;`, an unescaped newline, or `}` — each
// only significant at the statement's OWN paren/bracket depth (base), so a
// nested `print (a > b)` sees a comparison and `print (a, b) > "file"` (the
// nesting closed before the `>`) sees a redirection. `>` truncates,
// `>>` appends, `|` pipes to a command, `|&` (two-way coprocess) is
// unmodeled; a non-literal target for any of the three is insufficient.
func (p *awkParser) printStatement() bool {
	base := p.depth
	for {
		p.skip(false)
		if p.eof() {
			if p.depth > base {
				return p.fail("unterminated print statement")
			}
			return true
		}
		switch c := p.peek(); {
		case c == '\n':
			if p.depth <= base {
				return true
			}
			p.i++
		case c == ';':
			if p.depth <= base {
				p.i++
				return true
			}
			p.i++
		case c == '}':
			if p.depth <= base {
				return true
			}
			return p.fail("unexpected } inside print statement")
		case c == '"':
			val, ok := p.string()
			if !ok {
				return false
			}
			p.lastString, p.lastWasString = val, true
			p.regexContext = false
		case c == '/':
			if p.regexContext {
				if !p.regex() {
					return false
				}
				p.regexContext = false
			} else {
				p.i++
				p.regexContext = true
			}
			p.lastWasString = false
		case c == '(' || c == '[':
			p.depth++
			p.i++
			p.regexContext = true
			p.lastWasString = false
		case c == ')' || c == ']':
			if p.depth > 0 {
				p.depth--
			}
			p.i++
			p.regexContext = false
			p.lastWasString = false
		case c == '|':
			if p.peekAt(1) == '|' {
				p.i += 2
				p.regexContext = true
				p.lastWasString = false
				continue
			}
			if p.peekAt(1) == '&' {
				p.i += 2
				return p.fail("`print |&` two-way pipe is not modeled")
			}
			if p.depth != base {
				p.i++
				p.regexContext = true
				p.lastWasString = false
				continue
			}
			p.i++
			return p.pipeTarget()
		case c == '>':
			if p.peekAt(1) == '=' {
				p.i += 2
				p.regexContext = true
				p.lastWasString = false
				continue
			}
			if p.peekAt(1) == '>' {
				if p.depth != base {
					p.i += 2
					p.regexContext = true
					p.lastWasString = false
					continue
				}
				p.i += 2
				return p.redirTarget(AccessModify, "awk print >>")
			}
			if p.depth != base {
				p.i++
				p.regexContext = true
				p.lastWasString = false
				continue
			}
			p.i++
			return p.redirTarget(AccessTruncate, "awk print >")
		case c == '<':
			// A comparison inside print's argument list is still just a
			// comparison — the redirection spelling is `>`/`>>`/`|`/`|&` only.
			if p.peekAt(1) == '=' {
				p.i += 2
			} else {
				p.i++
			}
			p.regexContext = true
			p.lastWasString = false
		case isIdentStart(c):
			if !p.handleWord(p.ident()) {
				return false
			}
		case isAwkDigit(c):
			p.number()
			p.regexContext = false
			p.lastWasString = false
		default:
			p.i++
			p.regexContext = true
			p.lastWasString = false
		}
	}
}

// pipeTarget handles the target of a print `|` pipe: the string literal
// naming the shell command it feeds.
func (p *awkParser) pipeTarget() bool {
	p.skip(false)
	if p.peek() != '"' {
		return p.fail("print pipe target is not a string literal")
	}
	val, ok := p.string()
	if !ok {
		return false
	}
	p.children = append(p.children, ChildInvocation{Dialect: "shell", Program: val})
	return true
}

// redirTarget handles the target of a print `>`/`>>` redirection: a
// non-literal target is insufficient; "/dev/stdout", "/dev/stderr" and "-"
// are inert stdio, not real path effects.
func (p *awkParser) redirTarget(access PathAccess, source string) bool {
	p.skip(false)
	if p.peek() != '"' {
		return p.fail("print redirect target is not a string literal")
	}
	val, ok := p.string()
	if !ok {
		return false
	}
	switch val {
	case "/dev/stdout", "/dev/stderr", "-":
		// inert stdio target
	default:
		p.effects = append(p.effects, Effect{Kind: EffectPath, Path: val, Access: access, Source: source})
	}
	return true
}
