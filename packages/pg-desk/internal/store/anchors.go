package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// AnchorIssueType is the issue type of a PR's anchor work item (the parent of
// its feedback, review, fix-ci and resolve-conflict items). It is the one
// property that tells an anchor from a child item in the stored issue facts.
const AnchorIssueType = "merge-request"

// PRAnchors is the open-anchor view of the PRs at one instant, derived from
// the store's links (the derived work-item links of the stored anchor issues)
// and the stored PR rows. It reads neither the tracker nor the daemon.
//
// "Open anchor" means an active issue entity of type AnchorIssueType whose
// stored state is not "closed" and that has a "work" link to a PR. Hidden PRs
// are counted apart (OpenHidden) and take no part in the other fields.
type PRAnchors struct {
	// Open is the number of distinct PRs, hidden ones excluded, that have at
	// least one open anchor.
	Open int
	// OpenHidden is the number of distinct hidden PRs that have at least one
	// open anchor.
	OpenHidden int
	// Stale is how many of the Open PRs the store shows as merged or closed
	// although an anchor is still open.
	Stale int
	// StaleOldestAgeSeconds is, among those, the longest time since the PR's
	// row was last hydrated (the store's closed state dates from that
	// hydration), 0 when none. A PR with no hydrated_at counts from its
	// first-seen time, then its as_of.
	StaleOldestAgeSeconds int
	// OldestHydrationAgeSeconds is the longest time since the last hydration
	// among the Open PRs the store still shows as open (the closed ones are
	// Stale's), 0 when none. It is the signal for a lost or failed `merged`
	// hydration, which leaves the store saying "open". A PR with no
	// hydrated_at counts from its first-seen time, then its as_of; a PR whose
	// row is absent from the store has no age and is skipped.
	OldestHydrationAgeSeconds int
}

// CountPRAnchors computes the open-anchor view at now.
//
// It reads schema-2 tables and columns, so it fails on an older store;
// callers check SchemaVersion first and treat an older store as "no data",
// never as an empty view. A read failure is returned, never folded into zero.
func (s *Store) CountPRAnchors(now time.Time) (PRAnchors, error) {
	rows, err := s.sql.Query(
		`SELECT x.to_id, a.facts,
		        COALESCE(p.facts, ''), p.entity_id IS NOT NULL,
		        COALESCE(p.hydrated_at, p.first_seen_at, p.as_of, ''),
		        COALESCE((SELECT h.value FROM annotation h
		                   WHERE h.repo = a.repo AND h.entity_type = 'pr'
		                     AND h.entity_id = x.to_id AND h.key = ?), '')
		   FROM entity a
		   JOIN xref x ON x.repo = a.repo AND x.from_type = 'issue'
		              AND x.from_id = a.entity_id AND x.to_type = 'pr'
		              AND x.relation = 'work'
		   LEFT JOIN entity p ON p.repo = a.repo AND p.entity_type = 'pr'
		                     AND p.entity_id = x.to_id
		  WHERE a.entity_type = 'issue' AND a.active = 1`,
		AnnotationHidden,
	)
	if err != nil {
		return PRAnchors{}, fmt.Errorf("store: count pr anchors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type prState struct {
		hidden, closed, present bool
		since                   string
	}
	prs := map[string]prState{}
	for rows.Next() {
		var prID, anchorFacts, prFacts, since, hidden string
		var present bool
		if err := rows.Scan(&prID, &anchorFacts, &prFacts, &present, &since, &hidden); err != nil {
			return PRAnchors{}, fmt.Errorf("store: count pr anchors: scan: %w", err)
		}
		if !isOpenAnchor(anchorFacts) {
			continue
		}
		prs[prID] = prState{
			hidden:  isHiddenAnnotation(hidden),
			closed:  present && factsSayClosed(prFacts),
			present: present,
			since:   since,
		}
	}
	if err := rows.Err(); err != nil {
		return PRAnchors{}, fmt.Errorf("store: count pr anchors: %w", err)
	}

	var out PRAnchors
	for _, p := range prs {
		if p.hidden {
			out.OpenHidden++
			continue
		}
		out.Open++
		if !p.present {
			continue
		}
		age := 0
		if t, perr := time.Parse(time.RFC3339, p.since); perr == nil {
			age = int(now.Sub(t).Seconds())
		}
		if p.closed {
			out.Stale++
			if age > out.StaleOldestAgeSeconds {
				out.StaleOldestAgeSeconds = age
			}
		} else if age > out.OldestHydrationAgeSeconds {
			out.OldestHydrationAgeSeconds = age
		}
	}
	return out, nil
}

// isOpenAnchor reports whether a stored issue's facts describe an anchor
// (type AnchorIssueType) that is not closed. Facts that are empty or
// undecodable describe nothing, so they are not an anchor.
func isOpenAnchor(facts string) bool {
	if facts == "" {
		return false
	}
	var f struct {
		IssueShow struct {
			State     string `json:"state"`
			IssueType string `json:"issue_type"`
		} `json:"issue_show"`
	}
	if json.Unmarshal([]byte(facts), &f) != nil {
		return false
	}
	return f.IssueShow.IssueType == AnchorIssueType && f.IssueShow.State != "closed"
}
