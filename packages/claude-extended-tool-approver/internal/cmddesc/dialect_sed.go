package cmddesc

import (
	"fmt"
	"strings"
)

// sedDialect is a deliberately small, honest CLASSIFIER for sed scripts — not
// a sed parser. It walks the script command by command, recognises the
// constructs that touch the outside world (`r`/`R` read a file, `w`/`W` and
// the `s///w` flag write one, `e` and the `s///e` flag run a shell command)
// and treats the ordinary editing commands as inert. Anything it cannot
// classify with confidence — an unknown command, a flag it does not know, an
// unterminated regex, a backslash inside a bracket expression, unbalanced
// braces — makes the interpretation insufficient with a reason. It errs
// toward insufficient: a false "inert" is the one mistake it must not make.
//
// Verified against GNU sed 4.10 on this host: the delimiter is literal inside
// a bracket expression (`s/[/]/X/` works), branch labels end at `;` or
// newline, `q`/`Q`/`l`/`L` take an optional numeric argument, and `}` without
// a matching `{` is an error.
type sedDialect struct{}

// InterpretProgram implements DialectInterpreter.
func (sedDialect) InterpretProgram(program string, _ Context) ProgramInterpretation {
	p := &sedParser{src: program}
	p.run()
	return ProgramInterpretation{
		Effects:       p.effects,
		Sufficient:    p.insuff == "",
		Insufficiency: p.insuff,
	}
}

type sedParser struct {
	src     string
	i       int
	depth   int
	effects []Effect
	insuff  string
}

func (p *sedParser) fail(format string, a ...any) bool {
	if p.insuff == "" {
		p.insuff = fmt.Sprintf(format, a...)
	}
	return false
}

func (p *sedParser) eof() bool { return p.i >= len(p.src) }

func (p *sedParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.i]
}

func (p *sedParser) skip(set string) {
	for !p.eof() && strings.IndexByte(set, p.src[p.i]) >= 0 {
		p.i++
	}
}

func (p *sedParser) digits() string {
	start := p.i
	for !p.eof() && p.src[p.i] >= '0' && p.src[p.i] <= '9' {
		p.i++
	}
	return p.src[start:p.i]
}

// toEOL consumes through the end of the current line and returns the text
// before the newline with leading blanks trimmed.
func (p *sedParser) toEOL() string {
	start := p.i
	for !p.eof() && p.src[p.i] != '\n' {
		p.i++
	}
	s := p.src[start:p.i]
	if !p.eof() {
		p.i++
	}
	return strings.TrimLeft(s, " \t")
}

// toSeparator consumes up to (not through) `;`, newline or `}` and returns
// the text with surrounding blanks trimmed. It is the branch-label reader:
// the more conservative split (a `w` after `;` is a command, never label
// text).
func (p *sedParser) toSeparator() string {
	start := p.i
	for !p.eof() && strings.IndexByte(";\n}", p.src[p.i]) < 0 {
		p.i++
	}
	return strings.TrimSpace(p.src[start:p.i])
}

func (p *sedParser) run() {
	for {
		p.skip(" \t\n;")
		if p.eof() {
			break
		}
		if !p.address() {
			return
		}
		p.skip(" \t")
		for p.peek() == '!' {
			p.i++
			p.skip(" \t")
		}
		if p.eof() {
			p.fail("address without a command")
			return
		}
		c := p.src[p.i]
		p.i++
		if !p.command(c) {
			return
		}
	}
	if p.depth != 0 {
		p.fail("unbalanced braces")
	}
}

// address consumes an optional `addr1[,addr2]`. It reports false only on a
// malformed address; "no address" is fine.
func (p *sedParser) address() bool {
	if !p.addr1() {
		return false
	}
	if p.peek() != ',' {
		return true
	}
	p.i++
	switch c := p.peek(); {
	case c >= '0' && c <= '9':
		p.digits()
	case c == '+' || c == '~':
		p.i++
		if p.digits() == "" {
			return p.fail("malformed address after %q", string(c))
		}
	case c == '$':
		p.i++
	case c == '/':
		p.i++
		return p.regex('/') && p.regexFlags()
	case c == '\\':
		p.i++
		if p.eof() {
			return p.fail("malformed custom-delimiter address")
		}
		d := p.src[p.i]
		p.i++
		return p.regex(d) && p.regexFlags()
	default:
		return p.fail("malformed second address")
	}
	return true
}

func (p *sedParser) addr1() bool {
	switch c := p.peek(); {
	case c >= '0' && c <= '9':
		p.digits()
		if p.peek() == '~' {
			p.i++
			if p.digits() == "" {
				return p.fail("malformed first~step address")
			}
		}
	case c == '$':
		p.i++
	case c == '/':
		p.i++
		return p.regex('/') && p.regexFlags()
	case c == '\\':
		p.i++
		if p.eof() {
			return p.fail("malformed custom-delimiter address")
		}
		d := p.src[p.i]
		p.i++
		return p.regex(d) && p.regexFlags()
	}
	return true
}

// regexFlags consumes the GNU `I`/`M` address-regex modifiers.
func (p *sedParser) regexFlags() bool {
	p.skip("IM")
	return true
}

// regex consumes a regular expression up to and including its closing
// delimiter. A backslash escapes the next byte; the delimiter is literal
// inside a bracket expression; a raw newline is unterminated. A backslash
// inside a bracket expression is ambiguous between the POSIX and GNU
// readings and is refused.
func (p *sedParser) regex(delim byte) bool {
	for !p.eof() {
		c := p.src[p.i]
		switch c {
		case '\\':
			p.i += 2
		case '[':
			if !p.bracket() {
				return false
			}
		case delim:
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

// bracket consumes a bracket expression starting at `[`.
func (p *sedParser) bracket() bool {
	p.i++ // [
	if p.peek() == '^' {
		p.i++
	}
	if p.peek() == ']' {
		p.i++
	}
	for !p.eof() {
		c := p.src[p.i]
		switch {
		case c == ']':
			p.i++
			return true
		case c == '\\':
			return p.fail("backslash inside bracket expression")
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

// part consumes replacement- or transliteration-style text up to and
// including delim, honouring backslash escapes only.
func (p *sedParser) part(delim byte) bool {
	for !p.eof() {
		c := p.src[p.i]
		switch c {
		case '\\':
			p.i += 2
		case delim:
			p.i++
			return true
		case '\n':
			return p.fail("newline inside s/y text")
		default:
			p.i++
		}
	}
	return p.fail("unterminated s/y text")
}

// endOfCmd requires that nothing but blanks, a comment, or a separator
// follows a command.
func (p *sedParser) endOfCmd() bool {
	p.skip(" \t")
	switch c := p.peek(); {
	case p.eof(), c == ';', c == '\n', c == '}':
		return true
	case c == '#':
		p.toEOL()
		return true
	default:
		return p.fail("unexpected text after command: %q", p.toSeparator())
	}
}

func (p *sedParser) command(c byte) bool {
	switch c {
	case '{':
		p.depth++
		return true
	case '}':
		if p.depth == 0 {
			return p.fail("unexpected }")
		}
		p.depth--
		return p.endOfCmd()
	case 'p', 'd', 'n', 'N', 'g', 'G', 'h', 'H', 'x', 'z', '=', 'D', 'P', 'F':
		return p.endOfCmd()
	case 'q', 'Q', 'l', 'L':
		p.skip(" \t")
		p.digits()
		return p.endOfCmd()
	case 'a', 'i', 'c':
		p.text()
		return true
	case ':':
		if p.toSeparator() == "" {
			return p.fail("empty label")
		}
		return true
	case 'b', 't', 'T':
		p.toSeparator()
		return true
	case 'r', 'R':
		return p.fileCommand(c, AccessRead)
	case 'w', 'W':
		return p.fileCommand(c, AccessTruncate)
	case 'e':
		prog := p.toEOL()
		p.effects = append(p.effects, Effect{Kind: EffectProgram, Program: prog, Dialect: "shell", Source: "sed e"})
		return p.fail("`e` executes a shell command (not recursed in this slice)")
	case 's':
		return p.subst()
	case 'y':
		return p.transliterate()
	case '#':
		p.toEOL()
		return true
	default:
		return p.fail("unrecognised sed command %q", string(c))
	}
}

// fileCommand handles r/R/w/W: the rest of the line is the filename.
func (p *sedParser) fileCommand(c byte, access PathAccess) bool {
	name := p.toEOL()
	if name == "" {
		return p.fail("`%s` without a filename", string(c))
	}
	p.effects = append(p.effects, Effect{Kind: EffectPath, Path: name, Access: access, Source: "sed " + string(c)})
	return true
}

// text consumes the a/i/c text: `a\` followed by newline-separated lines
// with backslash continuation, or the GNU one-liner `a text`. Inert.
func (p *sedParser) text() {
	if p.peek() == '\\' {
		p.i++
		if p.peek() == '\n' {
			p.i++
		}
	}
	for {
		start := p.i
		for !p.eof() && p.src[p.i] != '\n' {
			p.i++
		}
		line := p.src[start:p.i]
		if !p.eof() {
			p.i++
		}
		if p.eof() || !strings.HasSuffix(line, "\\") || strings.HasSuffix(line, "\\\\") {
			return
		}
	}
}

// subst handles `s`: delimiter, regex, replacement, then flags.
func (p *sedParser) subst() bool {
	if p.eof() {
		return p.fail("`s` without a delimiter")
	}
	delim := p.src[p.i]
	p.i++
	if delim == '\n' || delim == '\\' {
		return p.fail("invalid s delimiter")
	}
	if !p.regex(delim) || !p.part(delim) {
		return false
	}
	for !p.eof() {
		switch c := p.src[p.i]; {
		case c >= '0' && c <= '9':
			p.digits()
		case strings.IndexByte("gpiImM", c) >= 0:
			p.i++
		case c == 'e':
			p.i++
			p.effects = append(p.effects, Effect{Kind: EffectProgram, Program: "<pattern space>", Dialect: "shell", Source: "sed s///e"})
			return p.fail("`s///e` executes the pattern space as a shell command (not recursed in this slice)")
		case c == 'w':
			p.i++
			name := p.toEOL()
			if name == "" {
				return p.fail("`s///w` without a filename")
			}
			p.effects = append(p.effects, Effect{Kind: EffectPath, Path: name, Access: AccessTruncate, Source: "sed s///w"})
			return true
		case strings.IndexByte(" \t;\n}#", c) >= 0:
			return p.endOfCmd()
		default:
			return p.fail("unknown s flag %q", string(c))
		}
	}
	return true
}

// transliterate handles `y/src/dst/`.
func (p *sedParser) transliterate() bool {
	if p.eof() {
		return p.fail("`y` without a delimiter")
	}
	delim := p.src[p.i]
	p.i++
	if delim == '\n' || delim == '\\' {
		return p.fail("invalid y delimiter")
	}
	// y/src/dst/ has two delim-terminated parts (src, then dst); each call
	// to part advances past its own part, so these are two distinct parses
	// despite the identical call expression.
	if !p.part(delim) {
		return false
	}
	if !p.part(delim) {
		return false
	}
	return p.endOfCmd()
}
