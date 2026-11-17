package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PRUnhydrated is the never-hydrated set of the PR lane: the PRs the store
// lists as open that no hydration has written yet.
type PRUnhydrated struct {
	// Count is how many PRs are in the set.
	Count int
	// OldestAgeSeconds is the age, in seconds, of the oldest member (0 when
	// the set is empty or no member has a parseable first-seen time). A
	// member's age runs from when pg-desk first saw it (first_seen_at, or as_of
	// when that is empty).
	OldestAgeSeconds int
}

// CountPRUnhydrated computes the never-hydrated set at now: pr entities that
// are active (not marked inactive), at version 0 (no hydration has ever
// written the row: a migrated entity not yet re-hydrated), not hidden, and not
// known to be closed or merged. A PR whose stored facts carry no state counts
// as open: nothing says otherwise and the listing still holds it.
//
// It reads schema-2 columns, so it fails on an older store; callers check
// SchemaVersion first and treat an older store as "no data", never as an empty
// set. A read failure is returned, never folded into a zero count.
func (s *Store) CountPRUnhydrated(ctx context.Context, now time.Time) (PRUnhydrated, error) {
	factsCol, indexedBy, err := s.prStateSource(ctx)
	if err != nil {
		return PRUnhydrated{}, err
	}
	rows, err := s.sql.QueryContext(
		ctx,
		`SELECT e.`+factsCol+`, COALESCE(e.first_seen_at, e.as_of),
		        COALESCE((SELECT a.value FROM annotation a
		                  WHERE a.repo = e.repo AND a.entity_type = e.entity_type
		                    AND a.entity_id = e.entity_id AND a.key = ?), '')
		   FROM entity e `+indexedBy+`
		  WHERE e.entity_type = 'pr' AND e.active = 1 AND e.version = 0`,
		AnnotationHidden,
	)
	if err != nil {
		return PRUnhydrated{}, fmt.Errorf("store: count unhydrated prs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out PRUnhydrated
	for rows.Next() {
		var facts, since, hidden string
		if err := rows.Scan(&facts, &since, &hidden); err != nil {
			return PRUnhydrated{}, fmt.Errorf("store: count unhydrated prs: scan: %w", err)
		}
		if isHiddenAnnotation(hidden) || factsSayClosed(facts) {
			continue
		}
		out.Count++
		if t, perr := time.Parse(time.RFC3339, since); perr == nil {
			if age := int(now.Sub(t).Seconds()); age > out.OldestAgeSeconds {
				out.OldestAgeSeconds = age
			}
		}
	}
	if err := rows.Err(); err != nil {
		return PRUnhydrated{}, fmt.Errorf("store: count unhydrated prs: %w", err)
	}
	return out, nil
}

// prStateSource names where a reader that only needs a PR's open/closed state
// (factsSayClosed reads pr_show.state and pr_show.merged) takes its facts: the
// pruned focus_facts column through the covering index entity_focus, which
// carries those two members and never touches the facts overflow pages (about
// 117 KB per PR on the live store), or, on a store opened without that column
// or index (read-only or raw, predating it), the full facts column. The pruned
// document decodes with factsSayClosed unchanged. The returned indexedBy is the
// INDEXED BY clause for the entity table (empty on the fallback).
func (s *Store) prStateSource(ctx context.Context) (factsCol, indexedBy string, err error) {
	have, err := s.tableColumns(ctx, "entity")
	if err != nil {
		return "", "", err
	}
	var indexed int
	if err := s.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('entity') WHERE name = 'entity_focus'`).Scan(&indexed); err != nil {
		return "", "", fmt.Errorf("store: read entity indexes: %w", err)
	}
	if !have["focus_facts"] || indexed == 0 {
		return "facts", "", nil
	}
	return "focus_facts", "INDEXED BY entity_focus", nil
}

// isHiddenAnnotation reads the hidden annotation value ({"value": bool,
// "reason": ...}); an absent or undecodable value is not hidden.
func isHiddenAnnotation(raw string) bool {
	if raw == "" {
		return false
	}
	var h struct {
		Value bool `json:"value"`
	}
	return json.Unmarshal([]byte(raw), &h) == nil && h.Value
}

// factsSayClosed reports whether a PR's stored facts say it is closed or
// merged. Facts that are empty, undecodable or carry no state say nothing.
func factsSayClosed(facts string) bool {
	if facts == "" {
		return false
	}
	var f struct {
		PRShow struct {
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		} `json:"pr_show"`
	}
	if json.Unmarshal([]byte(facts), &f) != nil {
		return false
	}
	return f.PRShow.Merged || f.PRShow.State == "closed" || f.PRShow.State == "merged"
}

// FinishedPRIDs returns the entity ids of the stored PRs that are finished:
// their stored facts say closed or merged, or they are marked inactive. The
// PR lane never runs for such a PR again, so state it recorded earlier can no
// longer change and must not be read as live. A PR with no row is not in the
// set. It reads schema-2 columns, so callers check SchemaVersion first.
func (s *Store) FinishedPRIDs(ctx context.Context) (map[string]bool, error) {
	factsCol, indexedBy, err := s.prStateSource(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.QueryContext(
		ctx,
		`SELECT entity_id, `+factsCol+`, active FROM entity `+indexedBy+` WHERE entity_type = 'pr'`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: finished prs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id, facts string
		var active int
		if err := rows.Scan(&id, &facts, &active); err != nil {
			return nil, fmt.Errorf("store: finished prs: scan: %w", err)
		}
		if active == 0 || factsSayClosed(facts) {
			out[id] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: finished prs: %w", err)
	}
	return out, nil
}
