package safecmds

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var alwaysSafe = map[string]bool{
	"echo": true, "test": true, "true": true, "false": true, "printf": true,
	"cut": true, "df": true, "ps": true, "tr": true, "where": true, "pgrep": true,
	"sleep": true, "tree": true,
	// Shell builtins and environment queries (no filesystem access)
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	"which": true, "type": true, "command": true, "unset": true, "export": true,
	"env": true, "printenv": true, "id": true, "whoami": true,
	"date": true, "uname": true, "hostname": true, "pwd": true, "cd": true,
	"sw_vers": true,
	// macOS system tools (read-only inspection)
	"sfltool": true, "plutil": true, "system_profiler": true, "launchctl": true,
	"claude-extended-tool-approver": true, "claude-pretool-hook": true,
	"shellcheck": true, "colima": true, "contained-claude": true,
	"my-code-review-support-cli": true,
}

// browsingCmds list/stat filesystem entries but don't read file contents.
// Safe to run on any path since they only expose names, sizes, timestamps.
var browsingCmds = map[string]bool{
	"ls": true, "find": true, "fd": true, "du": true, "stat": true, "file": true,
	"lsof": true,
}

// safeReadCmds read file contents — require path to be in a known zone.
var safeReadCmds = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"wc": true, "diff": true,
	"sort": true, "uniq": true, "awk": true,
	"jq": true, "tq": true, "xxd": true,
	// strings dumps a file's printable content, so it reads file contents and
	// is routed through the same readPathIssue zone check as cat/head/tail
	// (pg2-t76k8). Previously it hit the unknown-command fallthrough and abstained.
	"strings": true,
}

// logReadSubcommands are the macOS unified-logging verbs that only read; the
// mutating verbs (erase/config/collect) are NOT approved.
var logReadSubcommands = map[string]bool{
	"show": true, "stream": true, "stats": true,
}

var safeWriteCmds = map[string]bool{
	"rm": true, "cp": true, "mv": true,
	"mkdir": true, "touch": true, "chmod": true,
	"tee": true,
}

var lspServices = map[string]bool{
	"typescript-language-server": true, "gopls": true, "bash-language-server": true,
	"pylsp": true, "rust-analyzer": true,
}

type Rule struct {
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "safe-commands"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	if len(parsed) == 0 {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	baseEval := r.eval
	if input.PathEval != nil {
		baseEval = input.PathEval
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = baseEval.ProjectRoot()
	}
	pe := baseEval.WithCWD(cwd)
	for _, pc := range parsed {
		basename := filepath.Base(pc.Executable)
		if alwaysSafe[basename] || lspServices[basename] {
			continue
		}
		if browsingCmds[basename] {
			if hasRejectPath(pc.Args, pe) {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references rejected path (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// "$command --help" or "$command $subcommand --help" is always safe
		if isHelpRequest(basename, pc.Args) {
			continue
		}
		// xargs: evaluate the inner command being run
		if basename == "xargs" {
			innerExec, innerArgs := extractXargsCommand(pc.Args)
			if innerExec == "" {
				return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
			}
			innerBase := filepath.Base(innerExec)
			// sh/bash -c '<cmd>': parse the -c argument and evaluate it recursively
			if (innerBase == "sh" || innerBase == "bash") && len(innerArgs) >= 2 && innerArgs[0] == "-c" {
				shellCmd := strings.Join(innerArgs[1:], " ")
				innerParsed := cmdparse.Parse(shellCmd)
				if len(innerParsed) == 0 {
					return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
				}
				// Re-evaluate by constructing a synthetic hook input with the shell command
				syntheticInput := &hookio.HookInput{
					ToolName:  "Bash",
					CWD:       cwd,
					ToolInput: mustMarshalCommand(shellCmd),
				}
				result := r.Evaluate(syntheticInput)
				if result.Decision != hookio.Approve {
					return result
				}
				continue
			}
			if alwaysSafe[innerBase] || lspServices[innerBase] {
				continue
			}
			if browsingCmds[innerBase] {
				if hasRejectPath(innerArgs, pe) {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references rejected path (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			// grep/rg: skip pattern arg before path checking
			if innerBase == "grep" || innerBase == "rg" {
				fileArgs := cmdparse.SkipGrepPattern(innerBase, innerArgs)
				if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " " + issue + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			// No program-operand exemption here: `xargs awk '{print $1}'` is not a
			// real shape, and the conservative direction (a needless Abstain) is the
			// safe one to take for it.
			if safeReadCmds[innerBase] {
				if issue := readPathIssue(innerArgs, pe, ""); issue != "" {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " " + issue + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if safeWriteCmds[innerBase] {
				if unsafe, path := hasUnsafeWritePath(innerArgs, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: xargs " + innerBase + " references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			// Unknown inner command — abstain
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// bash/sh -n: syntax check only, no execution — safe read command
		if (basename == "bash" || basename == "sh") && hasBashSyntaxCheckFlag(pc.Args) {
			fileArgs := extractBashSyntaxCheckFiles(pc.Args)
			if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " -n " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// unzip: read archive, optionally write to -d destination or cwd
		if basename == "unzip" {
			result := evaluateUnzip(pc.Args, pe, cwd, r.Name())
			if result.Decision != hookio.Approve {
				return result
			}
			continue
		}
		// jar: tf/xf are safe read operations
		if basename == "jar" {
			if len(pc.Args) >= 1 && (pc.Args[0] == "tf" || pc.Args[0] == "xf") {
				if issue := readPathIssue(pc.Args[1:], pe, ""); issue != "" {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: jar " + pc.Args[0] + " " + issue + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// log (macOS unified logging): show/stream/stats read; erase/config/
		// collect mutate — approve only the read verbs, defer the rest.
		if basename == "log" {
			sub := ""
			for _, a := range pc.Args {
				if !strings.HasPrefix(a, "-") {
					sub = a
					break
				}
			}
			if logReadSubcommands[sub] {
				continue
			}
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		// yq: read command unless -i/--inplace is present
		if basename == "yq" {
			if isYqInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: yq -i references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if issue := readPathIssue(pc.Args, pe, ""); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: yq " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// sed: read command unless -i/--in-place is present
		if basename == "sed" {
			if isSedInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return hookio.RuleResult{
						Decision: hookio.Abstain,
						Reason:   "safe-commands: sed -i references non-writable path " + path + " (deferred to claude-code)",
						Module:   r.Name(),
					}
				}
				continue
			}
			if issue := readPathIssue(pc.Args, pe, programOperand("sed", pc.Args)); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: sed " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// gofmt: read-only unless it WRITES. -l (list), -d (diff), -s (simplify),
		// -e (all errors) and bare/path/stdin forms only print to stdout; -w
		// rewrites files in place. A -w invocation is NOT approved (deferred to the
		// normal flow); a read-only invocation is treated like a read command —
		// path-like args must be in a readable zone (matches cat/sed/yq).
		if basename == "gofmt" {
			if isGofmtWrite(pc.Args) {
				return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
			}
			if issue := readPathIssue(pc.Args, pe, ""); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: gofmt " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// grep/rg: first non-flag arg is a pattern, not a file — skip it in path checks
		if basename == "grep" || basename == "rg" {
			fileArgs := cmdparse.SkipGrepPattern(basename, pc.Args)
			if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// jq: skip value arguments for --arg, --argjson, --slurpfile, --rawfile
		// which take two args (name value) that may look like paths but aren't.
		if basename == "jq" {
			fileArgs := cmdparse.SkipJqValueFlags(pc.Args)
			if issue := readPathIssue(fileArgs, pe, programOperand("jq", fileArgs)); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: jq " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		if safeReadCmds[basename] {
			if issue := readPathIssue(pc.Args, pe, programOperand(basename, pc.Args)); issue != "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " " + issue + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// A write command with a dynamically-expanded path arg ($VAR / $(...) /
		// backtick) hides its real target from path evaluation (looksLikePath only
		// matches literal /, ./, ../, ~/). Defer such writes to Claude's prompt.
		// READS get the SAME refusal, one argument at a time, inside readPathIssue —
		// see its doc for why (pg2-2ke04: one variable hop erased the credential
		// deny-list on every read command). Only browsingCmds (ls/find/du/stat/file/
		// lsof) stay exempt: they expose names, sizes and timestamps, never file
		// CONTENT, so `ls $d` is not an exfiltration primitive. Command substitution
		// is also caught at the engine choke point for all commands.
		if safeWriteCmds[basename] && argsHaveDynamicExpansion(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: " + basename + " has a dynamically-expanded path arg (deferred to claude-code)",
				Module:   r.Name(),
			}
		}
		if basename == "cp" {
			result := evaluateCp(pc.Args, pe, r.Name())
			if result.Decision != hookio.Approve {
				return result
			}
			continue
		}
		if safeWriteCmds[basename] {
			if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: " + basename + " references non-writable path " + path + " (deferred to claude-code)",
					Module:   r.Name(),
				}
			}
			continue
		}
		// Unknown command - not our jurisdiction
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "safe-commands: all commands are safe",
		Module:   r.Name(),
	}
}

// hasSubcommands lists commands known to use subcommand syntax (e.g. "git log", "kubectl apply").
var hasSubcommands = map[string]bool{
	"git": true, "gh": true,
	"docker": true, "docker-compose": true, "podman": true,
	"kubectl": true,
	"nix":     true, "nix-env": true, "nix-store": true,
	"darwin-rebuild": true, "nixos-rebuild": true, "home-manager": true,
	"cargo": true, "go": true, "rustup": true,
	"npm": true, "yarn": true, "pnpm": true, "npx": true,
	"pip": true, "uv": true, "poetry": true,
	"gradle": true, "gradlew": true,
	"helm": true, "terraform": true, "aws": true, "gcloud": true,
	"bd": true,
}

// isHelpRequest returns true if the args represent a safe help invocation.
// Matches:
//   - "$command --help"
//   - "$command $subcommand --help" (if command has subcommands and subcommand starts with a letter)
//   - "$command help" (if command has subcommands)
//   - "$command help $subcommand" (if command has subcommands and subcommand starts with a letter)
func isHelpRequest(basename string, args []string) bool {
	if len(args) == 1 && args[0] == "--help" {
		return true
	}
	if len(args) == 2 && args[1] == "--help" && startsWithLetter(args[0]) && hasSubcommands[basename] {
		return true
	}
	// "help" subcommand form: "$command help" or "$command help $subcommand"
	if hasSubcommands[basename] && len(args) >= 1 && args[0] == "help" {
		if len(args) == 1 {
			return true
		}
		if len(args) == 2 && startsWithLetter(args[1]) {
			return true
		}
	}
	return false
}

// startsWithLetter returns true if s is non-empty and starts with an ASCII letter.
func startsWithLetter(s string) bool {
	if len(s) == 0 {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func looksLikePath(arg string) bool {
	// A bare "~" is the home directory just as much as "~/": the path Evaluator's
	// cleanPath expands both to $HOME. Without matching it here, a bare "~" arg
	// (e.g. `rm -rf ~`) is never classified and slips through as safe (tc-sfpto).
	return arg == "~" ||
		strings.HasPrefix(arg, "/") ||
		strings.HasPrefix(arg, "./") ||
		strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, "~/")
}

// argHasDynamicExpansion reports whether ONE argument contains a shell expansion
// ($VAR, ${VAR}, $(...), backtick) that would resolve a path at runtime, hiding it
// from static path evaluation.
//
// It is deliberately a TEXT test over a PARSED ARGUMENT, never over the raw command
// string: the argument's syntactic ROLE is what makes the expansion a path, so a
// commit message or a `bd comment` body that merely QUOTES `cat $F` carries the same
// bytes in a non-operand position and must not be gated (the pg2-5b901 failure mode,
// and the shape this bead's own report criticises in the `pathtraversal` rule).
//
// KNOWN LIMIT — resolving the variable is out of scope. This makes the read
// NON-APPROVING, not denied: a true `deny` would need the binding
// (`F=/Users/me/.ssh/id_rsa`) followed into the dereferencing leaf, i.e. an
// intra-command dataflow pass. pg2-553z3 weighs the SAME capability for the
// PATH/HOME component predicate, so when either is built the two MUST share one
// primitive rather than each growing their own.
func argHasDynamicExpansion(arg string) bool {
	return strings.ContainsAny(arg, "$`")
}

// programOperandValueFlags lists, per command, the flags that consume the NEXT
// argument as a value, so the scan for the program operand does not stop on it.
// Only the glue-free spellings matter: a glued `-F'\t'` / `-v x=1` is one token
// starting with `-`, which the scan already skips.
var programOperandValueFlags = map[string]map[string]bool{
	"awk": {"-v": true, "--assign": true, "-F": true, "--field-separator": true},
	"jq":  {},
	"sed": {},
}

// programOperandFromFlag lists, per command, the flags that supply the PROGRAM
// itself. When one is present there is NO positional program, so every positional
// is a path and gets the full path-operand treatment.
var programOperandFromFlag = map[string]map[string]bool{
	"awk": {"-f": true, "--file": true, "--source": true, "-e": true},
	"jq":  {"-f": true, "--from-file": true},
	"sed": {"-e": true, "--expression": true, "-f": true, "--file": true},
}

// programOperand returns the argument that plays the PROGRAM role for basename —
// awk's program text, jq's filter, sed's script — or "" when the command has no
// such operand or supplies it through a flag.
//
// It exists because those three commands' first positional is CODE, not a path, and
// code legitimately contains a literal `$`: awk field references (`{print $1}`), sed
// end-of-line anchors (`s/x$//`), jq variables bound by `--arg` (`{a:$a}`). The args
// this rule sees are POST-UNQUOTE, so a single-quoted `$` — which the shell never
// expands — is textually identical to a live expansion. Without this role split the
// read guard gated all three, breaking the pg2-gkd5e acceptance matrix
// (`action_meta=$(jq -nc --arg a b '{a:$a}')` must approve) and every everyday
// `awk '{print $2}' file`.
//
// The program operand is NOT exempted from the guard, only judged by the narrower
// isDynamicPathOperand — a program that is ITSELF a bare expansion (`awk $F`) is
// indistinguishable from a path and is refused. Its ZONE check is unchanged, so
// nothing this rule used to defer becomes approvable.
func programOperand(basename string, args []string) string {
	valueFlags, known := programOperandValueFlags[basename]
	if !known {
		return ""
	}
	fromFlag := programOperandFromFlag[basename]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if fromFlag[a] {
			return ""
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if valueFlags[a] {
				i++ // skip the flag's value; the loop's own i++ skips the flag
			}
			continue
		}
		return a
	}
	return ""
}

// isDynamicPathOperand reports whether an argument in the PROGRAM role is
// nonetheless indistinguishable from a dynamically resolved PATH. Three shapes
// qualify, and only three:
//
//   - the whole argument is one expansion — `$F`, `${F}`, `$(cmd)`, or a
//     backtick-wrapped command;
//   - it is path-shaped and carries an expansion — `~/$F`, `/tmp/$F`, `./$F`;
//   - it STARTS with an expansion and contains a path separator — `$D/id_rsa`,
//     `$(dirname x)/y`.
//
// `{print $1}`, `s/x$//`, `{a:$a}` and `.count = $count` match none of them, which
// is the whole point: they are code, and their `$` is never a shell expansion.
func isDynamicPathOperand(arg string) bool {
	if !argHasDynamicExpansion(arg) {
		return false
	}
	if looksLikePath(arg) {
		return true
	}
	if strings.HasPrefix(arg, "$") || strings.HasPrefix(arg, "`") {
		return strings.Contains(arg, "/") || isBareVarReference(arg)
	}
	return false
}

// isBareVarReference reports whether arg is exactly one variable reference or one
// command substitution and nothing else: `$NAME`, `${...}`, `$(...)`, or a
// backtick-wrapped command.
func isBareVarReference(arg string) bool {
	switch {
	case strings.HasPrefix(arg, "$(") && strings.HasSuffix(arg, ")"):
		return true
	case strings.HasPrefix(arg, "${") && strings.HasSuffix(arg, "}"):
		return true
	case len(arg) >= 2 && arg[0] == '`' && strings.HasSuffix(arg, "`"):
		return true
	case strings.HasPrefix(arg, "$") && len(arg) > 1:
		for i := 1; i < len(arg); i++ {
			if !isVarNameByte(arg[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// isVarNameByte reports whether c may appear in a shell variable NAME.
func isVarNameByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// argsHaveDynamicExpansion reports whether any non-flag arg contains a shell
// expansion ($VAR, ${VAR}, $(...), backtick) that would resolve a path at
// runtime, hiding it from static path evaluation. Used by the WRITE path, whose
// commands (rm/cp/mv/mkdir/touch/chmod/tee) take path operands only; the READ path
// applies the same per-argument predicate through readPathIssue.
func argsHaveDynamicExpansion(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if argHasDynamicExpansion(a) {
			return true
		}
	}
	return false
}

// hasRejectPath returns true if any path-like arg is in a rejected zone.
func hasRejectPath(args []string, pe *patheval.PathEvaluator) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			if pe.Evaluate(a) == patheval.PathReject {
				return true
			}
		}
	}
	return false
}

// readPathIssue returns a REASON FRAGMENT describing the first argument this rule
// cannot statically clear for reading, or "" when every argument is clear. It is
// the single choke point every read surface funnels through (the safeReadCmds
// branch, jq/sed/yq/gofmt/grep/rg, `jar tf|xf`, `bash -n`, and the xargs inner
// command), so both refusals below apply uniformly to all of them.
//
// Two things disqualify an argument:
//
//  1. It is path-like (looksLikePath) and its zone is not readable. ReadOnly and
//     ReadWrite are both acceptable for a read.
//
//  2. It carries a shell expansion — $VAR / ${VAR} / $(...) / backtick — so the
//     path it names is chosen by the target shell at runtime and is NOT statically
//     determinable here (pg2-2ke04, P0 SECURITY).
//
// WHY (2) APPLIES TO READS. It was previously wired to safeWriteCmds ONLY, and the
// asymmetry was a live, silent credential bypass in every permission mode: because
// looksLikePath matches only a literal `/`, `./`, `../` or `~/` prefix, a `$F`
// argument is not path-like, so NO zone check ran and the leaf auto-approved —
//
//	cat /Users/me/.ssh/id_rsa        -> deny     (the secrets rule's deny-list)
//	F=/Users/me/.ssh/id_rsa; cat $F  -> ALLOW    (the bypass: one variable hop)
//	F=/Users/me/.ssh/id_rsa; rm  $F  -> abstain  (write: the guard already fired)
//
// One hop through a shell variable erased the entire deny-list. A credential READ
// is an EXFILTRATION primitive — the secret leaves the machine — so if anything the
// read path warrants this refusal MORE than the write path, and treating the two
// alike introduces no new concept: it is the same predicate
// (argHasDynamicExpansion) the write path has always used.
//
// This is a NON-APPROVAL (Abstain), not a deny. ceta cannot resolve the variable,
// so it hands the call back to Claude Code's own prompt rather than claiming to
// know the target. Restoring a true `deny` needs the variable RESOLVED by
// intra-command dataflow, which is deliberately out of scope here — see the
// dataflow note on argHasDynamicExpansion.
//
// MEASURED PROMPT-VOLUME COST — replay of the full logged corpus through
// `claude-extended-tool-approver evaluate`, 333,349 rows graded before and after,
// 2026-07-31:
//
//	allow   -> abstain   3,455
//	abstain -> ask         250
//	allow   -> ask         142
//	                     -----
//	changed              3,847   (1.15% of the corpus)
//
// NOTHING moves toward allow — 0 rows in any `* -> allow` class, which is the
// invariant that makes this change safe to land. 3,597 rows lose an `allow`; that is
// 14.96% of the 24,045 corpus rows that both approved and contain a `$` or a
// backtick. The cost lands on benign dynamic idioms — `grep pat "$HOME/…/x.log"`,
// `cat "$f"` in a loop, `awk … $SCRATCH/out.tsv` — which now prompt instead of
// auto-approving. That is the accepted trade-off: option 1 of pg2-2ke04 makes the
// bypass NON-SILENT; only resolving the variable restores a true `deny`.
//
// The programOperand role split below is what keeps that number bounded: judging
// awk/sed/jq program text by the coarse predicate instead measured 4,524 changed
// rows, so the split saves 677 prompts AND is required for correctness — the
// coarse form breaks the pg2-gkd5e acceptance matrix.
//
// WHAT WOULD JUSTIFY CHANGING IT. Two things, and neither is "the prompt count
// feels high":
//
//   - QUOTE AWARENESS. The predicate runs on POST-UNQUOTE args, so it cannot tell a
//     live `$F` from a single-quoted literal `$` the shell never expands. The
//     programOperand split below handles the three commands where that distinction
//     is load-bearing; a general fix needs cmdparse to retain each argument's RAW
//     (pre-unquote) text so a quote-aware scan can run, which is a cmdparse
//     front-end change, not a safecmds one.
//   - RESOLVING THE VARIABLE (option 2 of pg2-2ke04): a single-leaf dataflow pass
//     would turn these Abstains back into a precise allow/deny instead of a prompt.
//
// Narrowing it any other way — exempting a command, or keying on the raw command
// text instead of parsed args — reopens the bypass.
func readPathIssue(args []string, pe *patheval.PathEvaluator, program string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		dynamic := argHasDynamicExpansion(a)
		if program != "" && a == program {
			// The PROGRAM operand is code, not a path, so it is judged by the
			// narrower predicate — see programOperand for why.
			dynamic = isDynamicPathOperand(a)
		}
		if dynamic {
			return "has a dynamically-expanded path arg " + a
		}
		if looksLikePath(a) {
			if !pe.Evaluate(a).CanRead() {
				return "references unknown path " + a
			}
		}
	}
	return ""
}

// hasUnsafeWritePath returns (true, path) if any path-like arg is not in a writable zone.
// Only ReadWrite paths are acceptable for write operations.
func hasUnsafeWritePath(args []string, pe *patheval.PathEvaluator) (bool, string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			if !pe.Evaluate(a).CanWrite() {
				return true, a
			}
		}
	}
	return false, ""
}

// evaluateCp handles cp with source (read) and destination (write) semantics.
func evaluateCp(args []string, pe *patheval.PathEvaluator, module string) hookio.RuleResult {
	// Check for -t/--target-directory
	targetDir := ""
	for i, a := range args {
		if (a == "-t" || a == "--target-directory") && i+1 < len(args) {
			targetDir = args[i+1]
			break
		}
		if v, ok := strings.CutPrefix(a, "--target-directory="); ok {
			targetDir = v
			break
		}
	}

	if targetDir != "" {
		if looksLikePath(targetDir) && !pe.Evaluate(targetDir).CanWrite() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: cp target directory is not writable " + targetDir + " (deferred to claude-code)",
				Module:   module,
			}
		}
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if a == targetDir {
				continue
			}
			if looksLikePath(a) && !pe.Evaluate(a).CanRead() {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "safe-commands: cp source references non-readable path " + a + " (deferred to claude-code)",
					Module:   module,
				}
			}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}
	}

	// Standard mode: last path-like arg is destination (write), rest are sources (read)
	var pathArgs []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if looksLikePath(a) {
			pathArgs = append(pathArgs, a)
		}
	}

	if len(pathArgs) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with no explicit paths", Module: module}
	}

	dest := pathArgs[len(pathArgs)-1]
	if !pe.Evaluate(dest).CanWrite() {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "safe-commands: cp destination is not writable " + dest + " (deferred to claude-code)",
			Module:   module,
		}
	}

	for _, src := range pathArgs[:len(pathArgs)-1] {
		if !pe.Evaluate(src).CanRead() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: cp source references non-readable path " + src + " (deferred to claude-code)",
				Module:   module,
			}
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}
}

// mustMarshalCommand creates a JSON ToolInput for a Bash command string.
func mustMarshalCommand(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// xargsValueFlags are xargs flags that consume the next argument as a value.
var xargsValueFlags = map[string]bool{
	"-I": true, "-L": true, "-n": true, "-P": true, "-d": true,
}

// xargsNoValueFlags are xargs flags that take no value.
var xargsNoValueFlags = map[string]bool{
	"-0": true, "--null": true,
	"-r": true, "--no-run-if-empty": true,
	"-t": true, "--verbose": true,
}

// extractXargsCommand skips xargs flags and returns the inner command executable
// and its remaining arguments. Returns ("", nil) if no command is found.
func extractXargsCommand(args []string) (string, []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			// Found the command executable
			return a, args[i+1:]
		}
		if xargsValueFlags[a] {
			i += 2 // skip flag and its value
			continue
		}
		if xargsNoValueFlags[a] {
			i++
			continue
		}
		// Could be a combined flag like -I{} or unknown flag — try to detect
		// -I with attached replacement string (e.g., -I{})
		for prefix := range xargsValueFlags {
			if strings.HasPrefix(a, prefix) && len(a) > len(prefix) {
				// Value is attached to the flag (e.g., -I{}, -n5, -d,)
				goto nextArg
			}
		}
		// Unknown flag — skip it
		i++
		continue
	nextArg:
		i++
	}
	return "", nil
}

// isYqInPlace returns true if args contain -i or --inplace.
func isYqInPlace(args []string) bool {
	for _, a := range args {
		if a == "-i" || a == "--inplace" {
			return true
		}
	}
	return false
}

// isSedInPlace returns true if args contain -i, -i<suffix>, or --in-place.
func isSedInPlace(args []string) bool {
	for _, a := range args {
		if a == "-i" || a == "--in-place" || (strings.HasPrefix(a, "-i") && !strings.HasPrefix(a, "-in")) {
			return true
		}
	}
	return false
}

// isGofmtWrite reports whether gofmt args request writing files in place.
// gofmt only mutates with -w (a bool flag); every other flag (-l, -d, -s, -e,
// -r) and bare/path/stdin invocations print to stdout and leave files
// untouched. Go's flag package treats -w and --w identically and accepts
// -w=<bool>, so all those forms are matched. Anything containing -w is
// conservatively classified as a write so a mutating invocation is NEVER
// approved (the rare -w=false read-only form Abstains, which is safe).
func isGofmtWrite(args []string) bool {
	for _, a := range args {
		if a == "-w" || a == "--w" ||
			strings.HasPrefix(a, "-w=") || strings.HasPrefix(a, "--w=") {
			return true
		}
	}
	return false
}

// unzipValueFlags lists unzip flags that consume the next argument.
var unzipValueFlags = map[string]bool{
	"-d": true, "-x": true, "-P": true,
}

// evaluateUnzip handles unzip with archive (read) and destination (write) semantics.
func evaluateUnzip(args []string, pe *patheval.PathEvaluator, cwd string, module string) hookio.RuleResult {
	var archivePath, destDir string
	readOnly := false // -l or -t means list/test only — no extraction

	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-l" || a == "-t" {
			readOnly = true
			i++
			continue
		}
		if a == "-d" && i+1 < len(args) {
			destDir = args[i+1]
			i += 2
			continue
		}
		if unzipValueFlags[a] && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if archivePath == "" {
			archivePath = a
		}
		i++
	}

	// Validate archive path is readable
	if archivePath != "" && looksLikePath(archivePath) {
		if !pe.Evaluate(archivePath).CanRead() {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "safe-commands: unzip archive references unknown path " + archivePath + " (deferred to claude-code)",
				Module:   module,
			}
		}
	}

	// For read-only operations (-l, -t), no write check needed
	if readOnly {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip read-only operation", Module: module}
	}

	// Validate write destination
	writeDest := destDir
	if writeDest == "" {
		writeDest = cwd
	}
	if looksLikePath(writeDest) && !pe.Evaluate(writeDest).CanWrite() {
		return hookio.RuleResult{
			Decision: hookio.Abstain,
			Reason:   "safe-commands: unzip destination is not writable " + writeDest + " (deferred to claude-code)",
			Module:   module,
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip with known paths", Module: module}
}

// hasBashSyntaxCheckFlag returns true if args contain -n as a standalone flag.
func hasBashSyntaxCheckFlag(args []string) bool {
	for _, a := range args {
		if a == "-n" {
			return true
		}
	}
	return false
}

// extractBashSyntaxCheckFiles extracts file path arguments from bash -n args,
// skipping flags. Returns only path-like arguments for validation.
func extractBashSyntaxCheckFiles(args []string) []string {
	var files []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		files = append(files, a)
	}
	return files
}
