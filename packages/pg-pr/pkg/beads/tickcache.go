// Package beads — TickCache: per-sync-tick bulk-fetched lookup tables.
//
// The sync engine previously made one bd call per PR for each of:
//   - FindOpenProcessingCycle
//   - ListFeedback
//   - FindFeedbackByFingerprint
//   - ListChildrenOfPR
//
// Across 20+ PRs that's hundreds of bd shell-outs per tick, plus the dolt
// round-trip cost. Most of those calls re-read the same workspace-wide
// data — there is only one set of open processing-cycle beads in a
// workspace per tick. TickCache fetches that set once at tick start and
// answers per-PR lookups from memory.
//
// The cache is read-only and short-lived (one sync tick). Writes
// (CreateFeedback, CreateProcessingCycle, etc.) bypass the cache and run
// against bd; callers must not mutate cached values.
package beads

import (
	"context"
	"strings"
)

// TickCache holds the bulk-fetched bd state for one sync tick.
//
// A nil *TickCache is safe to pass to lookup helpers; they fall back to
// reporting "not present" and the caller's existing live-call path runs.
type TickCache struct {
	// HumanLabeled is the set of bead IDs carrying the `human` label.
	// Populated from one `bd query "label=human" --json`.
	HumanLabeled map[string]bool

	// MergeRequestsByID indexes every merge-request bead (open + closed)
	// by ID. Populated from `bd list --type=merge-request --json
	// --limit=0 --all`.
	MergeRequestsByID map[string]MergeRequest

	// OpenProcessingByPR maps a merge-request bead ID to its currently
	// open processing-cycle bead ID, when one exists. Computed by listing
	// all open `process-feedback:` tasks and walking each cycle's parent
	// edges to its merge-request anchor.
	OpenProcessingByPR map[string]string

	// FeedbackByCycle maps a processing-cycle bead ID to all feedback
	// beads (open + closed) attached to it via parent-child edges.
	// Populated from `bd list --type=feedback --json --limit=0 --all`
	// combined with each cycle's `bd dep list <id> --direction=up`.
	FeedbackByCycle map[string][]Feedback

	// ChildrenByID maps a bead ID to the IDs of beads that depend on it
	// (its children in dep-list-up direction). Built only for the
	// processing-cycles and PR beads the cache walks, so a miss here
	// just means the caller should hit bd directly.
	ChildrenByID map[string][]string
}

// LoadTickCache builds the workspace-wide cache used by the sync engine
// for one tick. Best-effort: a single bd failure for one bulk read
// returns a partial cache rather than nil, so the engine can still make
// progress (per-PR live calls cover the missing pieces).
func (c *Client) LoadTickCache(ctx context.Context) *TickCache {
	cache := &TickCache{
		HumanLabeled:       map[string]bool{},
		MergeRequestsByID:  map[string]MergeRequest{},
		OpenProcessingByPR: map[string]string{},
		FeedbackByCycle:    map[string][]Feedback{},
		ChildrenByID:       map[string][]string{},
	}

	if set, err := c.HumanLabeledBeads(ctx); err == nil {
		cache.HumanLabeled = set
	}

	mrs, _ := c.ListMergeRequests(ctx, true /* includeClosed */)
	for _, mr := range mrs {
		cache.MergeRequestsByID[mr.ID] = mr
	}

	feedback, _ := c.ListFeedback(ctx, "" /* all cycles */, true /* includeClosed */)
	feedbackByID := make(map[string]Feedback, len(feedback))
	for _, fb := range feedback {
		feedbackByID[fb.ID] = fb
	}

	cycles, _ := c.listOpenProcessingCycles(ctx)
	for _, cycle := range cycles {
		// Walk this cycle's parents to find its merge-request anchor.
		parentOut, err := c.Runner.Run(ctx, "dep", "list", cycle.ID, "--json")
		if err == nil {
			for _, parentID := range extractIDs(parentOut) {
				if _, isMR := cache.MergeRequestsByID[parentID]; isMR {
					cache.OpenProcessingByPR[parentID] = cycle.ID
					break
				}
			}
		}

		// Walk this cycle's children to group feedback under it. The
		// children list also feeds ChildrenByID so the dedup walker in
		// findFeedbackForPR doesn't need its own bd call.
		childOut, err := c.Runner.Run(ctx, "dep", "list", cycle.ID, "--direction=up", "--json")
		if err == nil {
			children := extractIDs(childOut)
			cache.ChildrenByID[cycle.ID] = children
			for _, childID := range children {
				if fb, ok := feedbackByID[childID]; ok {
					cache.FeedbackByCycle[cycle.ID] = append(cache.FeedbackByCycle[cycle.ID], fb)
				}
			}
		}
	}

	return cache
}

// processingCycleCandidate is a parsed subset of bd's task-list JSON
// used to identify processing-cycle beads.
type processingCycleCandidate struct {
	ID    string
	Title string
}

// listOpenProcessingCycles returns the open task beads whose title carries
// the canonical processing-cycle prefix.
func (c *Client) listOpenProcessingCycles(ctx context.Context) ([]processingCycleCandidate, error) {
	out, err := c.Runner.Run(ctx,
		"list",
		"--type=task",
		"--status=open",
		"--json",
		"--limit=0",
	)
	if err != nil {
		return nil, err
	}
	issues, err := parseBDList(out)
	if err != nil {
		return nil, err
	}
	cycles := make([]processingCycleCandidate, 0, len(issues))
	for _, iss := range issues {
		if strings.HasPrefix(iss.Title, processingCycleTitlePrefix) {
			cycles = append(cycles, processingCycleCandidate{ID: iss.ID, Title: iss.Title})
		}
	}
	return cycles, nil
}

// OpenCycleFor returns the cycle ID cached for the given PR bead, plus
// whether the cache holds an entry for it. A nil receiver returns
// ("", false) — the caller should fall back to a live bd call.
func (cache *TickCache) OpenCycleFor(prBeadID string) (string, bool) {
	if cache == nil {
		return "", false
	}
	id, ok := cache.OpenProcessingByPR[prBeadID]
	return id, ok
}

// FeedbackUnder returns the feedback beads cached for the given cycle
// (open + closed). A nil receiver returns nil — the caller should fall
// back to a live ListFeedback call.
func (cache *TickCache) FeedbackUnder(cycleID string) []Feedback {
	if cache == nil {
		return nil
	}
	return cache.FeedbackByCycle[cycleID]
}

// FindMergeRequest returns the cached merge-request bead matching
// (repo, prNumber). ok=false means no cache or no match. The lookup is
// a linear scan over MergeRequestsByID — workspaces hold tens to low
// hundreds of merge-request beads, so this is cheap.
func (cache *TickCache) FindMergeRequest(repo string, prNumber int) (MergeRequest, bool) {
	if cache == nil {
		return MergeRequest{}, false
	}
	for _, mr := range cache.MergeRequestsByID {
		if mr.Fields.Repo == repo && mr.Fields.PRNumber == prNumber {
			return mr, true
		}
	}
	return MergeRequest{}, false
}

// FindFeedbackForPR walks the PR's cached cycles and returns the
// feedback bead whose Fingerprint matches. ok=false means either no
// cache (caller should fall back) or the cache holds no match.
//
// The fingerprint walk includes closed feedback — bd never re-creates a
// resolved feedback for the same fingerprint, so the sync engine wants
// to dedup against the full history.
func (cache *TickCache) FindFeedbackForPR(prBeadID, fingerprint string) (fb Feedback, ok bool) {
	if cache == nil || fingerprint == "" {
		return Feedback{}, false
	}
	cycleID, hasCycle := cache.OpenProcessingByPR[prBeadID]
	if !hasCycle {
		return Feedback{}, false
	}
	for _, candidate := range cache.FeedbackByCycle[cycleID] {
		if candidate.Fields.Fingerprint == fingerprint {
			return candidate, true
		}
	}
	return Feedback{}, false
}
