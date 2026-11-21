package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	// OpenHiddenFinished is how many of the OpenHidden PRs the store shows as
	// merged or closed. The decider still runs the closure rule for a hidden
	// PR in that state, so a nonzero value means a finished PR's anchor was
	// not closed.
	OpenHiddenFinished int
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
func (s *Store) CountPRAnchors(ctx context.Context, now time.Time) (PRAnchors, error) {
	prs, err := s.loadAnchorPRs(ctx)
	if err != nil {
		return PRAnchors{}, err
	}

	var out PRAnchors
	for _, p := range prs {
		if p.hidden {
			out.OpenHidden++
			if p.present && p.closed {
				out.OpenHiddenFinished++
			}
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

// anchorPR is one PR with an open anchor, as the store shows it.
type anchorPR struct {
	hidden, closed, present bool
	// since is the PR row's last hydration, else its first-seen time, else
	// its as_of ("" when the row is absent).
	since string
	// attempt is the PR row's read_attempt_at ("" when never set or the column
	// is absent).
	attempt string
}

// loadAnchorPRs reads every PR that has at least one open anchor, keyed by PR
// id. CountPRAnchors and SweepPRAnchors both read it, so the gauge and the
// sweep see the same population.
func (s *Store) loadAnchorPRs(ctx context.Context) (map[string]anchorPR, error) {
	prFactsCol, prIndexedBy, err := s.prStateSource(ctx)
	if err != nil {
		return nil, err
	}
	have, err := s.tableColumns(ctx, "entity")
	if err != nil {
		return nil, err
	}
	attemptCol := `''`
	if have["read_attempt_at"] {
		attemptCol = `COALESCE(p.read_attempt_at, '')`
	}
	rows, err := s.sql.QueryContext(
		ctx,
		`SELECT x.to_id, a.facts,
		        COALESCE(p.`+prFactsCol+`, ''), p.entity_id IS NOT NULL,
		        COALESCE(p.hydrated_at, p.first_seen_at, p.as_of, ''),
		        COALESCE((SELECT h.value FROM annotation h
		                   WHERE h.repo = a.repo AND h.entity_type = 'pr'
		                     AND h.entity_id = x.to_id AND h.key = ?), ''),
		        `+attemptCol+`
		   FROM entity a
		   JOIN xref x ON x.repo = a.repo AND x.from_type = 'issue'
		              AND x.from_id = a.entity_id AND x.to_type = 'pr'
		              AND x.relation = 'work'
		   LEFT JOIN entity p `+prIndexedBy+` ON p.repo = a.repo AND p.entity_type = 'pr'
		                     AND p.entity_id = x.to_id
		  WHERE a.entity_type = 'issue' AND a.active = 1`,
		AnnotationHidden,
	)
	if err != nil {
		return nil, fmt.Errorf("store: count pr anchors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	prs := map[string]anchorPR{}
	for rows.Next() {
		var prID, anchorFacts, prFacts, since, hidden, attempt string
		var present bool
		if err := rows.Scan(&prID, &anchorFacts, &prFacts, &present, &since, &hidden, &attempt); err != nil {
			return nil, fmt.Errorf("store: count pr anchors: scan: %w", err)
		}
		if !isOpenAnchor(anchorFacts) {
			continue
		}
		prs[prID] = anchorPR{
			hidden:  isHiddenAnnotation(hidden),
			closed:  present && factsSayClosed(prFacts),
			present: present,
			since:   since,
			attempt: attempt,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: count pr anchors: %w", err)
	}
	return prs, nil
}

// SweepPRAnchors returns up to limit ids of the PRs the age sweep should
// re-hydrate at now: the PRs CountPRAnchors counts in OldestHydrationAgeSeconds
// (an open anchor, not hidden, a stored row that still says open) whose last
// hydration is older than maxAge, oldest first (ties by id). A PR whose
// read_attempt_at is newer than retryAfter ago is skipped, so a PR that keeps
// failing to hydrate does not hold the front of the queue; the caller writes
// the attempt time.
func (s *Store) SweepPRAnchors(ctx context.Context, now time.Time, maxAge, retryAfter time.Duration, limit int) ([]string, error) {
	prs, err := s.loadAnchorPRs(ctx)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id    string
		since time.Time
	}
	var due []cand
	for id, p := range prs {
		if p.hidden || !p.present || p.closed {
			continue
		}
		since, perr := time.Parse(time.RFC3339, p.since)
		if perr == nil && now.Sub(since) <= maxAge {
			continue
		}
		if p.attempt != "" {
			if at, aerr := time.Parse(time.RFC3339Nano, p.attempt); aerr == nil && now.Sub(at) < retryAfter {
				continue
			}
		}
		due = append(due, cand{id, since})
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].since.Equal(due[j].since) {
			return due[i].since.Before(due[j].since)
		}
		return due[i].id < due[j].id
	})
	if limit >= 0 && len(due) > limit {
		due = due[:limit]
	}
	ids := make([]string, len(due))
	for i, c := range due {
		ids[i] = c.id
	}
	return ids, nil
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
