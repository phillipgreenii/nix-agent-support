// Package roles is pr-pool's role model: an ordered RoleSet of typed roles. Under
// the event model (design 2026-06-25) a role no longer embeds its query; it BINDS
// to one-or-more event TYPES (Observer subscription) and responds to ANY of them.
// A role carries its Binds and a type-specific config block (ccpool or
// command). This package does NOT import
// config (config imports roles to build the RoleSet), keeping the import DAG
// acyclic.
package roles

import (
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/budget"
)

// RoleSet is the ordered list of roles a drain dispatches (config order).
type RoleSet []Role

type Role struct {
	Name    string
	Type    string // "ccpool" | "command"
	Enabled bool
	// Binds is the event TYPES this role consumes (Observer subscription). It
	// replaces the former embedded Query: a role and a query are wired only
	// through a shared event-type string. A role responds to ANY of its Binds.
	//
	// Capacity is deliberately NOT declared here (INV-CONC-1): a per-role `cap`
	// existed once and was removed (bead pg2-f3mcb.2) because the core keeping a
	// concurrency ceiling contradicted the invariant outright — capacity is the
	// handler's own business, expressed only as a pre-accept `busy` decline.
	Binds   []string
	CCPool  *CCPoolConfig  // set iff Type == "ccpool"
	Command *CommandConfig // set iff Type == "command"
	// RetryBackoff is this role's HANDLER RETRY CADENCE (INV-FAIL-2, pg2-0c8yz):
	// how long the core waits before re-offering an event this role's handler
	// pre-accept declined, before expiresAt bounds it. A zero value is safe —
	// backoff.Policy.Duration sanitizes it against backoff.Default() — so a role
	// that never sets it (e.g. every built-in role) still gets a sane cadence.
	RetryBackoff backoff.Policy
}

// CCPoolConfig is the ccpool role type's behavior + launch config.
type CCPoolConfig struct {
	Actor           string
	SkillMD         string
	Completion      Completion
	OnFailure       FailureAction
	OnDispatchFail  DispatchFailAction
	AuthorshipGuard bool
	PromptBody      string             // the task prompt template source (no rails)
	Prompt          *template.Template // parsed PromptBody (missingkey=error)
	Budget          budget.Budget      // finite => watchdog + prompt line; unlimited => neither
	Isolation       IsolationConfig    // how the dispatched session's WORKSPACE_ROOT is prepared
}

// IsolationConfig selects how a ccpool role's WORKSPACE_ROOT is prepared before
// dispatch. The zero value (Type == "") means "worktree", the long-standing
// behavior (a fresh per-item git worktree off RepoRoot) — so an existing config
// that never sets this is unaffected.
type IsolationConfig struct {
	// Type is one of: "" / "worktree" (create-or-reuse a git worktree at
	// <WorktreeDir>/<itemID> off RepoRoot — the default), "none" (no isolation;
	// WORKSPACE_ROOT = RepoRoot, for a role whose own prompt/skill manages its
	// own isolation), "path" (create-or-reuse one fixed configured directory,
	// see Path), or "workforest" (create-or-reuse a coordinated multi-repo set
	// keyed by item id).
	Type string
	// Path is the fixed directory to create-or-reuse; set iff Type == "path".
	Path string
}

// CommandConfig is the command role type's config.
type CommandConfig struct {
	Argv     []string
	ArgvTmpl []*template.Template // parsed Argv elements
}

// ExternalID builds the per-attempt ccpool external_id:
// <prefix><name>-<beadid>-<stamp>. The stamp makes it unique per attempt (ADR 0015).
func (r Role) ExternalID(prefix, beadID, stamp string) string {
	return prefix + r.Name + "-" + beadID + "-" + stamp
}

// DisplayName builds the stable per-bead ccpool --name label: <prefix><name>-<beadid>.
func (r Role) DisplayName(prefix, beadID string) string {
	return prefix + r.Name + "-" + beadID
}
