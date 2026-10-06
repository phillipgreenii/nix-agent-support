// activity.go: Backend implements pkg/provider/activity.Provider against bd.
// list_activity is range-shaped and stateless: every call reads the operator's
// identity from the host-configured activity_actors list, runs ONE bounded
// `bd list --json -n 0` call in the configured workspace, and returns the
// happenings the operator caused; nothing is stored.
//
// This file emits the once-per-entity kind issue.created, whose item id is
// "<bead id>#issue.created" (identical on every call over any range), and the
// repeatable kinds issue.started and issue.closed, whose ids are
// "<bead id>#<kind>#<occurred_at>" (occurred_at RFC3339 in UTC) because a bead
// can start or close again after a reopen. The whole backend makes three
// bounded `bd list --json -n 0` calls per ListActivity (created, closed,
// in-progress); the closed result is fetched once and shared by the
// issue.closed and issue.started collectors. Comments are not emitted in v1.
//
// Attribution (operator-only): bd's workspace holds other people's beads too
// (agents and humans). Identity is the actor names configured for this host,
// the "activity_actors" key (a JSON list of strings) of this backend's opaque
// config block. issue.created is matched against each bead's created_by;
// issue.started and issue.closed against the bead's assignee or owner (bd
// exposes no per-event actor and no closed_by, so these attribute by the
// assignee at pull time, stated in fields.attribution: "assignee"). When the
// list is empty or missing the op answers unavailable naming activity_actors,
// before any bd call, never an unscoped result.
//
// Known limitation of issue.started: bd has no started-after filter, so the
// rule is "in-progress beads plus the beads closed in the (widened) range,
// filtered by started_at". A bead started in the range that is no longer
// in_progress and was not closed in the range (closed AFTER the range, which
// is the common case on a past-range pull or a backfill; or blocked, deferred
// or reopened to open) is NOT found, and its issue.started is not emitted.
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
//	issue.started: title, assignee, owner, issue_type, status, priority, attribution ("assignee")
//	issue.closed:  title, assignee, owner, issue_type, status, priority, attribution ("assignee")
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

// Activity kinds this backend emits.
const (
	// KindIssueCreated is the activity kind for a bead's creation.
	KindIssueCreated = "issue.created"
	// KindIssueStarted is the activity kind for a bead entering in-progress.
	KindIssueStarted = "issue.started"
	// KindIssueClosed is the activity kind for a bead being closed.
	KindIssueClosed = "issue.closed"
)

// ActivityKinds is the vocabulary this backend contributes to capabilities
// vocabulary.activity_kinds.
var ActivityKinds = []string{KindIssueCreated, KindIssueStarted, KindIssueClosed}

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

	// The closed-range result is fetched once and shared by the issue.closed
	// and issue.started collectors, keeping the backend at three bd list calls.
	closedRows, err := b.fetchClosedRange(ctx, since, before)
	if err != nil {
		return nil, err
	}

	closed, closedTrunc, err := b.collectClosed(ctx, actors, since, before, closedRows)
	if err != nil {
		return nil, err
	}
	items = append(items, closed...)
	truncated = truncated || closedTrunc

	started, startedTrunc, err := b.collectStarted(ctx, actors, since, before, closedRows)
	if err != nil {
		return nil, err
	}
	items = append(items, started...)
	truncated = truncated || startedTrunc

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

// fetchClosedRange runs the one closed-range bd call: every bead (all
// statuses) whose closed_at falls within the widened range. -n 0 lifts bd's
// default 50-row cap.
func (b *Backend) fetchClosedRange(ctx context.Context, since, before time.Time) ([]bdIssue, error) {
	args := []string{"list", "--all"}
	if !since.IsZero() {
		args = append(args, "--closed-after", since.Add(-activityRangePadding).UTC().Format(time.RFC3339))
	}
	args = append(args, "--closed-before", before.Add(activityRangePadding).UTC().Format(time.RFC3339))
	args = append(args, "--json", "-n", "0")
	data, err := b.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return bdIssuesFromArray(data)
}

// actorSet indexes actors for membership tests.
func actorSet(actors []string) map[string]bool {
	set := make(map[string]bool, len(actors))
	for _, a := range actors {
		set[a] = true
	}
	return set
}

// inActivityRange parses ts (RFC3339) and reports whether it lies within
// [since, before): since inclusive (a zero since is unbounded), before
// exclusive. A missing or unparseable ts is never in range, so a happening
// that cannot be dated is not emitted and no other timestamp is substituted.
func inActivityRange(ts string, since, before time.Time) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, false
	}
	if !since.IsZero() && t.Before(since) {
		return time.Time{}, false
	}
	if !t.Before(before) {
		return time.Time{}, false
	}
	return t, true
}

// assigneeOrOwnerIn reports whether the bead's assignee or owner is one of
// the configured actors.
func assigneeOrOwnerIn(iss bdIssue, isActor map[string]bool) bool {
	return (iss.Assignee != "" && isActor[iss.Assignee]) || (iss.Owner != "" && isActor[iss.Owner])
}

// collectClosed emits issue.closed for every bead in rows (the shared
// closed-range result) whose assignee or owner is in actors and whose own
// closed_at is within [since, before).
func (b *Backend) collectClosed(_ context.Context, actors []string, since, before time.Time, rows []bdIssue) (items []schema.ActivityItem, truncated bool, err error) {
	items = b.transitionItems(KindIssueClosed, "Closed", actors, since, before, rows,
		func(iss bdIssue) string { return iss.ClosedAt })
	return items, false, nil
}

// collectStarted emits issue.started for beads whose own started_at is within
// [since, before) and whose assignee or owner is in actors. bd has no
// started-after filter, so the candidates are the in-progress beads (one bd
// call, made here) plus closedRows, the shared closed-range result. See the
// package comment for the known limitation this implies.
func (b *Backend) collectStarted(ctx context.Context, actors []string, since, before time.Time, closedRows []bdIssue) (items []schema.ActivityItem, truncated bool, err error) {
	data, err := b.run(ctx, "list", "--status", "in_progress", "--json", "-n", "0")
	if err != nil {
		return nil, false, err
	}
	inProgress, err := bdIssuesFromArray(data)
	if err != nil {
		return nil, false, err
	}
	rows := make([]bdIssue, 0, len(inProgress)+len(closedRows))
	rows = append(rows, inProgress...)
	rows = append(rows, closedRows...)
	items = b.transitionItems(KindIssueStarted, "Started", actors, since, before, rows,
		func(iss bdIssue) string { return iss.StartedAt })
	return items, false, nil
}

// transitionItems builds the issue.started / issue.closed items from rows.
// timestamp selects the bead's own relevant timestamp. A bead is emitted once
// per distinct occurred_at (so duplicate rows, such as a bead present in both
// the in-progress and the closed result, collapse to one item), and once per
// happening even when both assignee and owner are configured actors.
func (b *Backend) transitionItems(kind, verb string, actors []string, since, before time.Time, rows []bdIssue, timestamp func(bdIssue) string) []schema.ActivityItem {
	isActor := actorSet(actors)
	workspace := b.activityWorkspaceLabel()
	seen := map[string]bool{}
	items := []schema.ActivityItem{}
	for _, iss := range rows {
		if iss.ID == "" || !assigneeOrOwnerIn(iss, isActor) {
			continue
		}
		at, ok := inActivityRange(timestamp(iss), since, before)
		if !ok {
			continue
		}
		occurredAt := at.UTC().Format(time.RFC3339Nano)
		id := iss.ID + "#" + kind + "#" + occurredAt
		if seen[id] {
			continue
		}
		seen[id] = true
		item := newActivityItem(iss.ID, kind, occurredAt,
			fmt.Sprintf("%s %s: %s", verb, iss.ID, iss.Title),
			activityLabels(workspace, iss.Labels),
			map[string]any{
				"title":       iss.Title,
				"assignee":    iss.Assignee,
				"owner":       iss.Owner,
				"issue_type":  iss.IssueType,
				"status":      iss.Status,
				"priority":    iss.Priority,
				"attribution": "assignee",
			})
		item.ID = id
		items = append(items, item)
	}
	return items
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
