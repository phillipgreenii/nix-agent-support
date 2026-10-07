// Package links answers the read-only `pg-desk links` verb: for a batch of
// attention-style refs ("<type>:<id>") it returns the cross-reference links
// (PR, issue, thread, and later build) that pg-desk's local store knows for
// each, so a consumer such as a menu bar plugin can offer quick links
// without any network call, hydration or write.
//
// The package is the READ side of the xref graph that internal/pipeline's
// LinkExtractor registry writes. Link production is a Strategy per entity
// type, registered in NewRegistry exactly as extractors are in
// pipeline.NewExtractorRegistry, so supporting a new type is one entry. The
// shared linked-entity read (ReadLinked, SnapshotURL) is deliberately separate
// from the Strategies so the Phase 6 composite view's links[] reuses it.
package links

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/cirun"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/dependency"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
)

// SchemaVersion is the verb's output schema version (additive evolution only).
const SchemaVersion = 1

// Link kinds: a closed set that is a rendering hint, not the entity type.
const (
	KindPR     = "pr"
	KindBuild  = "build"
	KindIssue  = "issue"
	KindThread = "thread"
	KindOther  = "other"
)

// Link is one cross-reference with a URL. A link whose URL cannot be
// determined is never produced (INV-LINKS-3).
type Link struct {
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
	Label    string `json:"label"`
	URL      string `json:"url"`
	State    string `json:"state,omitempty"`
}

// Item is the answer for one ref. Known is false for a ref pg-desk has no
// entity or link for (including an unsupported type or a malformed ref);
// Links is then empty, never null.
type Item struct {
	Known bool   `json:"known"`
	Links []Link `json:"links"`
}

// Result is the verb's JSON document.
type Result struct {
	SchemaVersion int `json:"schemaVersion"`
	// AsOf is the oldest as_of among the refs' stored entities; absent when
	// no ref has a stored entity. The consumer decides what staleness means.
	AsOf string `json:"as_of,omitempty"`
	// Degraded is true when the store is not yet on the migrated schema, so
	// only the legacy pr-to-issue links could be read.
	Degraded bool            `json:"degraded,omitempty"`
	Items    map[string]Item `json:"items"`
}

// Deps is what Resolve reads. Store MUST be opened read-only by the caller
// (store.OpenReadOnly); nothing here writes.
type Deps struct {
	Store *store.Store
	// Repo is the configured repository every stored row is keyed by.
	Repo string
	// Patterns is config ticket_patterns; a bare id matching one is a ticket
	// key and may be turned into a URL with IssueURLTemplate.
	Patterns []string
	// IssueURLTemplate is config links.issue_url_template ("" when unset).
	IssueURLTemplate string
	// CheckInterpreters is config check_interpreters: the build links drop the
	// runs these patterns exclude, exactly as the dashboard's CI rollup does.
	CheckInterpreters []config.CheckInterpreterConfig
	// Dependencies reads PR-to-PR dependencies. Nil builds one over Store and
	// Repo on first use; a caller answering a batch MAY share one.
	Dependencies *dependency.Resolver
}

// dependencies returns the Deps' resolver, creating it on first use.
func (d *Deps) dependencies() *dependency.Resolver {
	if d.Dependencies == nil {
		d.Dependencies = dependency.NewResolver(d.Store, d.Repo)
	}
	return d.Dependencies
}

// Subject is what a Strategy is handed for one ref.
type Subject struct {
	ID string
	// Entity is the stored entity row, nil when there is none (a ref can be
	// known through links alone, e.g. a ticket key).
	Entity *store.Entity
	// Linked is ReadLinked's answer for the ref.
	Linked []Linked
}

// Strategy produces the links for refs of one entity type.
type Strategy interface {
	Links(d Deps, s Subject) ([]Link, error)
}

// Registry maps an entity type name to its Strategy.
type Registry map[string]Strategy

// NewRegistry registers the supported types: pr, issue and thread. The pr
// strategy is where the build producer joins: one build link per failing CI
// run on the current head, read from the stored facts (no network).
func NewRegistry() Registry {
	return Registry{
		entityTypePR:     prStrategy{},
		entityTypeIssue:  issueStrategy{},
		entityTypeThread: threadStrategy{},
	}
}

// ParseRef splits "<type>:<id>" at the first colon (the id may itself
// contain colons) and trims surrounding whitespace. ok is false when either
// part is empty or there is no colon.
func ParseRef(ref string) (typ, id string, ok bool) {
	ref = strings.TrimSpace(ref)
	i := strings.IndexByte(ref, ':')
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// Resolve answers every ref against the local store. An unknown ref is not an
// error (INV-LINKS-2); only an unreadable store is.
func Resolve(d Deps, refs []string) (Result, error) {
	reg := NewRegistry()
	d.dependencies() // one resolver (one entity-table read) for the whole batch
	res := Result{SchemaVersion: SchemaVersion, Items: map[string]Item{}}
	if err := d.Store.RequireNewSchema(); err != nil {
		if !errors.Is(err, store.ErrOldSchema) {
			return Result{}, fmt.Errorf("links: read schema version: %w", err)
		}
		res.Degraded = true
	}
	var oldest time.Time
	var oldestRaw string
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		if _, dup := res.Items[ref]; dup {
			continue
		}
		unknown := Item{Known: false, Links: []Link{}}
		typ, id, ok := ParseRef(ref)
		strat := reg[typ]
		if !ok || strat == nil {
			res.Items[ref] = unknown
			continue
		}
		ent, found, err := d.Store.GetEntity(d.Repo, typ, id)
		if err != nil {
			return Result{}, fmt.Errorf("links: read entity %s: %w", ref, err)
		}
		linked, _, err := ReadLinked(d.Store, d.Repo, typ, id)
		if err != nil {
			return Result{}, fmt.Errorf("links: read links of %s: %w", ref, err)
		}
		if !found && len(linked) == 0 {
			res.Items[ref] = unknown
			continue
		}
		subj := Subject{ID: id, Linked: linked}
		if found {
			subj.Entity = &ent
			if t, perr := time.Parse(time.RFC3339, ent.AsOf); perr == nil && (oldest.IsZero() || t.Before(oldest)) {
				oldest, oldestRaw = t, ent.AsOf
			}
		}
		ls, err := strat.Links(d, subj)
		if err != nil {
			return Result{}, fmt.Errorf("links: build links of %s: %w", ref, err)
		}
		res.Items[ref] = Item{Known: true, Links: dedupe(ls)}
	}
	res.AsOf = oldestRaw
	return res, nil
}

// dedupe drops repeated links and any link without a URL, keeping order, and
// always returns a non-nil slice.
func dedupe(in []Link) []Link {
	out := []Link{}
	seen := map[Link]bool{}
	for _, l := range in {
		if l.URL == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// prLabel is "PR #<n>" for an id shaped "<owner>/<repo>#<n>".
func prLabel(id string) string {
	if i := strings.LastIndexByte(id, '#'); i >= 0 && i < len(id)-1 {
		return "PR #" + id[i+1:]
	}
	return "PR " + id
}

// entityURL is the snapshot URL of a stored entity, "" when it is not stored
// or has none.
func entityURL(d Deps, typ, id string) (string, error) {
	e, found, err := d.Store.GetEntity(d.Repo, typ, id)
	if err != nil || !found {
		return "", err
	}
	return SnapshotURL(typ, e.Facts), nil
}

// templateURL renders links.issue_url_template for a ticket key, "" when no
// template is configured. The template is validated at config load.
func templateURL(d Deps, key string) string {
	if d.IssueURLTemplate == "" {
		return ""
	}
	return cleanURL(strings.Replace(d.IssueURLTemplate, "%s", url.PathEscape(key), 1))
}

// issueURL is the URL of an issue-type entity. A ticket key (jiraEdge, or an
// id shaped like one) prefers the configured template and falls back to a
// stored snapshot; any other id (a bead) uses its snapshot only, so a bead id
// is never rendered through the tracker template.
func issueURL(d Deps, id string, jiraEdge bool, prFacts string) (string, error) {
	isKey := jiraEdge || ticketkey.MatchesShape(id, d.Patterns)
	if isKey {
		if u := templateURL(d, id); u != "" {
			return u, nil
		}
	}
	u, err := entityURL(d, entityTypeIssue, id)
	if err != nil || u != "" {
		return u, err
	}
	if jiraEdge && prFacts != "" {
		return jiraIssueSnapshotURL(prFacts, id), nil
	}
	return "", nil
}

func selfLink(kind, label, u string) []Link {
	return []Link{{Kind: kind, Relation: relationSelf, Label: label, URL: u}}
}

type prStrategy struct{}

// Links for a PR: itself; one build link per failing CI run on its current
// head (relation ci); the PRs it depends on (relation depends_on); the issues
// its text names (relation jira); the threads that mention it.
func (prStrategy) Links(d Deps, s Subject) ([]Link, error) {
	var out []Link
	if s.Entity != nil {
		out = append(out, selfLink(KindPR, prLabel(s.ID), SnapshotURL(entityTypePR, s.Entity.Facts))...)
		out = append(out, buildLinks(d, s.Entity)...)
	}
	deps, err := dependencyLinks(&d, s.ID)
	if err != nil {
		return nil, err
	}
	out = append(out, deps...)
	prFacts := ""
	if s.Entity != nil {
		prFacts = s.Entity.Facts
	}
	for _, l := range s.Linked {
		switch {
		case l.Direction == DirOut && l.Type == entityTypeIssue && l.Relation == relationJira:
			u, err := issueURL(d, l.ID, true, prFacts)
			if err != nil {
				return nil, err
			}
			out = append(out, Link{Kind: KindIssue, Relation: l.Relation, Label: l.ID, URL: u})
		case l.Direction == DirIn && l.Type == entityTypeThread:
			u, err := entityURL(d, entityTypeThread, l.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, Link{Kind: KindThread, Relation: l.Relation, Label: "Thread", URL: u})
		}
	}
	return out, nil
}

// dependencyLinks is the dependency producer of the pr strategy: one link per
// PR the PR depends on, from every dependency source (stack, derived from the
// stored branches; external, recorded by an operator). The link's state is the
// dependency's stored state, so a consumer can tell an open dependency from a
// merged one. Read from the store only, and available on the unmigrated store
// for the stack source.
func dependencyLinks(d *Deps, id string) ([]Link, error) {
	edges, err := d.dependencies().DependenciesOf(entityTypePR, id)
	if err != nil {
		return nil, err
	}
	var out []Link
	for _, e := range edges {
		u, err := entityURL(*d, entityTypePR, e.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Link{Kind: KindPR, Relation: relationDependsOn, Label: prLabel(e.ID), URL: u, State: e.State})
	}
	return out, nil
}

// buildLinks is the build producer of the pr strategy: one link per CI run on
// the PR's current head that the dashboard's CI rollup counts as failed. The
// runs come from the stored facts (the `ci list` fan-out), read through
// cirun so the head-only, newest-per-workflow and check_interpreters
// exclusion rules are the rollup's own (INV-LINKS-4). A failed run without a
// usable URL is dropped by dedupe (INV-LINKS-3).
func buildLinks(d Deps, e *store.Entity) []Link {
	var facts struct {
		PRShow struct {
			HeadSHA string `json:"head_sha"`
		} `json:"pr_show"`
		CI json.RawMessage `json:"ci"`
	}
	if err := json.Unmarshal([]byte(e.Facts), &facts); err != nil || len(facts.CI) == 0 {
		return nil
	}
	head := facts.PRShow.HeadSHA
	if head == "" {
		head = e.HeadSHA
	}
	var out []Link
	for _, r := range cirun.Evaluate(facts.CI, d.CheckInterpreters, head) {
		if r.Outcome != cirun.Failed {
			continue
		}
		state := r.Conclusion
		out = append(out, Link{Kind: KindBuild, Relation: relationCI, Label: r.Name + " (" + state + ")", URL: cleanURL(r.URL), State: state})
	}
	return out
}

type issueStrategy struct{}

// Links for an issue: itself; the PR it tracks (relation work) or that names
// it (relation jira, the reverse of a PR's ticket-key edge); its parent.
func (issueStrategy) Links(d Deps, s Subject) ([]Link, error) {
	var out []Link
	selfURL := ""
	if s.Entity != nil {
		selfURL = SnapshotURL(entityTypeIssue, s.Entity.Facts)
	}
	if selfURL == "" && ticketkey.MatchesShape(s.ID, d.Patterns) {
		selfURL = templateURL(d, s.ID)
	}
	out = append(out, selfLink(KindIssue, s.ID, selfURL)...)
	// The tracked/naming PR first, then the parent: the order a reader scans.
	for _, l := range s.Linked {
		if l.Type == entityTypePR && ((l.Direction == DirOut && l.Relation == relationWork) || (l.Direction == DirIn && l.Relation == relationJira)) {
			u, err := entityURL(d, entityTypePR, l.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, Link{Kind: KindPR, Relation: l.Relation, Label: prLabel(l.ID), URL: u})
		}
	}
	for _, l := range s.Linked {
		if l.Direction == DirOut && l.Type == entityTypeIssue && l.Relation == relationParent {
			u, err := issueURL(d, l.ID, false, "")
			if err != nil {
				return nil, err
			}
			out = append(out, Link{Kind: KindIssue, Relation: l.Relation, Label: l.ID, URL: u})
		}
	}
	return out, nil
}

type threadStrategy struct{}

// Links for a thread: itself and the PRs it mentions.
func (threadStrategy) Links(d Deps, s Subject) ([]Link, error) {
	var out []Link
	if s.Entity != nil {
		out = append(out, selfLink(KindThread, "Thread", SnapshotURL(entityTypeThread, s.Entity.Facts))...)
	}
	for _, l := range s.Linked {
		if l.Direction == DirOut && l.Type == entityTypePR && l.Relation == relationMentions {
			u, err := entityURL(d, entityTypePR, l.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, Link{Kind: KindPR, Relation: l.Relation, Label: prLabel(l.ID), URL: u})
		}
	}
	return out, nil
}
