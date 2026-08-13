package cmdparse

// Supported shell syntax:
//   - Simple commands: cmd arg1 arg2
//   - Compound commands: cmd1 && cmd2, cmd1 || cmd2, cmd1 ; cmd2, cmd1 | cmd2,
//     cmd1 & cmd2 (bare '&' background separator; '&>', '>&', '2>&1' preserved)
//   - Quoting: double quotes (with backslash escapes), single quotes (literal)
//   - Environment prefixes: FOO=bar cmd
//   - Redirections: any [FD]OP[TARGET] where FD is empty, a descriptor number, or
//     bash's `{varname}` open-and-assign form, and OP is one of <, >, >>, >|, >&,
//     <>, &>, &>>; plus fd duplication/close N>&M / N>&- (dropped: no file target)
//     and herestrings (<<<)
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
	"regexp"
	"strings"

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
// body is safe only when it contains no nested substitution AND it parses to
// EXACTLY ONE SIMPLE COMMAND with no redirection/heredoc, and that command's
// command+args pass isSafeSubstitutionCommand.
//
// Quote-awareness is now a PARSER FACT rather than a property inherited from a
// leaf count (ADR 0039 step 2a): the body goes through the seam, so the '|' in
// `grep -E 'a|b' file` is a literal inside one argument word because the bash
// grammar says so, not because a hand-rolled splitter happened to track quote
// state. `soleSimpleCommandLeaf` also TIGHTENS the shape test — the sole
// statement must BE a simple command, not merely reduce to one leaf — which is
// what keeps a real grammar from newly clearing `(cat VERSION)` or
// `{ cat VERSION; }`; see its own comment.
//
// An UNPARSEABLE body is refused for the same reason a nested substitution is,
// and then some: if the body does not parse, "no substitution found" is not
// evidence there is none, so clearing it would clear text nobody enumerated. That
// was the reachable half of the pg2-wguam P0 — the outgoing backtick extent scan
// (`indexUnescapedBacktick`, deleted in this step) was not quote-aware at all, so
// “ `echo don't` “ yielded a quote-unbalanced body that still reduced to one
// safe-looking `echo` leaf. A body that does not parse now cannot reach the
// allowlist at all, because the seam returns no leaf for it.
//
// This is the STATIC FLOOR consulted by the engine's substitution-body
// recursion (pg2-1q5i3): a body the allowlist rejects can never be made LESS
// restrictive by full-engine recursion (e.g. `git show HEAD` is approved by the
// git rule but deliberately excluded here for the textconv/external-diff RCE
// surface). Recursion may only ADD demotions, never unlock what this blocks.
func IsSafeSubstitutionBody(cmdStr string) bool {
	leaf, ok := soleSimpleCommandLeaf(cmdStr)
	if !ok {
		return false
	}
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
	// Substitutions are the top-level bodies found. When Unparseable is set this
	// list is a PREFIX, not an inventory: it holds the substitutions whose extents
	// the seam could still delimit exactly, and omits any whose closing delimiter
	// was missing — for those the extent is unknown, so nothing inside them has
	// been enumerated.
	Substitutions []Substitution
	// Unparseable reports that the text is not valid bash, as judged by the STRICT
	// parser behind the seam: an unterminated `$(`/backtick, an unbalanced quote, an
	// unclosed here-document, a zsh-only construct. It is deliberately NOT weakened
	// by the error-recovering pass the seam consults to salvage the prefix above —
	// recovery adds bodies to recurse and never clears this flag (I8 forbids a
	// fallback parser).
	Unparseable bool
	// Reason names the specific failure, for the deferring caller's reason string.
	Reason string
}

// DELETED, and the deletion is a coverage claim: `matchParen`.
//
// It was the last raw-text paren matcher outside the seam — structure derived from
// bytes, which ADR 0039's I9 forbids there. ADR 0039 step 2a (pg2-zeqa5) removed
// three of its four callers and kept it as a SHIM for the fourth,
// `classifyCmdSubstitution`, on the env-assignment classification path. pg2-hed0a
// migrated that path to the seam (see shellparse.go's env-assignment VALUE
// classifier), which left `classifyCmdSubstitution`,
// `classifyBacktickSubstitution` and this matcher with no callers at all, so all
// three are gone rather than kept as dead code the fuzz harness could enshrine.
//
// pg2-x9452 (step 5) asserts this removal; the RULE-side scanners its acceptance
// criteria also name (docker's `splitOnShellOperators`, gitdir's `scopeLeaves` and
// `containsVarRef`, envvars' value scan) are untouched and still its.

// DELETED, and the deletion is a coverage claim: `commandStartOffset`, the
// quote/paren-aware byte scan that found where a command's executable begins.
// `StripLeadingEnvAssignments` now lives on the seam (shellparse.go) and reads the
// boundary off the AST — the assignments are `CallExpr.Assigns` and the command
// starts at `Args[0]` — so the one thing this scan hand-rolled a paren counter for,
// the `FOO=(a b) cmd` array form, is a parse error under Variant(LangBash) and
// lands on the fail-closed branch instead of on a second structure model.

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

// commandRunner describes a command-runner wrapper's flag grammar: which options
// take a SEPARATE following value (consume the next token too), and whether the
// wrapper has a mandatory DURATION operand before the inner command.
type commandRunner struct {
	// valueOpts are the short/long options that take a value in their SEPARATE
	// form and therefore consume the following token. Glued forms (`-n10`,
	// `--adjustment=10`, `-oL`, `--output=L`) are a single `-`-prefixed token and
	// are skipped as ordinary options without a lookahead, so they need no entry.
	valueOpts map[string]bool
	// hasDuration marks a wrapper (only `timeout`) whose first BARE operand is a
	// DURATION that precedes the command, not the command itself.
	hasDuration bool
}

// commandRunnerPrefixes are command-runner wrappers that execute an INNER command
// after their own options. Like execPrefixes (env/command) they must be unwrapped
// so downstream argv[0]-keyed rules (dangerouscmds, buildtools, …) evaluate the
// real command — otherwise `nice dd if=/dev/zero of=x` parses to Executable
// `nice` and no rule matches → abstain, when dangerouscmds SHOULD see `dd` and
// Reject (tc-otuid). Unlike env, these wrappers set NO NAME=VALUE assignments, so
// no EnvAssignment capture is needed. Flag grammars follow GNU coreutils.
//
// `xargs` is DELIBERATELY EXCLUDED: internal/rules/safecmds handles it with
// richer semantics (extractXargsCommand, `sh -c` recursion, stdin-append) that a
// parser-level unwrap would conflict with and drop. Do NOT add it here.
var commandRunnerPrefixes = map[string]commandRunner{
	// nohup: only --help/--version, neither takes a value → no valueOpts.
	"nohup": {},
	// nice: -n/--adjustment take a value (legacy `-10` is a single option token).
	"nice": {valueOpts: map[string]bool{"-n": true, "--adjustment": true}},
	// stdbuf: -i/-o/-e and their long forms take a value.
	"stdbuf": {valueOpts: map[string]bool{
		"-i": true, "-o": true, "-e": true,
		"--input": true, "--output": true, "--error": true,
	}},
	// timeout: -s/--signal and -k/--kill-after take a value; -p/-f/-v (and long
	// forms) do not. The first bare operand is the DURATION, then the command.
	"timeout": {
		valueOpts:   map[string]bool{"-s": true, "--signal": true, "-k": true, "--kill-after": true},
		hasDuration: true,
	},
}

// timeoutDurationPattern matches a GNU timeout DURATION operand: an integer or
// decimal optionally suffixed with a unit (s/m/h/d). It is a SAFETY gate — see
// unwrapCommandRunner — so an unknown future option that swallows the command
// cannot make us skip the real command.
var timeoutDurationPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// unwrapCommandRunner strips a command-runner wrapper's options (and, for
// timeout, its mandatory duration operand) and returns the inner executable + its
// args. It walks args skipping `-`-prefixed option tokens (consuming a following
// value for cr.valueOpts entries), stops at `--` or the first bare token, then:
//   - for a plain wrapper, that first bare token is the inner command;
//   - for timeout (hasDuration), the first bare token is the DURATION — it is
//     skipped, and the SECOND bare token is the command; but ONLY when the first
//     bare token actually looks like a duration (timeoutDurationPattern). If it
//     does not, the command is left un-unwrapped (ok=false).
//
// CONSERVATISM (mirrors unwrapExecPrefix's ok=false path): whenever an inner
// command cannot be confidently identified — no bare command token, the required
// duration+command pair is absent, or a value-taking option consumed what would
// have been the command — ok is false and the caller leaves the ParsedCommand
// as-is. That yields the current behavior (abstain / defer to Claude), which is
// SAFE; mis-identifying a benign token as the command is the dangerous direction
// and never happens here.
func unwrapCommandRunner(cr commandRunner, args []string) (inner string, innerArgs []string, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" { // explicit end of options
			i++
			break
		}
		if strings.HasPrefix(a, "-") {
			// A value-taking option in its SEPARATE form consumes the following
			// token too; glued forms are a single token skipped by this same branch.
			if cr.valueOpts[a] {
				i += 2
				continue
			}
			i++
			continue
		}
		break // first bare (non-'-') token
	}
	if cr.hasDuration {
		// timeout: the first bare token is the DURATION. Only treat it as a
		// duration+command pair if it matches the duration shape; otherwise be
		// conservative and do not unwrap.
		if i >= len(args) || !timeoutDurationPattern.MatchString(args[i]) {
			return "", nil, false
		}
		i++ // skip the duration operand; the command is the next bare token
	}
	if i >= len(args) {
		return "", nil, false // no inner command (bare wrapper / options only)
	}
	return args[i], args[i+1:], true
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
			// COPY-then-override rather than a fresh literal: every field an unwrap does
			// not deliberately change (Raw, Heredocs, the pipeline coordinates, …) must
			// survive it, and a literal silently drops any field added later.
			next := pc
			next.Executable = inner
			next.Args = innerArgs
			next.EnvVars = appendEnvAssignments(pc.EnvVars, envAssigns)
			return unwrapCommand(next)
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
	// Command-runner wrappers (nice/timeout/nohup/stdbuf) run an inner command
	// after their own options. Recurse like execPrefixes so nested cases —
	// `nice env dd`, `timeout 5 nice dd` — unwrap all the way to the real command
	// (tc-otuid). On failure to identify an inner command, leave the leaf as-is
	// (the safe abstain/defer default).
	if cr, isRunner := commandRunnerPrefixes[base]; isRunner {
		if inner, innerArgs, ok := unwrapCommandRunner(cr, pc.Args); ok {
			next := pc
			next.Executable = inner
			next.Args = innerArgs
			return unwrapCommand(next)
		}
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
		next := pc
		next.Executable = pc.Args[1]
		next.Args = pc.Args[2:]
		return next
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
	// PipelineID and PipelineIndex expose the PIPELINE RELATION between leaves —
	// the one compound operator whose meaning splitCompound used to discard
	// (tc-vul7). `|` was consumed exactly like `;`, `&&` and `&`, so a rule holding
	// one leaf could not tell where that leaf's OUTPUT went; `cat .git/config | tee
	// /tmp/x` and `cat .git/config | grep url` were indistinguishable at leaf scope,
	// and RootExpression carries only text, not the relation.
	//
	// Leaves of the SAME expression that share a PipelineID are stages of one
	// pipeline, ordered by PipelineIndex; stage N's stdout is stage N+1's stdin. IDs
	// are per-Parse-call, so leaves from DIFFERENT Parse calls (e.g. an outer
	// expression and a substitution body) MUST NOT be compared — see
	// DownstreamStages, which is the supported accessor.
	//
	// The zero value is a lone stage of the first pipeline, which is what a
	// hand-built ParsedCommand should mean. Synthesized leaves that belong to no
	// pipeline (a `for` word list, a leftover heredoc extent) carry -1.
	PipelineID    int
	PipelineIndex int
}

// DownstreamStages returns the leaves of `leaves` that receive the STDOUT of a
// stage whose Raw text equals leafRaw — i.e. everything leafRaw pipes into.
//
// `leaves` MUST be a single Parse call's output (pipeline IDs are per-call), and
// is normally Parse of the whole expression a rule was handed as
// hookio.HookInput.RootExpression. Matching on Raw is exact: the engine builds a
// leaf's synthetic HookInput from that same Raw and hands the rule the same
// RootExpression it parsed the leaf out of, so re-parsing either yields the same
// text. A leafRaw that matches nothing returns no stages — the same answer as "no
// pipeline", which is the pre-tc-vul7 behavior and therefore never a regression.
//
// Every match is unioned, so a text appearing twice in an expression contributes
// both pipelines' downstreams rather than silently picking one.
func DownstreamStages(leaves []ParsedCommand, leafRaw string) []ParsedCommand {
	var out []ParsedCommand
	for _, stage := range leaves {
		if stage.PipelineID < 0 || stage.Raw != leafRaw {
			continue
		}
		for _, candidate := range leaves {
			if candidate.PipelineID == stage.PipelineID && candidate.PipelineIndex > stage.PipelineIndex {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// DELETED, and the deletion is a coverage claim: the shared byte scanner
// `shellScanner` (with `scanFrame`, `newShellScanner`, `advance` and `nested`),
// plus `hasLiveRedirChar`, `ExtractComment` and `StripComment`.
//
// The scanner was ADR 0039's root cause 1 in person: it reported what ONE BYTE
// does and had no extent API, so every caller needing a REGION hand-rolled a
// quote-blind depth counter while holding it. Commit 1c749bbd consolidated four
// drifted copies into it and nine more instances surfaced afterwards — which is why
// this step deletes the scanner rather than consolidating onto it again.
//
// Where each capability went:
//
//   - word start, quoting and nesting are now parser facts inside the seam;
//   - `UnquotedMask` survives as an EXPORTED seam function (shellparse.go) computed
//     from the AST's quoted/substitution/arithmetic spans. Its sole caller is
//     rules/ssh's `hasWriteRedirection`, a rule-side raw-text scanner ADR 0039's
//     step 5 (pg2-x9452) still owns, so the capability cannot be removed here — but
//     its IMPLEMENTATION no longer is a second structure model;
//   - `hasLiveRedirChar` is gone with its only caller, `extractRedirections`: a
//     quoted `>` is a *syntax.SglQuoted part of an argument word, so `grep '>' f`
//     yields no redirection structurally rather than by a subtractive guard;
//   - `ExtractComment` is gone, replaced by the seam's `CommandComment`, and
//     `StripComment` is gone with the per-line comment pass. Under
//     KeepComments(true) a comment never appears in a CallExpr at all, so the pass
//     is retired BY CONSTRUCTION rather than reimplemented.

// Parse lowers a command string to CETA's leaf commands.
//
// It is a FACADE over the seam (`ParseShell`) and carries no structure logic of its
// own. Before ADR 0039 step 2 it WAS the outgoing front end —
// `stripHeredocBodies`, then `splitCompound`, then `resolveLoops`, then per-segment
// `tokenize` / `extractRedirections` / `extractExecAndArgs` — five independent text
// passes each deciding command boundaries for itself. All five are deleted; see the
// obituaries in this file and in heredoc.go.
//
// It DISCARDS the seam's `Unparseable` flag, so every caller whose "no leaves"
// branch could reach an APPROVAL must call `ParseShell` and honour I1b instead. The
// engine does exactly that (`EvaluateExpression` folds the unparseable floor
// through MostRestrictive); a RULE reaching zero leaves reports
// hookio.ErrNotApplicable and gates nothing, which is why the flag is safe to drop
// at this boundary and nowhere else.
//
// Comments are NOT pre-stripped: under KeepComments(true) they are parser facts and
// never appear in a command's words, and `ParsedCommand.Comment` is filled from
// `Stmt.Comments`.
func Parse(command string) []ParsedCommand {
	return ParseShell(command).Leaves
}

// DELETED, and the deletion is a coverage claim: `segment`, `assignPipelineIDs`,
// `splitCompound`, `tokenize`, and the ENTIRE `resolveLoops` family —
// `resolveLoops`, `extractLoopBody`, `isLoopKeyword`, `isDoneKeyword`,
// `parseDoKeyword`, `doneResidue` and `forWordList`.
//
// `splitCompound` and `tokenize` are ADR 0039's ROOT CAUSE 2: every pass returned
// TEXT (`[]string`, a rewritten string), so structure discovered by one pass was
// discarded and the next re-derived it. The seam returns leaves from one AST.
//
// The `resolveLoops` family is ADR 0039's ROOT CAUSE 4 and inventory sites 12 and
// 13 — the class where structure is not mis-derived but DISCARDED. It replaced a
// loop with its body and advanced past the terminator segment; pg2-qkecz patched
// the two known drops back in by adding `doneResidue` (hole A, the terminator's
// redirection) and `forWordList` (hole B, the `for` word list), which is why those
// two are members of the family here even though the earlier inventory predates
// them. Patching a text pass cannot close the class: it can only close the
// instances somebody found. Over the seam both holes are STRUCTURAL and more
// general — a compound's redirections sit on the compound's own *syntax.Stmt, so
// `emitCompoundRedirs` covers `done > f`, `(cmd) > f`, `{ …; } > f`, `if … fi > f`
// and `case … esac > f` with one rule and no residue text-prefix match — and the
// class itself is now guarded by the I14 coverage check
// (`TestLeafSpansCoverEveryCallExpr`), which is the only mechanism that can see
// root cause 4 at all.
//
// While the text version survived, so did root cause 4. It does not survive.

// unquote is RETAINED, deliberately, as a POST-LOWERING helper — the one piece of
// the outgoing tokenizer this migration must NOT replace.
//
// Reason: it defines CETA's token spelling, and the parser's own literal expansion
// is STRICTER. `unquote` strips quoting only when the WHOLE token is wrapped in one
// quote character, so `a"b"c` KEEPS its quotes, and both `envvars.literalValue` and
// `isStaticAbsolutePath` reject any value with a surviving quote. A true literal
// expansion would yield `abc` and make mixed-quoted values newly CLEAR the very
// predicate I4 exists to fence — ADR 0039's Consequences names this as one of the
// constructs that changes verdict in the LESS-restrictive direction if lowered
// naively. The seam therefore applies THIS function to each word's exact source
// slice (see wordToken), which is what makes the parity provable rather than
// argued, and TestShellParse_UnquoteParity_MixedQuoting pins the mixed case.
//
// It is a pure string -> string function over an ALREADY-DELIMITED token: it
// derives no structure, so it is not a scanner and I9 does not reach it.
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

// DELETED, and the deletion is a coverage claim: `splitFDPrefix`, `isVarName` and
// `redirectionCore` — the token-level `[FD]OP[TARGET]` grammar tc-xs8x built to
// replace a fixed six-spelling table.
//
// They existed only because a redirection arrived as a TOKEN, so its descriptor
// prefix had to be re-derived from bytes. The parser hands the seam a
// *syntax.Redirect with `N` (the descriptor), `Op` and `Word` already separated, so
// `redirCore` (shellparse.go) maps the operator ENUM onto the same operator text
// and `redirectionKind` below is reached unchanged. bash's `{varname}>` open-and-
// assign form, which `isVarName` gated, is `Redirect.N` with a non-numeric value.

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// redirectionKind classifies a (descriptor, operator) pair. Kinds exist so that
// consumers can ask what STREAM is affected without re-parsing operator text:
// only stdout-bearing kinds capture a command's payload (cmdparse.CapturesStdout),
// while every non-stdin kind is a write for the engine's path check.
func redirectionKind(fd, core string) hookio.RedirectionKind {
	switch core {
	case "<":
		return hookio.RedirectStdin
	case "<>":
		return hookio.RedirectReadWrite
	case "&>", "&>>":
		return hookio.RedirectAll
	case ">&":
		// `>& FILE` with NO descriptor is bash's both-streams form. With an
		// explicit descriptor the construct is ambiguous in bash; fall through to
		// the descriptor's own classification, which is the conservative reading.
		if fd == "" {
			return hookio.RedirectAll
		}
	}
	switch fd {
	case "", "1":
		return hookio.RedirectStdout
	case "2":
		return hookio.RedirectStderr
	}
	return hookio.RedirectOtherFD
}

// DELETED, and the deletion is a coverage claim: `extractRedirections` and
// `extractExecAndArgs`.
//
// `extractRedirections` walked an already-tokenised segment looking for operator
// text, which is why it needed `raws` (the pre-unquote token text) to stay
// quote-aware and why the fd-prefixed heredoc `2<<EOF` LEAKED into the argument
// list as a phantom operand — a token-prefix match cannot see a descriptor. Over
// the seam `attachRedirs` reads `Stmt.Redirs`, so the operator, its descriptor and
// its target are never re-derived from text and the phantom operand cannot exist
// (TestShellParse_FdPrefixedHeredocHasNoPhantomOperand).
//
// `extractExecAndArgs` split a token list into the leading NAME=VALUE assignments
// and the command. The parser separates them itself: leading assignments are
// `CallExpr.Assigns`, the command is `Args[0]`. That distinction is pg2-gkd5e's
// position-independence invariant, and conflating them is exactly what a positional
// text scan cannot avoid — `cmd FOO=1` has an assignment-SHAPED operand that
// `Args` correctly keeps as an argument.
//
// `isEnvAssign` / `isValidEnvName` are RETAINED below: they are predicates over an
// ALREADY-DELIMITED assignment token, not scanners, and the seam consults them to
// decide whether an `*syntax.Assign` can become an `EnvAssignment` at all (the
// indexed form `BEAD_IDS[1]=x` cannot, and must reach a data leaf rather than
// vanish).

// HasUnsafeCommandSubstitution reports whether s embeds a substitution whose body is
// not on the static safe list (safeCmdSubstitutions), or text whose substitutions
// cannot be enumerated at all. Bare $VAR / ${VAR} references and arithmetic
// $((...)) are NOT substitutions and return false; $(date)/$(mktemp) return false.
//
// It lets a caller demote ANY leaf whose executable or args embed an arbitrary inner
// command (e.g. `echo $(rm -rf ~)`), even when the outer command is otherwise "safe"
// — the outer rule never sees the inner command.
//
// NO PRODUCTION CALLER, stated so nobody mistakes it for a live gate: every
// reference is a test or the fuzz harness. It is kept, and kept CORRECT, because the
// fuzz harness asserts properties over it and a wrong invariant enshrined there is
// worse than none. The live env-assignment path is classifyExpansion; the live leaf
// path is the engine's foldSubstitutionScan.
//
// Over the seam (ADR 0039 step 5's residue, pg2-hed0a) it no longer walks the text
// itself. That matters for two inputs its byte loop got wrong:
//
//   - `$(( $(curl|sh) + 1 ))` — the loop SKIPPED the whole `$((` extent on a
//     two-character lookahead, so a command substitution nested in arithmetic was
//     never examined. The scan enumerates it (step 2a's replay Cause B).
//   - text the scan cannot model — `$(oops` — now answers true through the
//     Unparseable branch rather than by a failed paren match, so absence of
//     enumerated bodies is never read as evidence of safety (the pg2-wguam rule).
//
// A PROCESS substitution body is held to the same allowlist. Its own kind carries no
// static allowlist on the engine's path, and this predicate has no production caller
// to widen, so the uniform (stricter) treatment is the safe default. A value that is
// ONLY a process substitution still returns false: it contains neither `$` nor a
// backtick, so the shortcut below answers first — the same pre-existing gap
// classifyExpansion records for pg2-x9452.
func HasUnsafeCommandSubstitution(s string) bool {
	if !strings.ContainsAny(s, "$`") {
		return false
	}
	scan := ScanSubstitutions(s)
	if scan.Unparseable {
		return true
	}
	for _, sub := range scan.Substitutions {
		if !IsSafeSubstitutionBody(sub.Body) {
			return true
		}
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
