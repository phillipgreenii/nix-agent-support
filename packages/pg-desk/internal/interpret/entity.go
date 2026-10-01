package interpret

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// EntityInterpreter is the generic per-entity-type interpret seam.
type EntityInterpreter func(result gather.GatherResult, clock Clock, cfg *config.Config) (Interpretation, error)

var entityInterpreters = map[string]EntityInterpreter{
	"pr":    InterpretPR,
	"issue": InterpretIssue,
}

// EntityInterpreters returns the registry: exactly "pr" and "issue".
func EntityInterpreters() map[string]EntityInterpreter { return entityInterpreters }

// InterpretPR decodes gather.Facts from the payload and runs Interpret
// unchanged. An empty payload is treated as empty Facts, carrying the
// result's metadata.
func InterpretPR(result gather.GatherResult, clock Clock, cfg *config.Config) (Interpretation, error) {
	var facts gather.Facts
	if len(result.Payload) > 0 {
		if err := json.Unmarshal(result.Payload, &facts); err != nil {
			return Interpretation{}, fmt.Errorf("interpret: decode pr facts: %w", err)
		}
	} else {
		facts = gather.Facts{AsOf: result.AsOf, Degraded: result.Degraded, RemovedState: result.RemovedState}
	}
	return Interpret(facts, clock, cfg)
}

// issueShowFields is the hand-decoded subset of the issue show payload.
type issueShowFields struct {
	ID        string `json:"id"`
	Owner     string `json:"owner"`
	Assignee  string `json:"assignee"`
	IssueType string `json:"issue_type"`
}

// InterpretIssue maps an issue gather result to an Interpretation: ownership
// from the owner/assignee against cfg.SelfIssueOwner, category echoing the
// issue type. Every other column stays zero.
func InterpretIssue(result gather.GatherResult, clock Clock, cfg *config.Config) (Interpretation, error) {
	now := clock.Now().UTC().Format(time.RFC3339)
	if len(result.Payload) == 0 {
		return Interpretation{Degraded: result.Degraded, AsOf: now}, nil
	}
	var facts gather.IssueFacts
	if err := json.Unmarshal(result.Payload, &facts); err != nil {
		return Interpretation{}, fmt.Errorf("interpret: decode issue facts: %w", err)
	}
	if len(facts.IssueShow) == 0 {
		return Interpretation{Degraded: result.Degraded, AsOf: now}, nil
	}
	var issue issueShowFields
	if err := json.Unmarshal(facts.IssueShow, &issue); err != nil {
		return Interpretation{}, fmt.Errorf("interpret: decode issue show: %w", err)
	}
	var self string
	if cfg != nil {
		self = cfg.SelfIssueOwner
	}
	return Interpretation{
		Ownership: string(classifyOwnership(self, issue.Owner, []string{issue.Assignee})),
		Category:  issue.IssueType,
		Degraded:  result.Degraded,
		AsOf:      now,
	}, nil
}
