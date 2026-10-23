package enginetest

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Content is the stored primary content of the invented entity, as the scripted
// classifier reads it. A test builds one, marshals it with Encode and stores it
// as the primary group (Rig.Seed does both).
type Content struct {
	Ref   string `json:"ref"`
	Stamp string `json:"stamp"`
	Title string `json:"title"`
	// Open is the flag whose flip is a toggled change.
	Open     bool `json:"open"`
	Gone     bool `json:"gone"`
	Terminal bool `json:"terminal"`
	// Await names the groups a change of this content asks to be fetched again
	// before its row settles.
	Await []string          `json:"await,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
	// At is the origin's own update time.
	At time.Time `json:"at"`
}

// Encode is the JSON the classifier decodes.
func (c Content) Encode() []byte {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return b
}

// Classifier is the scripted engine.Classifier: it reads Content. A changed title
// is the Retitled kind with a field "title", a flipped Open flag the Toggled kind
// with a field "open", and the groups the new content names in Await are the
// awaited set; a changed non-primary group is the Tweaked kind with a field named
// by the group. A test that needs another answer sets a hook, which replaces the
// default when non-nil.
type Classifier struct {
	reg Registration

	// OnPrimary replaces the primary classification.
	OnPrimary func(prev, next Content) (engine.Result, []string, error)
	// OnGroup replaces the group classification.
	OnGroup func(name string, prev, next []byte) (engine.Result, error)
}

// NewClassifier returns the classifier of the registration.
func NewClassifier(reg Registration) *Classifier { return &Classifier{reg: reg} }

var _ engine.Classifier = (*Classifier)(nil)

func decode(b []byte) (Content, error) {
	var c Content
	err := json.Unmarshal(b, &c)
	return c, err
}

// Describe implements engine.Classifier.
func (c *Classifier) Describe(content []byte) (engine.Primary, error) {
	v, err := decode(content)
	if err != nil {
		return engine.Primary{}, err
	}
	return engine.Primary{Ref: v.Ref, Stamp: v.Stamp, OriginAt: v.At, Gone: v.Gone, Terminal: v.Terminal, Attrs: v.Attrs}, nil
}

func quote(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Primary implements engine.Classifier.
func (c *Classifier) Primary(prev, next []byte, _ int) (engine.Result, []string, error) {
	p, err := decode(prev)
	if err != nil {
		return engine.Result{}, nil, err
	}
	n, err := decode(next)
	if err != nil {
		return engine.Result{}, nil, err
	}
	if c.OnPrimary != nil {
		return c.OnPrimary(p, n)
	}
	var res engine.Result
	if p.Title != n.Title {
		res.Kinds = append(res.Kinds, c.reg.Retitled)
		res.Fields = append(res.Fields, engine.Field{Name: "title", Before: quote(p.Title), After: quote(n.Title)})
	}
	if p.Open != n.Open {
		res.Kinds = append(res.Kinds, c.reg.Toggled)
		res.Fields = append(res.Fields, engine.Field{Name: "open", Before: quote(p.Open), After: quote(n.Open)})
	}
	res.Kinds = engine.OrderKinds(c.reg.Spec.Changes, res.Kinds)
	res.Fields = engine.Normalize(res.Fields)
	return res, slices.Clone(n.Await), nil
}

// Group implements engine.Classifier.
func (c *Classifier) Group(name string, prev, next []byte, _ int) (engine.Result, error) {
	if c.OnGroup != nil {
		return c.OnGroup(name, prev, next)
	}
	if string(prev) == string(next) {
		return engine.Result{}, nil
	}
	return engine.Result{
		Kinds:  []string{c.reg.Tweaked},
		Fields: []engine.Field{{Name: name, Before: quote(string(prev)), After: quote(string(next))}},
	}, nil
}

// Counts implements engine.Classifier: a fetch of the awaited group that started
// at or after the awaiting time counts.
func (c *Classifier) Counts(a engine.Awaiting, fetched store.GroupRow, _ store.PendingChange, started time.Time) bool {
	return a.Group == fetched.Group && !started.Before(a.Since)
}

// ListCall is one List call the Lister saw.
type ListCall struct {
	Key   query.Key
	Exprs []string
	Meta  query.CallMeta
}

// Lister is the scripted query.Lister: it answers each configured query name
// from the listing the test scripted for it, an empty complete one for a name
// with none, and records every call.
type Lister struct {
	mu      sync.Mutex
	answers map[string]listAnswer
	calls   []ListCall
}

type listAnswer struct {
	listing query.Listing
	err     error
}

// NewLister returns a Lister with nothing scripted.
func NewLister() *Lister { return &Lister{answers: map[string]listAnswer{}} }

var _ query.Lister = (*Lister)(nil)

// Script sets the answer of the query name: the members, in order, and whether
// the listing is the whole result.
func (l *Lister) Script(name string, complete bool, members ...query.Member) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers[name] = listAnswer{listing: query.Listing{Members: slices.Clone(members), Complete: complete}}
}

// Fail makes every List of the query name fail with err.
func (l *Lister) Fail(name string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answers[name] = listAnswer{err: err}
}

// List implements query.Lister.
func (l *Lister) List(_ context.Context, key query.Key, exprs []string, meta query.CallMeta) (query.Listing, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, ListCall{Key: key, Exprs: slices.Clone(exprs), Meta: meta})
	a, ok := l.answers[key.Name]
	if !ok {
		return query.Listing{Complete: true}, nil
	}
	return a.listing, a.err
}

// Calls returns every List call so far.
func (l *Lister) Calls() []ListCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// Aliases is the in-memory query.Aliases, including the reverse lookup Ref: Put
// moves an id's reference in place (a rename) and Resolve finds the id whose
// reference equals the one given under the registration's fold.
type Aliases struct {
	fold func(string) string
	mu   sync.Mutex
	byID map[string]string
}

// NewAliases returns an empty alias table comparing references under fold (nil
// is the identity).
func NewAliases(fold func(string) string) *Aliases {
	if fold == nil {
		fold = func(s string) string { return s }
	}
	return &Aliases{fold: fold, byID: map[string]string{}}
}

var _ query.Aliases = (*Aliases)(nil)

// Put implements query.Aliases.
func (a *Aliases) Put(_ context.Context, id, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byID[id] = ref
	return nil
}

// Resolve implements query.Aliases.
func (a *Aliases) Resolve(_ context.Context, refs []string) (map[string]query.Alias, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]query.Alias{}
	for _, ref := range refs {
		for id, stored := range a.byID {
			if a.fold(stored) == a.fold(ref) {
				out[ref] = query.Alias{ID: id, CanonicalRef: stored}
			}
		}
	}
	return out, nil
}

// Ref implements query.Aliases: the reference recorded for the id.
func (a *Aliases) Ref(_ context.Context, id string) (string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref, ok := a.byID[id]
	return ref, ok, nil
}

// Settings is the scripted query.Settings: the configured queries, their
// time-to-live and which of them are watched are all maps the test fills.
type Settings struct {
	mu      sync.Mutex
	exprs   map[string][]string
	ttl     map[string]time.Duration
	watched map[string]bool
	// DefaultTTL is the time-to-live of a configured query with none of its own;
	// AdHocTTL that of a key with arguments.
	DefaultTTL, AdHocTTL time.Duration
}

// NewSettings returns settings with no query and one-minute time-to-live values.
func NewSettings() *Settings {
	return &Settings{
		exprs: map[string][]string{}, ttl: map[string]time.Duration{}, watched: map[string]bool{},
		DefaultTTL: time.Minute, AdHocTTL: time.Minute,
	}
}

var _ query.Settings = (*Settings)(nil)

// Define configures the query name with its expressions.
func (s *Settings) Define(name string, exprs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exprs[name] = slices.Clone(exprs)
}

// SetTTL sets the time-to-live of the configured query name.
func (s *Settings) SetTTL(name string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttl[name] = ttl
}

// SetWatched sets what Watched answers for the query name.
func (s *Settings) SetWatched(name string, watched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watched[name] = watched
}

// Expr implements query.Settings.
func (s *Settings) Expr(name string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.exprs[name]
	return slices.Clone(e), ok
}

// Watched implements query.Settings: the answer set with SetWatched, false
// otherwise.
func (s *Settings) Watched(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watched[name]
}

// Names implements query.Settings.
func (s *Settings) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.exprs))
	for n := range s.exprs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TTL implements query.Settings.
func (s *Settings) TTL(key query.Key) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key.Adhoc() {
		return s.AdHocTTL
	}
	if d, ok := s.ttl[key.Name]; ok {
		return d
	}
	return s.DefaultTTL
}

// Tracker is the query.Tracker that records the references it was given.
type Tracker struct {
	mu      sync.Mutex
	tracked [][]string
	// Err is what Track returns as its error.
	Err error
}

var _ query.Tracker = (*Tracker)(nil)

// Track implements query.Tracker.
func (t *Tracker) Track(_ context.Context, refs []string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tracked = append(t.tracked, slices.Clone(refs))
	return nil, t.Err
}

// Calls returns the reference lists of every Track call so far.
func (t *Tracker) Calls() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.tracked)
}

// Confirmer is the scripted query.Confirmer: a departure is confirmed with the
// state scripted for the id, State otherwise.
type Confirmer struct {
	mu     sync.Mutex
	states map[string]query.Confirmation
	// State is the stored state a departure of an unscripted id reports.
	State string
	// Err is what Confirm returns as its error, when set.
	Err error
}

var _ query.Confirmer = (*Confirmer)(nil)

// NewConfirmer returns a Confirmer that reports the state "steady_state".
func NewConfirmer() *Confirmer {
	return &Confirmer{states: map[string]query.Confirmation{}, State: "steady_state"}
}

// Script sets what the confirming read of the id finds.
func (c *Confirmer) Script(id string, found query.Confirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states[id] = found
}

// Confirm implements query.Confirmer.
func (c *Confirmer) Confirm(_ context.Context, id, _ string) (query.Confirmation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Err != nil {
		return query.Confirmation{}, c.Err
	}
	if f, ok := c.states[id]; ok {
		return f, nil
	}
	return query.Confirmation{State: c.State}, nil
}

// Auth is the typed unauthenticated state of the runner: Set makes every run
// answer with the error until it is cleared with nil.
type Auth struct {
	mu  sync.Mutex
	bad error
}

var _ query.Auth = (*Auth)(nil)

// Set sets the error (nil clears it).
func (a *Auth) Set(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bad = err
}

// Unauthenticated implements query.Auth.
func (a *Auth) Unauthenticated() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bad != nil, a.bad
}
