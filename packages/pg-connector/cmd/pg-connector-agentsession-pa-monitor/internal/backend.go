// backend.go: Backend implements pkg/provider/agentsession.Provider against
// a real pa-monitor CLI (via Runner). No AuthChecker: pa-monitor's daemon
// has no per-caller credential concept (mirrors pg-connector-scm-git's
// identical reasoning for local git).
package internal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/agentsession"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-agentsession-pa-monitor's concrete
// agentsession.Provider implementation.
type Backend struct {
	runner Runner
	now    func() time.Time
}

func New(r Runner) *Backend {
	return &Backend{runner: r, now: func() time.Time { return time.Now().UTC() }}
}

var _ agentsession.Provider = (*Backend)(nil)

// sessionJSON mirrors pa-monitor's own wire shape (packages/pa-monitor's
// cmd/pa-monitor/status_json.go's sessionJSON) field-for-field. Duplicated
// here deliberately — see this task's own doc comment on why importing
// pa-monitor's internal package is not an option.
type sessionJSON struct {
	SessionID     string  `json:"session_id"`
	Pid           *int    `json:"pid,omitempty"`
	Cwd           string  `json:"cwd"`
	Name          string  `json:"name,omitempty"`
	Model         string  `json:"model"`
	Status        string  `json:"status"`
	Blocker       string  `json:"blocker,omitempty"`
	Branch        string  `json:"branch,omitempty"`
	TerminalHost  string  `json:"terminal_host,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	SessionTokens uint64  `json:"session_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LongIdle      bool    `json:"long_idle"`
}

func (s sessionJSON) toSchema(asOf time.Time) schema.AgentSession {
	return schema.AgentSession{
		SessionID: s.SessionID, PID: s.Pid, Cwd: s.Cwd, Name: s.Name, Model: s.Model,
		Status: s.Status, Blocker: s.Blocker, Branch: s.Branch, TerminalHost: s.TerminalHost,
		StartedAt: s.StartedAt, Tokens: s.SessionTokens, CostUSD: s.CostUSD, LongIdle: s.LongIdle,
		AsOf: asOf.Format(time.RFC3339), Stale: false,
	}
}

// Show execs `pa-monitor info session:<id> --json`.
func (b *Backend) Show(ctx context.Context, id string) (*schema.AgentSession, error) {
	raw, err := b.runner.Info(ctx, "session:"+id)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var sj sessionJSON
	if err := json.Unmarshal(raw, &sj); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: decode info --json: "+err.Error())
	}
	result := sj.toSchema(b.now())
	return &result, nil
}

type statusJSONDoc struct {
	Sessions []sessionJSON `json:"sessions"`
}

// List execs `pa-monitor status --json`. query is always nil as called by
// this capability's own dispatch table — accepted here only for
// interface-shape symmetry with thread.Provider.List.
func (b *Backend) List(ctx context.Context, _ schema.QueryExpr, idsOnly bool) (*schema.AgentSessionListResult, error) {
	raw, err := b.runner.Status(ctx)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc statusJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: decode status --json: "+err.Error())
	}
	asOf := b.now()
	result := &schema.AgentSessionListResult{Truncated: false}
	for _, sj := range doc.Sessions {
		result.PresentIDs = append(result.PresentIDs, sj.SessionID)
		if !idsOnly {
			result.Entities = append(result.Entities, sj.toSchema(asOf))
		}
	}
	return result, nil
}

// classifyPaMonitorError maps a Runner-level failure (typically an
// *exec.ExitError from a daemon-unreachable pa-monitor invocation) onto
// scriptout's closed taxonomy. pa-monitor has no well-formed not_found
// signal for a single missing session id today (it fails the whole
// invocation) — every failure here is ErrUnavailable, the same
// classification every other pg-connector backend uses for its own
// unreachable dependency (this reads as a "degraded" source in the CLI's
// existing fan-out, not "disabled" — this codebase's disabled detection
// keys only on ErrUnknownOp today, for every backend, not just this one).
func classifyPaMonitorError(err error) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: "+err.Error())
}
