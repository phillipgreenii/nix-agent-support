package safecmds

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// colima and plutil are NOT blanket alwaysSafe members (pg2-ul2nt5, operator ruling
// on pg2-usuewr, 2026-10-28: "colima ssh -- cmd recurse on the inner command.
// plutil replace should be fine if the path is writable."). Each keeps every
// read/non-exec invocation approved exactly as before; only the two shapes that
// run or write something are argument-checked:
//
//   - `colima ssh -- CMD` runs CMD inside the VM, so CMD is evaluated through the
//     full rule chain (the same I13 structural delegate the xargs sh -c branch
//     uses) and approved iff the chain approves it.
//   - plutil's write verbs (-replace/-insert/-remove/-convert/-create, and any -o
//     output) are approved only when every write target is in a writable zone.
//
// launchctl and sfltool stay blanket-approved in alwaysSafe, deliberately, pending
// an operator ruling (pg2-usuewr): `launchctl bootstrap|load|submit` loads a job
// definition into a launchd domain, which then RUNS the plist's
// Program/ProgramArguments, so those are program-execution verbs. The operator
// asked what bootstrap does and did not rule; behaviour is unchanged here.

// colimaVerb reports whether a colima invocation is an `ssh` verb, and
// ambiguous when an unknown flag precedes the verb while an `ssh` token exists (the
// verb cannot be told from a flag value, so the caller must not approve blindly).
func colimaVerb(args []string) (isSSH bool, ambiguous bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return false, false
		case a == "-p" || a == "--profile":
			i++
		case strings.HasPrefix(a, "--profile="), a == "-v", a == "--verbose", a == "--very-verbose":
		case strings.HasPrefix(a, "-"):
			for _, rest := range args[i+1:] {
				if rest == "ssh" {
					return false, true
				}
			}
			return false, false
		default:
			return a == "ssh", false
		}
	}
	return false, false
}

// colimaSSHCommand extracts the inner command of `colima ssh [flags] -- CMD...`.
// ok=false means the shape is not a recognised command form (bare ssh, no `--`,
// unknown flag, empty command): the caller abstains. help=true is `colima ssh
// --help`, which runs nothing. ssh joins multiple words with spaces and the VM
// shell re-parses the result, so a multi-word CMD is accepted only when every word
// is free of shell syntax; a single word is a script and is parsed as one.
func colimaSSHCommand(args []string) (script string, leaves []cmdparse.ParsedCommand, help bool, ok bool) {
	i := 0
	for ; i < len(args) && args[i] != "ssh"; i++ {
		if args[i] == "-p" || args[i] == "--profile" {
			i++
		}
	}
	if i >= len(args) {
		return "", nil, false, false
	}
	i++
	sawHelp := false
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest := args[i+1:]
			if len(rest) == 0 {
				return "", nil, false, false
			}
			if len(rest) == 1 {
				parsed := cmdparse.Parse(rest[0])
				if len(parsed) == 0 {
					return "", nil, false, false
				}
				return rest[0], parsed, false, true
			}
			for _, w := range rest {
				if w == "" || strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:=,@%+-") != "" {
					return "", nil, false, false
				}
			}
			return "", []cmdparse.ParsedCommand{{Executable: rest[0], Args: rest[1:]}}, false, true
		case a == "-p" || a == "--profile":
			i++
		case strings.HasPrefix(a, "--profile="), a == "-v", a == "--verbose", a == "--very-verbose",
			a == "--layer", a == "--layer=true", a == "--layer=false":
		case a == "-h" || a == "--help":
			sawHelp = true
		default:
			return "", nil, false, false
		}
	}
	return "", nil, sawHelp, false
}

// evaluateColima judges one colima leaf. A non-ssh verb is approved as it always
// was; ssh recurses on its inner command.
func (r *Rule) evaluateColima(pc cmdparse.ParsedCommand, input *hookio.HookInput, pe *patheval.PathEvaluator, cwd string) (hookio.RuleResult, error) {
	isSSH, ambiguous := colimaVerb(pc.Args)
	if ambiguous {
		return r.refuse("safe-commands: colima has an unrecognised flag before its verb, so whether it is `ssh` is not statically known (deferred to claude-code)")
	}
	if !isSSH {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: colima non-exec verb", Module: r.Name()}, nil
	}
	script, leaves, help, ok := colimaSSHCommand(pc.Args)
	if help {
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: colima ssh --help", Module: r.Name()}, nil
	}
	if !ok {
		return r.refuse("safe-commands: colima ssh without a statically known `-- COMMAND` is an interactive or opaque shell (deferred to claude-code)")
	}
	if r.exprEval == nil {
		// Construction-state posture, same as the xargs sh -c branch: not a judgement
		// about the inner command.
		return hookio.NotApplicable()
	}
	source := script
	if source == "" {
		source = pc.Raw
	}
	// The direct-argv form has no script text, so `source` is the outer leaf's own
	// raw text and a stack frame for it would trip the cycle guard on the very
	// expression being judged. docker exec passes a nil stack for the same shape: the
	// finite argv cannot embed itself, so no unbounded loop is reachable.
	var stack []hookio.StackFrame
	if script != "" {
		stack = []hookio.StackFrame{{RuleName: r.Name(), Command: "colima ssh", Expression: normalizeExpr(pc.Raw)}}
	}
	scoped := *input
	scoped.CWD = cwd
	scoped.PathEval = pe
	// ADR 0043 recursion boundary; see the xargs sh -c call site for the rationale.
	res, err := hookio.FromRecursion(r.exprEval.EvaluateStructure(source, leaves, stack, &scoped, nil, nil))
	if err != nil {
		return res, err
	}
	if res.Decision != hookio.Approve {
		return res, nil
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: colima ssh inner command approved", Module: r.Name()}, nil
}

// plutilWriteTargets scans a plutil invocation. writes reports whether it modifies
// files (a write verb, or -o); targets are the paths written: the -o value when
// given (the input is then only read), otherwise every file operand (in-place).
// ok=false means the invocation could not be scanned (a flag missing its value).
func plutilWriteTargets(args []string) (writes bool, targets []string, ok bool) {
	var files []string
	out := ""
	hasOut, writeVerb := false, false
	take := func(i *int, n int) bool {
		if *i+n >= len(args) {
			return false
		}
		*i += n
		return true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case "-o":
			if !take(&i, 1) {
				return true, nil, false
			}
			hasOut, out = true, args[i]
		case "-convert", "-create":
			writeVerb = true
			if !take(&i, 1) { // fmt
				return true, nil, false
			}
		case "-remove":
			writeVerb = true
			if !take(&i, 1) { // keypath
				return true, nil, false
			}
		case "-insert", "-replace":
			writeVerb = true
			if !take(&i, 2) { // keypath, type flag
				return true, nil, false
			}
			if t := args[i]; t != "-dictionary" && t != "-array" {
				if !take(&i, 1) { // value
					return true, nil, false
				}
			}
		case "-extract":
			if !take(&i, 2) { // keypath, fmt
				return true, nil, false
			}
		case "-type", "-expect", "-e":
			if !take(&i, 1) {
				return true, nil, false
			}
		default:
			if !strings.HasPrefix(a, "-") || a == "-" {
				if a != "-" {
					files = append(files, a)
				}
			}
		}
	}
	if hasOut {
		if out == "-" {
			return writeVerb, nil, true
		}
		return true, []string{out}, true
	}
	if writeVerb {
		return true, files, true
	}
	return false, nil, true
}

// plutilIssue returns a non-empty refusal reason when plutil would write somewhere
// not statically writable. Read verbs and stdout output return "".
func plutilIssue(args []string, pe *patheval.PathEvaluator) string {
	writes, targets, ok := plutilWriteTargets(args)
	if !ok {
		return "plutil has an operand missing its value, so its write target is not known"
	}
	if !writes {
		return ""
	}
	if argsHaveDynamicExpansion(args) {
		return "plutil write has a dynamically-expanded argument, so its target is not statically known"
	}
	if unsafe, path := hasUnsafeWritePath(targets, pe); unsafe {
		return "plutil write references non-writable path " + path
	}
	return ""
}
