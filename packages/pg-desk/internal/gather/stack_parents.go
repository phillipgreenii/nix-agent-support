package gather

import (
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// stackParentDepthCap bounds how many parent PRs gatherStackParents follows.
// A real stack is a handful deep; the cap only guards a malformed store.
const stackParentDepthCap = 16

// StackParent is one open PR this PR is stacked on, directly or through a
// chain: its base branch is the head branch of the parent (same repository).
// Only what the interpret stage needs to judge whether the stack can land.
type StackParent struct {
	ID               string `json:"id"`
	Branch           string `json:"branch"`
	Base             string `json:"base"`
	State            string `json:"state"`
	Mergeable        string `json:"mergeable,omitempty"`
	MergeStateStatus string `json:"merge_state_status,omitempty"`
}

// entityLister is the store read gatherStackParents needs; *store.Store
// satisfies it. Like the linked-thread read it is a passive store read, never
// a pg-connector call, so a parent's mergeability is as current as its own
// stored row.
type entityLister interface {
	ListEntities() ([]store.Entity, error)
}

// gatherStackParents records into f the open PRs the triggering PR is stacked
// on: the open PR in the same repository whose head branch is this PR's base
// branch, then that PR's own parent, and so on, with a depth cap and a visited
// set. A parent that is merged, closed, unknown to the store, or whose head
// branch is its own base ends the chain. A store without ListEntities (or a
// nil one) or a read failure leaves f unchanged apart from a degraded mark on
// the failure.
func (g *Gatherer) gatherStackParents(repo, entityID, base string, f *Facts) {
	lister, ok := g.xrefs.(entityLister)
	if !ok || lister == nil || base == "" {
		return
	}
	rows, err := lister.ListEntities()
	if err != nil {
		f.degrade("stack parents")
		return
	}
	byHead := map[string][]StackParent{}
	for _, r := range rows {
		if r.EntityType != "pr" || r.Repo != repo || r.EntityID == entityID {
			continue
		}
		var pr struct {
			State            string `json:"state"`
			Branch           string `json:"branch"`
			Base             string `json:"base"`
			Mergeable        string `json:"mergeable"`
			MergeStateStatus string `json:"merge_state_status"`
		}
		var facts struct {
			PRShow json.RawMessage `json:"pr_show"`
		}
		if json.Unmarshal([]byte(r.Facts), &facts) != nil || json.Unmarshal(facts.PRShow, &pr) != nil {
			continue
		}
		if pr.State != "open" || pr.Branch == "" || pr.Branch == pr.Base {
			continue
		}
		byHead[pr.Branch] = append(byHead[pr.Branch], StackParent{
			ID: r.EntityID, Branch: pr.Branch, Base: pr.Base, State: pr.State,
			Mergeable: pr.Mergeable, MergeStateStatus: pr.MergeStateStatus,
		})
	}
	visited := map[string]bool{entityID: true}
	for depth := 0; depth < stackParentDepthCap; depth++ {
		var next *StackParent
		for i, c := range byHead[base] {
			if !visited[c.ID] {
				next = &byHead[base][i]
				break
			}
		}
		if next == nil {
			return
		}
		visited[next.ID] = true
		f.StackParents = append(f.StackParents, *next)
		base = next.Base
	}
}
