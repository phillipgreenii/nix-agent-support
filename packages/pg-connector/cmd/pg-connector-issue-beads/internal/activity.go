// activity.go: Backend implements pkg/provider/activity.Provider against bd.
// list_activity is range-shaped and stateless: every call reads the operator's
// identity from the host-configured activity_actors list, runs ONE bounded
// `bd list --json -n 0` call in the configured workspace, and returns the
// happenings the operator caused; nothing is stored.
//
// This file emits the once-per-entity kind issue.created. It happens at most
// once per bead, so its item id is "<bead id>#issue.created", identical on every
// call over any range.
//
// Attribution (operator-only): bd's workspace holds other people's beads too
// (agents and humans). Identity is the actor names configured for this host,
// the "activity_actors" key (a JSON list of strings) of this backend's opaque
// config block, matched against each bead's created_by. When the list is empty
// or missing the op answers unavailable naming activity_actors, before any bd
// call, never an unscoped result.
//
// Seam for further kinds: ListActivity validates the identity once and then
// calls each collector of the form
//
//	func (b *Backend) collect...(ctx, actors []string, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error)
//
// concatenating their items and ORing their truncated flags. A new kind family
// adds one collector and one call in ListActivity, and builds its items with
// newActivityItem.
//
// Documented item "fields" keys (an object, always present; the envelope is the
// contract, these keys are this backend's own):
//
//	issue.created: title, created_by, issue_type, status, priority
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ activity.Provider = (*Backend)(nil)

// KindIssueCreated is the activity kind for a bead's creation.
const KindIssueCreated = "issue.created"

// ActivityKinds is the vocabulary this backend contributes to capabilities
// vocabulary.activity_kinds.
var ActivityKinds = []string{KindIssueCreated}

// activityRangePadding widens the bounds handed to bd. bd documents its
// --created-after/--created-before flags only as "after"/"before" and does not
// say whether the bound is inclusive or day-granular, so each bound is moved
// one day OUTWARD (never tighter than the requested range) and the exact cut is
// made client-side on each bead's own created_at.
const activityRangePadding = 24 * time.Hour

// activityConfig is the {"activity_actors": [...]} shape this backend reads
// from its per-backend opaque config block.
type activityConfig struct {
	Actors []string `json:"activity_actors"`
}

// activityActorsFrom resolves config's activity_actors list, dropping blank
// entries. It returns nil when config is empty, undecodable or lists no actor.
func activityActorsFrom(config json.RawMessage) []string {
	if len(config) == 0 {
		return nil
	}
	var cfg activityConfig
	if err := scriptout.Decode(config, &cfg); err != nil {
		return nil
	}
	var actors []string
	for _, a := range cfg.Actors {
		if a = strings.TrimSpace(a); a != "" {
			actors = append(actors, a)
		}
	}
	return actors
}

// ListActivity implements activity.Provider. It reads the operator's identity
// (activity_actors) before any bd call, failing as unavailable when none is
// configured, and then runs each kind collector.
func (b *Backend) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	actors := activityActorsFrom(scriptout.ConfigFromContext(ctx))
	if len(actors) == 0 {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable,
			"pg-connector-issue-beads: list_activity needs the operator's identity: activity_actors is empty or missing in this backend's config")
	}

	items := []schema.ActivityItem{}
	truncated := false

	created, createdTrunc, err := b.collectCreated(ctx, actors, since, before)
	if err != nil {
		return nil, err
	}
	items = append(items, created...)
	truncated = truncated || createdTrunc

	return &schema.ActivityListResult{Items: items, Truncated: truncated}, nil
}

// collectCreated emits issue.created for every bead created by one of actors
// within [since, before). The bd query is bounded by --created-after/
// --created-before (widened, see activityRangePadding) and asks for all
// statuses, because a bead created in the range may since have been closed.
// -n 0 lifts bd's default 50-row cap, so the result is never cut short.
func (b *Backend) collectCreated(ctx context.Context, actors []string, since, before time.Time) (items []schema.ActivityItem, truncated bool, err error) {
	args := []string{"list", "--all"}
	if !since.IsZero() {
		args = append(args, "--created-after", since.Add(-activityRangePadding).UTC().Format(time.RFC3339))
	}
	args = append(args, "--created-before", before.Add(activityRangePadding).UTC().Format(time.RFC3339))
	args = append(args, "--json", "-n", "0")

	data, err := b.run(ctx, args...)
	if err != nil {
		return nil, false, err
	}
	issues, err := bdIssuesFromArray(data)
	if err != nil {
		return nil, false, err
	}

	isActor := make(map[string]bool, len(actors))
	for _, a := range actors {
		isActor[a] = true
	}
	workspace := b.activityWorkspaceLabel()
	seen := map[string]bool{}
	items = []schema.ActivityItem{}
	for _, iss := range issues {
		if iss.ID == "" || !isActor[iss.CreatedBy] || seen[iss.ID] {
			continue
		}
		// A bead whose created_at is missing or unparseable cannot be dated, so
		// it is not emitted (no silent substitute timestamp).
		created, perr := time.Parse(time.RFC3339, iss.CreatedAt)
		if perr != nil {
			continue
		}
		if !since.IsZero() && created.Before(since) {
			continue
		}
		if !created.Before(before) {
			continue
		}
		seen[iss.ID] = true
		item := newActivityItem(iss.ID, KindIssueCreated, iss.CreatedAt,
			fmt.Sprintf("Created %s: %s", iss.ID, iss.Title),
			activityLabels(workspace, iss.Labels),
			map[string]any{
				"title":      iss.Title,
				"created_by": iss.CreatedBy,
				"issue_type": iss.IssueType,
				"status":     iss.Status,
				"priority":   iss.Priority,
			})
		item.ID = iss.ID + "#" + KindIssueCreated
		items = append(items, item)
	}
	return items, false, nil
}

// activityWorkspaceLabel is the workspace:<name> label value: the base name of
// the configured workspace directory, empty when it cannot be resolved.
func (b *Backend) activityWorkspaceLabel() string {
	dir := b.tracker()
	if dir == "" {
		return ""
	}
	return filepath.Base(dir)
}

// activityLabels builds an item's labels: workspace:<name> (when known),
// tracker:beads, and each of the bead's own labels as bead-label:<l>.
func activityLabels(workspace string, beadLabels []string) []string {
	labels := []string{}
	if workspace != "" {
		labels = append(labels, "workspace:"+workspace)
	}
	labels = append(labels, "tracker:beads")
	for _, l := range beadLabels {
		labels = append(labels, "bead-label:"+l)
	}
	return labels
}

// newActivityItem builds an item for an issue entity. It does NOT build the id:
// the caller sets item.ID (once-per-entity kinds use "<entityID>#<kind>").
// fields is marshaled as a JSON object ({} when nil or not an object); as_of is
// the read time in RFC3339 UTC and stale is false (this backend keeps no
// cache).
func newActivityItem(entityID, kind, occurredAt, summary string, labels []string, fields any) schema.ActivityItem {
	raw := json.RawMessage("{}")
	if fields != nil {
		if b, err := json.Marshal(fields); err == nil && len(b) > 0 && b[0] == '{' {
			raw = b
		}
	}
	return schema.ActivityItem{
		Kind:       kind,
		EntityType: "issue",
		EntityID:   entityID,
		OccurredAt: occurredAt,
		Summary:    summary,
		Labels:     labels,
		Fields:     raw,
		AsOf:       time.Now().UTC().Format(time.RFC3339),
		Stale:      false,
	}
}
