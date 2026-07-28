// Package pathtraversal prompts (Ask) on Bash commands that contain a `../..`
// path-traversal escape (a hook-support parity capability;
// PathTraversalEvaluator).
//
// Decision policy: Ask (NOT Reject). hook-support hard-DENYs `../..`, but that
// is too blunt for ceta, which is general-purpose rather than monorepo-bounded:
// an agent working in a git worktree reaches the workspace root via exactly
// `../..`, and routine navigation (`cat ../../README.md`, `cd ../../other-repo`)
// uses it constantly. A hard Reject would break those workflows. Ask keeps a
// human in the loop — it can never be a silent auto-approval (Ask outranks
// Approve in the most-restrictive fold) — without hard-blocking legitimate
// traversal. A single-level `../` stays Abstain (defer entirely).
//
// This is a lexical guard, complementing (not duplicating) ceta's zone-based
// path rules (`path-safety`, `safe-commands`), which only see tokens that "look
// like" a path; a literal `../..` substring anywhere in the command is caught
// here regardless.
//
// WS3 refinement: replace the raw substring test with a resolve-and-check
// against an allowed root (so a traversal that stays inside the workspace can
// auto-approve while one that escapes it is Rejected). Tracked for WS3.
package pathtraversal

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "path-traversal" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if strings.Contains(cmdStr, "../..") {
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "path traversal detected (../..) — confirm before proceeding",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
