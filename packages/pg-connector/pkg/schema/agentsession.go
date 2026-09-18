// agentsession.go: the agentsession entity/capability's shared JSON wire
// shape. Every field mirrors a fact pa-monitor already tracks — nothing
// here is newly computed by this capability. Deliberately carries no
// transcript path/content field: transcript access happens only through
// the search capability.
package schema

// AgentSessionSchemaVersion is the agentsession capability's own schema
// version, mirroring every other capability's identical
// <Entity>SchemaVersion convention (INV-VER-1).
const AgentSessionSchemaVersion = 1

// AgentSession is one Claude Code agent session as pa-monitor reports it.
type AgentSession struct {
	SessionID    string  `json:"session_id"`
	PID          *int    `json:"pid,omitempty"` // nil when the process is dead
	Cwd          string  `json:"cwd"`
	Name         string  `json:"name,omitempty"`
	Model        string  `json:"model"`
	Status       string  `json:"status"`            // "working" | "blocked" | "idle"
	Blocker      string  `json:"blocker,omitempty"` // "human_input" | "human_authn" | "usage_limit" | "error"
	Branch       string  `json:"branch,omitempty"`
	TerminalHost string  `json:"terminal_host,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	Tokens       uint64  `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	LongIdle     bool    `json:"long_idle"`

	// AsOf/Stale: INV-ASOF-1/2, the same contract every other capability
	// carries.
	AsOf  string `json:"as_of"`
	Stale bool   `json:"stale"`
}

// AgentSessionListResult is the "list" op's wire result payload — the same
// generic shape ThreadListResult/IssueListResult document in full.
type AgentSessionListResult struct {
	Entities   []AgentSession `json:"entities"`
	PresentIDs []string       `json:"present_ids"`
	Cursor     *string        `json:"cursor"`
	Truncated  bool           `json:"truncated"`
}
