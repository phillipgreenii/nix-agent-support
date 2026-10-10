package dependency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TypeIssue is the entity type IssueDependents indexes.
const TypeIssue = "issue"

// EdgeBlocks is the stored edge type (schema.IssueDependency.Type) that makes
// an issue a dependent: only an edge of this type counts.
const EdgeBlocks = "blocks"

// IssueDependents is the issue counterpart of Resolver.DependentsOf: a
// reverse-edge index over the stored DIRECT issue edges, so "how many open
// items does this issue block" is a pure in-store join (daily-focus design
// section 6, "Unblocks").
//
// The edges are the typed one-level edges of each stored issue_show.deps
// ({id, type}); an issue B whose deps name A with type "blocks" is blocked BY
// A, so B is a dependent of A. The recursive blocked-by set (issue_deps, which
// the Jira backend returns empty) is deliberately NOT the edge source: a chain
// A blocked by B blocked by C yields DependentsOf(C) == [B] and
// DependentsOf(B) == [A], never [B, A] for C. issue_deps is read only to
// answer Available.
//
// It reads the store once, at construction, and never again: build a fresh
// index per read so it never outlives the state it indexed. It reads no clock;
// terminal-ness is the caller's predicate. Issue ids are indexed by id alone,
// across every repository the store holds issues for.
type IssueDependents struct {
	dependents map[string][]string // blocker id -> sorted dependent ids
	available  map[string]bool     // issue id -> its stored facts carry issue_deps
}

// NewIssueDependents indexes every stored issue entity of r. A dependent is
// listed only while its entity is active (store.Entity.Inactive false) and
// isTerminal reports false for it; a nil isTerminal treats nothing as
// terminal. An issue whose facts do not decode contributes no edge. It
// returns the store's old-schema refusal on an unmigrated store, where
// activity is not recorded.
func NewIssueDependents(r Reader, isTerminal func(store.Entity) bool) (*IssueDependents, error) {
	if err := r.RequireNewSchema(); err != nil {
		return nil, err
	}
	rows, err := r.ListEntities()
	if err != nil {
		return nil, fmt.Errorf("dependency: read entities: %w", err)
	}
	sets := map[string]map[string]struct{}{}
	x := &IssueDependents{available: map[string]bool{}}
	for _, e := range rows {
		if e.EntityType != TypeIssue {
			continue
		}
		var facts struct {
			IssueShow struct {
				Deps []struct {
					ID   string `json:"id"`
					Type string `json:"type"`
				} `json:"deps"`
			} `json:"issue_show"`
			IssueDeps json.RawMessage `json:"issue_deps"`
		}
		if json.Unmarshal([]byte(e.Facts), &facts) != nil {
			continue
		}
		if d := bytes.TrimSpace(facts.IssueDeps); len(d) > 0 && !bytes.Equal(d, []byte("null")) {
			x.available[e.EntityID] = true
		}
		if e.Inactive || (isTerminal != nil && isTerminal(e)) {
			continue
		}
		for _, d := range facts.IssueShow.Deps {
			if d.ID == "" || d.ID == e.EntityID || !strings.EqualFold(d.Type, EdgeBlocks) {
				continue
			}
			if sets[d.ID] == nil {
				sets[d.ID] = map[string]struct{}{}
			}
			sets[d.ID][e.EntityID] = struct{}{}
		}
	}
	x.dependents = make(map[string][]string, len(sets))
	for blocker, set := range sets {
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		x.dependents[blocker] = ids
	}
	return x, nil
}

// DependentsOf returns the sorted ids of the issues blocked BY issueID
// directly (one hop, never transitive), restricted to active, non-terminal
// dependents. An unknown issue, or one that blocks nothing open, yields nil.
// The returned slice is a copy.
func (x *IssueDependents) DependentsOf(issueID string) []string {
	ids := x.dependents[issueID]
	if len(ids) == 0 {
		return nil
	}
	return append([]string(nil), ids...)
}

// Available reports whether issueID's own stored facts carry issue_deps, that
// is whether issue-dependency hydration was on when it was last hydrated.
// False means the unblocks key for the issue is unavailable (counted as such
// by the rank), not zero by evidence: DependentsOf is derived from the
// dependents' own stored edges and so may be empty or partial for an issue
// whose blockers were never read.
func (x *IssueDependents) Available(issueID string) bool {
	return x.available[issueID]
}
