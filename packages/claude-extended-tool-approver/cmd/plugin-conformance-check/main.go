// Command plugin-conformance-check is pg2-z3u4f item 2's conformance-check
// tooling (widened by pg2-amzvw's wiring pass): extract every fenced shell
// command our plugins prescribe and assert each resolves to a DECISIVE ceta
// verdict (Approve/Ask/Reject) offline, rather than an abstain that falls
// through to the auto-mode LLM classifier or a dontAsk denial.
//
// # Scope of this pass — read before trusting the numbers
//
// pg2-z3u4f's own brief is explicit that a well-reasoned partial beats a
// rushed, wrong check that would gate every consumer's build incorrectly.
// What this tool DOES and does NOT cover:
//
//   - Extracts FENCED code blocks only (```bash / ```sh / ```shell / ```zsh).
//     Inline single-backtick spans (a bare `bd show <id>` in prose) are
//     DELIBERATELY NOT extracted: most inline backtick spans in this repo's
//     plugin prose are identifiers, flag names, or bead IDs, not commands,
//     and telling the two apart reliably needs its own heuristic (or a
//     doc-authoring convention) that has not been designed yet. Extracting
//     them naively would flood the report with false positives and make it
//     useless as a build gate.
//   - Treats a run of PHYSICAL lines inside a matched fenced block as one
//     candidate command, joining `\`-continued lines, an unterminated quote
//     or bracket/brace/paren, and a heredoc body (`<<'DELIM'`/`<<DELIM`
//     through its terminator line) into a single candidate — otherwise a
//     multi-line jq/heredoc snippet fragments into unparseable noise (this
//     was pg2-amzvw's main empirical finding: on a first pass, 64% of this
//     repo's OWN candidates were fragments like this, not genuine abstains).
//     It does NOT detect interleaved OUTPUT lines shown inside the same
//     fenced block (a worked example's `$ ls` followed by a literal
//     directory listing) — those still misclassify as unparseable noise.
//   - Skips a handful of mechanically-identifiable NON-command lines before
//     evaluating them at all (see isNonCommandLine): a bare doc-convention
//     placeholder token like `<id>`/`<repo>`/`<worktree-path>` (this repo's
//     established fill-in-the-blank convention — distinguished from a real
//     `< file` redirect, which never has a matching close-bracket glued to
//     the token); a markdown checklist bullet (`- ...`) sharing a fence with
//     real commands; a bare shell keyword/brace (`if`/`fi`/`case`/`esac`/
//     `{`/`}`) or a `name() {` function-definition opener; and any line
//     ending in `;;` (only ever valid inside a `case` block, so it is always
//     a case-arm, never a standalone invocation).
//   - Does NOT AUTOMATICALLY distinguish "genuinely abstains" from "the
//     extracted text is not valid shell at all" (a parse failure also folds
//     to a NoOpinion verdict, per internal/engine's unparseableExpressionFloor)
//     — both are counted together as "non-decisive". The report DOES print
//     each candidate's verdict Reason alongside it, which is usually enough
//     for a human to tell the two apart at a glance — but nothing here
//     classifies that automatically.
//   - Evaluates entirely OFFLINE with no side effects: it builds the engine
//     via setup.NewEngineForCWD (no shell store, no asklog.Store opened), so
//     unlike the compiled binary's hook mode it writes NOTHING to
//     ~/.local/share/claude-extended-tool-approver/asks.db.
//   - `--allow <prefix>` (repeatable) marks a non-decisive candidate whose
//     trimmed text starts with that literal prefix (at a word boundary) as
//     a KNOWN, TRACKED miss rather than a gate failure: it is still counted
//     and printed, under its own "known (allowed)" heading, but does not
//     make the process exit non-zero. Use this for a specific uncovered
//     tool/command CLASS (e.g. `--allow "gh stack"`).
//   - `--allow-reason <substring>` (repeatable) does the same, but matches a
//     substring of the engine verdict's Reason instead of the candidate
//     text. Use this for a cross-cutting ENGINE classification that shows
//     up across many unrelated commands (e.g. `--allow-reason "env
//     assignments only"` for a bare `x="$(cmd)"` assignment statement,
//     which several rule modules deliberately leave NoOpinion since nothing
//     runs at the statement's own top level) — one entry covers the whole
//     class, where a text-prefix entry would need one per assigned variable
//     name.
//     Both are deliberately mechanical, content-based allowlists with no
//     bead-tracking metadata of their own — the CALLER (a repo's flake.nix)
//     documents which bead tracks each value in a comment next to the
//     invocation, keeping this tool itself consumer-blind (see this repo's
//     CLAUDE.md).
//
// # Usage
//
//	go run ./cmd/plugin-conformance-check [--cwd <dir>] [--allow <prefix>]... <root>...
//
// Each <root> is scanned recursively for SKILL.md, commands/*.md, agents/*.md,
// any file under a hooks/ directory, and *-prompt.txt files. --cwd sets the
// CWD every extracted command is evaluated against (default: the first root
// given) — most extracted commands are generic snippets not tied to a
// specific repo path, so one representative CWD per invocation is enough;
// re-run once per repo root to get repo-accurate path resolution.
//
// Exit codes: 0 success (no unallowed non-decisive candidates); 2 usage
// error; 1 a generic/unexpected error (e.g. a root does not exist); 3 one or
// more non-decisive candidates were found that no `--allow` prefix covers —
// the actual build-gate-failure code, kept >= 2 per this workspace's exit-code
// convention (1 stays reserved for the generic/unexpected case above).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// candidate is one extracted command (possibly spanning several physical
// doc lines once quote/bracket/heredoc continuation is joined), with enough
// provenance to let a human find it again.
type candidate struct {
	file string
	line int // 1-based, the doc line the candidate started on
	text string
}

// fenceRe matches a fenced code block tagged bash/sh/shell/zsh. (?s) makes
// `.` match newlines so the body can span multiple lines; the tag capture
// group is unused beyond anchoring which languages count.
var fenceRe = regexp.MustCompile("(?s)```(?:bash|sh|shell|zsh)[ \\t]*\\n(.*?)```")

// heredocRe finds a heredoc operator opening on an accumulated candidate
// line: `<<DELIM`, `<<-DELIM`, `<<'DELIM'`, or `<<"DELIM"`. Best-effort: it
// does not know whether the `<<` it matched is itself inside an
// already-open quote (a rare, accepted false-negative).
var heredocRe = regexp.MustCompile(`<<-?\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// placeholderRe matches this workspace's doc-convention bare placeholder
// token: angle brackets glued tight (no space right after `<`) around a
// word, path, ellipsis, or short free-text description — e.g. `<id>`,
// `<worktree-path>`, `<path-to-settings.local.json>`, `<...>`, `<observable
// outcome that must hold before this is workable>`. A real `<` redirect
// operator has a SPACE before its filename (`cmd < file`) or is glued
// directly to a bare filename with no matching `>` anywhere nearby, so this
// still cannot mistake the common case for a placeholder. The one accepted
// false-positive risk (documented, not observed in this corpus): two
// glued, space-free redirects on one line, e.g. `cmd <infile >outfile`,
// would also match and get skipped — an under-count (reduced visibility),
// never a wrong verdict, matching this tool's existing "under-match is
// cheap, over-match is not" bias.
var placeholderRe = regexp.MustCompile(`<[A-Za-z.][A-Za-z0-9_./ -]*>`)

// funcDefRe matches a bash function-definition opener, e.g. `show_help() {`.
var funcDefRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\(\)\s*\{$`)

// structuralLines are bare shell keywords/braces that only have meaning as
// part of a surrounding compound statement — never a standalone invocation
// on their own.
var structuralLines = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"do": true, "done": true, "case": true, "esac": true,
	"{": true, "}": true,
}

// compoundHeaders maps a compound statement's opening keyword to the
// reserved word its header line always ends with: `if ...; then`, `while
// ...; do`, `for ...; do`, `case ... in`. A header line can never execute as
// a standalone command — its BODY is a separate run of top-level commands,
// each already extracted as its own candidate on its own line — so skipping
// the header loses no real coverage. Anchored at BOTH ends (the first token
// must be the keyword AND the last must be its matching closer), so this
// cannot mistake an arbitrary real command that merely happens to end in a
// common word like "do".
var compoundHeaders = map[string]string{
	"if": "then", "elif": "then",
	"while": "do", "until": "do", "for": "do",
	"case": "in",
}

func isCompoundHeader(text string) bool {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return false
	}
	closer, ok := compoundHeaders[fields[0]]
	return ok && fields[len(fields)-1] == closer
}

// isNonCommandLine reports whether text (a fully-joined candidate, after any
// quote/bracket/heredoc continuation) is a mechanically-identifiable
// non-command: a doc placeholder, a markdown bullet, a bare structural
// keyword/brace, a function-definition opener, a compound-statement header,
// or a case-arm (anything ending in `;;`, which is only ever valid inside a
// `case` block).
func isNonCommandLine(text string) bool {
	if structuralLines[text] {
		return true
	}
	if strings.HasPrefix(text, "- ") {
		return true
	}
	if strings.HasSuffix(text, ";;") {
		return true
	}
	if funcDefRe.MatchString(text) {
		return true
	}
	if placeholderRe.MatchString(text) {
		return true
	}
	if isCompoundHeader(text) {
		return true
	}
	return false
}

// isPluginSource decides whether a path is one of pg2-23z9w's named source
// classes: every SKILL.md, commands/*.md, agents/*.md, hook script, and
// prompt template. Matched by shape (basename / parent-dir name / suffix),
// not by repo, so the same binary works unchanged against any consumer's
// plugin tree.
func isPluginSource(path string) bool {
	base := filepath.Base(path)
	if base == "SKILL.md" {
		return true
	}
	if strings.HasSuffix(base, "-prompt.txt") {
		return true
	}
	parent := filepath.Base(filepath.Dir(path))
	if (parent == "commands" || parent == "agents") && strings.HasSuffix(base, ".md") {
		return true
	}
	if parent == "hooks" {
		return true
	}
	return false
}

func walkSources(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isPluginSource(path) {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}

// scanQuotesAndDepth updates the running quote/bracket-depth state after
// consuming s, and reports the (possibly still-open) quote char (0 if
// none). Characters inside an active single quote are fully literal; a
// backslash escapes the next character everywhere else (including inside a
// double quote). depth counts unmatched ( { [ outside any quote — this is a
// best-effort shell-ish scanner, not a full parser (it does not special-case
// `$'...'` ANSI-C quoting or comments), which is enough to correctly join
// the multi-line jq/heredoc-adjacent constructs this tool actually sees.
func scanQuotesAndDepth(s string, quote byte, depth *int) byte {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch quote {
		case '\'':
			if c == '\'' {
				quote = 0
			}
		case '"':
			if c == '\\' && i+1 < len(s) {
				i++
			} else if c == '"' {
				quote = 0
			}
		default:
			switch c {
			case '\\':
				if i+1 < len(s) {
					i++
				}
			case '\'':
				quote = '\''
			case '"':
				quote = '"'
			case '(', '{', '[':
				*depth++
			case ')', '}', ']':
				if *depth > 0 {
					*depth--
				}
			}
		}
	}
	return quote
}

// endsInImplicitContinuation reports whether s (already known to be outside
// any quote) ends in `&&`, `||`, or `|` — each of these implies the
// statement is incomplete and continues on the next physical line, with no
// backslash needed (the shell parser cannot terminate the statement there).
func endsInImplicitContinuation(s string) bool {
	return strings.HasSuffix(s, "&&") || strings.HasSuffix(s, "||") || strings.HasSuffix(s, "|")
}

// extractCandidates finds every fenced bash/sh/shell/zsh block in content
// and returns one candidate per logical command inside it. A logical
// command may span several physical lines: a trailing `\` continuation, an
// unterminated quote or bracket/brace/paren, or an open heredoc (`<<DELIM`
// through its terminator line) all keep accumulating onto the same
// candidate rather than fragmenting into separate, unparseable lines. Blank
// lines, `#`-comments, and the mechanically-identifiable non-commands
// isNonCommandLine names are dropped entirely (never evaluated, never
// counted).
func extractCandidates(file, content string) []candidate {
	var out []candidate
	for _, m := range fenceRe.FindAllStringSubmatchIndex(content, -1) {
		bodyStart, bodyEnd := m[2], m[3]
		body := content[bodyStart:bodyEnd]
		startLine := 1 + strings.Count(content[:bodyStart], "\n")

		rawLines := strings.Split(body, "\n")
		lineNo := startLine
		var acc strings.Builder
		accStartLine := 0
		var quote byte
		depth := 0
		heredocDelim := ""

		flush := func() {
			text := acc.String()
			if text != "" && !isNonCommandLine(text) {
				out = append(out, candidate{file: file, line: accStartLine, text: text})
			}
			acc.Reset()
			quote = 0
			depth = 0
		}

		for _, raw := range rawLines {
			if heredocDelim != "" {
				// Heredoc body: absorbed verbatim (no quote/depth scanning,
				// no trimming) until the terminator line closes it.
				acc.WriteString("\n")
				acc.WriteString(raw)
				lineNo++
				if strings.TrimSpace(raw) == heredocDelim {
					heredocDelim = ""
					if quote == 0 && depth == 0 {
						flush()
					}
				}
				continue
			}

			trimmed := strings.TrimSpace(raw)
			if acc.Len() == 0 {
				if trimmed == "" || strings.HasPrefix(trimmed, "#") {
					lineNo++
					continue
				}
				trimmed = strings.TrimPrefix(trimmed, "$ ")
				// Mechanically-identifiable non-commands (bare structural
				// keywords/braces, markdown bullets, function-def openers,
				// case-arms) are skipped BEFORE accumulation starts: letting
				// a lone "{" or "if" enter quote/bracket-depth tracking
				// would wrongly merge it with an unrelated later line that
				// happens to close the same bracket/keyword class.
				if isNonCommandLine(trimmed) {
					lineNo++
					continue
				}
				accStartLine = lineNo
			}

			quote = scanQuotesAndDepth(trimmed, quote, &depth)
			if hd := heredocRe.FindStringSubmatch(trimmed); hd != nil && quote == 0 {
				heredocDelim = hd[2]
			}

			if quote == 0 && heredocDelim == "" && strings.HasSuffix(trimmed, "\\") {
				// Backslash continuation: space-joined, exactly like the
				// original (pre-pg2-amzvw) behavior — not newline-joined,
				// since the two physical lines together form one logical
				// shell line.
				acc.WriteString(strings.TrimSuffix(trimmed, "\\"))
				acc.WriteString(" ")
				lineNo++
				continue
			}
			if quote == 0 && heredocDelim == "" && endsInImplicitContinuation(trimmed) {
				// A trailing `&&`, `||`, or `|` (outside any quote) is ALSO
				// an implicit shell line continuation — no backslash needed,
				// since the parser cannot have a complete statement yet.
				// Space-joined for the same reason as the backslash case.
				acc.WriteString(trimmed)
				acc.WriteString(" ")
				lineNo++
				continue
			}
			acc.WriteString(trimmed)

			if heredocDelim != "" {
				// A heredoc operator just opened on this line; the
				// heredocDelim branch above supplies its own "\n" before the
				// next (body) line, so nothing more to add here.
				lineNo++
				continue
			}
			if quote != 0 || depth > 0 {
				// Still inside an unterminated quote or bracket/brace/paren:
				// newline-join, preserving the original line breaks (unlike
				// backslash continuation, collapsing these to spaces can
				// change meaning, e.g. inside a `case`/`{ }` compound
				// statement that relies on the newline as a statement
				// separator).
				acc.WriteString("\n")
				lineNo++
				continue
			}
			flush()
			lineNo++
		}
		// Unterminated construct at fence close (e.g. a heredoc/quote that
		// never closed before the ``` fence): flush whatever accumulated
		// rather than silently dropping it.
		if acc.Len() > 0 {
			flush()
		}
	}
	return out
}

// verdict is the classification this tool cares about: whether ceta's engine
// reached a DECISIVE terminal verdict (Approve/Ask/Reject) or not (NoOpinion —
// either genuine chain exhaustion or an unparseable extraction).
type verdict struct {
	decisive bool
	decision hookio.Decision
	reason   string
}

// firstLine returns s up to its first newline (a rule's Reason is usually
// one sentence, but is not guaranteed to be — e.g. an unparseable-expression
// floor can quote multi-line source), so the grouped report stays one line
// per candidate.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func evaluate(eng *engine.Engine, cwd, command string) verdict {
	ti, _ := json.Marshal(hookio.BashToolInput{Command: command})
	res := eng.EvaluateHook(&hookio.HookInput{
		ToolName:  "Bash",
		CWD:       cwd,
		ToolInput: ti,
	})
	return verdict{
		decisive: res.Decision != hookio.NoOpinion,
		decision: res.Decision,
		reason:   res.Reason,
	}
}

// allowlist is a set of literal, caller-supplied prefixes (`--allow`); a
// non-decisive candidate matching one is a KNOWN, TRACKED miss rather than a
// gate failure. Matching is at a word boundary: the prefix must match the
// full candidate text, or be followed immediately by whitespace, so `--allow
// "gh stack"` does not also swallow an unrelated `gh stacked-thing`.
type allowlist []string

// isWordChar reports whether b can be part of a bare identifier/command
// name — used to decide whether a `--allow` prefix match landed on a real
// word boundary (matches() below), so `--allow "gh stack"` covers `gh stack
// merge` but not an unrelated `gh stacked-thing`, while still covering a
// boundary as punctuation-flavored as `cat <<'HELP'` (the char right after
// "cat" is `<`, not a word char).
func isWordChar(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func (a allowlist) matches(text string) bool {
	for _, prefix := range a {
		if prefix == "" {
			continue
		}
		if text == prefix {
			return true
		}
		if strings.HasPrefix(text, prefix) {
			rest := text[len(prefix):]
			if rest == "" || !isWordChar(rest[0]) {
				return true
			}
		}
	}
	return false
}

// reasonAllowlist is the `--allow-reason` counterpart to allowlist: it
// matches on the ENGINE's verdict Reason (substring) rather than the
// candidate's command text. This is the right tool for an existing,
// documented, cross-cutting ENGINE classification (e.g. "env assignments
// only, no rule has an opinion (nothing is executed)" — a bare `x="$(cmd)"`
// assignment) that shows up across many unrelated commands/tools: a
// text-prefix allowlist entry per assigned variable name would be an
// unbounded, unmaintainable list, where one reason-substring entry covers
// the whole class precisely.
type reasonAllowlist []string

func (a reasonAllowlist) matchesReason(reason string) bool {
	for _, substr := range a {
		if substr != "" && strings.Contains(reason, substr) {
			return true
		}
	}
	return false
}

// multiFlag implements flag.Value to accept a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// run is main's testable body: it takes explicit argv (excluding argv[0])
// and writers instead of touching os.Args/os.Stdout/os.Stderr/os.Exit
// directly, so tests can assert on the returned exit code and captured
// output without spawning a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin-conformance-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cwdFlag := fs.String("cwd", "", "CWD to evaluate every extracted command against (default: first root argument)")
	var allow multiFlag
	fs.Var(&allow, "allow", "literal prefix of a non-decisive candidate's command text to treat as a known, tracked miss rather than a gate failure (repeatable)")
	var allowReason multiFlag
	fs.Var(&allowReason, "allow-reason", "substring of a non-decisive verdict's Reason to treat as a known, accepted classification rather than a gate failure (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	roots := fs.Args()
	if len(roots) == 0 {
		fmt.Fprintln(stderr, "usage: plugin-conformance-check [--cwd <dir>] [--allow <prefix>]... <root>...")
		return 2
	}

	cwd := *cwdFlag
	if cwd == "" {
		cwd = roots[0]
	}
	eng := setup.NewEngineForCWD(cwd)

	var all []candidate
	for _, root := range roots {
		files, err := walkSources(root)
		if err != nil {
			fmt.Fprintf(stderr, "plugin-conformance-check: walk %s: %v\n", root, err)
			return 1
		}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintf(stderr, "plugin-conformance-check: read %s: %v\n", f, err)
				continue
			}
			all = append(all, extractCandidates(f, string(content))...)
		}
	}

	// reasons pairs each non-decisive candidate with WHY the engine did not
	// decide it — surfaced in the report so a human triaging it can tell a
	// genuine chain-exhaustion abstain from an extraction artifact without
	// re-running each line by hand.
	var decisive, knownNonDecisive, unallowedNonDecisive []candidate
	reasons := map[candidate]string{}
	byDecision := map[hookio.Decision]int{}
	for _, c := range all {
		v := evaluate(eng, cwd, c.text)
		byDecision[v.decision]++
		if v.decisive {
			decisive = append(decisive, c)
			continue
		}
		reasons[c] = v.reason
		if allowlist(allow).matches(c.text) || reasonAllowlist(allowReason).matchesReason(v.reason) {
			knownNonDecisive = append(knownNonDecisive, c)
		} else {
			unallowedNonDecisive = append(unallowedNonDecisive, c)
		}
	}

	fmt.Fprintf(stdout, "plugin-conformance-check: %d candidates extracted from %d root(s)\n", len(all), len(roots))
	fmt.Fprintf(stdout, "  decisive: %d (approve=%d ask=%d reject=%d)\n", len(decisive),
		byDecision[hookio.Approve], byDecision[hookio.Ask], byDecision[hookio.Reject])
	fmt.Fprintf(stdout, "  non-decisive (abstain / unparseable): %d (%d known/allowed, %d unallowed)\n",
		len(knownNonDecisive)+len(unallowedNonDecisive), len(knownNonDecisive), len(unallowedNonDecisive))

	printGroup(stdout, "unallowed (gate-failing), grouped by leading token:", unallowedNonDecisive, reasons)
	printGroup(stdout, "known/allowed (tracked, non-failing), grouped by leading token:", knownNonDecisive, reasons)

	if len(unallowedNonDecisive) > 0 {
		return 3
	}
	return 0
}

// printGroup renders one non-decisive bucket (unallowed or known/allowed),
// grouped by leading token, the same shape as `report --group-by command`.
func printGroup(w io.Writer, heading string, candidates []candidate, reasons map[candidate]string) {
	if len(candidates) == 0 {
		return
	}
	byFirstToken := map[string][]candidate{}
	for _, c := range candidates {
		tok := strings.Fields(c.text)
		key := "(empty)"
		if len(tok) > 0 {
			key = tok[0]
		}
		byFirstToken[key] = append(byFirstToken[key], c)
	}
	var keys []string
	for k := range byFirstToken {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(byFirstToken[keys[i]]) > len(byFirstToken[keys[j]]) })

	fmt.Fprintln(w)
	fmt.Fprintln(w, heading)
	for _, k := range keys {
		group := byFirstToken[k]
		fmt.Fprintf(w, "  %-4d %s\n", len(group), k)
		for i, c := range group {
			if i >= 3 {
				fmt.Fprintf(w, "       ... and %d more\n", len(group)-3)
				break
			}
			fmt.Fprintf(w, "       %s:%d: %q (%s)\n", c.file, c.line, c.text, firstLine(reasons[c]))
		}
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
