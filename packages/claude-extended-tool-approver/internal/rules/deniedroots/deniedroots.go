// Package deniedroots implements the pg2-fxu7k fabricated-absolute-root guard.
//
// # The defect this closes
//
// Retro pg2-5q1xj (window 2026-08-17 -> 2026-08-31) measured 230 failed Read/Bash
// calls rooted at /home across 214 sessions, plus 93 bare-/ roots: a model
// invents a Linux-flavored absolute root (/home, /mnt, /repo, ...) instead of
// resolving a repo-relative path against the session cwd, burning a whole tool
// call to discover the invented root does not exist. A month of a prose rule
// (A-1, "Absolute-Path Provenance", pg2-ocaku) did not move the rate. This rule
// converts the wasted round trip into an immediate Reject naming the real fix.
//
// # The operator's design constraint
//
// The denied-root list MUST NOT be hard-coded here — Phillip, 2026-08-31,
// verbatim: "on a Linux machine these paths legitimately exist." Each machine
// configures its own list via a nix option:
// phillipgreenii.programs.claude-code.knownAbsentRoots — the SAME option
// home/programs/agent-rules already renders into the prose A-1 rule, reused
// rather than duplicated (see home/programs/claude-extended-tool-approver's
// default.nix) — which the claude-extended-tool-approver home-manager module
// threads through to the CETA_DENIED_ROOTS env var (mirroring the established
// extraReadWriteRoots/extraReadOnlyRoots -> CETA_EXTRA_*_ROOTS pattern) that
// patheval.PathEvaluator.MatchedDeniedRoot reads. This rule contains no root
// literal of its own; the list lives entirely in machine config.
//
// # Scope: Read and Bash only
//
// The bead calls out both explicitly, with Bash as the larger marginal win
// (Read's existing "File does not exist" error already names the cwd; Bash's
// bare shell error today does not). Other file tools (Write/Edit/MultiEdit/
// Delete) are out of scope — a model fabricating a root to WRITE through is not
// the measured defect, and pathsafety/safe-commands already govern those paths
// on their own terms.
//
// # Bash scope limits (deliberate, not oversights)
//
// This rule scans each leaf's own Args and I/O-redirection targets — the shape
// of the measured evidence (`cat /home/user/repo/file.go`,
// `ls /repo/...`, `find /mnt/... -name x`). It does NOT descend into a nested
// `sh -c '<inner>'` script body, and does NOT unwrap an `xargs` inner command,
// the way internal/rules/secrets does for the same class of Bash scan. Both are
// real gaps, but closing them needs the same shared-cache/depth-budget
// machinery secrets.go built for exactly this reason, which is out of
// proportion to an M-sized bead whose evidence is dominated by top-level
// invocations. Widen this rule the same way if a corpus measurement ever shows
// the gap matters.
//
// cmdparse.SkipMessageArgs filters out free-text message arguments (git commit
// -m, gh pr comment --body, bd close --reason, ...) before scanning, the same
// carve-out internal/rules/secrets applies to its own Bash scan and for the
// identical reason: prose that happens to MENTION a path is not a path
// reference, and it is a no-op for any command not in its enumerated table.
package deniedroots

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// Rule rejects a Read or Bash tool call that names an absolute path under a
// machine-configured denied root (see patheval.PathEvaluator.MatchedDeniedRoot).
type Rule struct {
	eval *patheval.PathEvaluator
}

// New constructs the rule against the shared path evaluator, matching every
// other pe-consuming rule's own New (pathsafety.New, secrets.New, ...).
func New(eval *patheval.PathEvaluator) *Rule { return &Rule{eval: eval} }

func (r *Rule) Name() string { return "denied-roots" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	switch input.ToolName {
	case "Read":
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("denied-roots: read file_path: %w", err)
		}
		eval := r.evaluatorFor(input)
		if root, ok := eval.MatchedDeniedRoot(path); ok {
			return r.reject(root, path, input.CWD, eval), nil
		}
	case "Bash":
		leaves, err := cmdparse.LeavesOf(input)
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("denied-roots: read bash command: %w", err)
		}
		eval := r.evaluatorFor(input)
		if root, path, ok := bashMatch(leaves, eval); ok {
			return r.reject(root, path, input.CWD, eval), nil
		}
	}
	// NOT-APPLICABLE, not a refusal: absence of a denied-root reference is not
	// this rule examining the input and declining to clear it — every later
	// rule (gitdir, secrets, pathsafety, safe-commands, ...) still needs to run.
	return hookio.NotApplicable()
}

// evaluatorFor prefers a per-call override (input.PathEval, set by the docker
// rule for a container-scoped inner evaluation) over the rule's own injected
// evaluator, mirroring safe-commands' identical preference (safecmds.go's
// baseEval selection) — the denied-root list is machine config carried on the
// evaluator, and a container-scoped delegate must not fall back to the wrong
// one.
func (r *Rule) evaluatorFor(input *hookio.HookInput) *patheval.PathEvaluator {
	if input.PathEval != nil {
		return input.PathEval
	}
	return r.eval
}

// reject builds the redirect verdict: name the offending root, the path that
// named it, and the real fix (resolve against the session cwd).
func (r *Rule) reject(root, path, cwd string, eval *patheval.PathEvaluator) hookio.RuleResult {
	hint := cwd
	if hint == "" && eval != nil {
		hint = eval.ProjectRoot()
	}
	return hookio.RuleResult{
		Decision: hookio.Reject,
		Reason: "denied-roots: " + root + " does not exist on this machine — " + path +
			" is a fabricated root; resolve the intended path against the session cwd (" + hint + ") instead",
		Module: r.Name(),
	}
}

// bashMatch returns the first denied-root reference in leaves: the matched
// root, the argument (or redirection target) that named it, and whether one
// was found at all.
func bashMatch(leaves []cmdparse.ParsedCommand, eval *patheval.PathEvaluator) (root, path string, found bool) {
	for _, pc := range leaves {
		// Data leaves (a `for` word list, a `case` subject, an arithmetic/test/
		// let command's embedded substitution — cmdparse's emitDataSpan,
		// PipelineID -1, no Executable) are never commands and carry no
		// command-shaped path argument to judge (mirrors safecmds' identical
		// skip).
		if pc.PipelineID == -1 {
			continue
		}
		basename := filepath.Base(pc.Executable)
		args := cmdparse.SkipMessageArgs(basename, pc.Args)
		for _, a := range args {
			candidate := a
			if value, glued, malformed := cmdparse.GluedFlagValue(a); glued {
				// A glued flag value this rule cannot unwrap (pg2-52eod's residual
				// case) carries no usable candidate — skip it rather than testing
				// the still-quote-wrapped text, which cleanPath would treat as a
				// relative path and never match an absolute denied root anyway.
				if malformed {
					continue
				}
				candidate = value
			} else if strings.HasPrefix(a, "-") {
				// A bare flag name (no glued value) is not a path.
				continue
			}
			if root, ok := eval.MatchedDeniedRoot(candidate); ok {
				return root, candidate, true
			}
		}
		for _, redir := range pc.Redirections {
			if root, ok := eval.MatchedDeniedRoot(redir.Path); ok {
				return root, redir.Path, true
			}
		}
	}
	return "", "", false
}
