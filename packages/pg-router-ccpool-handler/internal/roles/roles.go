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
	"time"

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
	// PoolDir is this role's own dedicated ccpool pool directory (bead
	// pg2-mr0sl): every ccpool call this role's dispatch makes (Capacity's
	// admission gate, Ensure/Send/Close, and every List call the wait-loop
	// polls) is scoped to it by overriding CCPOOL_POOL for just that
	// subprocess invocation (internal/ccpool.NewCLIRunnerForPool) — never by
	// mutating this process's own environment, which every OTHER role's
	// dispatch running in the same process must not see change. "" (the
	// default) is unchanged behavior: the ccpool CLI resolves CCPOOL_POOL
	// however it is already inherited from this process's own environment
	// (today, pg-router core's single, process-wide
	// daemon.handlerCcpoolPool / periodicDrain.handlerCcpoolPool override, or
	// ccpool's own default XDG pool when that is unset too) — every role
	// sharing that one pool, exactly as before this bead.
	PoolDir string
	// BeadsDir (bead pg2-2grpj) is the workspace directory of the bd tracker the
	// items this role dispatches live in, when that is NOT cfg.RepoRoot's
	// tracker (e.g. the pg2 tracker for the escalation triager). Every bd call
	// the handler makes for this role -- completion polling, OnFailure,
	// Unclaim, Comment -- resolves against it, and the dispatched session's
	// BEADS_DIR is BeadsDir/.beads. "" (default) keeps cfg.RepoRoot.
	BeadsDir string
	// BudgetStopEscalateAfter (bead pg2-6akgz): the per-bead budget-stop count
	// at which later beads escalate (split-review, then human). This part only
	// RECORDS and counts stops; 0 disables recording (kill switch). Loaded from
	// the role config; the loader defaults an absent value to 3. Budgets are
	// never changed by it.
	BudgetStopEscalateAfter int
	// WorktreeQuietWindow (bead pg2-uyahp, INV-CCH-23) overrides, for THIS role
	// only, how long a finished dispatch's session and its Agent-tool
	// subagents' transcripts must be quiet before the handler removes the
	// worktree and closes the settled session (config.Config.WorktreeQuietWindow
	// is the handler-wide default, 2m). 0 (the zero value, an absent key) means
	// "use the handler-wide value", so a role that never sets it is unchanged.
	// While the session is idle but not yet closed it still counts against the
	// pool's max_sessions, so a shorter window returns the slot sooner; the
	// cost is a higher chance of mistaking a silent-but-running subagent for a
	// finished one (see executor.waitSessionQuiet). Validated at load: > 0, at
	// least config.QuietWindowOverrideMinPolls x PollInterval, and no more than
	// WorktreeQuietMax.
	WorktreeQuietWindow time.Duration
	// ExtraAllowedTools (bead pg2-nk6th.5) are tool grants, in the same syntax
	// as config.Config.AllowedTools (e.g. "Bash(gh pr create:*)"), merged onto
	// the handler-wide AllowedTools for THIS role's dispatches only
	// (config.MergeAllowedTools, applied in buildDeps): handler-wide grants
	// first, then these in order, duplicates dropped. Empty (the default) leaves
	// the handler-wide list unchanged. A grant that only one role needs (e.g.
	// `git push`) belongs here, never in the handler-wide list, which would
	// widen every role.
	ExtraAllowedTools []string
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
	// Prefetch (bead pg2-hh32y), when non-nil, has the HANDLER pin the freshly
	// ensured worktree to a commit and pre-generate the diff before the session
	// starts, instead of leaving the model to fetch/checkout/diff on its own
	// clock. Valid only with the "worktree" strategy (anything else would
	// check out into a directory this handler does not own).
	Prefetch *PrefetchConfig
}

// PrefetchConfig configures the handler-side pre-fetch of a worktree-isolation
// role (bead pg2-hh32y). Every *Tmpl-style field is a text/template rendered
// against prompt.Context (so {{index .Item.Metadata "head_sha"}} works). The
// pre-fetch is BEST EFFORT: a failure is logged and surfaces to the prompt as
// {{.Prefetch.OK}} == false so the prompt can fall back to its own steps.
type PrefetchConfig struct {
	// Remote is the git remote Refspec is fetched from. "" means "origin".
	Remote string
	// Refspec is the ref to fetch when Rev is not already a local commit
	// (e.g. pull/{{index .Item.Metadata "pr_number"}}/head). "" disables the
	// fetch: Rev must then already be local.
	Refspec string
	// Rev is the commit the worktree is checked out at, detached. Required;
	// must render to a hex object id (7-64 hex digits).
	Rev string
	// DiffBase, when non-empty, names the base the diff is taken against; the
	// handler writes `git diff --numstat <base>...<rev>` and `git diff
	// <base>...<rev>` files. "" writes neither.
	DiffBase string
	// Timeout bounds the whole pre-fetch (a Go duration string, e.g. "5m").
	// "" means the default (5m).
	Timeout string
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
