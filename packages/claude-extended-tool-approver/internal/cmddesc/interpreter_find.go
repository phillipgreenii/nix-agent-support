package cmddesc

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// findInterpreter is a full custom WALK of find's argv, not a schema.Flags/
// Positionals table: find's grammar interleaves STARTING POINTS (bare
// operands, but only before the expression begins) with an EXPRESSION of
// tests/actions that are ALSO bare (`(`, `)`, `!`) or `-`-prefixed
// (everything else), so "is this bare token a path" depends on its POSITION
// relative to the expression's start, not on whether some flag consumed it —
// exactly the shape scan()/PositionalSpec cannot express (a bare `(` inside
// the expression would be misread as a Rest positional). Like
// dialect_awk.go, this is a deliberately small CLASSIFIER over the primary
// vocabulary below, not a full find(1) parser: boolean structure
// (`-a`/`-o`/`-not`/`(`/`)`) is walked but not evaluated — every primary that
// appears is judged as if it always runs, which is the same
// always-present-effects simplification the awk classifier makes for a
// conditional print.
//
// Leading options -H/-L/-P/-O*/-D are consumed first (inert; -D takes ONE
// following value). Then bare tokens up to the first token starting with
// `-`, `(` or `!` are STARTING POINTS, each a PathRead; none at all is an
// implicit PathRead of "." (find's own default). The rest is the
// EXPRESSION, walked primary by primary via the arity tables below.
type findInterpreter struct{}

// findOptLevelPattern matches the glued-digit `-O0`.."-O3" optimisation
// level (bare "-O" also accepted); the digit itself is inert.
var findOptLevelPattern = regexp.MustCompile(`^-O[0-9]?$`)

// findNewerXYPattern matches the `-newerXY` family (X and Y each one of
// a/B/c/m/t): a FILE operand compared against, e.g., `-newermt`. `-newer`
// and `-samefile` (the plain two-file forms) are listed in
// findPathArgPrimaries instead.
var findNewerXYPattern = regexp.MustCompile(`^-newer[aBcmt][aBcmt]$`)

// findNoArgPrimaries take no operand at all: pure tests/actions with no
// argument, and the boolean/grouping vocabulary (`-not`/`!`/`-a`/`-and`/
// `-o`/`-or`/`(`/`)`), all inert to this classifier (see the type doc for why
// the boolean structure itself is not evaluated).
var findNoArgPrimaries = map[string]bool{
	"-print": true, "-print0": true, "-ls": true, "-empty": true, "-prune": true,
	"-quit": true, "-true": true, "-false": true, "-daystart": true, "-depth": true,
	"-xdev": true, "-mount": true, "-follow": true, "-noleaf": true,
	"-nouser": true, "-nogroup": true, "-readable": true, "-writable": true,
	"-executable": true,
	"-not":        true, "!": true, "-a": true, "-and": true, "-o": true, "-or": true,
	"(": true, ")": true,
}

// findLiteralArgPrimaries take exactly one literal (inert) operand: a
// pattern, a size/permission/ownership/time comparison, or a depth/count
// bound. None of these read, write, or execute anything.
var findLiteralArgPrimaries = map[string]bool{
	"-name": true, "-iname": true, "-path": true, "-ipath": true,
	"-wholename": true, "-regex": true, "-iregex": true,
	"-type": true, "-xtype": true,
	"-size": true, "-perm": true,
	"-user": true, "-group": true, "-uid": true, "-gid": true,
	"-mtime": true, "-atime": true, "-ctime": true,
	"-mmin": true, "-amin": true, "-cmin": true,
	"-maxdepth": true, "-mindepth": true,
	"-links": true, "-inum": true, "-fstype": true,
	"-lname": true, "-ilname": true, "-used": true, "-printf": true,
}

// findPathArgPrimaries take exactly one FILE operand that is genuinely
// READ (its timestamp/identity is compared against): the two-file forms of
// `-newer`/`-samefile` (the `-newerXY` family is matched by
// findNewerXYPattern instead, since its own name carries the two-letter
// suffix).
var findPathArgPrimaries = map[string]bool{"-newer": true, "-samefile": true}

// findTruncateArgPrimaries take exactly one FILE operand that find
// TRUNCATES and rewrites with output (`-fprint`/`-fprint0`/`-fls`).
// `-fprintf` is handled separately: its FILE comes first, then a literal
// format string.
var findTruncateArgPrimaries = map[string]bool{"-fprint": true, "-fprint0": true, "-fls": true}

// findMetaUnescape returns tok with a leading backslash removed when the
// rest is EXACTLY one of the shell metacharacters find's own expression
// syntax needs (`;` would otherwise end the command, `(`/`)` would otherwise
// open/close a subshell) — the same literal character a QUOTED spelling
// (`';'`, `'('`) already arrives as once cmdparse's unquote() strips its
// wrapping quotes (parser.go's unquote only strips a WHOLE-token quote
// pair; it does not itself resolve an unquoted backslash escape, so
// `\;`/`\(`/`\)` survive into Args with the backslash still attached). This
// classifier is applied only at find's own structural comparisons (the
// starting-point boundary, `(`/`)` grouping, an -exec/-execdir terminator)
// — never to an ordinary operand's text, which a real invocation may
// legitimately contain a literal backslash in. `!` needs no such treatment:
// it is not a shell metacharacter outside interactive history expansion, so
// an unquoted `!` already arrives literal.
func findMetaUnescape(tok string) string {
	if len(tok) == 2 && tok[0] == '\\' {
		switch tok[1] {
		case ';', '(', ')':
			return tok[1:]
		}
	}
	return tok
}

// Interpret implements Interpreter.
func (findInterpreter) Interpret(leaf cmdparse.ParsedCommand, _ CommandSchema, _ Context) Interpretation {
	args := leaf.Args
	fs := &findState{leaf: leaf}
	i := fs.leadingOptions(args, 0)
	i = fs.startingPoints(args, i)
	fs.expression(args, i)
	return Interpretation{
		Effects:       fs.effects,
		Children:      fs.children,
		Sufficient:    fs.insuff == "",
		Insufficiency: fs.insuff,
	}
}

// findState accumulates one find interpretation across the three walks
// (leading options, starting points, expression). insuff is recorded at the
// FIRST problem, matching interpState's convention elsewhere in this
// package; later tokens are still walked so their effects are still
// reported, but the interpretation can never be Sufficient again.
type findState struct {
	leaf     cmdparse.ParsedCommand
	starts   []int // argv indices of the starting-point tokens
	effects  []Effect
	children []ChildInvocation
	insuff   string
}

func (fs *findState) fail(format string, a ...any) {
	if fs.insuff == "" {
		fs.insuff = fmt.Sprintf(format, a...)
	}
}

func (fs *findState) live(idx int) bool { return fs.leaf.ArgIsLiveExpansion(idx) }

// leadingOptions consumes -H/-L/-P (inert booleans), -O* (a glued optional
// digit, inert) and -D (inert, ONE following value) from the front of argv.
// It returns the index of the first token that is none of these.
func (fs *findState) leadingOptions(args []string, i int) int {
	for i < len(args) {
		tok := args[i]
		switch {
		case tok == "-H" || tok == "-L" || tok == "-P":
			i++
		case findOptLevelPattern.MatchString(tok):
			i++
		case tok == "-D":
			if i+1 >= len(args) {
				fs.fail("flag -D is missing its value")
				return len(args)
			}
			i += 2
		default:
			return i
		}
	}
	return i
}

// startingPoints collects bare operands up to the first token starting with
// `-`, `(` or `!` — each is a PathRead, recorded by index in fs.starts for
// -delete to revisit. No starting point at all is find's own default: an
// implicit PathRead of ".".
func (fs *findState) startingPoints(args []string, i int) int {
	for i < len(args) {
		tok := args[i]
		if strings.HasPrefix(tok, "-") || findMetaUnescape(tok) == "(" || tok == "!" {
			break
		}
		fs.starts = append(fs.starts, i)
		fs.effects = append(fs.effects, Effect{
			Kind: EffectPath, Path: tok, Access: AccessRead,
			Dynamic: fs.live(i), Source: fmt.Sprintf("arg %d", i), FromPositional: true,
		})
		i++
	}
	if len(fs.starts) == 0 {
		fs.effects = append(fs.effects, Effect{Kind: EffectPath, Path: ".", Access: AccessRead, Source: "implicit"})
	}
	return i
}

// expression walks the primary vocabulary from i to the end of argv,
// dispatching each primary to its own arity per the tables and special
// cases above. An unrecognised token stops the walk (fail-closed): later
// tokens after an unknown primary are not trusted either, matching
// scan()'s own "later tokens still not trusted" convention.
func (fs *findState) expression(args []string, i int) {
	for i < len(args) {
		tok := findMetaUnescape(args[i])
		switch {
		case tok == "-delete":
			fs.delete(i)
			i++
		case tok == "-exec" || tok == "-execdir":
			i = fs.exec(args, i, tok)
		case tok == "-ok" || tok == "-okdir":
			fs.fail("%s is interactive: its per-item confirmation is not statically knowable", tok)
			return
		case findTruncateArgPrimaries[tok]:
			i = fs.oneOperand(args, i, tok, AccessTruncate)
		case tok == "-fprintf":
			i = fs.fprintf(args, i)
		case findPathArgPrimaries[tok] || findNewerXYPattern.MatchString(tok):
			i = fs.oneOperand(args, i, tok, AccessRead)
		case findLiteralArgPrimaries[tok]:
			if i+1 >= len(args) {
				fs.fail("%s is missing its value", tok)
				return
			}
			i += 2
		case findNoArgPrimaries[tok]:
			i++
		default:
			fs.fail("unmodeled find primary %s", tok)
			return
		}
	}
}

// delete emits a PathDelete of every starting point (judged by the
// DeleteAccess policy: writable-not-deletable abstains, gitignored approves,
// a read-only/reject zone rejects); no starting points at all deletes the
// implicit ".".
func (fs *findState) delete(_ int) {
	if len(fs.starts) == 0 {
		fs.effects = append(fs.effects, Effect{Kind: EffectPath, Path: ".", Access: AccessDelete, Source: "implicit", Detail: "find -delete"})
		return
	}
	for _, idx := range fs.starts {
		fs.effects = append(fs.effects, Effect{
			Kind: EffectPath, Path: fs.leaf.Args[idx], Access: AccessDelete,
			Dynamic: fs.live(idx), Source: fmt.Sprintf("arg %d", idx), FromPositional: true, Detail: "find -delete",
		})
	}
}

// oneOperand handles a primary that takes exactly one FILE operand under the
// given access class (read for -newer/-samefile/-newerXY, truncate for
// -fprint/-fprint0/-fls), returning the index just past it.
func (fs *findState) oneOperand(args []string, i int, tok string, access PathAccess) int {
	if i+1 >= len(args) {
		fs.fail("%s is missing its FILE operand", tok)
		return len(args)
	}
	fileIdx := i + 1
	fs.effects = append(fs.effects, Effect{
		Kind: EffectPath, Path: args[fileIdx], Access: access,
		Dynamic: fs.live(fileIdx), Source: fmt.Sprintf("arg %d", fileIdx),
	})
	return i + 2
}

// fprintf is -fprintf's own shape: FILE (truncated) THEN a literal format
// string — the one truncate primary whose FILE is not its only operand.
func (fs *findState) fprintf(args []string, i int) int {
	if i+2 >= len(args) {
		fs.fail("-fprintf is missing its FILE and format operands")
		return len(args)
	}
	fileIdx := i + 1
	fs.effects = append(fs.effects, Effect{
		Kind: EffectPath, Path: args[fileIdx], Access: AccessTruncate,
		Dynamic: fs.live(fileIdx), Source: fmt.Sprintf("arg %d", fileIdx),
	})
	return i + 3
}

// exec handles -exec/-execdir CMD ARG... ; (or +): the command and its
// arguments become an "argv" ChildInvocation, mirroring xargs's shape
// (interpreter_xargs.go) — every `{}` token (find's replacement placeholder,
// matched anywhere in the token like xargs's own embedded-token case) is
// Dynamic, and so is any token that is itself a live shell expansion.
// Returns the index just past the terminator; a missing terminator or an
// empty command makes the interpretation insufficient without consuming any
// further tokens (fail-closed: the rest of argv cannot be trusted once the
// command's own extent is unknown).
func (fs *findState) exec(args []string, i int, tok string) int {
	j := i + 1
	var argv []string
	var dynamic []bool
	terminator := ""
	for j < len(args) {
		a := args[j]
		if n := findMetaUnescape(a); n == ";" || n == "+" {
			terminator = n
			break
		}
		argv = append(argv, a)
		dynamic = append(dynamic, fs.live(j) || strings.Contains(a, "{}"))
		j++
	}
	if terminator == "" {
		fs.fail("%s has no terminating ; or +", tok)
		return len(args)
	}
	if len(argv) == 0 {
		fs.fail("%s has no command", tok)
		return j + 1
	}
	fs.children = append(fs.children, ChildInvocation{Dialect: "argv", Argv: argv, ArgvDynamic: dynamic, Source: "find " + tok})
	return j + 1
}
