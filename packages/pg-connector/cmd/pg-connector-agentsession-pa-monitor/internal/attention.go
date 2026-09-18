// attention.go: Backend's attention.Provider implementation — per-session
// blocked/long-idle escalations, plus an account-level item when the
// active 5h block or 7-day week usage cap has been hit.
package internal

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var _ attention.Provider = (*Backend)(nil)

// blockerSeverity maps a blocked session's blocker reason to its attention
// severity. human_input/human_authn need a person; usage_limit
// self-recovers at the reset time.
func blockerSeverity(blocker string) schema.Severity {
	switch blocker {
	case "human_input", "human_authn":
		return schema.SeverityHigh
	case "usage_limit":
		return schema.SeverityMedium
	default:
		return schema.SeverityMedium
	}
}

func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	raw, err := b.runner.Status(ctx)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc statusJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, classifyPaMonitorError(err)
	}

	var items []schema.AttentionItem
	for _, sj := range doc.Sessions {
		switch {
		case sj.Status == "blocked":
			items = append(items, schema.AttentionItem{
				Type: "agentsession", ID: sj.SessionID,
				Summary:  "session " + sj.SessionID + " is blocked (" + sj.Blocker + ")",
				Severity: blockerSeverity(sj.Blocker),
			})
		case sj.LongIdle:
			items = append(items, schema.AttentionItem{
				Type: "agentsession", ID: sj.SessionID,
				Summary:  "session " + sj.SessionID + " has been idle a long time",
				Severity: schema.SeverityLow,
			})
		}
	}
	if doc.ActiveBlock != nil && doc.ActiveBlock.CapHitAt != "" {
		items = append(items, schema.AttentionItem{
			Type: "agentsession-usage-limit", ID: doc.ActiveBlock.ID,
			Summary: "5-hour usage block cap has been hit", Severity: schema.SeverityCritical,
		})
	}
	if doc.ActiveWeek != nil && doc.ActiveWeek.CapHitAt != "" {
		items = append(items, schema.AttentionItem{
			Type: "agentsession-usage-limit", ID: doc.ActiveWeek.ID,
			Summary: "7-day usage week cap has been hit", Severity: schema.SeverityCritical,
		})
	}
	return items, nil
}
