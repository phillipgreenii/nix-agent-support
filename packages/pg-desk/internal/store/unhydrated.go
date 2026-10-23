package store

import (
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
func (s *Store) CountPRUnhydrated(now time.Time) (PRUnhydrated, error) {
	rows, err := s.sql.Query(
		`SELECT e.facts, COALESCE(e.first_seen_at, e.as_of),
		        COALESCE((SELECT a.value FROM annotation a
		                  WHERE a.repo = e.repo AND a.entity_type = e.entity_type
		                    AND a.entity_id = e.entity_id AND a.key = ?), '')
		   FROM entity e
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
