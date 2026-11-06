package ccpool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/pg-router-handlers/internal/usage"
)

// consumptionJSON is `ccpool consumption <external_id> --json`: token totals
// and an estimated cost in integer USD cents at API list prices. ccpool owns
// the price table; this module only reads the result.
type consumptionJSON struct {
	Model              string `json:"model"`
	InputTokens        int    `json:"input_tokens"`
	OutputTokens       int    `json:"output_tokens"`
	CacheCreationToken int    `json:"cache_creation_tokens"`
	CacheReadTokens    int    `json:"cache_read_tokens"`
	EstimatedCostCents int64  `json:"estimated_cost_cents"`
}

// Consumption runs `ccpool consumption <externalID> --json` and returns the
// session's cumulative consumption (usage.Source).
func (c *CLIRunner) Consumption(ctx context.Context, externalID string) (usage.Snapshot, error) {
	out, err := c.ccpool(ctx, quickCallTimeout, "consumption", externalID, "--json")
	if err != nil {
		return usage.Snapshot{}, err
	}
	var r consumptionJSON
	if err := json.Unmarshal(out, &r); err != nil {
		return usage.Snapshot{}, fmt.Errorf("ccpool consumption --json decode: %w", err)
	}
	return usage.Snapshot{
		Model: r.Model, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		CacheCreationTokens: r.CacheCreationToken, CacheReadTokens: r.CacheReadTokens,
		CostCents: r.EstimatedCostCents,
	}, nil
}
