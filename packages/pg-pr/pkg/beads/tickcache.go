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

	// DepsUpByPR holds the recursive dep tree (in the `up` direction)
	// for every merge-request bead. Pre-computed by BFS over a
	// workspace-wide edge map fetched in one `bd list --json --limit=0
	// --all` call, replacing the previous one-`bd dep tree`-per-PR loop
	// (~21 calls per tick on the zr workspace) with a single bulk read.
	//
	// DepNode.Labels is empty here — overlay `human` labels via
	// ApplyHumanLabels(deps, cache.HumanLabeled) before consulting
	// AllNonClosedHumanLabeled.
	DepsUpByPR map[string][]DepNode
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
		DepsUpByPR:         map[string][]DepNode{},
	}

	if set, err := c.HumanLabeledBeads(ctx); err == nil {
		cache.HumanLabeled = set
	}

	// One workspace-wide list with embedded dep edges. The same payload
	// feeds MergeRequestsByID, FeedbackByCycle, OpenProcessingByPR, and
	// DepsUpByPR — previously four separate bd calls plus per-cycle dep
	// list calls. Closed beads are included so dep edges remain visible
	// after a feedback or PR is closed.
	issues, err := c.listAllWithDeps(ctx)
	if err != nil {
		// On bulk-fetch failure, the cache is still safe to consult —
		// every helper short-circuits on a nil/empty map and the caller
		// falls back to its live bd call path.
		return cache
	}

	feedbackByID := map[string]Feedback{}
	cycles := make([]processingCycleCandidate, 0)
	// childrenOf reverses each `depends_on_id → issue_id` edge so a BFS
	// in the up direction is a simple map lookup.
	childrenOf := map[string][]string{}
	titleOf := map[string]string{}
	statusOf := map[string]string{}

	for _, iss := range issues {
		titleOf[iss.ID] = iss.Title
		statusOf[iss.ID] = iss.Status
		for _, dep := range iss.Dependencies {
			childrenOf[dep.DependsOnID] = append(childrenOf[dep.DependsOnID], dep.IssueID)
		}
		switch iss.Type {
		case TypeMergeRequest:
			cache.MergeRequestsByID[iss.ID] = bdIssueToMergeRequest(iss)
		case TypeFeedback:
			fb := Feedback{
				ID:     iss.ID,
				Title:  iss.Title,
				Status: iss.Status,
				Fields: feedbackFieldsFromMetadata(iss.Metadata),
			}
			feedbackByID[iss.ID] = fb
		case "task":
			if iss.Status != "closed" && strings.HasPrefix(iss.Title, processingCycleTitlePrefix) {
				cycles = append(cycles, processingCycleCandidate{ID: iss.ID, Title: iss.Title})
			}
		}
	}

	// Map each open processing-cycle to its merge-request parent and its
	// feedback children via the workspace-wide edge map.
	for _, cycle := range cycles {
		// Children: anything depending on the cycle. Feedback beads are
		// the only kind the engine cares about under a cycle.
		children := childrenOf[cycle.ID]
		cache.ChildrenByID[cycle.ID] = children
		for _, childID := range children {
			if fb, ok := feedbackByID[childID]; ok {
				cache.FeedbackByCycle[cycle.ID] = append(cache.FeedbackByCycle[cycle.ID], fb)
			}
		}
		// Parents: walk the cycle's dependencies (depends_on_id) to find
		// the merge-request anchor. We don't have a built parentsOf map
		// for cycles, so re-derive from the original dependencies list.
		for _, iss := range issues {
			if iss.ID != cycle.ID {
				continue
			}
			for _, dep := range iss.Dependencies {
				if _, isMR := cache.MergeRequestsByID[dep.DependsOnID]; isMR {
					cache.OpenProcessingByPR[dep.DependsOnID] = cycle.ID
					break
				}
			}
			break
		}
	}

	// Pre-compute the recursive dep tree (up direction) for every
	// merge-request bead via BFS over childrenOf. Replaces N per-PR
	// `bd dep tree` calls with one workspace-wide list + in-memory walk.
	for prID := range cache.MergeRequestsByID {
		cache.DepsUpByPR[prID] = bfsDescendants(prID, childrenOf, titleOf, statusOf)
	}

	return cache
}

// bfsDescendants returns every bead transitively depending on rootID in
// the up direction, excluding rootID itself. The returned DepNode.Labels
// is empty; callers overlay labels via ApplyHumanLabels.
func bfsDescendants(rootID string, childrenOf map[string][]string, titleOf, statusOf map[string]string) []DepNode {
	if rootID == "" {
		return nil
	}
	seen := map[string]bool{rootID: true}
	var out []DepNode
	queue := []string{rootID}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range childrenOf[cur] {
			if seen[child] {
				continue
			}
			seen[child] = true
			out = append(out, DepNode{
				ID:     child,
				Title:  titleOf[child],
				Status: statusOf[child],
			})
			queue = append(queue, child)
		}
	}
	return out
}

// listAllWithDeps returns every bead in the workspace (open + closed)
// with its embedded dependency edges. Replaces the previous trio of
// `bd list --type=merge-request`, `bd list --type=feedback`, and
// `bd list --type=task --status=open` calls plus the per-cycle dep
// list/dep tree calls in the snapshot loop.
func (c *Client) listAllWithDeps(ctx context.Context) ([]bdIssue, error) {
	out, err := c.Runner.Run(ctx, "list", "--json", "--limit=0", "--all")
	if err != nil {
		return nil, err
	}
	return parseBDList(out)
}

// processingCycleCandidate is a parsed subset of bd's task-list JSON
// used to identify processing-cycle beads.
type processingCycleCandidate struct {
	ID    string
	Title string
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

// DepsUpFor returns the cached recursive dep tree for the given PR bead.
// ok=true means the cache has authoritative data; the empty []DepNode +
// ok=true case still means "PR exists in the cache but has no
// dependents" and the caller should NOT fall back to a live call. ok=false
// indicates either nil cache or a missing/failed bulk fetch — caller
// should run DepTreeUp.
func (cache *TickCache) DepsUpFor(prBeadID string) ([]DepNode, bool) {
	if cache == nil {
		return nil, false
	}
	deps, ok := cache.DepsUpByPR[prBeadID]
	return deps, ok
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
