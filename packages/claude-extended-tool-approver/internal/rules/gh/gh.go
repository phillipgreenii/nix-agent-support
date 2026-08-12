package gh

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var readOnlyPR = map[string]bool{
	"view": true, "list": true, "status": true, "diff": true, "checks": true,
}

var readOnlyIssue = map[string]bool{
	"view": true, "list": true, "status": true,
}

var readOnlyRepo = map[string]bool{
	"view": true, "list": true,
}

var readOnlyRun = map[string]bool{
	"view": true, "list": true, "watch": true,
}

var readOnlyRelease = map[string]bool{
	"view": true, "list": true,
}

// There is no modifyingPR map. `gh pr create` used to be its only member, at Ask; it now
// has a draft-aware verdict of its own in pr.go (operator ruling pg2-4yy4r item 2), and a
// one-entry map whose entry moved out would only invite the next modifying `pr`
// subcommand to be added where nothing reads it.

var modifyingIssue = map[string]bool{
	"create": true,
}

// prCreateSubcommands are the spellings that CREATE a pull request. `new` is a
// documented ALIAS of `create` (`gh pr create --help`, ALIASES section) and is live —
// measured on gh 2.97.0, 2026-08-12, `gh pr new -d` parses exactly as `gh pr create -d`.
// Gating only `create` would leave the verdict one synonym away from a bypass.
var prCreateSubcommands = map[string]bool{
	"create": true, "new": true,
}

// BranchResolver looks up branch context for runtime decisions.
type BranchResolver interface {
	CurrentBranch(cwd string) (string, error)
	RunBranch(runID string) (string, error)
}

type Rule struct {
	resolver BranchResolver
}

func New(resolver BranchResolver) *Rule {
	return &Rule{resolver: resolver}
}

func (r *Rule) Name() string {
	return "gh"
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
	for _, pc := range parsed {
		if !isGhExecutable(pc.Executable) {
			continue
		}
		resource, subcmd := "", ""
		// rest are the tokens AFTER the `<resource> <subcommand>` pair — the argv slice
		// every flag test below is asked about, so a verdict cannot be changed by the
		// subcommand words themselves and is independent of where in `rest` a flag sits.
		var rest []string
		if len(pc.Args) >= 1 {
			resource = pc.Args[0]
		}
		if len(pc.Args) >= 2 {
			subcmd = pc.Args[1]
			rest = pc.Args[2:]
		}
		if resource == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh status",
				Module:   r.Name(),
			}
		}
		if resource == "auth" && subcmd == "status" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh auth status",
				Module:   r.Name(),
			}
		}
		if resource == "api" {
			return r.apiVerdict(pc.Args[1:])
		}
		if resource == "search" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh search",
				Module:   r.Name(),
			}
		}
		if resource == "pr" && subcmd == "merge" {
			if v, ok := lastLongFlag(rest, "auto"); ok && boolFlagIsTrue(v) {
				// Intentionally Abstain — NOT a bypass, and the gate it defers to is now REAL.
				// --auto cannot merge while the PR is a draft, and since pg2-4yy4r item 2 the
				// un-drafting is ENFORCED as a human step: non-draft creation is Rejected and
				// `gh pr ready` Asks (see pr.go). This comment previously ASSUMED that gate —
				// it did not exist, because `gh pr ready` was ungated and emitted `{}`, so the
				// chain ran end to end un-prompted. Abstain also keeps the second reason
				// intact: toggling --auto refreshes the merge-commit message from the current
				// PR title/body. Do not change to Reject; do not weaken the `gh pr ready` Ask
				// without moving this branch with it, because the two together ARE the gate.
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh pr merge --auto: allowed (cannot merge until `gh pr ready`, which Asks; --auto refreshes merge message from PR title/body)",
					Module:   r.Name(),
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "gh pr merge (immediate) is prohibited: it merges now, bypassing the draft-first landing flow. Open/keep the PR as draft and use --auto, or merge via the WORKSPACE landing flow.",
				Module:   r.Name(),
			}
		}
		// The draft-first PR gate (pg2-25oru). Both branches live in pr.go; they are
		// tested here rather than under readOnlyPR/modifyingIssue because `create` and
		// `ready` are the two acts the draft-first ruling keys on, and `ready` reached the
		// final Abstain before this existed.
		if resource == "pr" && prCreateSubcommands[subcmd] {
			return r.prCreateVerdict(rest)
		}
		if resource == "pr" && subcmd == "ready" {
			return r.prReadyVerdict(rest)
		}
		if modifyingIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "modifying gh issue command",
				Module:   r.Name(),
			}
		}
		if readOnlyPR[subcmd] && resource == "pr" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh pr",
				Module:   r.Name(),
			}
		}
		if readOnlyIssue[subcmd] && resource == "issue" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh issue",
				Module:   r.Name(),
			}
		}
		if readOnlyRepo[subcmd] && resource == "repo" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh repo",
				Module:   r.Name(),
			}
		}
		if resource == "run" && subcmd == "rerun" {
			runID := extractRunID(pc.Args)
			if runID == "" {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: no run ID found",
					Module:   r.Name(),
				}
			}
			if r.resolver == nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: no resolver configured",
					Module:   r.Name(),
				}
			}
			currentBranch, err := r.resolver.CurrentBranch(input.CWD)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine current branch",
					Module:   r.Name(),
				}
			}
			runBranch, err := r.resolver.RunBranch(runID)
			if err != nil {
				return hookio.RuleResult{
					Decision: hookio.Abstain,
					Reason:   "gh run rerun: cannot determine run branch",
					Module:   r.Name(),
				}
			}
			if currentBranch == runBranch {
				return hookio.RuleResult{
					Decision: hookio.Approve,
					Reason:   "gh run rerun for current branch",
					Module:   r.Name(),
				}
			}
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "gh run rerun for different branch",
				Module:   r.Name(),
			}
		}
		if readOnlyRun[subcmd] && resource == "run" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh run",
				Module:   r.Name(),
			}
		}
		if readOnlyRelease[subcmd] && resource == "release" {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "read-only gh release",
				Module:   r.Name(),
			}
		}
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func isGhExecutable(exec string) bool {
	return exec == "gh" || filepath.Base(exec) == "gh"
}

// There is no local hasFlag. It was an EXACT-TOKEN test, which is the wrong shape for
// every flag question this rule asks: it misses a short form (`-d` for `--draft`), a
// clustered short (`-dw`), and an `=`-glued value (`--draft=false`, `--auto=false` — the
// latter being an IMMEDIATE merge that must not reach the --auto Abstain). Flag matching
// now goes through cmdparse.HasShortFlag / cmdparse.HasLongFlag, with the arity and
// precedence answers those primitives push to their caller supplied in pr.go
// (prCreateShortFlagTokens, lastLongFlag).

// extractRunID returns the first positional (non-flag) argument after the
// "rerun" subcommand in a gh run rerun invocation. Returns "" if not found.
func extractRunID(args []string) string {
	// args layout: ["run", "rerun", ...rest]
	// Find "rerun" index and scan after it for first non-flag arg.
	rerunIdx := -1
	for i, a := range args {
		if a == "rerun" {
			rerunIdx = i
			break
		}
	}
	if rerunIdx < 0 {
		return ""
	}
	for _, a := range args[rerunIdx+1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		// Only return if all characters are digits (run IDs are numeric).
		allDigits := len(a) > 0
		for _, c := range a {
			if !unicode.IsDigit(c) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return a
		}
	}
	return ""
}
