package links

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Direction says which end of a stored edge the focus entity is.
type Direction string

const (
	// DirOut: the edge leaves the focus entity (the focus entity is its source).
	DirOut Direction = "out"
	// DirIn: the edge arrives at the focus entity (the focus entity is its target).
	DirIn Direction = "in"
)

// Origin is one claim on a link: "derived:<extractor>" or
// "external:<actor>" (with when and why for the latter).
type Origin struct {
	Origin string
	At     string
	Reason string
}

// Linked is one entity related to a focus entity through one relation,
// with every origin that claims the link.
type Linked struct {
	Direction Direction
	Type      string
	ID        string
	Relation  string
	Origins   []Origin
}

// Normalized relation of the legacy pr-to-issue ticket-key rows, which the
// store marks relation "references" / origin "derived:legacy".
const (
	relationJira     = "jira"
	legacyOrigin     = "derived:legacy"
	legacyRelation   = "references"
	legacyJiraSource = "pr"
	legacyJiraTarget = "issue"
	relationSelf     = "self"
	relationWork     = "work"
	relationParent   = "parent"
	relationMentions = "mentions"
	entityTypePR     = "pr"
	entityTypeIssue  = "issue"
	entityTypeThread = "thread"
)

// ReadLinked is the shared linked-entity read helper: every entity related to
// (repo, entityType, id), from both ends of each stored edge, with the
// origins that claim each link. It is read-only and does no network I/O.
//
// On a migrated store (schema version 2) it reads the origin/relation xref
// API; legacy pr-to-issue rows (relation "references") are folded into the
// "jira" relation so they merge with the derived jira-key claim for the same
// edge. On an unmigrated store it degrades: degraded is true and only the
// legacy pr-to-issue rows are returned (a pr's issues, an issue's prs), all
// as relation "jira". Callers wanting a different shape (the Phase 6 `show`
// composite view) add fields on top of Linked rather than re-reading the
// xref table.
func ReadLinked(st *store.Store, repo, entityType, id string) (linked []Linked, degraded bool, err error) {
	if err := st.RequireNewSchema(); err != nil {
		if !errors.Is(err, store.ErrOldSchema) {
			return nil, false, err
		}
		l, derr := readLinkedLegacy(st, repo, entityType, id)
		return l, true, derr
	}
	out, err := st.ListXrefLinksFrom(repo, entityType, id)
	if err != nil {
		return nil, false, err
	}
	in, err := st.ListXrefLinksTo(repo, entityType, id)
	if err != nil {
		return nil, false, err
	}
	type key struct {
		dir          Direction
		typ, id, rel string
	}
	merged := map[key]*Linked{}
	var order []key
	add := func(dir Direction, l store.XrefLink, otherType, otherID string) {
		rel := l.Relation
		if l.Origin == legacyOrigin && rel == legacyRelation && l.FromType == legacyJiraSource && l.ToType == legacyJiraTarget {
			rel = relationJira
		}
		k := key{dir, otherType, otherID, rel}
		entry := merged[k]
		if entry == nil {
			entry = &Linked{Direction: dir, Type: otherType, ID: otherID, Relation: rel}
			merged[k] = entry
			order = append(order, k)
		}
		o := Origin{Origin: l.Origin}
		if strings.HasPrefix(l.Origin, "external:") {
			o.At, o.Reason = l.ActedAt, l.Reason
		}
		for _, have := range entry.Origins {
			if have == o {
				return
			}
		}
		entry.Origins = append(entry.Origins, o)
	}
	for _, l := range out {
		add(DirOut, l, l.ToType, l.ToID)
	}
	for _, l := range in {
		add(DirIn, l, l.FromType, l.FromID)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.dir != b.dir {
			return a.dir == DirOut
		}
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.rel < b.rel
	})
	for _, k := range order {
		e := merged[k]
		sort.SliceStable(e.Origins, func(i, j int) bool { return e.Origins[i].Origin < e.Origins[j].Origin })
		linked = append(linked, *e)
	}
	return linked, false, nil
}

// readLinkedLegacy serves an unmigrated store: only the legacy pr-to-issue
// ticket-key rows exist there.
func readLinkedLegacy(st *store.Store, repo, entityType, id string) ([]Linked, error) {
	var out []Linked
	switch entityType {
	case entityTypePR:
		rows, err := st.ListXrefsByFrom(repo, entityTypePR, id, entityTypeIssue)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, Linked{Direction: DirOut, Type: entityTypeIssue, ID: r.ToID, Relation: relationJira, Origins: []Origin{{Origin: legacyOrigin}}})
		}
	case entityTypeIssue:
		rows, err := st.ListXrefsByTo(repo, entityTypeIssue, id)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.FromType != entityTypePR {
				continue
			}
			out = append(out, Linked{Direction: DirIn, Type: entityTypePR, ID: r.FromID, Relation: relationJira, Origins: []Origin{{Origin: legacyOrigin}}})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// SnapshotURL returns the web URL stored in an entity's gathered facts JSON,
// per entity type (pr: pr_show.url; issue: issue_show.url; thread:
// thread_show.permalink, else .url), or "" when absent, unparsable or not a
// plain http(s) URL.
func SnapshotURL(entityType, factsJSON string) string {
	var f map[string]json.RawMessage
	if err := json.Unmarshal([]byte(factsJSON), &f); err != nil {
		return ""
	}
	field := map[string]string{entityTypePR: "pr_show", entityTypeIssue: "issue_show", entityTypeThread: "thread_show"}[entityType]
	if field == "" {
		return ""
	}
	var show struct {
		URL       string `json:"url"`
		Permalink string `json:"permalink"`
	}
	if err := json.Unmarshal(f[field], &show); err != nil {
		return ""
	}
	if entityType == entityTypeThread && show.Permalink != "" {
		return cleanURL(show.Permalink)
	}
	return cleanURL(show.URL)
}

// jiraIssueSnapshotURL returns the url of the Jira issue snapshot a PR's facts
// recorded under facts.jira_issues[key], or "".
func jiraIssueSnapshotURL(prFactsJSON, key string) string {
	var f struct {
		JiraIssues map[string]struct {
			URL string `json:"url"`
		} `json:"jira_issues"`
	}
	if err := json.Unmarshal([]byte(prFactsJSON), &f); err != nil {
		return ""
	}
	return cleanURL(f.JiraIssues[key].URL)
}

// cleanURL returns u when it is a plain absolute http(s) URL with no
// whitespace or control characters, and "" otherwise: a consumer renders
// these as clickable rows, so a javascript: or file: URI must never pass.
func cleanURL(u string) string {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	for _, r := range u {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	return u
}
