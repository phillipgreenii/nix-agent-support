package safecmds

import (
	"encoding/json"
	"fmt"
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

// refuse is this rule's ADR 0044 refusal-and-continue return, and every site that
// uses it carries the same shape: safe-commands KNOWS this command family, has
// examined this invocation, will not clear it — and yet must not stop the chain,
// because kubectl, build-tools and sqlite3 still run after it.
//
// ADR 0043 had no outcome for that. Those sites became ErrNotApplicable and their
// reasons were demoted to comments opening "Former Reason, kept because it is the only
// record of WHY". This restores each one as a real Reason, and the restoration is not
// cosmetic: a not-applicable leaves the leaf indistinguishable from one NO rule ever
// examined, so `rm -rf /etc` reported as a chain EXHAUSTION (measured on this tree,
// 2026-08-13: every one of the 26 rules answered "rule does not apply"). A consumer
// acting on an exhaustion — envvars' cleared-body predicate — would then have cleared
// it. The refusal is what makes the classification true.
//
// It can only make a leaf MORE restrictive: the engine folds it as a floor and keeps
// going, so a later rule's Ask or Reject still wins and nothing is shadowed.
func (r *Rule) refuse(reason string) (hookio.RuleResult, error) {
	return hookio.Refused(r.Name(), reason)
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("safe-commands: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)
	if len(parsed) == 0 {
		return hookio.NotApplicable()
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
				return r.refuse("safe-commands: " + basename + " references rejected path (deferred to claude-code)")
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
				return hookio.NotApplicable()
			}
			innerBase := filepath.Base(innerExec)
			// sh/bash -c '<cmd>': parse the -c argument and evaluate it recursively
			if (innerBase == "sh" || innerBase == "bash") && len(innerArgs) >= 2 && innerArgs[0] == "-c" {
				shellCmd := strings.Join(innerArgs[1:], " ")
				innerParsed := cmdparse.Parse(shellCmd)
				if len(innerParsed) == 0 {
					return hookio.NotApplicable()
				}
				// Re-evaluate by constructing a synthetic hook input with the shell command
				syntheticInput := &hookio.HookInput{
					ToolName:  "Bash",
					CWD:       cwd,
					ToolInput: mustMarshalCommand(shellCmd),
				}
				// SELF-RECURSION. An inner error propagates UNCHANGED, which is what
				// preserves the pre-ADR-0043 outcome: the inner Evaluate used to answer
				// Abstain and the outer returned it, so the chain continued. Forwarding
				// the error keeps that, and never converts a genuine failure into a
				// not-applicable (or the reverse).
				result, err := r.Evaluate(syntheticInput)
				// Forwarded WITH the RuleResult since ADR 0044: an inner REFUSAL's floor
				// is the only record of why the inner command was not clearable, and
				// dropping it would report `xargs sh -c '<refused>'` as a leaf nobody
				// examined. A genuine failure still carries the zero RuleResult, so the
				// pre-ADR-0044 behaviour is unchanged for that case.
				if err != nil {
					return result, err
				}
				if result.Decision != hookio.Approve {
					return result, nil
				}
				continue
			}
			if alwaysSafe[innerBase] || lspServices[innerBase] {
				continue
			}
			if browsingCmds[innerBase] {
				if hasRejectPath(innerArgs, pe) {
					return r.refuse("safe-commands: xargs " + innerBase + " references rejected path (deferred to claude-code)")
				}
				continue
			}
			// grep/rg: skip pattern arg before path checking
			if innerBase == "grep" || innerBase == "rg" {
				// The read-only disqualification applies through xargs too (pg2-ygjs5).
				// `xargs rg --pre CMD` runs CMD exactly as the direct spelling does, and
				// a guard on only the direct route is the one-spelling coverage this
				// whole family of defects is made of.
				if flag, executes := cmdparse.GrepExecFlag(innerBase, innerArgs); executes {
					return r.refuse("safe-commands: xargs " + innerBase + " " + flag + " runs a program, so this is not a read-only invocation (deferred to claude-code)")
				}
				fileArgs := cmdparse.SkipGrepPattern(innerBase, innerArgs)
				if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
					return r.refuse("safe-commands: xargs " + innerBase + " " + issue + " (deferred to claude-code)")
				}
				continue
			}
			// No program-operand exemption here: `xargs awk '{print $1}'` is not a
			// real shape, and the conservative direction (a needless Abstain) is the
			// safe one to take for it.
			if safeReadCmds[innerBase] {
				if issue := readPathIssue(innerArgs, pe, ""); issue != "" {
					return r.refuse("safe-commands: xargs " + innerBase + " " + issue + " (deferred to claude-code)")
				}
				continue
			}
			if safeWriteCmds[innerBase] {
				if unsafe, path := hasUnsafeWritePath(innerArgs, pe); unsafe {
					return r.refuse("safe-commands: xargs " + innerBase + " references non-writable path " + path + " (deferred to claude-code)")
				}
				continue
			}
			// Unknown inner command — abstain
			return hookio.NotApplicable()
		}
		// bash/sh -n: syntax check only, no execution — safe read command
		if (basename == "bash" || basename == "sh") && hasBashSyntaxCheckFlag(pc.Args) {
			fileArgs := extractBashSyntaxCheckFiles(pc.Args)
			if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
				return r.refuse("safe-commands: " + basename + " -n " + issue + " (deferred to claude-code)")
			}
			continue
		}
		// unzip: read archive, optionally write to -d destination or cwd
		if basename == "unzip" {
			result, err := evaluateUnzip(pc.Args, pe, cwd, r.Name())
			// See the cp call site for why the RuleResult is forwarded with the error.
			if err != nil {
				return result, err
			}
			if result.Decision != hookio.Approve {
				return result, nil
			}
			continue
		}
		// jar: tf/xf are safe read operations
		if basename == "jar" {
			if len(pc.Args) >= 1 && (pc.Args[0] == "tf" || pc.Args[0] == "xf") {
				if issue := readPathIssue(pc.Args[1:], pe, ""); issue != "" {
					return r.refuse("safe-commands: jar " + pc.Args[0] + " " + issue + " (deferred to claude-code)")
				}
				continue
			}
			return hookio.NotApplicable()
		}
		// log (macOS unified logging): show/stream/stats read; erase/config/
		// collect mutate — approve only the read verbs, defer the rest.
		if basename == "log" {
			sub := ""
			// pg2-wxbr9: NOT routed through pathCandidate. This loop hunts for the
			// log SUBCOMMAND KEYWORD (show/stream/stats/erase/config/collect), not a
			// path — a glued flag's value carries no subcommand-name signal, so
			// extracting it would only risk misidentifying a flag's value as the
			// subcommand.
			for _, a := range pc.Args {
				if !strings.HasPrefix(a, "-") {
					sub = a
					break
				}
			}
			if logReadSubcommands[sub] {
				continue
			}
			return hookio.NotApplicable()
		}
		// yq: read command unless -i/--inplace is present
		if basename == "yq" {
			if isYqInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return r.refuse("safe-commands: yq -i references non-writable path " + path + " (deferred to claude-code)")
				}
				continue
			}
			if issue := readPathIssue(pc.Args, pe, ""); issue != "" {
				return r.refuse("safe-commands: yq " + issue + " (deferred to claude-code)")
			}
			continue
		}
		// sed: read command unless -i/--in-place is present
		if basename == "sed" {
			if isSedInPlace(pc.Args) {
				if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
					return r.refuse("safe-commands: sed -i references non-writable path " + path + " (deferred to claude-code)")
				}
				continue
			}
			if issue := readPathIssue(pc.Args, pe, programOperand("sed", pc.Args)); issue != "" {
				return r.refuse("safe-commands: sed " + issue + " (deferred to claude-code)")
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
				return hookio.NotApplicable()
			}
			if issue := readPathIssue(pc.Args, pe, ""); issue != "" {
				return r.refuse("safe-commands: gofmt " + issue + " (deferred to claude-code)")
			}
			continue
		}
		// grep/rg: first non-flag arg is a pattern, not a file — skip it in path checks
		if basename == "grep" || basename == "rg" {
			// A flag that makes grep/rg RUN A PROGRAM disqualifies the whole
			// invocation (pg2-ygjs5). These commands are approvable because they only
			// READ; `rg --pre CMD` searches the output of CMD per file and ugrep's
			// `--filter`/`--pager`/`--view` likewise name programs, so the invocation
			// is an execution primitive and no amount of screening its ARGUMENTS makes
			// it read-only. Screening the operand as a path is not the fix either: it
			// catches `--pre /tmp/evil` only because that is spelled as a path, and
			// misses `--pre evilcmd` entirely.
			if flag, executes := cmdparse.GrepExecFlag(basename, pc.Args); executes {
				return r.refuse("safe-commands: " + basename + " " + flag + " runs a program, so this is not a read-only invocation (deferred to claude-code)")
			}
			fileArgs := cmdparse.SkipGrepPattern(basename, pc.Args)
			if issue := readPathIssue(fileArgs, pe, ""); issue != "" {
				return r.refuse("safe-commands: " + basename + " " + issue + " (deferred to claude-code)")
			}
			continue
		}
		// jq: skip value arguments for --arg, --argjson, --slurpfile, --rawfile
		// which take two args (name value) that may look like paths but aren't.
		if basename == "jq" {
			fileArgs := cmdparse.SkipJqValueFlags(pc.Args)
			if issue := readPathIssue(fileArgs, pe, programOperand("jq", fileArgs)); issue != "" {
				return r.refuse("safe-commands: jq " + issue + " (deferred to claude-code)")
			}
			continue
		}
		if safeReadCmds[basename] {
			if issue := readPathIssue(pc.Args, pe, programOperand(basename, pc.Args)); issue != "" {
				return r.refuse("safe-commands: " + basename + " " + issue + " (deferred to claude-code)")
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
			return r.refuse("safe-commands: " + basename + " has a dynamically-expanded path arg (deferred to claude-code)")
		}
		if basename == "cp" {
			result, err := evaluateCp(pc.Args, pe, r.Name())
			// `return result, err` rather than `return RuleResult{}, err`: since ADR
			// 0044 the helper's non-nil error may be a REFUSAL, whose RuleResult is the
			// floor and must not be dropped. For a genuine failure the helper returns
			// the zero RuleResult anyway, so this is behaviour-preserving there.
			if err != nil {
				return result, err
			}
			if result.Decision != hookio.Approve {
				return result, nil
			}
			continue
		}
		if safeWriteCmds[basename] {
			if unsafe, path := hasUnsafeWritePath(pc.Args, pe); unsafe {
				return r.refuse("safe-commands: " + basename + " references non-writable path " + path + " (deferred to claude-code)")
			}
			continue
		}
		// Unknown command - not our jurisdiction
		return hookio.NotApplicable()
	}
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "safe-commands: all commands are safe",
		Module:   r.Name(),
	}, nil
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

// pathCandidate returns the token a per-argument zone scan should test in place
// of arg, and whether there is a candidate at all. It is this file's zone-model
// counterpart to internal/rules/secrets.firstSecretRef's use of
// cmdparse.GluedFlagValue (pg2-cu3ro): a bare positional is tested as itself, a
// flag glued to a value via "=" is tested by that VALUE — the flag NAME is not a
// filename, but the value is exactly what the command opens — and a value-free
// flag contributes no candidate at all.
//
// pg2-wxbr9: every scanner below used to skip ANY `-`-prefixed token whole,
// which is right for a bare flag name but wrong for `--flag=value`: the value
// was discarded along with the name, so `cat --file=/etc/shadow` never reached
// the zone check that `cat --file /etc/shadow` correctly ran. That is the same
// one-token-hides-two-halves shape pg2-cu3ro fixed for the secrets deny-list,
// reconciled here for the zone model (project root / workspace root / sandbox
// zones). Measured on this tree before the fix: `cat --file=/etc/shadow`
// approved while `cat --file /etc/shadow` correctly abstained.
//
// AN UNRECOGNIZED GLUED FLAG'S VALUE IS EMITTED, not dropped, matching
// cmdparse.SkipGrepPattern's own documented direction: the cost of testing one
// extra candidate that fails looksLikePath is nothing, the cost of missing a
// real one is this defect.
//
// Not every skip site in this file is a zone scan — see the inline comments at
// the sites that are NOT routed through this helper (the `log`/xargs/unzip
// subcommand-or-executable-name scans and programOperand's role classifier) for
// why a glued value carries no path signal there.
func pathCandidate(arg string) (string, bool) {
	if value, glued := cmdparse.GluedFlagValue(arg); glued {
		return value, true
	}
	if strings.HasPrefix(arg, "-") {
		return "", false
	}
	return arg, true
}

// looksLikePath DELEGATES to cmdparse.LooksLikePath, which is now the single definition
// of "is this token shaped like a filesystem path" for the whole repo (pg2-zpct4).
//
// It stays as a named function rather than being inlined at its ~20 call sites so this
// rule reads unchanged and so the delegation has ONE place to be read. The delegation is
// the reconciliation's other half: cmdparse's static substitution seam DELEGATES path
// READABILITY to this rule's readPathIssue, and in exchange both seams answer "which
// tokens are paths" from the same predicate. Two definitions of that question is how a
// captured read came to clear `/etc/shadow` while the bare read refused it — the two
// screens disagreed about the answer, and nothing made them meet. Do not re-inline a
// local copy here; see THE pg2-zpct4 RECONCILIATION in cmdparse/parser.go for why the
// lexical half lives there (no config, no filesystem) and the readability half lives here
// (both, plus the zone model).
//
// The tc-sfpto bare-"~" and tc-fielf "~user" bypasses this predicate closes are recorded
// on the cmdparse definition; safecmds_test.go's TestLooksLikePath still pins them from
// this side, which is what proves the delegation preserved the behaviour.
func looksLikePath(arg string) bool {
	return cmdparse.LooksLikePath(arg)
}

// argHasDynamicExpansion reports whether ONE argument contains a shell expansion
// ($VAR, ${VAR}, $(...), backtick) that would resolve a path at runtime, hiding it
// from static path evaluation.
//
// It is deliberately a TEXT test over a PARSED ARGUMENT, never over the raw command
// string: the argument's syntactic ROLE is what makes the expansion a path, so a
// commit message or a `bd comment` body that merely QUOTES `cat $F` carries the same
// bytes in a non-operand position and must not be gated (the pg2-5b901 failure mode,
// and the shape this bead's own report criticises in the `pathtraversal` rule,
// which pg2-bn7sx has since DELETED for exactly that reason).
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
	// pg2-wxbr9: NOT routed through pathCandidate. This loop classifies which
	// single ARGUMENT plays the PROGRAM role (awk's script, jq's filter, sed's
	// expression) so readPathIssue can judge it by the narrower
	// isDynamicPathOperand instead of the ordinary path check — it does not
	// itself zone-check anything. Every argument, including a glued flag's
	// value, still reaches readPathIssue's own per-argument scan over this
	// same args slice, which is where the pg2-wxbr9 parity fix lives (see
	// pathCandidate). A glued spelling of one of THIS table's own flags
	// (`--field-separator=x`) is harmlessly treated as "just a flag" here: its
	// value is not a separate args[] token to mis-skip, so nothing is lost by
	// not extracting it in this classifier.
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
		// pg2-wxbr9: route through pathCandidate so a glued `--flag=$VAR` is
		// tested by its VALUE rather than discarded whole with its flag name.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		if argHasDynamicExpansion(cand) {
			return true
		}
	}
	return false
}

// hasRejectPath returns true if any path-like arg is in a rejected zone.
func hasRejectPath(args []string, pe *patheval.PathEvaluator) bool {
	for _, a := range args {
		// pg2-wxbr9: see pathCandidate's doc.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		if looksLikePath(cand) {
			if pe.Evaluate(cand) == patheval.PathReject {
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
		// pg2-wxbr9: route through pathCandidate so a glued flag's VALUE (not
		// just a bare positional) is tested — see pathCandidate's doc. `program`
		// is always a bare positional token (programOperand's own scan never
		// returns a `-`-prefixed value), so comparing the extracted candidate
		// against it is safe: a glued flag's value can only equal `program` in
		// the harmless coincidental case where the literal strings match.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		dynamic := argHasDynamicExpansion(cand)
		if program != "" && cand == program {
			// The PROGRAM operand is code, not a path, so it is judged by the
			// narrower predicate — see programOperand for why.
			dynamic = isDynamicPathOperand(cand)
		}
		if dynamic {
			return "has a dynamically-expanded path arg " + cand
		}
		if looksLikePath(cand) {
			if !pe.Evaluate(cand).CanRead() {
				return "references unknown path " + cand
			}
		}
	}
	return ""
}

// hasUnsafeWritePath returns (true, path) if any path-like arg is not in a writable zone.
// Only ReadWrite paths are acceptable for write operations.
func hasUnsafeWritePath(args []string, pe *patheval.PathEvaluator) (bool, string) {
	for _, a := range args {
		// pg2-wxbr9: see pathCandidate's doc.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		if looksLikePath(cand) {
			if !pe.Evaluate(cand).CanWrite() {
				return true, cand
			}
		}
	}
	return false, ""
}

// evaluateCp handles cp with source (read) and destination (write) semantics.
func evaluateCp(args []string, pe *patheval.PathEvaluator, module string) (hookio.RuleResult, error) {
	// Check for -t/--target-directory
	targetDir := ""
	for i, a := range args {
		if (a == "-t" || a == "--target-directory") && i+1 < len(args) {
			targetDir = args[i+1]
			break
		}
		if v, ok := strings.CutPrefix(a, "--target-directory="); ok {
			// THE VALUE HALF IS UNWRAPPED (cmdparse.UnwrapGluedQuotes, pg2-6f2gu)
			// before targetDir is used below. This is a bespoke CutPrefix, not
			// cmdparse.GluedFlagValue/pathCandidate, but it has the identical gap:
			// `--target-directory='/etc/'` arrives as the literal 8-byte value
			// `'/etc/'`, quote characters and all. looksLikePath requires an
			// UNquoted `/`/`./`/`../`/`~` prefix, so that leading quote made
			// looksLikePath(targetDir) false below — not merely "path looks
			// unfamiliar", but the entire writability/zone check for the
			// destination SKIPPED, and evaluateCp fell through to the unconditional
			// Approve at the end of this branch. MEASURED on this tree before this
			// fix: with cwd /home/user/project, `cp ./a.txt --target-directory=/etc/`
			// correctly abstained (destination not writable) while both
			// `cp ./a.txt --target-directory='/etc/'` and the double-quoted
			// spelling APPROVED. The separate-token spellings (`-t /etc/`,
			// `-t '/etc/'`) are unaffected
			// — cmdparse's ordinary lowering already strips a quote wrapping a WHOLE
			// token, so only THIS glued form needs the explicit unwrap.
			//
			// PER-CALL-SITE, NOT ROUTED THROUGH pathCandidate/GluedFlagValue: this
			// extraction is a one-flag special case that has never gone through
			// either (it predates pg2-wxbr9's pathCandidate seam), and folding it in
			// now would mean re-deriving this same targetDir value from a candidate
			// that could ALSO match a non-flag positional — pathCandidate's whole
			// point. A direct, explicit unwrap on the substring this loop already
			// knows is the flag's value keeps the change to the one shape that
			// regressed, matching secrets.firstSecretRef's identical call and
			// UnwrapGluedQuotes' own pg2-9zgso decision record for why a shared
			// helper is called explicitly rather than pushed into a lower layer.
			unwrapped := cmdparse.UnwrapGluedQuotes(v)

			// pg2-mp9oq: UnwrapGluedQuotes DECLINES — returns v UNCHANGED — on
			// malformed quoting: a double-wrapped value (`''/etc/''`), one whose
			// interior holds the wrapper character (multi-segment concatenation
			// `'/etc/'x'/etc/'`, or adjacent glued quotes `'/etc'"'"'/passwd'`),
			// or a mismatched-quote-character pair. (A truly UNTERMINATED quote,
			// by contrast, makes the whole command unparseable — cmdparse.Parse
			// returns zero commands and evaluateCp is never reached for it; that
			// path is not this bug and is unaffected by this change — see
			// TestEvaluateCp_TargetDirectoryMalformedGluedQuotingAbstains.)
			//
			// A declined value stays quote-wrapped, so it still fails
			// looksLikePath below (no unquoted `/`/`./`/`../`/`~` prefix) EXACTLY
			// as the pre-pg2-6f2gu bug did for the CLEAN case: the destination
			// zone/writability check is skipped entirely and this branch falls
			// through to the unconditional Approve. MEASURED on this tree before
			// this fix (post pg2-6f2gu): with cwd /home/user/project,
			// `cp ./a.txt --target-directory='/etc/'x'/etc/'` and
			// `cp ./a.txt --target-directory=''/etc/''` both APPROVED regardless
			// of destination — the exact "unclassifiable value defaults to the
			// wrong thing" shape as pg2-9zgso/pg2-6f2gu, just for the malformed
			// subset UnwrapGluedQuotes itself declines to touch.
			//
			// FIX: detect the decline directly (unwrapped == v, and v opened
			// with a quote character — so UnwrapGluedQuotes actually attempted
			// and declined, rather than trivially passing through a value that
			// was never quoted at all) and refuse via hookio.Refused, the SAME
			// "can't tell, won't clear it" verdict (NoOpinion/Abstain) this file
			// already uses one line below for "destination is not writable" and
			// throughout readPathIssue for "references unknown path" / "has a
			// dynamically-expanded path arg". This is fail-closed and consistent
			// with the rest of the file's vocabulary: an unclassifiable
			// destination must defer to Claude Code's own prompt, never
			// silently clear.
			if unwrapped == v && len(v) > 0 && (v[0] == '\'' || v[0] == '"') {
				return hookio.Refused(module, "safe-commands: cp target directory has malformed quoting "+v+" (deferred to claude-code)")
			}
			targetDir = unwrapped
			break
		}
	}

	if targetDir != "" {
		if looksLikePath(targetDir) && !pe.Evaluate(targetDir).CanWrite() {
			return hookio.Refused(module, "safe-commands: cp target directory is not writable "+targetDir+" (deferred to claude-code)")
		}
		for _, a := range args {
			// pg2-wxbr9: see pathCandidate's doc.
			cand, ok := pathCandidate(a)
			if !ok {
				continue
			}
			if cand == targetDir {
				continue
			}
			if looksLikePath(cand) && !pe.Evaluate(cand).CanRead() {
				return hookio.Refused(module, "safe-commands: cp source references non-readable path "+cand+" (deferred to claude-code)")
			}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}, nil
	}

	// Standard mode: last path-like arg is destination (write), rest are sources (read)
	var pathArgs []string
	for _, a := range args {
		// pg2-wxbr9: see pathCandidate's doc.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		if looksLikePath(cand) {
			pathArgs = append(pathArgs, cand)
		}
	}

	if len(pathArgs) == 0 {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with no explicit paths", Module: module}, nil
	}

	dest := pathArgs[len(pathArgs)-1]
	if !pe.Evaluate(dest).CanWrite() {
		return hookio.Refused(module, "safe-commands: cp destination is not writable "+dest+" (deferred to claude-code)")
	}

	for _, src := range pathArgs[:len(pathArgs)-1] {
		if !pe.Evaluate(src).CanRead() {
			return hookio.Refused(module, "safe-commands: cp source references non-readable path "+src+" (deferred to claude-code)")
		}
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: cp with known paths", Module: module}, nil
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
		// pg2-wxbr9: NOT routed through pathCandidate. This loop finds where
		// xargs' OWN flags stop so it can return the INNER COMMAND NAME —
		// which, by definition, is whatever token does not start with "-". A
		// glued xargs flag's value carries no signal about that boundary, so
		// extracting one here would answer a question this loop never asks.
		// The inner command's own args (returned below) go through this
		// file's ordinary zone checks once Evaluate recurses into them.
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
func evaluateUnzip(args []string, pe *patheval.PathEvaluator, cwd string, module string) (hookio.RuleResult, error) {
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
		// pg2-wxbr9: NOT routed through pathCandidate. unzip is an
		// Info-Zip-style tool: every flag is single-dash (-d, -x, -P, -l, -t,
		// plus the unenumerated -o/-q/-n/... that fall through to here) and
		// NONE support the GNU `--flag=value` glued convention, so there is no
		// glued value for cmdparse.GluedFlagValue to ever find on this
		// command — this skip is a genuine "not a candidate at all", not an
		// instance of the glued-value-discarding defect.
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
			return hookio.Refused(module, "safe-commands: unzip archive references unknown path "+archivePath+" (deferred to claude-code)")
		}
	}

	// For read-only operations (-l, -t), no write check needed
	if readOnly {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip read-only operation", Module: module}, nil
	}

	// Validate write destination
	writeDest := destDir
	if writeDest == "" {
		writeDest = cwd
	}
	if looksLikePath(writeDest) && !pe.Evaluate(writeDest).CanWrite() {
		return hookio.Refused(module, "safe-commands: unzip destination is not writable "+writeDest+" (deferred to claude-code)")
	}

	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: unzip with known paths", Module: module}, nil
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
		// pg2-wxbr9: see pathCandidate's doc.
		cand, ok := pathCandidate(a)
		if !ok {
			continue
		}
		files = append(files, cand)
	}
	return files
}
