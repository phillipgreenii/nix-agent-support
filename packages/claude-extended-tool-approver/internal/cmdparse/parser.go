package cmdparse

// Supported shell syntax:
//   - Simple commands: cmd arg1 arg2
//   - Compound commands: cmd1 && cmd2, cmd1 || cmd2, cmd1 ; cmd2, cmd1 | cmd2,
//     cmd1 & cmd2 (bare '&' background separator; '&>', '>&', '2>&1' preserved)
//   - Quoting: double quotes (with backslash escapes), single quotes (literal)
//   - Environment prefixes: FOO=bar cmd
//   - Redirections: <, >, >>, 2>, 2>>, &>, heredocs (<<, <<<)
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
	if len(EnumerateSubstitutions(cmdStr)) > 0 {
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

// EnumerateSubstitutions scans raw shell text and returns every TOP-LEVEL
// command/process substitution body: $(...), `...`, <(...) and >(...). It is the
// single shared substitution enumerator (pg2-1q5i3) used by the engine's
// substitution-body recursion and reusable by sibling env-value recursion
// (pg2-gkd5e).
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
//   - An unterminated substitution stops the scan (best-effort, safe default).
func EnumerateSubstitutions(s string) []Substitution {
	var out []Substitution
	inSingle, inDouble := false, false
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
		case c == '\'' && !inDouble:
			inSingle = true
			i++
		case c == '"':
			inDouble = !inDouble
			i++
		case c == '`':
			end := indexUnescapedBacktick(s, i+1)
			if end < 0 {
				return out // unterminated backtick — stop
			}
			out = append(out, Substitution{Kind: SubstBacktick, Body: s[i+1 : end]})
			i = end + 1
		case c == '$' && i+1 < n && s[i+1] == '(':
			rel := matchParen(s[i+1:]) // s[i+1] == '('
			if rel < 0 {
				return out // unterminated — stop
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
	return out
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
	// (leading / export / env-prefix), while keeping the leaf rule-visible with
	// Executable=="export". Keeping Executable non-empty matters: a leaf with an
	// empty Executable is handled by the engine's command-less-leaf branch and
	// never reaches the env-var rule (pg2-gkd5e). Non-assignment args (a bare name
	// to export, `-f`, ...) stay as args so a bare `export`/`export NAME` remains a
	// read-only query the safe-commands rule can approve; the env-var rule is
	// DECISIVE for flagged vars and runs first, so it prevents auto-approval of a
	// dangerous `export VAR=VALUE` before safe-commands is consulted.
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
	Raw                  string
	Comment              string
}

// ExtractComment returns the text of a bash-style inline comment (after the
// first unquoted '#'), trimmed. Returns "" if none.
func ExtractComment(cmd string) string {
	inSingle, inDouble := false, false
	inBacktick := false
	parenDepth := 0
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'' && !inDouble && !inBacktick && parenDepth == 0:
			inSingle = !inSingle
		case c == '"' && !inSingle && !inBacktick && parenDepth == 0:
			inDouble = !inDouble
		case c == '\\' && inDouble && i+1 < len(cmd):
			i++ // skip next char (it's escaped)
		case c == '`' && !inSingle:
			inBacktick = !inBacktick
		case c == '$' && !inSingle && i+1 < len(cmd) && cmd[i+1] == '(':
			parenDepth++
			i++
		case c == ')' && !inSingle && parenDepth > 0:
			parenDepth--
		case c == '#' && !inSingle && !inDouble && !inBacktick && parenDepth == 0:
			if i == 0 || unicode.IsSpace(rune(cmd[i-1])) {
				return strings.TrimSpace(cmd[i+1:])
			}
		default:
			// continue
		}
	}
	return ""
}

// StripComment returns cmd with any bash-style inline comment removed, trimmed.
func StripComment(cmd string) string {
	inSingle, inDouble := false, false
	inBacktick := false
	parenDepth := 0
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'' && !inDouble && !inBacktick && parenDepth == 0:
			inSingle = !inSingle
		case c == '"' && !inSingle && !inBacktick && parenDepth == 0:
			inDouble = !inDouble
		case c == '\\' && inDouble && i+1 < len(cmd):
			i++ // skip next char (it's escaped)
		case c == '`' && !inSingle:
			inBacktick = !inBacktick
		case c == '$' && !inSingle && i+1 < len(cmd) && cmd[i+1] == '(':
			parenDepth++
			i++
		case c == ')' && !inSingle && parenDepth > 0:
			parenDepth--
		case c == '#' && !inSingle && !inDouble && !inBacktick && parenDepth == 0:
			if i == 0 || unicode.IsSpace(rune(cmd[i-1])) {
				return strings.TrimSpace(cmd[:i])
			}
		default:
			// continue
		}
	}
	return strings.TrimSpace(cmd)
}

func Parse(command string) []ParsedCommand {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	segments := splitCompound(command)
	segments = resolveLoops(segments)
	result := make([]ParsedCommand, 0, len(segments))
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
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
		if len(tokens) == 0 {
			// A segment that reduces to redirections/heredoc only — e.g. the
			// trailing "> /etc/passwd" of "(cmd) > /etc/passwd", which
			// splitCompound emits as its own segment. Keep it as a command-less
			// leaf so the engine still evaluates the redirection; dropping it
			// silently loses a write to a protected path.
			if len(redirs) > 0 || hasHeredoc {
				result = append(result, ParsedCommand{Redirections: redirs, HasHeredoc: hasHeredoc, Raw: seg})
			}
			continue
		}
		exec, args, envVars := extractExecAndArgs(tokens)
		if exec == "" {
			// Env-assignment-only segment; keep any redirections/heredoc it carries.
			if len(redirs) > 0 || hasHeredoc {
				result = append(result, ParsedCommand{Redirections: redirs, HasHeredoc: hasHeredoc, Raw: seg})
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
			Raw:                  seg,
			Comment:              comment,
		}))
	}
	return result
}

func splitCompound(s string) []string {
	var result []string
	var buf strings.Builder
	inSingle, inDouble := false, false
	inBacktick := false
	parenDepth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' && !inDouble && !inBacktick && parenDepth == 0:
			inSingle = !inSingle
			buf.WriteByte(c)
		case c == '"' && !inSingle && !inBacktick && parenDepth == 0:
			inDouble = !inDouble
			buf.WriteByte(c)
		case c == '\\' && !inSingle && i+1 < len(s):
			// Backslash escaping: in bare or double-quote context, consume the next char.
			// Prevents \( from being treated as subshell start (e.g. find \( ... \)).
			buf.WriteByte(c)
			i++
			buf.WriteByte(s[i])
		case c == '`' && !inSingle:
			inBacktick = !inBacktick
			buf.WriteByte(c)
		case c == '$' && !inSingle && i+1 < len(s) && s[i+1] == '(':
			parenDepth++
			buf.WriteByte(c)
			buf.WriteByte(s[i+1])
			i++
		case c == ')' && !inSingle && parenDepth > 0:
			parenDepth--
			buf.WriteByte(c)
		case inSingle || inDouble || inBacktick || parenDepth > 0:
			buf.WriteByte(c)
		default:
			// Comment detection: unquoted # preceded by whitespace or at start of input.
			// Consume the rest of the line into the buffer WITHOUT updating quote state,
			// so that quote-like characters inside comments (e.g. "it's") don't desync tracking.
			if c == '#' && (i == 0 || unicode.IsSpace(rune(s[i-1]))) {
				for i < len(s) && s[i] != '\n' {
					buf.WriteByte(s[i])
					i++
				}
				// i now points to \n or past end; let the outer loop handle \n as a splitter
				continue
			}
			// Bare subshell grouping: ( cmd1; cmd2 )
			// Must not be preceded by $, <, or > (those are command/process substitution).
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
						if buf.Len() > 0 {
							result = append(result, buf.String())
							buf.Reset()
						}
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
					if buf.Len() > 0 {
						result = append(result, buf.String())
						buf.Reset()
					}
					i++
					i++
					continue
				}
			}
			// Bare '&' is a background-job separator. '&&' is already consumed by
			// the two-char block above, so this only fires on a lone '&'. Guard
			// the redirect / fd-dup forms splitCompound sees before redirection
			// tokenization: '&>' (redirect-all) and '>&' / '2>&1' / '>&2' (fd dup).
			if c == '&' && (i+1 >= len(s) || s[i+1] != '>') && (i == 0 || s[i-1] != '>') {
				if buf.Len() > 0 {
					result = append(result, buf.String())
					buf.Reset()
				}
				i++
				continue
			}
			if c == ';' || c == '|' || c == '\n' {
				if buf.Len() > 0 {
					result = append(result, buf.String())
					buf.Reset()
				}
				i++
				continue
			}
			buf.WriteByte(c)
		}
		i++
	}
	if buf.Len() > 0 {
		result = append(result, buf.String())
	}
	return result
}

// resolveLoops post-processes segments from splitCompound to handle
// for/while/until ... do ... done constructs.  The loop keywords are
// discarded and only the body commands (and while/until conditions) are
// returned so the rule engine can evaluate them individually.
func resolveLoops(segments []string) []string {
	var result []string
	i := 0
	for i < len(segments) {
		trimmed := strings.TrimSpace(segments[i])
		if isLoopKeyword(trimmed) {
			body, endIdx := extractLoopBody(segments, i)
			if endIdx > i {
				result = append(result, resolveLoops(body)...)
				i = endIdx + 1
				continue
			}
		}
		result = append(result, segments[i])
		i++
	}
	return result
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
func extractLoopBody(segments []string, start int) (body []string, endIdx int) {
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
				return append(conditionSegs, bodySegs...), i
			}
		}

		bodySegs = append(bodySegs, segments[i])
	}

	return nil, start
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
	inSingle, inDouble := false, false
	inBacktick := false
	parenDepth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' && !inDouble && !inBacktick && parenDepth == 0:
			inSingle = !inSingle
			buf.WriteByte(c)
		case c == '"' && !inSingle && !inBacktick && parenDepth == 0:
			inDouble = !inDouble
			buf.WriteByte(c)
		case c == '\\' && inDouble && i+1 < len(s):
			buf.WriteByte(c)
			i++
			buf.WriteByte(s[i])
		case c == '`' && !inSingle:
			inBacktick = !inBacktick
			buf.WriteByte(c)
		case c == '$' && !inSingle && i+1 < len(s) && s[i+1] == '(':
			parenDepth++
			buf.WriteByte(c)
			buf.WriteByte(s[i+1])
			i++
		case c == ')' && !inSingle && parenDepth > 0:
			parenDepth--
			buf.WriteByte(c)
		case !inSingle && !inDouble && !inBacktick && parenDepth == 0 &&
			(c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(':
			// Process substitution: <(cmd) or >(cmd)
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
				i = j - 1 // loop will i++
			} else {
				buf.WriteByte(c) // malformed, pass through
			}
		case inSingle || inDouble || inBacktick || parenDepth > 0:
			buf.WriteByte(c)
		case c == ' ' || c == '\t':
			if buf.Len() > 0 {
				tokens = append(tokens, unquote(buf.String()))
				buf.Reset()
			}
		default:
			buf.WriteByte(c)
		}
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

func isEnvAssign(s string) bool {
	if strings.HasPrefix(s, "-") {
		return false
	}
	eq := strings.Index(s, "=")
	return eq > 0
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
