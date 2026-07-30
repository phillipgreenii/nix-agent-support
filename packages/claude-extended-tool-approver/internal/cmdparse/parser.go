package cmdparse

// Supported shell syntax:
//   - Simple commands: cmd arg1 arg2
//   - Compound commands: cmd1 && cmd2, cmd1 || cmd2, cmd1 ; cmd2, cmd1 | cmd2,
//     cmd1 & cmd2 (bare '&' background separator; '&>', '>&', '2>&1' preserved)
//   - Quoting: double quotes (with backslash escapes), single quotes (literal)
//   - Environment prefixes: FOO=bar cmd
//   - Redirections: <, >, >>, 2>, 2>>, &>, herestrings (<<<)
//   - Heredocs (<<, <<-, quoted or unquoted delimiter): the BODY is an opaque extent
//     lifted out before splitting, never leaves and never args — see heredoc.go
//   - Command substitution: $(cmd), `cmd`
//   - Process substitution: <(cmd), >(cmd)
//   - Subshell grouping: ( cmd1; cmd2 )
//   - Inline comments: cmd # comment
//   - Loops: for VAR in LIST; do CMD; done  /  while COND; do CMD; done
//
// Unsupported (falls through as Abstain — safe default):
//   - Brace expansion: {a,b,c}
//   - Array syntax: ${arr[@]}
//   - Coproc: coproc cmd
//   - Cross-token quote concatenation: 'it'\''s'

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// safeCmdSubstitutions: commands that never mutate and never read a file, safe
// inside $(...) regardless of arguments.
var safeCmdSubstitutions = map[string]bool{
	"mktemp": true, "date": true, "whoami": true, "id": true,
	"pwd": true, "basename": true, "dirname": true,
	"readlink": true, "realpath": true, "uname": true,
	"echo": true, "printf": true,
}

// fileReaderSubstitutions: read-only readers whose PATH ARGS must be re-checked
// against secretpath so a $(cat .env) still forces a prompt.
var fileReaderSubstitutions = map[string]bool{
	"cat": true, "grep": true, "head": true, "tail": true, "wc": true, "ls": true,
}

// gitReadSubcommands: git subcommands that only read metadata (no diff/show/log —
// those honor textconv/external-diff, an RCE surface a hook cannot neutralize).
var gitReadSubcommands = map[string]bool{
	"rev-parse": true, "rev-list": true, "symbolic-ref": true,
	"merge-base": true, "describe": true, "status": true,
}

func isSafeSubstitutionCommand(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	cmd := tokens[0]
	if safeCmdSubstitutions[cmd] {
		return true
	}
	if cmd == "hostname" && len(tokens) == 1 { // bare hostname reads; `hostname X` sets it
		return true
	}
	if cmd == "go" && len(tokens) >= 2 && tokens[1] == "env" {
		for _, t := range tokens[2:] { // go env -w/-u mutate persistent config
			if isGoEnvMutatingFlag(t) {
				return false
			}
		}
		return true
	}
	if cmd == "git" && len(tokens) >= 2 && gitReadSubcommands[tokens[1]] {
		return true
	}
	if fileReaderSubstitutions[cmd] {
		for _, t := range tokens[1:] {
			if strings.HasPrefix(t, "-") {
				// A glued value (--flag=value) still names a real path to
				// read (e.g. `grep --file=.env`); recheck the value half.
				// Bare short flags (-c, -v, ...) carry no value — skip them.
				if eq := strings.IndexByte(t, '='); eq >= 0 && secretpath.IsSecret(t[eq+1:]) {
					return false
				}
				continue
			}
			if secretpath.IsSecret(t) {
				return false // reading a secret → force a prompt
			}
		}
		return true
	}
	return false
}

// isGoEnvMutatingFlag reports whether t is any spelling of go env's -w/-u
// mutating flag: dash count (-w, --w) and a glued value (-w=true) must not
// let it slip past a naive exact-token match.
func isGoEnvMutatingFlag(t string) bool {
	if !strings.HasPrefix(t, "-") {
		return false
	}
	name := strings.TrimLeft(t, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	return name == "w" || name == "u"
}

// IsSafeSubstitutionBody reports whether cmdStr — the inner body of a
// $(...) or `...` command substitution — is safe under the STATIC allowlist. A
// body is safe only when it contains no nested substitution AND it parses (via
// the quote-aware Parse) to EXACTLY ONE leaf command with no redirection/heredoc,
// and that leaf's command+args pass isSafeSubstitutionCommand.
//
// Requiring exactly one leaf is what makes this quote-aware for free: Parse
// (via splitCompound/tokenize) already tracks single/double-quote and
// backtick state while scanning for ';', '&&', '||', '|', and '&', so a
// top-level (unquoted) compound operator splits the body into 2+ leaves
// (unsafe), while the SAME byte inside the command's own quotes — e.g. the
// '|' in `grep -E 'a|b' file` — stays glued into one argument token and the
// body still parses to a single leaf.
//
// This is the STATIC FLOOR consulted by the engine's substitution-body
// recursion (pg2-1q5i3): a body the allowlist rejects can never be made LESS
// restrictive by full-engine recursion (e.g. `git show HEAD` is approved by the
// git rule but deliberately excluded here for the textconv/external-diff RCE
// surface). Recursion may only ADD demotions, never unlock what this blocks.
func IsSafeSubstitutionBody(cmdStr string) bool {
	// A body that itself embeds a command/process substitution is opaque to the
	// static allowlist — the nested command is never inspected here. Reject it;
	// the engine's full-engine recursion evaluates the inner command instead.
	//
	// An UNPARSEABLE body is rejected for the same reason and then some: if the
	// scan desynced, "no substitution found" is not evidence there is none, so
	// clearing the body would clear text nobody enumerated. A backtick body is the
	// reachable case — indexUnescapedBacktick is not quote-aware, so `` `echo don't` ``
	// yields a quote-unbalanced body that Parse still reduces to one safe-looking
	// `echo` leaf (pg2-wguam).
	if scan := ScanSubstitutions(cmdStr); scan.Unparseable || len(scan.Substitutions) > 0 {
		return false
	}
	leaves := Parse(cmdStr)
	if len(leaves) != 1 {
		return false
	}
	leaf := leaves[0]
	if leaf.Executable == "" || len(leaf.Redirections) > 0 || leaf.HasHeredoc {
		return false
	}
	tokens := append([]string{leaf.Executable}, leaf.Args...)
	return isSafeSubstitutionCommand(tokens)
}

// SubstitutionKind classifies an extracted shell substitution.
type SubstitutionKind int

const (
	SubstCommand    SubstitutionKind = iota // $(...)
	SubstBacktick                           // `...`
	SubstProcessIn                          // <(...)
	SubstProcessOut                         // >(...)
)

// Substitution is one top-level shell substitution extracted from raw command
// text by EnumerateSubstitutions.
type Substitution struct {
	Kind SubstitutionKind
	Body string // inner command text, verbatim (NOT unquoted)
}

// IsCommandSubstitution reports whether the substitution captures a command's
// OUTPUT — $(...) or `...`. Only these are governed by the static allowlist
// floor (IsSafeSubstitutionBody); process substitutions (<(...) / >(...)) have
// no static allowlist and are governed by full-engine recursion alone.
func (s Substitution) IsCommandSubstitution() bool {
	return s.Kind == SubstCommand || s.Kind == SubstBacktick
}

// SubstitutionScan is the result of scanning text for substitutions: the
// TOP-LEVEL bodies found, plus whether the scan actually managed to model the
// text it was given.
//
// Unparseable is the security-load-bearing half (pg2-wguam). The bodies alone
// cannot distinguish "this text contains no substitution" from "the scan lost
// track and stopped looking", and the engine's fold is Approve iff no leaf
// objects — so an empty result read as the former is an AUTO-APPROVE of whatever
// the scan skipped. A caller making a security decision MUST floor an
// Unparseable scan at Abstain and MUST NOT treat its Substitutions as complete.
type SubstitutionScan struct {
	// Substitutions are the top-level bodies found before the scan stopped. When
	// Unparseable is set this list is a PREFIX, not an inventory.
	Substitutions []Substitution
	// Unparseable reports that the scan could not model the text: an unterminated
	// `$(`/backtick, a `$( )` whose extent does not balance, or (shell-text mode)
	// a quote left open at end of input.
	Unparseable bool
	// Reason names the specific desync, for the deferring caller's reason string.
	Reason string
}

// EnumerateSubstitutions scans raw shell text and returns every TOP-LEVEL
// command/process substitution body: $(...), `...`, <(...) and >(...). It is the
// single shared substitution enumerator (pg2-1q5i3) used by the engine's
// substitution-body recursion and reusable by sibling env-value recursion
// (pg2-gkd5e).
//
// It DISCARDS the scan's Unparseable flag, so it is only appropriate where a
// truncated list is conservative in the caller's own direction (gitdir's
// direction inference defaults to write when a variable's use is unseen; envvars
// refuses to clear a value that enumerates to zero substitutions). Any caller
// whose "no substitutions" branch is an approval MUST use ScanSubstitutions and
// honor Unparseable instead.
func EnumerateSubstitutions(s string) []Substitution {
	return ScanSubstitutions(s).Substitutions
}

// ScanSubstitutions scans SHELL TEXT — a command line or a substitution body,
// where quote characters are syntax.
//
// Semantics:
//   - Single-quoted spans are literal — bash performs NO substitution there — so
//     they are skipped entirely.
//   - Double-quoted spans still permit $(...) and `...` (bash performs those
//     inside double quotes); a backslash-escaped $ or ` is literal.
//   - Arithmetic $((...)) is NOT a command substitution and is skipped.
//   - Only TOP-LEVEL substitutions are returned; a nested substitution stays
//     inside the returned outer body and surfaces when the engine re-evaluates
//     that body (parallel to the existing process-substitution recursion). This
//     avoids double-processing and lets the engine cycle check apply per level.
//   - Paren matching counts every '(' (bare, $(, <(, >() so a process sub nested
//     inside a command sub — the `$(cat <(rm -rf ~))` depth-counter gotcha — is
//     NOT truncated.
//   - An unterminated substitution, or a quote still open at end of input, stops
//     the scan and sets Unparseable.
func ScanSubstitutions(s string) SubstitutionScan {
	return scanSubstitutions(s, true)
}

// ScanSubstitutionsInHeredocBody scans an UNQUOTED heredoc BODY, where quote
// characters are DATA rather than syntax.
//
// bash expands an unquoted heredoc body — parameter expansion, command
// substitution, arithmetic — but performs no word splitting and no quote
// removal: a `'` in the body is one literal apostrophe, not the start of a
// quoted region. Scanning a body with ScanSubstitutions therefore mis-models it
// twice over (pg2-wguam): an apostrophe in prose opened a phantom single-quoted
// region that swallowed every following `$( )`, so
//
//	cat <<EOF        ->  Reject (the substitution is seen)
//	$(rm -rf .git/objects)
//	don't
//	EOF
//
//	cat <<EOF        ->  the substitution is NEVER enumerated
//	don't
//	$(rm -rf .git/objects)
//	EOF
//
// reached different verdicts for the same body — the order-dependent-verdict
// class heredocFloor's fold exists to eliminate. Only the delimiter's quoting
// decides whether a body expands at all, and that discriminator is upstream:
// ParsedCommand.UnquotedHeredocBodies never hands a `<<'EOF'` body to this.
//
// Backslash pairs are still skipped, matching bash: `\$(x)` in a body is literal
// text and must not be enumerated.
func ScanSubstitutionsInHeredocBody(body string) SubstitutionScan {
	return scanSubstitutions(body, false)
}

// scanSubstitutions is the shared scan. quotesAreSyntax distinguishes shell text
// (quotes delimit literal/expanding regions, and an unclosed one is a desync)
// from an unquoted heredoc body (quotes are ordinary bytes, so no quote state
// exists to be unbalanced).
func scanSubstitutions(s string, quotesAreSyntax bool) SubstitutionScan {
	var out []Substitution
	inSingle, inDouble := false, false
	stop := func(reason string) SubstitutionScan {
		return SubstitutionScan{Substitutions: out, Unparseable: true, Reason: reason}
	}
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			i++
		case c == '\\' && i+1 < n:
			// Escaped char (bare or double-quote context): the next byte is
			// literal, so \$ / \` cannot begin a substitution.
			i += 2
		case quotesAreSyntax && c == '\'' && !inDouble:
			inSingle = true
			i++
		case quotesAreSyntax && c == '"':
			inDouble = !inDouble
			i++
		case c == '`':
			end := indexUnescapedBacktick(s, i+1)
			if end < 0 {
				return stop("unterminated backtick command substitution")
			}
			out = append(out, Substitution{Kind: SubstBacktick, Body: s[i+1 : end]})
			i = end + 1
		case c == '$' && i+1 < n && s[i+1] == '(':
			rel := matchParen(s[i+1:]) // s[i+1] == '('
			if rel < 0 {
				// Either genuinely unterminated, or the body holds an unbalanced quote
				// that makes matchParen's own quote tracking never see the closing ')'.
				// Both leave the substitution's EXTENT unknown, so nothing inside it has
				// been enumerated.
				return stop("unterminated or quote-unbalanced $( ) command substitution")
			}
			closeAbs := i + 1 + rel
			if i+2 < n && s[i+2] == '(' {
				// Arithmetic $((...)) — not a command substitution; skip it.
				i = closeAbs + 1
				continue
			}
			out = append(out, Substitution{Kind: SubstCommand, Body: s[i+2 : closeAbs]})
			i = closeAbs + 1
		case (c == '<' || c == '>') && !inDouble && i+1 < n && s[i+1] == '(':
			rel := matchParen(s[i+1:])
			if rel < 0 {
				i++ // malformed <( — treat '<' as a redirect operator, keep scanning
				continue
			}
			closeAbs := i + 1 + rel
			kind := SubstProcessIn
			if c == '>' {
				kind = SubstProcessOut
			}
			out = append(out, Substitution{Kind: kind, Body: s[i+2 : closeAbs]})
			i = closeAbs + 1
		default:
			i++
		}
	}
	if inSingle || inDouble {
		// A quote left open at end of input means every byte after it was read as
		// inert quoted text. bash would reject the command outright, but ceta must
		// not: the scan's own model is what failed, so it cannot certify that the
		// skipped tail held no substitution.
		return stop("unbalanced quote")
	}
	return SubstitutionScan{Substitutions: out}
}

// matchParen returns the index within s of the ')' that closes the '(' assumed
// to be at s[0], or -1 if unbalanced. Quote- and backslash-aware: it counts
// every '(' as depth+1 and every ')' as depth-1 outside single/double quotes, so
// nested $()/<()/>()/subshells all balance and a '(' from '<(' or '>(' does NOT
// leak (the pg2-1q5i3 truncation gotcha). Parens inside single or double quotes
// are literal and ignored.
func matchParen(s string) int {
	inSingle, inDouble := false, false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '\\':
			i++ // skip escaped char
		case c == '\'' && !inDouble:
			inSingle = true
		case c == '"':
			inDouble = !inDouble
		case inDouble:
			// Parens are literal inside double quotes — ignore them.
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// indexUnescapedBacktick returns the index of the next '`' at or after from
// that is not backslash-escaped, or -1.
func indexUnescapedBacktick(s string, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '`' {
			return i
		}
	}
	return -1
}

// StripLeadingEnvAssignments returns raw with any leading NAME=VALUE
// environment-assignment tokens (and the whitespace separating them) removed,
// yielding the raw text of the command itself (executable + args + redirections
// + process/command substitutions). The engine feeds THIS — not the whole
// segment — to EnumerateSubstitutions so a substitution inside an env VALUE
// (e.g. the `$(curl evil)` in `FOO=$(curl evil) echo hi`) is NOT recursed here:
// env-value handling is the static classifyExpansion path (and the separate
// env-value recursion of pg2-gkd5e), not this bead's command choke point.
func StripLeadingEnvAssignments(raw string) string {
	return raw[commandStartOffset(raw):]
}

// commandStartOffset returns the byte offset into raw where the executable
// begins — i.e. after any leading NAME=VALUE env-assignment tokens. It scans
// quote/paren-aware so a value with spaces inside $(...) or quotes stays one
// token. Returns len(raw) when raw is entirely env assignments.
//
// This scan deliberately does NOT use the shared shellScanner: unlike the four
// callers of that scanner, it must also glue a TOP-LEVEL bare paren group into one
// token (the `FOO=(a b) cmd` bash-array form), which the shared scanner hands back
// to its caller. Its case order already matches the shared scanner's discipline
// (single quotes first, symmetric parens), so it never had the pg2-3ggxm desync.
func commandStartOffset(raw string) int {
	inSingle, inDouble := false, false
	parenDepth := 0
	tokenStart := -1
	i, n := 0, len(raw)
	for i < n {
		c := raw[i]
		if !inSingle && !inDouble && parenDepth == 0 && (c == ' ' || c == '\t' || c == '\n') {
			if tokenStart >= 0 {
				if !isEnvAssign(raw[tokenStart:i]) {
					return tokenStart
				}
				tokenStart = -1
			}
			i++
			continue
		}
		if tokenStart < 0 {
			tokenStart = i
		}
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			i++
		case c == '\\' && i+1 < n:
			i += 2
		case c == '\'' && !inDouble:
			inSingle = true
			i++
		case c == '"':
			inDouble = !inDouble
			i++
		case c == '(':
			parenDepth++
			i++
		case c == ')':
			if parenDepth > 0 {
				parenDepth--
			}
			i++
		default:
			i++
		}
	}
	if tokenStart >= 0 {
		if !isEnvAssign(raw[tokenStart:]) {
			return tokenStart
		}
		return n
	}
	return 0
}

// wrapperPrefixes lists (executable, subcommand) pairs that act as transparent
// wrappers. A command matching one of these is unwrapped so downstream rules
// evaluate the inner command instead.
var wrapperPrefixes = []struct {
	executable string
	subcommand string
}{
	{"cloudflared", "access"},
}

// execPrefixes run an inner command after their own flags / NAME=VALUE
// assignments. They must be unwrapped so downstream rules evaluate the inner
// command — otherwise `env rm -rf /etc` / `command rm -rf /etc` hide a
// dangerous command behind a name that looks read-only.
var execPrefixes = map[string]bool{"env": true, "command": true}

// unwrapExecPrefix strips an env/command prefix's flags and NAME=VALUE
// assignments and returns the inner executable + its args. Any `env NAME=VALUE`
// assignments encountered are CAPTURED into envAssigns (not merely stripped) so
// the env-var guard sees them regardless of the prefix form (pg2-gkd5e). ok is
// false when no inner command follows (bare `env`, or only flags/assignments) —
// the caller then leaves the command as-is (a read-only env query) but still
// records the captured assignments.
func unwrapExecPrefix(base string, args []string) (inner string, innerArgs []string, envAssigns []EnvAssignment, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" { // explicit end of options
			i++
			break
		}
		// `command -v/-V NAME` describes/locates NAME (like `which`) — it does NOT
		// execute NAME. Do not unwrap: leave the bare `command` for the safe-commands
		// rule to approve as a read-only lookup. (`command -p` DOES execute → unwrap.)
		if base == "command" && (a == "-v" || a == "-V") {
			return "", nil, nil, false
		}
		// env NAME=VALUE assignment (command has none) — capture it so the env-var
		// guard can inspect it, then continue past it to the inner command.
		if base == "env" && !strings.HasPrefix(a, "-") && strings.Contains(a, "=") {
			envAssigns = append(envAssigns, newEnvAssignment(a))
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			// env -u NAME and -C DIR take a following argument; consume it too.
			if base == "env" && (a == "-u" || a == "-C") {
				i += 2
				continue
			}
			i++
			continue
		}
		break // first bare, non-assignment token is the inner executable
	}
	if i >= len(args) {
		return "", nil, envAssigns, false
	}
	return args[i], args[i+1:], envAssigns, true
}

// newEnvAssignment builds an EnvAssignment from a raw NAME=VALUE token,
// classifying its value's expansion the same way extractExecAndArgs does. The
// bash append form NAME+=VALUE is normalized to NAME so name-based guards (e.g.
// the env-var rule's injector set) are not bypassed by `export LD_PRELOAD+=…` —
// a '+' can only be the append operator here, since env-var names cannot contain
// one.
func newEnvAssignment(raw string) EnvAssignment {
	eq := strings.Index(raw, "=")
	name := strings.TrimSuffix(raw[:eq], "+")
	return EnvAssignment{
		Name:      name,
		Value:     raw[eq+1:],
		Raw:       raw,
		Expansion: classifyExpansion(raw[eq+1:]),
	}
}

func unwrapCommand(pc ParsedCommand) ParsedCommand {
	base := filepath.Base(pc.Executable)
	// `export VAR=VALUE ...` is an assignment builtin: lift each NAME=VALUE arg
	// into EnvVars so the env-var guard sees the assignment regardless of position
	// (leading / export / env-prefix / its own compound segment), while keeping the
	// leaf rule-visible with Executable=="export" so a bare `export`/`export NAME`
	// stays a read-only query the safe-commands rule can approve. Non-assignment
	// args (a bare name to export, `-f`, ...) therefore stay as args. The env-var
	// rule is DECISIVE for flagged vars and runs first, so it prevents auto-approval
	// of a dangerous `export VAR=VALUE` before safe-commands is consulted.
	//
	// A leaf with an EMPTY Executable is now equally rule-visible: the engine's
	// command-less-leaf branch runs the rule chain on its EnvVars (pg2-mtnmb). It did
	// not before, which is why an assignment-only compound segment auto-approved.
	if base == "export" {
		return liftAssignmentArgs(pc)
	}
	if execPrefixes[base] {
		if inner, innerArgs, envAssigns, ok := unwrapExecPrefix(base, pc.Args); ok {
			return unwrapCommand(ParsedCommand{
				Executable:           inner,
				Args:                 innerArgs,
				EnvVars:              appendEnvAssignments(pc.EnvVars, envAssigns),
				Redirections:         pc.Redirections,
				ProcessSubstitutions: pc.ProcessSubstitutions,
				HasHeredoc:           pc.HasHeredoc,
				Heredocs:             pc.Heredocs,
				Raw:                  pc.Raw,
				Comment:              pc.Comment,
			})
		} else if len(envAssigns) > 0 {
			// No inner command (bare `env`/`command`, or flags + NAME=VALUE only) —
			// leave the leaf as a read-only env query, but keep the captured
			// assignments visible to the env-var guard so a standalone
			// `env DANGEROUS=…` does not slip past it (pg2-gkd5e).
			pc.EnvVars = appendEnvAssignments(pc.EnvVars, envAssigns)
			return pc
		}
		// No inner command and no assignments (bare `env`/`command` or flags only) —
		// leave as-is; it is a read-only environment query handled by safe-commands.
		return pc
	}
	for _, wp := range wrapperPrefixes {
		if base != wp.executable {
			continue
		}
		// Args must be: [subcommand, innerExec, ...]
		if len(pc.Args) < 2 || pc.Args[0] != wp.subcommand {
			continue
		}
		return ParsedCommand{
			Executable:           pc.Args[1],
			Args:                 pc.Args[2:],
			EnvVars:              pc.EnvVars,
			Redirections:         pc.Redirections,
			ProcessSubstitutions: pc.ProcessSubstitutions,
			HasHeredoc:           pc.HasHeredoc,
			Heredocs:             pc.Heredocs,
			Raw:                  pc.Raw,
			Comment:              pc.Comment,
		}
	}
	return pc
}

// liftAssignmentArgs moves every leading-style NAME=VALUE token out of pc.Args
// and into pc.EnvVars (classifying each value), returning the leaf with the
// remaining non-assignment args. Used for the `export` assignment builtin so the
// env-var guard inspects `export VAR=VALUE` exactly like the leading `VAR=VALUE`
// form (pg2-gkd5e).
func liftAssignmentArgs(pc ParsedCommand) ParsedCommand {
	var remaining []string
	envVars := pc.EnvVars
	for _, a := range pc.Args {
		if isEnvAssign(a) {
			envVars = append(envVars, newEnvAssignment(a))
			continue
		}
		remaining = append(remaining, a)
	}
	pc.EnvVars = envVars
	pc.Args = remaining
	return pc
}

// appendEnvAssignments concatenates captured assignments onto an existing slice,
// returning base unchanged when there is nothing to add.
func appendEnvAssignments(base, extra []EnvAssignment) []EnvAssignment {
	if len(extra) == 0 {
		return base
	}
	return append(base, extra...)
}

type ExpansionKind int

const (
	ExpansionNone       ExpansionKind = iota // static value: "/foo/bar"
	ExpansionVarRef                          // $VAR, ${VAR:-default}
	ExpansionSafeCmd                         // $(mktemp), $(date +%F)
	ExpansionArithmetic                      // $((1+2))
	ExpansionUnknown                         // can't classify
)

type EnvAssignment struct {
	Name      string
	Value     string
	Raw       string
	Expansion ExpansionKind
}

type ParsedCommand struct {
	Executable           string
	Args                 []string
	EnvVars              []EnvAssignment
	Redirections         []hookio.Redirection
	ProcessSubstitutions []string // inner commands from <(cmd) and >(cmd)
	HasHeredoc           bool
	// Heredocs are this leaf's heredoc EXTENTS: delimiter, quoting, and the body
	// text that stripHeredocBodies lifted out of the command before splitCompound
	// could shred it into pseudo-leaves (pg2-r2rf3). Empty for a `<<<` herestring,
	// which carries its word inline and has no body.
	Heredocs []Heredoc
	Raw      string
	Comment  string
}

// shellScanner is the SINGLE byte-level shell-context scanner shared by every
// quote-aware scan in this package: ExtractComment, StripComment, splitCompound
// and tokenize. Those four each carried their own hand-written copy of this state
// machine, and the copies had drifted: splitCompound and tokenize gated quote
// tracking on `parenDepth == 0` while decrementing that depth on ANY ')'. A
// single-quoted `jq` filter inside a $(...) therefore closed the substitution at
// its own `select(` ... `)`, after which the scanner split MID-substitution — the
// pg2-3ggxm desync, which both invented phantom NAME=VALUE env assignments out of
// command fragments AND silently erased real command leaves from evaluation.
//
// Nesting is a STACK of frames, not flat booleans, because a command substitution
// starts a FRESH quoting context: a '"' inside $(...) opens a double-quoted region
// that ends inside the substitution, while a $(...) inside '"' opens a substitution
// whose own ')' closes it. One flat inDouble flag cannot express both — whichever
// way it is resolved, the other form desyncs.
type shellScanner struct {
	// frames is never empty. frames[0] is the top-level command context; every
	// unescaped `$(` pushes a nested one, and its matching ')' pops it.
	frames []scanFrame
	// escapeUnquoted selects the backslash rule, the one place the four callers
	// legitimately differ. true: a backslash escapes the next byte in BARE as well
	// as double-quoted context (splitCompound, which must not read find's `\(` as a
	// subshell). false: only inside double quotes (tokenize and the comment
	// scanners, which keep a bare backslash as a plain byte of the token).
	// Single-quoted regions never honor a backslash, matching bash.
	escapeUnquoted bool
}

// scanFrame is one command context's quoting state plus the nesting depth of bare
// `(` groups opened within it.
type scanFrame struct {
	inSingle   bool
	inDouble   bool
	inBacktick bool
	bareParens int
}

func newShellScanner(escapeUnquoted bool) *shellScanner {
	return &shellScanner{frames: []scanFrame{{}}, escapeUnquoted: escapeUnquoted}
}

// advance consumes the bytes at s[i] that merely change scanning state or that are
// INERT in the current context (inside quotes, inside a command substitution, or
// inside a bare paren group), and returns how many bytes it consumed. It returns 0
// — consuming nothing — exactly when s[i] is a LIVE top-level shell byte the
// caller must interpret itself: a separator, a '#', whitespace, a subshell '(', a
// process substitution's '<'/'>'.
//
// Callers that build output copy s[i:i+n] VERBATIM; advance never rewrites bytes.
func (sc *shellScanner) advance(s string, i int) int {
	f := &sc.frames[len(sc.frames)-1]
	c := s[i]
	switch {
	case f.inSingle:
		// A single-quoted region is literal — ONLY its closing quote is special.
		// Evaluated first (as matchParen and commandStartOffset already do) so that
		// quoted parens, pipes, newlines and backslashes cannot desync the scan,
		// whatever the substitution depth.
		if c == '\'' {
			f.inSingle = false
		}
		return 1
	case c == '\\' && i+1 < len(s) && (sc.escapeUnquoted || f.inDouble):
		return 2 // escaped pair: the next byte carries no syntax
	case c == '\'' && !f.inDouble && !f.inBacktick:
		f.inSingle = true
		return 1
	case c == '"' && !f.inBacktick:
		f.inDouble = !f.inDouble
		return 1
	case c == '`':
		f.inBacktick = !f.inBacktick
		return 1
	case c == '$' && i+1 < len(s) && s[i+1] == '(':
		sc.frames = append(sc.frames, scanFrame{})
		return 2
	case c == ')' && len(sc.frames) > 1 && f.bareParens == 0 && !f.inDouble:
		sc.frames = sc.frames[:len(sc.frames)-1]
		return 1
	case sc.nested():
		// Inert byte. Bare parens are counted SYMMETRICALLY (and only outside
		// quotes, matching matchParen) so an inner group's ')' — jq's `select(...)`,
		// awk's `print (1+2)` — cannot be mistaken for the ')' closing the
		// substitution.
		if !f.inDouble && !f.inBacktick {
			switch {
			case c == '(':
				f.bareParens++
			case c == ')' && f.bareParens > 0:
				f.bareParens--
			}
		}
		return 1
	}
	return 0
}

// nested reports whether the scanner is inside a quoted region or a command
// substitution — i.e. whether the current byte is inert rather than live top-level
// shell syntax. advance returns 0 only when this is false.
func (sc *shellScanner) nested() bool {
	if len(sc.frames) > 1 {
		return true
	}
	f := sc.frames[0]
	return f.inSingle || f.inDouble || f.inBacktick
}

// ExtractComment returns the text of a bash-style inline comment (after the
// first unquoted '#'), trimmed. Returns "" if none.
func ExtractComment(cmd string) string {
	sc := newShellScanner(false)
	i := 0
	for i < len(cmd) {
		if n := sc.advance(cmd, i); n > 0 {
			i += n
			continue
		}
		if cmd[i] == '#' && (i == 0 || unicode.IsSpace(rune(cmd[i-1]))) {
			return strings.TrimSpace(cmd[i+1:])
		}
		i++
	}
	return ""
}

// StripComment returns cmd with any bash-style inline comment removed, trimmed.
func StripComment(cmd string) string {
	sc := newShellScanner(false)
	i := 0
	for i < len(cmd) {
		if n := sc.advance(cmd, i); n > 0 {
			i += n
			continue
		}
		if cmd[i] == '#' && (i == 0 || unicode.IsSpace(rune(cmd[i-1]))) {
			return strings.TrimSpace(cmd[:i])
		}
		i++
	}
	return strings.TrimSpace(cmd)
}

func Parse(command string) []ParsedCommand {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	// Heredoc extents FIRST (pg2-r2rf3): every top-level heredoc body is lifted out
	// of the text before splitCompound can split it on its newlines, so body lines
	// never become pseudo-leaves and body words never become pseudo-args. The
	// `<<DELIM` operator token stays behind, so extractRedirections still flags the
	// leaf heredoc-bearing.
	command, heredocs := stripHeredocBodies(command)
	segments := splitCompound(command)
	segments, loopWordLists := resolveLoops(segments)
	result := make([]ParsedCommand, 0, len(segments))
	// carried holds the heredocs claimed by segments walked so far but not yet
	// attached to a leaf. Normally it is emptied onto the very next leaf. It also
	// makes the hand-out LOSSLESS if a claiming segment is dropped (resolveLoops can
	// discard keyword segments): the extent still reaches a leaf rather than
	// vanishing, so an unquoted body's substitutions are never silently unevaluated.
	var carried []Heredoc
	claim := func(seg string) {
		k := countHeredocOperators(seg)
		if k > len(heredocs) {
			k = len(heredocs)
		}
		if k == 0 {
			return
		}
		carried = append(carried, heredocs[:k]...)
		heredocs = heredocs[k:]
	}
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		claim(seg)
		comment := ExtractComment(seg)
		seg = StripComment(seg)
		if seg == "" {
			continue
		}
		tokens, procSubs := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		tokens, redirs, hasHeredoc := extractRedirections(tokens)
		leafHeredocs := carried
		carried = nil
		// A recorded extent is itself proof of a heredoc, which also closes the
		// fd-prefixed form (`2<<EOF`) that extractRedirections' token-prefix match
		// never flagged.
		hasHeredoc = hasHeredoc || len(leafHeredocs) > 0
		if len(tokens) == 0 {
			// A segment that reduces to redirections/heredoc only — e.g. the
			// trailing "> /etc/passwd" of "(cmd) > /etc/passwd", which
			// splitCompound emits as its own segment. Keep it as a command-less
			// leaf so the engine still evaluates the redirection; dropping it
			// silently loses a write to a protected path.
			if len(redirs) > 0 || hasHeredoc {
				result = append(result, ParsedCommand{Redirections: redirs, HasHeredoc: hasHeredoc, Heredocs: leafHeredocs, Raw: seg})
			}
			continue
		}
		exec, args, envVars := extractExecAndArgs(tokens)
		if exec == "" {
			// Env-assignment-only segment (`LD_PRELOAD=/evil.so && cmd`, or a whole
			// command that is nothing but assignments). Keep it as a command-less leaf
			// carrying its EnvVars — the same shape used above for a
			// redirection/heredoc-only segment, and the shape the engine's
			// command-less-leaf branch evaluates.
			//
			// Dropping the segment was a live auto-approve BYPASS (pg2-mtnmb, P1
			// SECURITY): the assignments never became rule-visible, and
			// engine.EvaluateExpression is Approve iff EVERY surviving leaf approves, so
			// `LD_PRELOAD=/evil.so && echo hi` folded to the verdict of `echo hi` alone
			// and the hook answered `allow`. This is the env-assignment half of the same
			// class c1aedd14 fixed for redirections; only the redirection half was fixed
			// then. There is no executable to unwrapCommand, so the leaf is appended
			// as-is.
			if len(envVars) > 0 || len(redirs) > 0 || hasHeredoc {
				result = append(result, ParsedCommand{EnvVars: envVars, Redirections: redirs, HasHeredoc: hasHeredoc, Heredocs: leafHeredocs, Raw: seg})
			}
			continue
		}
		result = append(result, unwrapCommand(ParsedCommand{
			Executable:           exec,
			Args:                 args,
			EnvVars:              envVars,
			Redirections:         redirs,
			ProcessSubstitutions: procSubs,
			HasHeredoc:           hasHeredoc,
			Heredocs:             leafHeredocs,
			Raw:                  seg,
			Comment:              comment,
		}))
	}
	// Any extent left unclaimed still has to reach a leaf, or an unquoted body's
	// substitutions would go unevaluated. Attach the remainder to the last leaf, or
	// synthesize a command-less heredoc leaf when there is none. Worst case this floors a
	// leaf at Abstain that did not need it — never the reverse.
	//
	// Three ways an extent goes unclaimed: resolveLoops DISCARDS the segment that held
	// the operator (`while read c; do …; done <<'EOF'` — the operator rides the `done`
	// keyword, the commonest real instance), resolveLoops reorders segments so a loop
	// condition precedes the body, or the extent pass and splitCompound disagree about
	// where a comment starts. Attribution is then imprecise; losslessness is not.
	//
	// The synthesized leaf carries an EMPTY Raw on purpose: the whole command is not a
	// single leaf, and handing it back as one would break the atomicity contract the
	// engine relies on (re-parsing a leaf's Raw must not reveal further commands).
	// Nothing downstream needs Raw here — the leaf has no executable, no assignments
	// and no redirections, only the extent.
	if leftover := append(carried, heredocs...); len(leftover) > 0 {
		if len(result) == 0 {
			result = append(result, ParsedCommand{HasHeredoc: true, Heredocs: leftover})
		} else {
			last := &result[len(result)-1]
			last.Heredocs = append(last.Heredocs, leftover...)
			last.HasHeredoc = true
		}
	}
	// A `for` loop's word list reaches a leaf of its own (pg2-qkecz hole B). It carries
	// ONLY Raw: it is data, so it has no executable and must never be judged as a
	// command, but its text can hold a live `$(...)` that genuinely executes. The
	// engine's command-less-leaf branch recurses substitutions in Raw, and that fold is
	// seeded with the neutral Approve — so a literal list such as `*.md` contributes
	// nothing and the 10,004 corpus commands with a for-loop keep their verdicts, while
	// `for x in $(curl|sh)` is judged.
	//
	// Appended AFTER the heredoc leftover net on purpose: the net attaches an unclaimed
	// extent to the LAST leaf, and a word-list leaf must not become that leaf. Leaf
	// order is otherwise immaterial — verdicts fold through MostRestrictive.
	for _, wl := range loopWordLists {
		if wl != "" {
			result = append(result, ParsedCommand{Raw: wl})
		}
	}
	return result
}

func splitCompound(s string) []string {
	var result []string
	var buf strings.Builder
	// escapeUnquoted: a bare `\(` must not be read as a subshell start (find \( … \)).
	sc := newShellScanner(true)
	flush := func() {
		if buf.Len() > 0 {
			result = append(result, buf.String())
			buf.Reset()
		}
	}
	i := 0
	for i < len(s) {
		// Quoting / command-substitution bytes are inert here: copy them verbatim and
		// let the shared scanner own the state. Only a byte it declines (a LIVE
		// top-level byte) reaches the separator logic below.
		if n := sc.advance(s, i); n > 0 {
			_, _ = buf.WriteString(s[i : i+n])
			i += n
			continue
		}
		c := s[i]
		// Comment detection: unquoted # at the start of a word — the start of
		// input, after whitespace, OR at the start of a segment (buf empty) so a
		// `#` immediately after a command separator (`;#`, `&#`, `|#`, `\n#`, or a
		// closed subshell) is a comment, exactly as bash treats the start of a word
		// after an operator. Missing the buf-empty case let an unterminated quote in
		// the comment (`;#"x`) swallow the newline, gluing the NEXT line's command
		// into the comment segment where StripComment then dropped it — a leaf that
		// silently escaped evaluation (fuzz-found bypass, pg2-t4uyx class).
		// Consume the rest of the line into the buffer WITHOUT updating quote state,
		// so that quote-like characters inside comments (e.g. "it's") don't desync tracking.
		if c == '#' && (i == 0 || buf.Len() == 0 || unicode.IsSpace(rune(s[i-1]))) {
			for i < len(s) && s[i] != '\n' {
				buf.WriteByte(s[i])
				i++
			}
			// i now points to \n or past end; let the outer loop handle \n as a splitter
			continue
		}
		// Bare subshell grouping: ( cmd1; cmd2 )
		// Must not be preceded by < or > (process substitution); a `$(` never reaches
		// here at all, the scanner consumes both bytes and pushes a frame.
		if c == '(' {
			preceded := i > 0 && (s[i-1] == '$' || s[i-1] == '<' || s[i-1] == '>')
			if !preceded {
				depth := 1
				start := i + 1
				j := start
				for j < len(s) && depth > 0 {
					switch s[j] {
					case '(':
						depth++
					case ')':
						depth--
					}
					j++
				}
				if depth == 0 {
					flush()
					inner := s[start : j-1]
					// Recursively split inner content (it may contain &&, ||, ;, etc.)
					result = append(result, splitCompound(inner)...)
					i = j
					continue
				}
			}
		}
		if i+1 < len(s) {
			two := s[i : i+2]
			if two == "&&" || two == "||" {
				flush()
				i += 2
				continue
			}
		}
		// Bare '&' is a background-job separator. '&&' is already consumed by
		// the two-char block above, so this only fires on a lone '&'. Guard
		// the redirect / fd-dup forms splitCompound sees before redirection
		// tokenization: '&>' (redirect-all) and '>&' / '2>&1' / '>&2' (fd dup).
		if c == '&' && (i+1 >= len(s) || s[i+1] != '>') && (i == 0 || s[i-1] != '>') {
			flush()
			i++
			continue
		}
		if c == ';' || c == '|' || c == '\n' {
			flush()
			i++
			continue
		}
		buf.WriteByte(c)
		i++
	}
	flush()
	return result
}

// resolveLoops post-processes segments from splitCompound to handle
// for/while/until ... do ... done constructs.  The loop keywords are
// discarded and only the body commands (and while/until conditions) are
// returned so the rule engine can evaluate them individually.
// wordLists carries the `for` word lists that were previously discarded. They are
// returned SEPARATELY from result because a word list is DATA (a list of words to
// iterate), not a command: routing it back through the ordinary segment stream would
// make `*.md` in `for f in *.md` a bogus executable and demote 10,004 distinct corpus
// commands. Parse turns each into a command-less leaf carrying only Raw, so its
// substitutions are recursed while a literal list contributes nothing (pg2-qkecz).
func resolveLoops(segments []string) (result []string, wordLists []string) {
	i := 0
	for i < len(segments) {
		trimmed := strings.TrimSpace(segments[i])
		if isLoopKeyword(trimmed) {
			body, endIdx, wordList := extractLoopBody(segments, i)
			if endIdx > i {
				innerSegs, innerWordLists := resolveLoops(body)
				result = append(result, innerSegs...)
				wordLists = append(wordLists, innerWordLists...)
				if wordList != "" {
					wordLists = append(wordLists, wordList)
				}
				// pg2-qkecz hole A: the terminator segment carried the loop
				// compound's redirections, and dropping it dropped them with it —
				// `for f in a b; do echo hi; done > /etc/passwd` approved because
				// evaluateRedirections never ran. Emit whatever trails the `done`
				// keyword as its own segment; it reduces to redirections only, which
				// is exactly the command-less-leaf shape the subshell form
				// `(cmd) > /etc/passwd` already relies on above. No new leftover net.
				if residue := doneResidue(strings.TrimSpace(segments[endIdx])); residue != "" {
					result = append(result, residue)
				}
				i = endIdx + 1
				continue
			}
		}
		result = append(result, segments[i])
		i++
	}
	return result, wordLists
}

// doneResidue returns the text trailing the `done` keyword, or "" when the segment is
// a bare `done`. The keyword test mirrors isDoneKeyword so the two cannot disagree
// about what counts as a terminator.
func doneResidue(seg string) string {
	if seg == "done" {
		return ""
	}
	if strings.HasPrefix(seg, "done ") || strings.HasPrefix(seg, "done\t") {
		return strings.TrimSpace(seg[len("done"):])
	}
	return ""
}

// forWordList returns the word-list text of a `for x in <words>` header, or "" when
// the header has no `in` clause at all — both `for x; do` (which iterates "$@") and
// the C-style `for ((i=0;i<10;i++))`. The `in` keyword is always the third word of the
// header and a loop variable can contain neither whitespace nor quotes, so the FIRST
// standalone `in` is the keyword; anything quoted necessarily sits after it.
func forWordList(header string) string {
	rest := header
	for {
		idx := strings.Index(rest, "in")
		if idx < 0 {
			return ""
		}
		before, after := rest[:idx], rest[idx+2:]
		// Standalone word: whitespace on both sides (or end of header after it).
		leftOK := before != "" && (before[len(before)-1] == ' ' || before[len(before)-1] == '\t')
		rightOK := after == "" || after[0] == ' ' || after[0] == '\t'
		if leftOK && rightOK {
			return strings.TrimSpace(after)
		}
		rest = rest[idx+2:]
	}
}

func isLoopKeyword(seg string) bool {
	return strings.HasPrefix(seg, "for ") ||
		strings.HasPrefix(seg, "while ") ||
		strings.HasPrefix(seg, "until ")
}

// extractLoopBody finds the matching do/done for a loop starting at segments[start].
// Returns the body segments and the index of the "done" segment.
// For while/until, the condition command is included in the returned body.
// If no matching done is found, returns (nil, start) to fall through to abstain.
func extractLoopBody(segments []string, start int) (body []string, endIdx int, wordList string) {
	trimmedStart := strings.TrimSpace(segments[start])
	isCondLoop := strings.HasPrefix(trimmedStart, "while ") || strings.HasPrefix(trimmedStart, "until ")

	var conditionSegs []string
	if isCondLoop {
		spaceIdx := strings.IndexByte(trimmedStart, ' ')
		if spaceIdx > 0 {
			cond := strings.TrimSpace(trimmedStart[spaceIdx+1:])
			if cond != "" {
				conditionSegs = append(conditionSegs, cond)
			}
		}
	} else {
		// pg2-qkecz hole B: for a `for` loop isCondLoop is false, so the header
		// segment was added to NEITHER conditionSegs NOR bodySegs and vanished —
		// taking any command substitution in its word list with it. The engine
		// recurses per leaf, so `for x in $(curl|sh); do echo hi; done` had `echo hi`
		// as its only leaf and the substitution reached nothing. The word list is
		// returned for a command-less leaf rather than pushed into the segment
		// stream, because it is data and must not be judged as a command.
		wordList = forWordList(trimmedStart)
	}

	doDepth := 0
	doFound := false
	var bodySegs []string

	for i := start + 1; i < len(segments); i++ {
		trimmed := strings.TrimSpace(segments[i])
		isDo, afterDo := parseDoKeyword(trimmed)

		if !doFound {
			if isDo {
				doDepth = 1
				doFound = true
				if afterDo != "" {
					bodySegs = append(bodySegs, afterDo)
				}
			} else if isCondLoop {
				// Segments between loop keyword and first 'do' are part of the condition
				conditionSegs = append(conditionSegs, segments[i])
			}
			continue
		}

		// Inside body — track depth for nested loops
		if isDo {
			doDepth++
		}
		if isDoneKeyword(trimmed) {
			doDepth--
			if doDepth == 0 {
				return append(conditionSegs, bodySegs...), i, wordList
			}
		}

		bodySegs = append(bodySegs, segments[i])
	}

	// No matching `done`: resolveLoops keeps the header segment verbatim, so the word
	// list is already in the segment stream and MUST NOT also be returned here — it
	// would be judged twice, once as a command.
	return nil, start, ""
}

// parseDoKeyword checks if a trimmed segment is the "do" keyword, optionally
// followed by a command.  Returns (true, afterDo) or (false, "").
func parseDoKeyword(seg string) (bool, string) {
	if seg == "do" {
		return true, ""
	}
	if strings.HasPrefix(seg, "do ") || strings.HasPrefix(seg, "do\t") {
		return true, strings.TrimSpace(seg[2:])
	}
	return false, ""
}

func isDoneKeyword(seg string) bool {
	return seg == "done" || strings.HasPrefix(seg, "done ") || strings.HasPrefix(seg, "done\t")
}

func tokenize(s string) ([]string, []string) {
	var tokens []string
	var procSubs []string
	var buf strings.Builder
	// escapeUnquoted=false: outside double quotes a backslash stays a plain byte of
	// the token (tokenize has never collapsed `\(`/`\ ` in bare context).
	sc := newShellScanner(false)
	i := 0
	for i < len(s) {
		// Quoting / command-substitution bytes are inert here: copy them verbatim and
		// let the shared scanner own the state, so a quoted `(`/`)`/space inside a
		// $(...) cannot break the token apart (pg2-3ggxm).
		if n := sc.advance(s, i); n > 0 {
			_, _ = buf.WriteString(s[i : i+n])
			i += n
			continue
		}
		c := s[i]
		// Process substitution: <(cmd) or >(cmd) — top level only, which is exactly
		// where the scanner declines the '<'/'>'.
		if (c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(' {
			depth := 1
			start := i + 2
			j := start
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			if depth == 0 {
				procSubs = append(procSubs, s[start:j-1])
				_, _ = buf.WriteString("/dev/fd/63")
				i = j
				continue
			}
			buf.WriteByte(c) // malformed, pass through
			i++
			continue
		}
		if c == ' ' || c == '\t' {
			if buf.Len() > 0 {
				tokens = append(tokens, unquote(buf.String()))
				buf.Reset()
			}
			i++
			continue
		}
		buf.WriteByte(c)
		i++
	}
	if buf.Len() > 0 {
		tokens = append(tokens, unquote(buf.String()))
	}
	return tokens, procSubs
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		inner := s[1 : len(s)-1]
		var buf strings.Builder
		buf.Grow(len(inner))
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				next := inner[i+1]
				switch next {
				case '"', '\\':
					buf.WriteByte(next)
					i++
				default:
					buf.WriteByte(inner[i])
				}
			} else {
				buf.WriteByte(inner[i])
			}
		}
		return buf.String()
	}
	return s
}

// redirectionOperators maps shell redirection operators to their RedirectionKind.
// Ordered longest-first so prefix matching works correctly.
var redirectionOperators = []struct {
	op   string
	kind hookio.RedirectionKind
}{
	{"2>>", hookio.RedirectStderr},
	{"2>", hookio.RedirectStderr},
	{"&>", hookio.RedirectAll},
	{">>", hookio.RedirectStdout},
	{">", hookio.RedirectStdout},
	{"<", hookio.RedirectStdin},
}

// extractRedirections scans tokens for redirection operators and their targets,
// returning cleaned tokens, collected redirections, and whether a heredoc was found.
func extractRedirections(tokens []string) (cleaned []string, redirs []hookio.Redirection, hasHeredoc bool) {
	i := 0
	for i < len(tokens) {
		tok := tokens[i]

		// Process substitution placeholders should not be treated as redirections
		if strings.HasPrefix(tok, "<(") || strings.HasPrefix(tok, ">(") {
			cleaned = append(cleaned, tok)
			i++
			continue
		}

		// Check for fd duplication patterns: 2>&1, >&2, etc.
		if tok == "2>&1" || tok == ">&2" || tok == "1>&2" || tok == "2>&-" || tok == ">&-" {
			i++
			continue
		}

		// Check for heredoc/herestring operators
		if tok == "<<<" || tok == "<<" {
			hasHeredoc = true
			// Skip the operator and delimiter token
			i++
			if i < len(tokens) {
				i++ // skip delimiter
			}
			continue
		}
		if strings.HasPrefix(tok, "<<<") {
			hasHeredoc = true
			i++
			continue
		}
		if strings.HasPrefix(tok, "<<") && !strings.HasPrefix(tok, "<<<") {
			hasHeredoc = true
			i++
			continue
		}

		// Try to match a redirection operator
		matched := false
		for _, ro := range redirectionOperators {
			if tok == ro.op {
				// Operator is a standalone token; next token is the path
				if i+1 < len(tokens) {
					redirs = append(redirs, hookio.Redirection{
						Operator: ro.op,
						Path:     tokens[i+1],
						Kind:     ro.kind,
					})
					i += 2
				} else {
					// No path follows — skip the dangling operator
					i++
				}
				matched = true
				break
			}
			if strings.HasPrefix(tok, ro.op) {
				// Operator and path glued together, e.g. "2>/dev/null"
				path := tok[len(ro.op):]
				redirs = append(redirs, hookio.Redirection{
					Operator: ro.op,
					Path:     path,
					Kind:     ro.kind,
				})
				i++
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		cleaned = append(cleaned, tok)
		i++
	}
	if len(redirs) == 0 {
		redirs = nil
	}
	return
}

func extractExecAndArgs(tokens []string) (exec string, args []string, envVars []EnvAssignment) {
	for i, t := range tokens {
		if !isEnvAssign(t) {
			exec = t
			if i+1 < len(tokens) {
				args = tokens[i+1:]
			}
			return
		}
		envVars = append(envVars, newEnvAssignment(t))
	}
	return "", nil, envVars
}

func classifyExpansion(value string) ExpansionKind {
	if !strings.ContainsAny(value, "$`") {
		return ExpansionNone
	}
	if strings.Contains(value, "$((") {
		return ExpansionArithmetic
	}
	if strings.Contains(value, "$(") {
		return classifyCmdSubstitution(value)
	}
	if strings.Contains(value, "`") {
		return classifyBacktickSubstitution(value)
	}
	// Simple $VAR or ${VAR} reference
	return ExpansionVarRef
}

func classifyCmdSubstitution(value string) ExpansionKind {
	start := strings.Index(value, "$(")
	if start == -1 {
		return ExpansionUnknown
	}
	// Find the matching ')' with the shared quote/paren-aware matcher so a
	// process sub nested in the body (e.g. `$(cat <(rm -rf ~))`) does NOT
	// truncate the body at the process sub's own ')' (pg2-1q5i3 gotcha).
	rel := matchParen(value[start+1:]) // value[start+1] == '('
	if rel < 0 {
		return ExpansionUnknown
	}
	closeAbs := start + 1 + rel
	// Check for additional expansions in prefix or remainder (security: multiple substitutions)
	remainder := value[closeAbs+1:]
	prefix := value[:start]
	if strings.Contains(remainder, "$(") || strings.Contains(remainder, "`") || strings.Contains(remainder, "$") ||
		strings.Contains(prefix, "$(") || strings.Contains(prefix, "`") || strings.Contains(prefix, "$") {
		return ExpansionUnknown
	}
	cmdStr := strings.TrimSpace(value[start+2 : closeAbs])
	if cmdStr == "" {
		return ExpansionUnknown
	}
	if IsSafeSubstitutionBody(cmdStr) {
		return ExpansionSafeCmd
	}
	return ExpansionUnknown
}

func classifyBacktickSubstitution(value string) ExpansionKind {
	first := strings.Index(value, "`")
	last := strings.LastIndex(value, "`")
	if first == last {
		return ExpansionUnknown
	}
	// Check for additional expansions (security: multiple substitutions)
	prefix := value[:first]
	remainder := value[last+1:]
	inner := value[first+1 : last]
	if strings.Contains(remainder, "$(") || strings.Contains(remainder, "`") || strings.Contains(remainder, "$") ||
		strings.Contains(prefix, "$(") || strings.Contains(prefix, "`") || strings.Contains(prefix, "$") ||
		strings.Contains(inner, "`") {
		return ExpansionUnknown
	}
	cmdStr := strings.TrimSpace(value[first+1 : last])
	if cmdStr == "" {
		return ExpansionUnknown
	}
	if IsSafeSubstitutionBody(cmdStr) {
		return ExpansionSafeCmd
	}
	return ExpansionUnknown
}

// HasUnsafeCommandSubstitution reports whether s contains a $(...) (non-arithmetic)
// or `...` command substitution whose inner command is not on the safe list
// (safeCmdSubstitutions). Bare $VAR / ${VAR} references and arithmetic $((...))
// are NOT substitutions and return false; $(date)/$(mktemp) return false.
//
// This lets the engine demote ANY leaf whose executable or args embed an
// arbitrary inner command (e.g. `echo $(rm -rf ~)`), even when the outer command
// is otherwise "safe" — the outer rule never sees the inner command.
func HasUnsafeCommandSubstitution(s string) bool {
	if !strings.ContainsAny(s, "$`") {
		return false
	}
	if strings.Contains(s, "`") && classifyBacktickSubstitution(s) == ExpansionUnknown {
		return true
	}
	idx := 0
	for {
		p := strings.Index(s[idx:], "$(")
		if p < 0 {
			break
		}
		abs := idx + p
		if strings.HasPrefix(s[abs:], "$((") { // arithmetic, not a command substitution
			idx = abs + 3
			continue
		}
		if classifyCmdSubstitution(s[abs:]) == ExpansionUnknown {
			return true
		}
		idx = abs + 2
	}
	return false
}

// isEnvAssign reports whether s is a NAME=VALUE environment assignment. It is the
// single gate in front of newEnvAssignment (which indexes raw[:eq] with no bounds
// check), so all four call sites — commandStartOffset, liftAssignmentArgs and
// extractExecAndArgs — share one definition of "assignment".
//
// The NAME must be a valid shell identifier. Without that check ANY token
// containing an '=' was accepted, so a command FRAGMENT produced by a scanner
// desync (pg2-3ggxm) was lifted into EnvVars as a phantom assignment whose "name"
// was raw shell text — escalated to Ask by the env-var rule and echoed into the
// user-facing prompt. This is defense-in-depth behind the quote-aware scanners:
// a real assignment always has a valid identifier, so the guard can never mask a
// legitimate one. It also subsumes the former leading-'-' check, since '-' is not
// a valid identifier start.
func isEnvAssign(s string) bool {
	eq := strings.Index(s, "=")
	if eq <= 0 {
		return false
	}
	return isValidEnvName(s[:eq])
}

// isValidEnvName reports whether name matches ^[A-Za-z_][A-Za-z0-9_]*$, tolerating
// the single trailing '+' of the bash append form NAME+=VALUE (newEnvAssignment
// normalizes that suffix away, so the guard must accept it or `export
// LD_PRELOAD+=…` would stop being seen as an assignment).
func isValidEnvName(name string) bool {
	name = strings.TrimSuffix(name, "+")
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

func NormalizeExecutable(executable, projectRoot, cwd string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return executable
	}
	projectRoot = filepath.Clean(projectRoot)
	cwd = filepath.Clean(cwd)
	if !filepath.IsAbs(executable) {
		if strings.HasPrefix(executable, "./") {
			executable = filepath.Join(cwd, executable)
		} else if !strings.Contains(executable, "/") {
			return executable
		} else {
			executable = filepath.Join(cwd, executable)
		}
	}
	executable = filepath.Clean(executable)
	if projectRoot != "" && (executable == projectRoot || strings.HasPrefix(executable+"/", projectRoot+"/")) {
		rel, err := filepath.Rel(projectRoot, executable)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return executable
}

// NormalizeCommand returns a canonical, NON-truncated representation of a bash
// command, suitable as a stable grouping key for "same command" analysis (e.g.
// bucketing decision rows in the identify-hook-misses taxonomy).
//
// It parses the command into leaf commands — splitting on &&, ||, ;, |,
// newlines, loops and subshells (see Parse / splitCompound) — and re-joins each
// leaf's normalized "<executable> <args...>" form with " && ". Because Parse
// splits newline- and &&-separated compounds into the SAME leaf sequence,
// "cd foo && work" and "cd foo\nwork" yield the SAME key. Env-assignment-only
// and redirection-only leaves (no executable) are dropped from the key.
//
// Unlike asklog.bashSummary, NormalizeCommand NEVER truncates at the first
// newline or at a fixed length, so two distinct commands that share a long
// (>120-char) common prefix remain distinct keys instead of collapsing into a
// phantom bucket (bead pg2-okd13.3). projectRoot/cwd thread through to
// NormalizeExecutable so path executables normalize consistently; pass "" when
// unknown (non-path executables are unaffected by either).
func NormalizeCommand(command, projectRoot, cwd string) string {
	leaves := Parse(command)
	parts := make([]string, 0, len(leaves))
	for _, lc := range leaves {
		if lc.Executable == "" {
			continue
		}
		exec := NormalizeExecutable(lc.Executable, projectRoot, cwd)
		if len(lc.Args) > 0 {
			parts = append(parts, exec+" "+strings.Join(lc.Args, " "))
		} else {
			parts = append(parts, exec)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(command)
	}
	return strings.Join(parts, " && ")
}
