package collector

import (
	"context"
	"fmt"
	"sort"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
)

// ProjectionFields are the semantic fields a sweep-origin find is judged on
// (never content_hash, never as_of/age_seconds/served_from).
var ProjectionFields = []string{
	"head_sha", "state", "updated_at", "comment_count", "review_count", "review_decision",
	"checks_rollup", "merge_state_status", "mergeable", "threads_total", "comments_total",
	"reviews_total", "draft", "label_count",
}

// ProjectionSQL reads the projection of every ACTIVE pr row.
const ProjectionSQL = `SELECT entity_id,
  json_extract(facts,'$.pr_show.head_sha') AS head_sha,
  json_extract(facts,'$.pr_show.state') AS state,
  json_extract(facts,'$.pr_show.updated_at') AS updated_at,
  json_extract(facts,'$.pr_show.comment_count') AS comment_count,
  json_extract(facts,'$.pr_show.review_count') AS review_count,
  json_extract(facts,'$.pr_show.review_decision') AS review_decision,
  json_extract(facts,'$.pr_show.checks_rollup') AS checks_rollup,
  json_extract(facts,'$.pr_show.merge_state_status') AS merge_state_status,
  json_extract(facts,'$.pr_show.mergeable') AS mergeable,
  json_extract(facts,'$.pr_show.connections.threads.total') AS threads_total,
  json_extract(facts,'$.pr_show.connections.comments.total') AS comments_total,
  json_extract(facts,'$.pr_show.connections.reviews.total') AS reviews_total,
  json_extract(facts,'$.pr_show.draft') AS draft,
  json_extract(facts,'$.pr_show.label_count') AS label_count
FROM entity WHERE entity_type='pr' AND active=1`

// Projection maps entity id to field to its rendered value.
type Projection map[string]map[string]string

// ReadProjection reads the current projection from the scratch store.
func ReadProjection(ctx context.Context, db sqlite.DB) (Projection, error) {
	rows, err := db.Query(ctx, ProjectionSQL)
	if err != nil {
		return nil, fmt.Errorf("projection: %w", err)
	}
	out := Projection{}
	for _, r := range rows {
		id := sqlite.Str(r["entity_id"])
		m := map[string]string{}
		for _, f := range ProjectionFields {
			v, ok := r[f]
			if !ok || v == nil {
				m[f] = ""
				continue
			}
			m[f] = fmt.Sprint(v)
		}
		out[id] = m
	}
	return out, nil
}

// NewEntityField is the pseudo field reported for an entity absent from the
// previous projection.
const NewEntityField = "new-entity"

// Diff returns, per entity, the projection fields whose value changed from
// prev to cur. An entity missing from prev reports NewEntityField only. A nil
// prev (no baseline) reports nothing.
func Diff(prev, cur Projection) map[string][]string {
	out := map[string][]string{}
	if prev == nil {
		return out
	}
	for id, c := range cur {
		p, ok := prev[id]
		if !ok {
			out[id] = []string{NewEntityField}
			continue
		}
		var f []string
		for _, name := range ProjectionFields {
			if p[name] != c[name] {
				f = append(f, name)
			}
		}
		if len(f) > 0 {
			sort.Strings(f)
			out[id] = f
		}
	}
	return out
}

// CursorSQL reads the shadow consumer's cursor (0 when unregistered).
func CursorSQL(consumer string) string {
	return fmt.Sprintf("SELECT COALESCE((SELECT cursor FROM consumer WHERE name=%s AND type='pr'),0)", sqlite.Quote(consumer))
}

// HydrationsSQL reads the persisted hydration counter.
const HydrationsSQL = "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='change_flow.hydrations.pr'),0)"

// RecoverItems rebuilds the items of an unfinished tick from the scratch
// change_log: the rows past the cursor recorded in its tick_start up to the
// cursor now.
func RecoverItems(ctx context.Context, db sqlite.DB, from, to int64) ([]schema.Item, error) {
	rows, err := db.Query(ctx, fmt.Sprintf(
		"SELECT seq, entity_id, kinds, origin, at FROM change_log WHERE entity_type='pr' AND seq>%d AND seq<=%d ORDER BY seq", from, to,
	))
	if err != nil {
		return nil, err
	}
	var items []schema.Item
	for _, r := range rows {
		items = append(items, schema.Item{
			EntityID: sqlite.Str(r["entity_id"]),
			Kinds:    parseKinds(sqlite.Str(r["kinds"])),
			Seq:      sqlite.Num(r["seq"]),
			Origin:   sqlite.Str(r["origin"]),
			At:       sqlite.Str(r["at"]),
		})
	}
	return items, nil
}
