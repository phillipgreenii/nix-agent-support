// Package roles is this module's OWN role model: the ccpool/command role-kind
// distinction ADR 0065's "Open question resolved" section keeps as this
// module's own private concern, never exposed to packages/pg-router's core
// (which, after Task 5.4, keeps only Name/Enabled/Binds/RetryBackoff on its
// own roles.Role — see docs/adr/0065's "Wire contract" section). This is NOT
// a copy of packages/pg-router/internal/roles: that package stays in-core and
// is unreachable from here (Go's internal-package visibility rule); this
// Config carries only the launch-time fields the moved ccpool/command
// executor logic actually reads, dropping the core-only RoleSet/Binds/
// RetryBackoff/DeclaredBindTypes machinery that has no meaning on this side
// of the wire boundary.
package roles

import (
	"text/template"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
)

// Role is one dispatched participant role, resolved from this module's own
// (not-yet-built, Task 5.7's job) config at process start rather than
// per-dispatch over the wire — dispatch's wire message carries only the
// event; which role this running process IS gets decided once, at startup.
type Role struct {
	Name    string
	Type    string         // "ccpool" | "command" — this module's own private enum
	CCPool  *CCPoolConfig  // set iff Type == "ccpool"
	Command *CommandConfig // set iff Type == "command"
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
	// WORKSPACE_ROOT = RepoRoot), "path" (create-or-reuse one fixed configured
	// directory, see Path), or "workforest" (create-or-reuse a coordinated
	// multi-repo set keyed by item id).
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
// <prefix><name>-<beadid>-<stamp>. The stamp makes it unique per attempt.
func (r Role) ExternalID(prefix, beadID, stamp string) string {
	return prefix + r.Name + "-" + beadID + "-" + stamp
}

// DisplayName builds the stable per-bead ccpool --name label: <prefix><name>-<beadid>.
func (r Role) DisplayName(prefix, beadID string) string {
	return prefix + r.Name + "-" + beadID
}
