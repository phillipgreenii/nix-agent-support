// Package roles is pr-pool's role model: an ordered RoleSet of typed roles. Under
// the event model (design 2026-06-25) a role no longer embeds its query; it BINDS
// to one-or-more event TYPES (Observer subscription) and responds to ANY of them.
// A role carries its Binds, an optional opt-in correlation (Aggregator, Q2), and a
// type-specific config block (ccpool or command). This package does NOT import
// config (config imports roles to build the RoleSet), keeping the import DAG
// acyclic.
package roles

import (
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/event"
)

// RoleSet is the ordered list of roles a drain dispatches (config order).
type RoleSet []Role

type Role struct {
	Name    string
	Type    string // "ccpool" | "command"
	Cap     int
	Enabled bool
	// Binds is the event TYPES this role consumes (Observer subscription). It
	// replaces the former embedded Query: a role and a query are wired only
	// through a shared event-type string. A role responds to ANY of its Binds.
	Binds []string
	// Correlation is the OPT-IN Aggregator (EIP, Q2) declaration: when non-nil,
	// the role collects correlated events by CorrelationID and fires once the
	// Completeness condition is met. nil => the simple ANY path (built-ins).
	Correlation *event.CorrelationSpec
	CCPool      *CCPoolConfig  // set iff Type == "ccpool"
	Command     *CommandConfig // set iff Type == "command"
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
