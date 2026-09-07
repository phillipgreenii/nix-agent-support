// Package evalcontract is the PORT of the effect-graph spike: the
// hook-independent request/response types an adapter (a PreToolUse hook, a
// CLI, a test) uses to ask for a decision. It deliberately does not import
// internal/hookio; Decision is a new type, not an alias of the hook's.
//
// Known import cycle to fix later: effectgraph reaches internal/hookio
// transitively through cmdparse (ParsedCommand embeds hookio.Redirection).
package evalcontract

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"

// Request is what the caller knows about the command. ProjectRoot may be left
// empty; the evaluator then detects it from CWD. Dialect is the shell the
// command is written in (informational in this slice; parsing is bash).
type Request struct {
	Command     string
	Dialect     string
	CWD         string
	ProjectRoot string
	Env         map[string]string
}

// Decision is the verdict vocabulary. Ask exists in the vocabulary but nothing
// in this slice emits it.
type Decision int

const (
	// Abstain: the model is insufficient to decide (the zero value, so an
	// unset decision never reads as approval).
	Abstain Decision = iota
	// Approve: every node is understood and every effect permitted.
	Approve
	// Ask: defer to a human (reserved).
	Ask
	// Reject: at least one effect is known-forbidden.
	Reject
)

// String returns the deterministic decision name.
func (d Decision) String() string {
	switch d {
	case Abstain:
		return "abstain"
	case Approve:
		return "approve"
	case Ask:
		return "ask"
	case Reject:
		return "reject"
	default:
		return "decision-invalid"
	}
}

// Response carries the decision, a reason naming the deciding node and effect,
// and both graphs (the interpreted one carrying policy marks).
type Response struct {
	Decision    Decision
	Reason      string
	Structural  effectgraph.Graph
	Interpreted effectgraph.Graph
}
