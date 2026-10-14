// Package revalidate is the daemon-side behavior that runs when a kept-alive
// pull request's summary has been refreshed (the spec's "Keep-alive,
// revalidation and expiry"):
//
//   - REVALIDATE: when the new summary's fingerprint equals the previous one,
//     the stored detail and conversation rows have fresh_until extended (ruling
//     6), never past their *_max_age, so the hard maximum forces a re-pull that
//     the list fingerprint cannot trigger (it cannot see merge-state status or
//     edited comment bodies).
//   - QUEUE DEPENDENTS: when the summary changed in a way that stales other
//     groups (head, base, conversation counts or updatedAt, a known mergeability
//     move, the CI rollup), those groups are queued at class 2.
//   - POPULATE: a first sighting of a kept-alive PR books detail, conversation
//     and runs for the background.
//
// Decide is the pure decision; Handler applies it through the group refresh
// engine. Nothing here writes a change row or holds one back: the settle
// barrier is the change engine's, which composes its own handler after this
// one's OnSummaryRefreshed.
package revalidate

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

// The result words of the revalidation metric.
const (
	// ResultExtended: the group's fresh_until was extended instead of a re-pull.
	ResultExtended = "extended"
	// ResultRepulled: the group was queued for a re-pull (a dependent of a
	// changed summary, or a first-sighting population).
	ResultRepulled = "repulled"
	// ResultMaxAge: the group could not be extended because it is at its hard
	// maximum age, so its due re-pull stands.
	ResultMaxAge = "max_age"
)

// Plan is what Decide says to do about one refreshed summary.
type Plan struct {
	// Extend names the groups whose fresh_until is extended (detail and
	// conversation), in the order of groups.All. A group the summary queues is
	// never also extended.
	Extend []string
	// Queue names the groups queued at class 2, due now, in the order of
	// groups.All; a group several triggers name appears once.
	Queue []string
	// Populate is true for a first sighting: the caller books the background
	// groups (see PopulateGroups) when the entity is in the keep-alive set.
	Populate bool
}

// Empty reports whether the plan asks for nothing.
func (p Plan) Empty() bool { return len(p.Extend) == 0 && len(p.Queue) == 0 && !p.Populate }

// PopulateGroups are the groups a first sighting of a kept-alive PR books in the
// background: the ones the spec's cost model re-pulls for every kept-alive PR.
// files and commits are fetched on a read, or when the head or base changes.
func PopulateGroups() []string {
	return []string{groups.Detail, groups.Conversation, groups.Runs}
}

// Decide reads the decision table of the spec's "Keep-alive, revalidation and
// expiry" against the previous summary (nil for a first sighting) and what was
// stored. It touches no store.
func Decide(prev *summary.Summary, next summary.Stored) Plan {
	if next.First {
		return Plan{Populate: true}
	}
	var plan Plan
	queue := map[string]bool{}

	if prev != nil {
		cur := next.Summary
		if cur.HeadRefOid != prev.HeadRefOid {
			queue[groups.Files], queue[groups.Commits], queue[groups.Runs] = true, true, true
		}
		if cur.BaseRefOid != prev.BaseRefOid || cur.BaseRefName != prev.BaseRefName {
			queue[groups.Files], queue[groups.Commits], queue[groups.Detail] = true, true, true
		}
		if cur.CommentCount != prev.CommentCount || cur.ReviewCount != prev.ReviewCount ||
			cur.ReviewThreadCount != prev.ReviewThreadCount || cur.UpdatedAt != prev.UpdatedAt {
			queue[groups.Conversation] = true
		}
		if cur.ChecksState != prev.ChecksState {
			queue[groups.Runs] = true
		}
	}
	if next.MergeabilityChanged {
		queue[groups.Detail] = true
	}

	unchanged := next.PrevFingerprint != "" && next.Fingerprint == next.PrevFingerprint
	extendable := unchanged && !summary.IsTerminal(next.Summary.State)
	for _, g := range groups.All() {
		switch {
		case queue[g]:
			plan.Queue = append(plan.Queue, g)
		case extendable && (g == groups.Detail || g == groups.Conversation):
			plan.Extend = append(plan.Extend, g)
		}
	}
	return plan
}
