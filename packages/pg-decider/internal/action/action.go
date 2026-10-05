// Package action defines the action schema of entity-change-flow design 9.7:
// what a decider's `plan` returns and `apply` consumes.
//
// Two conventions bind the rule packets (which emit actions) and the apply
// packet (which executes them):
//
//   - A child create action whose anchor is created earlier in the same
//     action list carries Fields.Parent == AnchorParent; apply resolves it to
//     the id returned by that earlier anchor create.
//   - An annotate action has Target == the annotation key and Fields.Value ==
//     the string to set, or Fields.Clear == true to remove the key.
package action

import "encoding/json"

// Op is an action's operation.
type Op string

const (
	OpCreate   Op = "create"
	OpUpdate   Op = "update"
	OpReopen   Op = "reopen"
	OpClose    Op = "close"
	OpAnnotate Op = "annotate"
)

// AnchorParent is the Fields.Parent placeholder resolved by apply to the id
// of an earlier anchor create in the same action list.
const AnchorParent = "$anchor"

// The five skip reasons, as printed in PlanResult.Skipped[].Reason.
const (
	ReasonHidden         = "hidden"
	ReasonSuppressed     = "suppressed"
	ReasonAlreadyHandled = "already handled"
	ReasonReviewPending  = "review-pending"
	ReasonNotMatched     = "not matched"
)

// Fields are the per-op payload of an action; every member is optional.
type Fields struct {
	Title        string            `json:"title,omitempty"`
	IssueType    string            `json:"issue_type,omitempty"`
	Description  string            `json:"description,omitempty"`
	Parent       string            `json:"parent,omitempty"`
	Priority     string            `json:"priority,omitempty"`
	Labels       []string          `json:"labels,omitempty"`        // on create
	AddLabels    []string          `json:"add_labels,omitempty"`    // on update
	RemoveLabels []string          `json:"remove_labels,omitempty"` // on update
	Metadata     map[string]string `json:"metadata,omitempty"`
	Value        *string           `json:"value,omitempty"` // annotate: value to set
	Clear        bool              `json:"clear,omitempty"` // annotate: remove the key
}

// Action is one write a decider wants applied.
type Action struct {
	Op Op `json:"op"`
	// Kind is the work-item kind, or "" for annotations.
	Kind string `json:"kind"`
	// Target is the work-item id (nil, printed as null, for create) or the
	// annotation key.
	Target *string        `json:"target"`
	Fields Fields         `json:"fields"`
	Rule   string         `json:"rule"`
	Facts  map[string]any `json:"facts,omitempty"`
	// RequiresPrior: apply this action only if every EARLIER action with the
	// same Rule in the list was applied or deduped; otherwise apply reports it
	// as skipped-dependency.
	RequiresPrior bool `json:"requires_prior,omitempty"`
}

// Skip records a rule that produced no action, and why.
type Skip struct {
	Rule   string         `json:"rule"`
	Reason string         `json:"reason"`
	Facts  map[string]any `json:"facts,omitempty"`
}

// PlanResult is a decider's complete plan for one view.
type PlanResult struct {
	Actions []Action `json:"actions"`
	Skipped []Skip   `json:"skipped"`
}

// MarshalJSON prints both lists as arrays, never null.
func (p PlanResult) MarshalJSON() ([]byte, error) {
	type plain PlanResult
	if p.Actions == nil {
		p.Actions = []Action{}
	}
	if p.Skipped == nil {
		p.Skipped = []Skip{}
	}
	return json.Marshal(plain(p))
}
