// Package beads — action bead wrappers (bd builtin type=task or type=bug).
package beads

import "context"

// ActionKind enumerates the canonical action kinds the LLM may create.
type ActionKind string

const (
	ActionKindFixCI           ActionKind = "fix-ci"
	ActionKindRespond         ActionKind = "respond"
	ActionKindApplySuggestion ActionKind = "apply-suggestion"
	ActionKindRefactor        ActionKind = "refactor"
	ActionKindDeferToFuturePR ActionKind = "defer-to-future-pr"
)

// CreateActionInput is the typed input for creating an action bead.
type CreateActionInput struct {
	MergeRequestID    string
	AddressesFeedback []string
	Kind              ActionKind
	BdType            string // "task" or "bug"
	Title             string
	Body              string
}

// CreateAction creates an action bead. Phase 0 stub.
func CreateAction(_ context.Context, _ CreateActionInput) (id string, err error) {
	return "", ErrNotImplemented
}
