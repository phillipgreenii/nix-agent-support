// attention.go: Backend implements pkg/provider/attention.Provider against bd
// as a label-driven to-do list (bead pg2-wyeq4). list_attention reports the
// beads in this instance's own tracker that carry ANY label of a configured
// set, so an operator (or an agent) can flag a bead for attention by labelling
// it, and the umbrella's attention feed then surfaces it.
//
// This is a NEW op with its own semantics, not a revival of the retired
// deadline-based list_attention (pg2-w2oe5.1, which asked bd for overdue or
// soon-due beads and was deleted when pg-desk took over entity attention,
// ADR 0081). The operator ruled on 2026-10-09 (Phillip, bead pg2-wyeq4) that
// the beads backend, and only the beads backend, answers attention again,
// driven by labels. pg-desk's read-time evaluator is unchanged and still owns
// PR and Jira attention. The record is the "Amendment 2026-10-09" section of
// docs/adr/0081-entity-attention-evaluated-at-read-time-in-pg-desk.md.
//
// Configuration: the "attention_labels" key (a JSON list of strings) of this
// backend's opaque backends.<name> config block, per instance, rendered by the
// nix module from attention.perBackend.<name>.attentionLabels. When the list is
// empty or missing the op answers unavailable naming attention_labels, before
// any bd call; it NEVER falls back to an unscoped result.
//
// Which beads qualify (decisions recorded in INV-ATTN-BEADS-1):
//   - status open, in_progress or blocked. A label is an explicit request for
//     attention, so a bead bd marks blocked still counts; deferred is the
//     operator's own "not until later" and is excluded, as are closed, pinned
//     and hooked.
//   - at least one of the configured labels (bd's --label-any, re-checked
//     client-side so a bd that ignored the flag could never widen the list).
//
// Wire item (the existing AttentionItem, schema version unchanged): type
// "issue", id the bead id, severity mapped from bd priority (see
// severityForPriority), summary "<title> [P<n>; <matched labels>]", and no
// url (bd has no hosted page). The tracker is not carried: bead ids are unique
// per tracker prefix (pg2-*, zr-*), so {type, id} never clashes across the two
// instances, and the umbrella's own "via" names the instance that reported it.
// Order is bd priority ascending (most urgent first), then id.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ attention.Provider = (*Backend)(nil)

// attentionItemType is the AttentionItem.Type this backend reports; it matches
// the pg-desk issue entity type so a bead dedups with the same bead surfaced
// elsewhere.
const attentionItemType = "issue"

// attentionStatuses are the stored bd statuses that qualify (see the file
// comment). bd's --status takes this comma-separated form for OR.
var attentionStatuses = []string{"open", "in_progress", "blocked"}

// attentionConfig is the {"attention_labels": [...]} shape this backend reads
// from its per-backend opaque config block.
type attentionConfig struct {
	Labels []string `json:"attention_labels"`
}

// attentionLabelsFrom resolves config's attention_labels list, trimming
// entries and dropping blanks and duplicates (first occurrence wins, so the
// configured order is kept). It returns nil when config is empty, undecodable
// or lists no label.
func attentionLabelsFrom(config json.RawMessage) []string {
	if len(config) == 0 {
		return nil
	}
	var cfg attentionConfig
	if err := scriptout.Decode(config, &cfg); err != nil {
		return nil
	}
	var labels []string
	seen := map[string]bool{}
	for _, l := range cfg.Labels {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			labels = append(labels, l)
		}
	}
	return labels
}

// severityForPriority maps bd's priority (0 = highest) onto the attention
// severity enum: P0 critical, P1 high, P2 medium, P3 and P4 low. An
// out-of-range priority (bd accepts only 0-4) is treated as low.
func severityForPriority(p int) schema.Severity {
	switch p {
	case 0:
		return schema.SeverityCritical
	case 1:
		return schema.SeverityHigh
	case 2:
		return schema.SeverityMedium
	default:
		return schema.SeverityLow
	}
}

// ListAttention implements attention.Provider. It reads the configured labels
// before any bd call, then runs ONE bounded `bd list` for them.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	labels := attentionLabelsFrom(scriptout.ConfigFromContext(ctx))
	if len(labels) == 0 {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable,
			"pg-connector-issue-beads: list_attention needs the labels that mark a bead for attention: attention_labels is empty or missing in this backend's config")
	}
	joined, err := joinBDLabels(labels)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "attention_labels: "+err.Error())
	}
	// -n 0 lifts bd's default 50-row cap, so the list is never cut short.
	// --label-any/--status use the = form so a label that begins with "-" can
	// never be read as a flag.
	data, err := b.run(ctx, "list", "--readonly", "--json",
		"--label-any="+joined, "--status="+strings.Join(attentionStatuses, ","), "-n", "0")
	if err != nil {
		return nil, err
	}
	issues, err := bdIssuesFromArray(data)
	if err != nil {
		return nil, err
	}

	wantStatus := map[string]bool{}
	for _, s := range attentionStatuses {
		wantStatus[s] = true
	}
	type entry struct {
		item     schema.AttentionItem
		priority int
	}
	var entries []entry
	seen := map[string]bool{}
	for _, iss := range issues {
		if iss.ID == "" || seen[iss.ID] || !wantStatus[iss.Status] {
			continue
		}
		matched := matchedLabels(labels, iss.Labels)
		if len(matched) == 0 {
			continue
		}
		seen[iss.ID] = true
		entries = append(entries, entry{
			priority: iss.Priority,
			item: schema.AttentionItem{
				Type:     attentionItemType,
				ID:       iss.ID,
				Summary:  fmt.Sprintf("%s [%s; %s]", iss.Title, formatPriority(iss.Priority), strings.Join(matched, ", ")),
				Severity: severityForPriority(iss.Priority),
			},
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		return entries[i].item.ID < entries[j].item.ID
	})
	items := make([]schema.AttentionItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, e.item)
	}
	return items, nil
}

// matchedLabels returns the configured labels (in configured order) that the
// bead carries.
func matchedLabels(configured, beadLabels []string) []string {
	has := make(map[string]bool, len(beadLabels))
	for _, l := range beadLabels {
		has[l] = true
	}
	var matched []string
	for _, l := range configured {
		if has[l] {
			matched = append(matched, l)
		}
	}
	return matched
}
