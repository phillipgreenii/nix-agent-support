// Package dependency answers "which pull requests does this one wait for?",
// the read side of the PR-to-PR dependency data the attention evaluator's
// suppression rule and its stack grouping consume (docs/behavior/pg-desk/
// attention.md, "Deferred", and docs/behavior/pg-desk/links.md, "PR dependencies").
//
// Dependency data comes from a registry of Sources (a Strategy per way of
// knowing), exactly as internal/classify and internal/links register theirs,
// so a new way of knowing a dependency is one entry. The first members are:
//
//   - stack: PR A depends on PR B when A's base branch equals B's head branch
//     in the same repository and B is open. Derived at READ time from the
//     stored entity rows, so it is always consistent with the current store
//     and writes nothing. Works on both store schema versions. A PR whose
//     head branch is its own base branch (a fork's default branch proposed
//     upstream) is never a stack base.
//   - external: an operator recorded the relation `depends_on` between two PRs
//     with the external-link verb (`pg-desk pr link add <id> pr:<id>
//     --relation depends_on`). Needs the migrated schema; on an unmigrated
//     store it yields nothing rather than failing.
//
// A shared Jira issue is NOT a dependency: PRs on one issue are siblings (a
// group), never dependencies. This package knows no suppression rule and no
// grouping; it only reports edges.
//
// Everything here is read-only and offline: it reads the store through
// Reader and never writes, execs or opens a connection.
package dependency

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Names of the registered sources, and the relation the external source reads.
const (
	SourceStack    = "stack"
	SourceExternal = "external"

	// RelationDependsOn is the xref relation an operator records, from the
	// dependent PR to the PR it depends on.
	RelationDependsOn = "depends_on"

	// TypePR is the only entity type that has dependencies.
	TypePR = "pr"
)

// Stored PR states an Edge reports. "" means the other end has no usable
// stored row (never gathered, no longer watched, or unreadable facts); such a
// PR is NOT open, so an unknown dependency never holds anything back.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// Reader is the read-only slice of the store this package needs.
// *store.Store satisfies it.
type Reader interface {
	RequireNewSchema() error
	ListEntities() ([]store.Entity, error)
	ListXrefLinksFrom(repo, fromType, fromID string) ([]store.XrefLink, error)
	ListXrefLinksTo(repo, toType, toID string) ([]store.XrefLink, error)
}

// Edge is one dependency relationship seen from one end. For DependenciesOf
// the other end is the PR depended on; for DependentsOf it is the PR that
// depends on the focus PR.
type Edge struct {
	// Type and ID name the other end ("pr" and its entity id).
	Type string
	ID   string
	// State is the other end's stored state: StateOpen, StateMerged,
	// StateClosed or "" (see above).
	State string
	// Sources names every source that claims the edge, sorted.
	Sources []string
	// Origins is every claim behind the edge: "derived:stack" for the stack
	// source, "external:<actor>" for each actor that recorded the link. Sorted.
	Origins []string
}

// Open reports that the other end is an open, unmerged PR.
func (e Edge) Open() bool { return e.State == StateOpen }

// Claim is what one Source reports about one edge.
type Claim struct {
	ID      string
	Origins []string
}

// Source is one way of knowing PR-to-PR dependencies. Implementations read
// only through the Context.
type Source interface {
	Name() string
	// Dependencies returns the PRs that PR id depends on.
	Dependencies(c *Context, id string) ([]Claim, error)
	// Dependents returns the PRs that depend on PR id.
	Dependents(c *Context, id string) ([]Claim, error)
}

// Registry is the ordered set of Sources.
type Registry struct {
	sources []Source
}

// NewRegistry registers the first members: stack and external.
func NewRegistry() *Registry {
	r := &Registry{}
	r.Register(stackSource{})
	r.Register(externalSource{})
	return r
}

// Register adds a source. It panics on a duplicate name, as
// classify.Register does: a duplicate is a programming error, not a runtime
// condition.
func (r *Registry) Register(s Source) {
	for _, have := range r.sources {
		if have.Name() == s.Name() {
			panic(fmt.Sprintf("dependency: source %q registered twice", s.Name()))
		}
	}
	r.sources = append(r.sources, s)
}

// Names lists the registered sources in registration order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, s.Name())
	}
	return out
}

// Resolver is the read API for the evaluator and the verbs: every edge from
// every registered source, merged per other end. It indexes the stored PR
// rows once, lazily, so one Resolver answers many questions for the price of
// one table read; build a fresh Resolver per read so it never outlives the
// store state it indexed.
type Resolver struct {
	reg *Registry
	ctx *Context
}

// NewResolver builds a Resolver over the default registry for one repository.
func NewResolver(r Reader, repo string) *Resolver {
	return NewResolverWith(NewRegistry(), r, repo)
}

// NewResolverWith builds a Resolver over a caller-supplied registry.
func NewResolverWith(reg *Registry, r Reader, repo string) *Resolver {
	return &Resolver{reg: reg, ctx: &Context{Reader: r, Repo: repo}}
}

// DependenciesOf returns every PR the given entity depends on, whatever its
// state, ordered by id. An entity type other than pr has none.
func (r *Resolver) DependenciesOf(entityType, id string) ([]Edge, error) {
	return r.collect(entityType, id, Source.Dependencies)
}

// OpenDependenciesOf is DependenciesOf restricted to dependencies that are
// still open: the set a suppression rule waits on. It empties once the last
// dependency has merged or closed.
func (r *Resolver) OpenDependenciesOf(entityType, id string) ([]Edge, error) {
	all, err := r.DependenciesOf(entityType, id)
	if err != nil {
		return nil, err
	}
	var open []Edge
	for _, e := range all {
		if e.Open() {
			open = append(open, e)
		}
	}
	return open, nil
}

// DependentsOf returns every PR that depends on the given entity, ordered by
// id. The reverse of DependenciesOf: the stack source reports a dependent
// only while the entity is itself open.
func (r *Resolver) DependentsOf(entityType, id string) ([]Edge, error) {
	return r.collect(entityType, id, Source.Dependents)
}

func (r *Resolver) collect(entityType, id string, ask func(Source, *Context, string) ([]Claim, error)) ([]Edge, error) {
	if entityType != TypePR || id == "" {
		return nil, nil
	}
	merged := map[string]*Edge{}
	for _, s := range r.reg.sources {
		claims, err := ask(s, r.ctx, id)
		if err != nil {
			return nil, fmt.Errorf("dependency: source %s: %w", s.Name(), err)
		}
		for _, c := range claims {
			e := merged[c.ID]
			if e == nil {
				st, err := r.ctx.stateOf(c.ID)
				if err != nil {
					return nil, err
				}
				e = &Edge{Type: TypePR, ID: c.ID, State: st}
				merged[c.ID] = e
			}
			e.Sources = addSorted(e.Sources, s.Name())
			for _, o := range c.Origins {
				e.Origins = addSorted(e.Origins, o)
			}
		}
	}
	out := make([]Edge, 0, len(merged))
	for _, e := range merged {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func addSorted(in []string, s string) []string {
	for _, have := range in {
		if have == s {
			return in
		}
	}
	in = append(in, s)
	sort.Strings(in)
	return in
}

// Context is what a Source reads: the store and a lazily built index of the
// stored PR rows.
type Context struct {
	Reader Reader
	Repo   string

	indexed bool
	prs     map[string]*prRow
	byBase  map[string][]*prRow
	byHead  map[string][]*prRow
}

// prRow is the part of a stored PR the dependency data needs.
type prRow struct {
	ID     string
	Branch string
	Base   string
	State  string
}

func (p *prRow) open() bool { return p.State == StateOpen }

// index reads every stored PR row once. A row that is not for this
// repository, that the store marks inactive (no longer watched, so its state
// is unreliable) or whose facts do not decode is left out: it is then unknown,
// and an unknown PR is not open.
func (c *Context) index() error {
	if c.indexed {
		return nil
	}
	rows, err := c.Reader.ListEntities()
	if err != nil {
		return fmt.Errorf("dependency: read entities: %w", err)
	}
	c.prs = map[string]*prRow{}
	c.byBase = map[string][]*prRow{}
	c.byHead = map[string][]*prRow{}
	for _, e := range rows {
		if e.Repo != c.Repo || e.EntityType != TypePR || e.Inactive {
			continue
		}
		var facts struct {
			PRShow struct {
				State  string `json:"state"`
				Branch string `json:"branch"`
				Base   string `json:"base"`
				Merged bool   `json:"merged"`
			} `json:"pr_show"`
		}
		if json.Unmarshal([]byte(e.Facts), &facts) != nil {
			continue
		}
		p := &prRow{ID: e.EntityID, Branch: facts.PRShow.Branch, Base: facts.PRShow.Base}
		switch s := strings.ToLower(strings.TrimSpace(facts.PRShow.State)); {
		case facts.PRShow.Merged || s == StateMerged:
			p.State = StateMerged
		case s == StateOpen:
			p.State = StateOpen
		case s == StateClosed:
			p.State = StateClosed
		}
		c.prs[p.ID] = p
		if p.Base != "" {
			c.byBase[p.Base] = append(c.byBase[p.Base], p)
		}
		// A PR whose head branch is its own base branch (a fork's default
		// branch proposed upstream) is not a stack base: every PR against
		// that branch would otherwise appear to depend on it.
		if p.Branch != "" && p.Branch != p.Base {
			c.byHead[p.Branch] = append(c.byHead[p.Branch], p)
		}
	}
	c.indexed = true
	return nil
}

func (c *Context) stateOf(id string) (string, error) {
	if err := c.index(); err != nil {
		return "", err
	}
	if p := c.prs[id]; p != nil {
		return p.State, nil
	}
	return "", nil
}

// repoOf is the "<owner>/<repo>" of a PR id shaped "<owner>/<repo>#<n>".
func repoOf(id string) string {
	if i := strings.LastIndexByte(id, '#'); i >= 0 {
		return id[:i]
	}
	return id
}

type stackSource struct{}

func (stackSource) Name() string { return SourceStack }

const originStack = "derived:stack"

// Dependencies: the open PRs in the same repository whose head branch is this
// PR's base branch.
func (stackSource) Dependencies(c *Context, id string) ([]Claim, error) {
	if err := c.index(); err != nil {
		return nil, err
	}
	a := c.prs[id]
	if a == nil || a.Base == "" {
		return nil, nil
	}
	var out []Claim
	for _, b := range c.byHead[a.Base] {
		if b.ID != a.ID && b.open() && repoOf(b.ID) == repoOf(a.ID) {
			out = append(out, Claim{ID: b.ID, Origins: []string{originStack}})
		}
	}
	return out, nil
}

// Dependents: the PRs in the same repository whose base branch is this PR's
// head branch, while this PR is open.
func (stackSource) Dependents(c *Context, id string) ([]Claim, error) {
	if err := c.index(); err != nil {
		return nil, err
	}
	b := c.prs[id]
	if b == nil || b.Branch == "" || b.Branch == b.Base || !b.open() {
		return nil, nil
	}
	var out []Claim
	for _, a := range c.byBase[b.Branch] {
		if a.ID != b.ID && repoOf(a.ID) == repoOf(b.ID) {
			out = append(out, Claim{ID: a.ID, Origins: []string{originStack}})
		}
	}
	return out, nil
}

type externalSource struct{}

func (externalSource) Name() string { return SourceExternal }

// Dependencies: the PRs this PR has an external depends_on link to.
func (externalSource) Dependencies(c *Context, id string) ([]Claim, error) {
	if ok, err := newSchema(c); err != nil || !ok {
		return nil, err
	}
	rows, err := c.Reader.ListXrefLinksFrom(c.Repo, TypePR, id)
	if err != nil {
		return nil, fmt.Errorf("read links from %s: %w", id, err)
	}
	return externalClaims(rows, func(l store.XrefLink) (string, string) { return l.ToType, l.ToID }), nil
}

// Dependents: the PRs that have an external depends_on link to this PR.
func (externalSource) Dependents(c *Context, id string) ([]Claim, error) {
	if ok, err := newSchema(c); err != nil || !ok {
		return nil, err
	}
	rows, err := c.Reader.ListXrefLinksTo(c.Repo, TypePR, id)
	if err != nil {
		return nil, fmt.Errorf("read links to %s: %w", id, err)
	}
	return externalClaims(rows, func(l store.XrefLink) (string, string) { return l.FromType, l.FromID }), nil
}

// newSchema reports whether the migrated schema (the only one with external
// links) is present. An old-schema store is not an error: the external source
// simply has nothing to say there.
func newSchema(c *Context) (bool, error) {
	if err := c.Reader.RequireNewSchema(); err != nil {
		if errors.Is(err, store.ErrOldSchema) {
			return false, nil
		}
		return false, fmt.Errorf("read schema version: %w", err)
	}
	return true, nil
}

// externalClaims keeps the external depends_on rows whose other end is a PR,
// one Claim per other end carrying every actor's origin. Derived rows with
// the same relation are not external claims and are ignored.
func externalClaims(rows []store.XrefLink, other func(store.XrefLink) (typ, id string)) []Claim {
	byID := map[string]*Claim{}
	var order []string
	for _, l := range rows {
		if l.Relation != RelationDependsOn || !strings.HasPrefix(l.Origin, "external:") {
			continue
		}
		typ, id := other(l)
		if typ != TypePR || id == "" {
			continue
		}
		c := byID[id]
		if c == nil {
			c = &Claim{ID: id}
			byID[id] = c
			order = append(order, id)
		}
		c.Origins = addSorted(c.Origins, l.Origin)
	}
	sort.Strings(order)
	out := make([]Claim, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}
