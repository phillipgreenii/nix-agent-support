package links

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Detail is one link of the Phase 6 composite view's links[]: the linked
// entity, how it relates to the focus entity, every origin that claims the
// link, its web page, and (when it has a stored row) its own state, labels,
// metadata and assignee read from that stored snapshot. It is built on top of
// Linked rather than changing it, so ReadLinked's callers are unaffected.
type Detail struct {
	Type     string
	ID       string
	Relation string
	// URL is the linked entity's own web page: its stored snapshot's, or for
	// an issue-tracker key the configured issue_url_template. "" when none
	// can be determined.
	URL     string
	Origins []Origin
	// Stored reports that the linked entity has a stored row; State, Labels,
	// Metadata and Assignee are meaningful only then. State is "" when the
	// stored snapshot carries none (a thread has no state).
	Stored bool
	// Title is the linked entity's title as stored; "" when the snapshot
	// carries none (a thread has no title) or the entity has no stored row.
	Title    string
	State    string
	Labels   []string
	Metadata map[string]string
	// Assignee is "" when the linked entity is unclaimed.
	Assignee string
}

// Detailed is ReadDetailed's answer.
type Detailed struct {
	Links []Detail
	// LinksAsOf is the newest time any of the edges was last confirmed; ""
	// when the entity has no edges.
	LinksAsOf string
	// Degraded is true on an unmigrated store (only the legacy pr-to-issue
	// rows could be read).
	Degraded bool
}

// ReadDetailed is the composite view's linked-entity read: ReadLinked's
// edges (both ends of each stored one), merged across direction so a link
// appears once per (type, id, relation) with the union of its origins, each
// joined with the linked entity's stored snapshot. Read-only, no network.
func ReadDetailed(d Deps, entityType, id string) (Detailed, error) {
	linked, asOf, degraded, err := readLinkedAsOf(d.Store, d.Repo, entityType, id)
	if err != nil {
		return Detailed{}, err
	}
	// A derived dependency (the stack source) has no stored xref row, so it is
	// added here with its origin; an external depends_on link is already in
	// linked, with its actor, and is not repeated.
	if entityType == entityTypePR {
		edges, derr := d.dependencies().DependenciesOf(entityTypePR, id)
		if derr != nil {
			return Detailed{}, derr
		}
		for _, e := range edges {
			for _, o := range e.Origins {
				if strings.HasPrefix(o, "derived:") {
					linked = append(linked, Linked{Direction: DirOut, Type: entityTypePR, ID: e.ID, Relation: relationDependsOn, Origins: []Origin{{Origin: o}}})
				}
			}
		}
	}
	focusFacts := ""
	if entityType == entityTypePR {
		e, found, gerr := d.Store.GetEntity(d.Repo, entityType, id)
		if gerr != nil {
			return Detailed{}, gerr
		}
		if found {
			focusFacts = e.Facts
		}
	}

	type key struct{ typ, id, rel string }
	merged := map[key]*Detail{}
	var order []key
	for _, l := range linked {
		k := key{l.Type, l.ID, l.Relation}
		det := merged[k]
		if det == nil {
			det = &Detail{Type: l.Type, ID: l.ID, Relation: l.Relation}
			merged[k] = det
			order = append(order, k)
		}
	origins:
		for _, o := range l.Origins {
			for _, have := range det.Origins {
				if have == o {
					continue origins
				}
			}
			det.Origins = append(det.Origins, o)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.rel < b.rel
	})

	out := Detailed{LinksAsOf: asOf, Degraded: degraded}
	for _, k := range order {
		det := merged[k]
		sort.SliceStable(det.Origins, func(i, j int) bool { return det.Origins[i].Origin < det.Origins[j].Origin })
		ent, found, gerr := d.Store.GetEntity(d.Repo, det.Type, det.ID)
		if gerr != nil {
			return Detailed{}, fmt.Errorf("links: read linked %s %s: %w", det.Type, det.ID, gerr)
		}
		if det.Type == entityTypeIssue {
			det.URL, err = issueURL(d, det.ID, det.Relation == relationJira, focusFacts)
		} else if found {
			det.URL = SnapshotURL(det.Type, ent.Facts)
		}
		if err != nil {
			return Detailed{}, fmt.Errorf("links: url of linked %s %s: %w", det.Type, det.ID, err)
		}
		if found {
			fillFromSnapshot(det, ent)
		}
		out.Links = append(out.Links, *det)
	}
	return out, nil
}

// fillFromSnapshot reads the linked entity's own title, state, labels, metadata and
// assignee out of its stored facts.
func fillFromSnapshot(det *Detail, ent store.Entity) {
	det.Stored = true
	var f map[string]json.RawMessage
	if json.Unmarshal([]byte(ent.Facts), &f) != nil {
		return
	}
	switch det.Type {
	case entityTypeIssue:
		var s struct {
			Title    string            `json:"title"`
			State    string            `json:"state"`
			Labels   []string          `json:"labels"`
			Metadata map[string]string `json:"metadata"`
			Assignee string            `json:"assignee"`
		}
		if json.Unmarshal(f["issue_show"], &s) == nil {
			det.Title, det.State, det.Labels, det.Metadata, det.Assignee = s.Title, s.State, s.Labels, s.Metadata, s.Assignee
		}
	case entityTypePR:
		var s struct {
			Title  string   `json:"title"`
			State  string   `json:"state"`
			Merged bool     `json:"merged"`
			Labels []string `json:"labels"`
		}
		if json.Unmarshal(f["pr_show"], &s) == nil {
			det.Title, det.State, det.Labels = s.Title, s.State, s.Labels
			if s.Merged {
				det.State = "merged"
			}
		}
	}
}
