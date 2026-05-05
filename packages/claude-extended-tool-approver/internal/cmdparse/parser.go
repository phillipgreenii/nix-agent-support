package cmdparse

// Supported shell syntax:
//   - Simple commands: cmd arg1 arg2
//   - Compound commands: cmd1 && cmd2, cmd1 || cmd2, cmd1 ; cmd2, cmd1 | cmd2
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
)

var safeCmdSubstitutions = map[string]bool{
	"mktemp": true, "date": true, "whoami": true, "id": true,
	"pwd": true, "basename": true, "dirname": true,
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

func unwrapCommand(pc ParsedCommand) ParsedCommand {
	base := filepath.Base(pc.Executable)
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

type ExpansionKind int

const (
	ExpansionNone       ExpansionKind = iota // static value: "/foo/bar"
	ExpansionVarRef                           // $VAR, ${VAR:-default}
	ExpansionSafeCmd                          // $(mktemp), $(date +%F)
	ExpansionArithmetic                       // $((1+2))
	ExpansionUnknown                          // can't classify
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
			continue
		}
		exec, args, envVars := extractExecAndArgs(tokens)
		if exec == "" {
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
						if s[j] == '(' {
							depth++
						} else if s[j] == ')' {
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
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
				}
				j++
			}
			if depth == 0 {
				procSubs = append(procSubs, s[start:j-1])
				buf.WriteString("/dev/fd/63")
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
		eq := strings.Index(t, "=")
		envVars = append(envVars, EnvAssignment{
			Name:      t[:eq],
			Value:     t[eq+1:],
			Raw:       t,
			Expansion: classifyExpansion(t[eq+1:]),
		})
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
	inner := value[start+2:]
	depth := 1
	end := -1
	for i := 0; i < len(inner); i++ {
		if i+1 < len(inner) && inner[i] == '$' && inner[i+1] == '(' {
			depth++
			i++
		} else if inner[i] == ')' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end == -1 {
		return ExpansionUnknown
	}
	// Check for additional expansions in prefix or remainder (security: multiple substitutions)
	fullEnd := start + 2 + end + 1
	remainder := value[fullEnd:]
	prefix := value[:start]
	if strings.Contains(remainder, "$(") || strings.Contains(remainder, "`") || strings.Contains(remainder, "$") ||
		strings.Contains(prefix, "$(") || strings.Contains(prefix, "`") || strings.Contains(prefix, "$") {
		return ExpansionUnknown
	}
	cmdStr := strings.TrimSpace(inner[:end])
	tokens := strings.Fields(cmdStr)
	if len(tokens) == 0 {
		return ExpansionUnknown
	}
	if safeCmdSubstitutions[tokens[0]] {
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
	tokens := strings.Fields(cmdStr)
	if len(tokens) == 0 {
		return ExpansionUnknown
	}
	if safeCmdSubstitutions[tokens[0]] {
		return ExpansionSafeCmd
	}
	return ExpansionUnknown
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
