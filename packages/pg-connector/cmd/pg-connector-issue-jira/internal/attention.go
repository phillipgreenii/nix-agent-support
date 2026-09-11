// attention.go: Backend's attention.Provider implementation (bead
// pg2-7wqkr, DESIGN DECISION operator/2026-09-11 via /unblock-human-beads:
// "pg-connector-issue-jira: deadline source is Jira's standard duedate
// field (query via pjira, per reference_jira_via_pjira memory). Same
// threshold + exclude-filter shape as beads").
//
// attention_threshold/attention_exclude mirror
// cmd/pg-connector-issue-beads/internal/attention.go's identical two
// recognized per-backend config keys, read the same way (directly via
// scriptout.ConfigFromContext — list_attention receives no wire args at
// all). attention_exclude's own grammar differs from the beads backend's
// plain --exclude-label value: here it is a raw JQL boolean fragment
// ANDed-out of the generated search, matching this backend's existing
// convention everywhere else (List/Search already pass caller-supplied
// JQL straight through with no parsing of their own).
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// defaultAttentionThreshold mirrors
// cmd/pg-connector-issue-beads/internal/attention.go's identical default —
// one day's notice when no attention_threshold is configured.
const defaultAttentionThreshold = 24 * time.Hour

// attentionConfig is the {"attention_threshold": "...", "attention_exclude":
// "..."} shape this backend's own list_attention op reads from its
// per-backend opaque config block. attention_threshold is a Go
// time.ParseDuration string (there is no d/w unit, only ns/us/ms/s/m/h —
// e.g. "72h" for three days).
type attentionConfig struct {
	Threshold string `json:"attention_threshold,omitempty"`
	Exclude   string `json:"attention_exclude,omitempty"`
}

// attentionThresholdFrom resolves config's own attention_threshold key,
// falling back to defaultAttentionThreshold when config is empty, fails to
// decode, or its value fails to parse as a Go duration — mirrors
// cmd/pg-connector-issue-beads/internal/attention.go's identical
// "malformed config means the default" convention.
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
// via two `pjira search --jql <JQL> --all` calls against Jira's own
// standard duedate field: one for the full "needs attention" set (due
// within the configured threshold from now, OR already overdue) and a
// second, narrower one scoped to today's date purely to classify Severity
// (high once genuinely overdue, medium while only approaching) — mirroring
// cmd/pg-connector-issue-beads/internal/attention.go's identical
// --due-before/--overdue split, so this backend never has to parse Jira's
// own duedate string itself either. Both queries AND in "duedate is not
// EMPTY AND resolution = Unresolved" (never surface a closed/resolved
// issue — matching the beads backend's identical "bd list already
// excludes closed by default" semantics) plus attention_exclude, if
// configured, ANDed-out via "AND NOT (...)".
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	config := scriptout.ConfigFromContext(ctx)
	threshold := attentionThresholdFrom(config)
	exclude := attentionExcludeFrom(config)
	now := time.Now().UTC()
	cutoff := now.Add(threshold).Format("2006-01-02")
	today := now.Format("2006-01-02")

	dueSoon, err := b.attentionSearch(ctx, fmt.Sprintf(`duedate <= "%s"`, cutoff), exclude)
	if err != nil {
		return nil, err
	}
	overdue, err := b.attentionSearch(ctx, fmt.Sprintf(`duedate < "%s"`, today), exclude)
	if err != nil {
		return nil, err
	}
	isOverdue := make(map[string]bool, len(overdue.Items))
	for _, item := range overdue.Items {
		isOverdue[item.Key] = true
	}

	items := make([]schema.AttentionItem, 0, len(dueSoon.Items))
	for _, item := range dueSoon.Items {
		var due string
		if item.Duedate != nil {
			due = *item.Duedate
		}
		severity := schema.SeverityMedium
		summary := fmt.Sprintf("%s: due %s", item.Summary, due)
		if isOverdue[item.Key] {
			severity = schema.SeverityHigh
			summary = fmt.Sprintf("%s: overdue (due %s)", item.Summary, due)
		}
		items = append(items, schema.AttentionItem{
			Type:     "issue",
			ID:       item.Key,
			Summary:  summary,
			Severity: severity,
		})
	}
	return items, nil
}

// attentionSearch runs one `pjira search --jql <JQL> --all` call for
// ListAttention, ANDing dueClause together with "duedate is not EMPTY AND
// resolution = Unresolved" and, when exclude is non-empty, "AND NOT
// (exclude)".
func (b *Backend) attentionSearch(ctx context.Context, dueClause, exclude string) (*pjiraSearchResult, error) {
	jql := dueClause + ` AND duedate is not EMPTY AND resolution = Unresolved`
	if exclude != "" {
		jql += fmt.Sprintf(` AND NOT (%s)`, exclude)
	}
	out, runErr := b.runner.Run(ctx, "search", "--jql", jql, "--all")
	if runErr != nil {
		return nil, classifyPJIRAErrorMessage(runErr.Error())
	}
	result, decodeErr := decodePJIRASearchResult(out)
	if decodeErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pjira: decode search result: "+decodeErr.Error())
	}
	return result, nil
}
