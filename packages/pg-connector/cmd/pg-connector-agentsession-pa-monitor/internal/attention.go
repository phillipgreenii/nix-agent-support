// attention.go: Backend's attention.Provider implementation — per-session
// blocked/long-idle escalations only. It deliberately emits NO account-level
// usage-cap item (5h block / 7-day week): Grafana already alerts on those
// (pa-monitor-5h-usage-limit-hit, pa-monitor-weekly-usage-limit-hit) and the
// alerts reach the same menu through pg-connector-alert-grafana, so an item
// here would show the same fact twice (INV-AGS-2).
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
	return items, nil
}
