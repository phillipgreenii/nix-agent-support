// Package roles is pr-pool's role model: an ordered RoleSet of typed roles. A role
// carries a query and a type-specific config block (ccpool or command). RoleKind is
// gone — behavior is declared by config enums. This package does NOT import config
// (config imports roles to build the RoleSet), keeping the import DAG acyclic.
package roles

import (
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

// RoleSet is the ordered list of roles a drain dispatches (config order).
type RoleSet []Role

type Role struct {
	Name    string
	Type    string // "ccpool" | "command"
	Cap     int
	Enabled bool
	Query   query.Query
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
