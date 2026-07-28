// Package killshell gates the KillShell Claude Code tool: it auto-approves
// terminating a background shell that ceta tracked as agent-owned, and asks for
// everything else (a hook-support parity capability; KillShellEvaluator).
//
// KillShell is a NON-Bash tool, so it is dispatched by tool name through the
// engine's first-match `Evaluate` path (not `EvaluateExpression`). Ownership is
// resolved via an injected ShellStore, which the background-shell tracker
// populates on PostToolUse of a `run_in_background` Bash call (see
// internal/asklog shells). When no store is available (e.g. offline replay), the
// rule fails secure: Ask.
package killshell

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// ShellStore resolves the recorded owner of a background shell. It is satisfied
// by *asklog.Store; a nil ShellStore means "no ownership record available" and
// the rule fails secure (Ask). Kept as a narrow interface here so the rule does
// not depend on the persistence layer (dependency inversion).
type ShellStore interface {
	// ShellOwner returns the recorded creator of shellID ("agent") and whether
	// a record was found.
	ShellOwner(shellID string) (owner string, known bool)
}

// killShellInput is the KillShell tool_input shape (only shell_id is needed).
type killShellInput struct {
	ShellID string `json:"shell_id"`
}

type Rule struct {
	store ShellStore
}

// New constructs the KillShell rule. store MAY be nil — the rule then fails
// secure (Ask), since it cannot verify ownership.
func New(store ShellStore) *Rule { return &Rule{store: store} }

func (r *Rule) Name() string { return "killshell" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "KillShell" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}

	var ti killShellInput
	if err := json.Unmarshal(input.ToolInput, &ti); err != nil || ti.ShellID == "" {
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "KillShell missing shell_id — cannot determine ownership",
			Module:   r.Name(),
		}
	}

	if r.store == nil {
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "cannot verify ownership of shell " + ti.ShellID,
			Module:   r.Name(),
		}
	}

	if owner, known := r.store.ShellOwner(ti.ShellID); known && owner == "agent" {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "agent-owned background shell " + ti.ShellID + " — safe to terminate",
			Module:   r.Name(),
		}
	}

	return hookio.RuleResult{
		Decision: hookio.Ask,
		Reason:   "shell " + ti.ShellID + " is not a tracked agent-owned shell — confirm termination",
		Module:   r.Name(),
	}
}
