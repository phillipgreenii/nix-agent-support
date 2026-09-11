// attention.go: Backend's attention.Provider implementation (bead
// pg2-7wqkr, DESIGN DECISION operator/2026-09-11 via /unblock-human-beads:
// "pg-connector-issue-beads: deadline source is bd's --due field.
// list_attention returns issues within/past the configured threshold of
// their due date, honoring the per-backend exclude filter if
// configured").
//
// attention_threshold/attention_exclude are this backend's own two
// recognized keys inside its opaque per-backend config block (registered
// under backends.<this-binary> in the shared config file --
// home/programs/pg-connector's new attention.perBackend option renders
// them there), read directly via scriptout.ConfigFromContext the same way
// provider.go's rateReservePoints already reads pg-connector-pr-github's
// own rate_reserve_points key — list_attention receives no wire args at
// all (pkg/provider/attention.NewDispatchTable's own "list_attention"
// entry passes p.ListAttention(ctx) with no decode step), so there is no
// other channel for this backend's own semantics config to arrive.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// defaultAttentionThreshold is this backend's own built-in "how close to
// (or how far past) its due date" window applied when no
// attention_threshold is configured — one day's notice.
const defaultAttentionThreshold = 24 * time.Hour

// attentionConfig is the {"attention_threshold": "...", "attention_exclude":
// "..."} shape this backend's own list_attention op reads from its
// per-backend opaque config block. attention_threshold is a Go
// time.ParseDuration string (there is no d/w unit, only ns/us/ms/s/m/h —
// e.g. "72h" for three days); attention_exclude is passed straight through
// as bd's own --exclude-label flag VALUE (a single string — bd's own
// pflag StringSlice decodes a CSV-quoted multi-label value itself, see
// bd.go's joinBDLabels doc comment, so this backend does no parsing of
// its own).
type attentionConfig struct {
	Threshold string `json:"attention_threshold,omitempty"`
	Exclude   string `json:"attention_exclude,omitempty"`
}

// attentionThresholdFrom resolves config's own attention_threshold key,
// falling back to defaultAttentionThreshold when config is empty, fails to
// decode, or its value fails to parse as a Go duration — mirrors
// cmd/pg-connector-pr-github/internal/provider.go's rateReservePoints'
// identical "malformed config means the default" convention.
func attentionThresholdFrom(config json.RawMessage) time.Duration {
	if len(config) == 0 {
		return defaultAttentionThreshold
	}
	var cfg attentionConfig
	if err := scriptout.Decode(config, &cfg); err != nil || cfg.Threshold == "" {
		return defaultAttentionThreshold
	}
	d, err := time.ParseDuration(cfg.Threshold)
	if err != nil {
		return defaultAttentionThreshold
	}
	return d
}

// attentionExcludeFrom resolves config's own attention_exclude key, empty
// when config is empty, fails to decode, or the key is simply unset.
func attentionExcludeFrom(config json.RawMessage) string {
	if len(config) == 0 {
		return ""
	}
	var cfg attentionConfig
	if err := scriptout.Decode(config, &cfg); err != nil {
		return ""
	}
	return cfg.Exclude
}

// ListAttention implements the attention capability's attention.Provider
// via bd's own native due-date filters: `bd list --due-before <cutoff>
// --json --limit 0` for the full "needs attention" set (due within the
// configured threshold from now, OR already overdue — --due-before's own
// cutoff, computed here as now+threshold, matches both at once since an
// overdue due_at is always before a future cutoff too), and a second `bd
// list --overdue --json --limit 0` call purely to classify Severity
// (high once genuinely overdue, medium while only approaching) without
// this backend ever having to parse bd's own due_at string itself. Both
// calls carry attention_exclude (if configured) as --exclude-label's flag
// value. bd list already excludes closed issues by default (no --all
// flag), matching the design's own "needing attention" semantics — a
// closed issue past its due date is not something to raise attention on.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	config := scriptout.ConfigFromContext(ctx)
	threshold := attentionThresholdFrom(config)
	exclude := attentionExcludeFrom(config)
	cutoff := time.Now().UTC().Add(threshold).Format("2006-01-02")

	dueSoon, err := b.attentionQuery(ctx, exclude, "--due-before", cutoff)
	if err != nil {
		return nil, err
	}
	overdue, err := b.attentionQuery(ctx, exclude, "--overdue")
	if err != nil {
		return nil, err
	}
	isOverdue := make(map[string]bool, len(overdue))
	for _, iss := range overdue {
		isOverdue[iss.ID] = true
	}

	items := make([]schema.AttentionItem, 0, len(dueSoon))
	for _, iss := range dueSoon {
		severity := schema.SeverityMedium
		summary := fmt.Sprintf("%s: due %s", iss.Title, iss.DueAt)
		if isOverdue[iss.ID] {
			severity = schema.SeverityHigh
			summary = fmt.Sprintf("%s: overdue (due %s)", iss.Title, iss.DueAt)
		}
		items = append(items, schema.AttentionItem{
			Type:     "issue",
			ID:       iss.ID,
			Summary:  summary,
			Severity: severity,
		})
	}
	return items, nil
}

// attentionQuery runs `bd list <extraArgs...> --json --limit 0
// [--exclude-label <exclude>]` and decodes the matched issue set — the
// shared plumbing behind ListAttention's two calls (the due-before set and
// the overdue subset).
func (b *Backend) attentionQuery(ctx context.Context, exclude string, extraArgs ...string) ([]bdIssue, error) {
	args := append([]string{"list"}, extraArgs...)
	args = append(args, "--json", "--limit", "0")
	if exclude != "" {
		args = append(args, "--exclude-label", exclude)
	}
	data, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return bdIssuesFromArray(data)
}
