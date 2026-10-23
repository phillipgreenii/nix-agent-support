package groupsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The origins a refresh request can note, as the change engine reads them when
// it classifies the refresh that follows (the shared engine's words).
const (
	OriginQuery   = engine.OriginQuery
	OriginRefresh = engine.OriginRefresh
	OriginWrite   = engine.OriginWrite
	OriginRead    = engine.OriginRead
)

// Service runs the refresh of the non-primary groups. Its methods are added by
// the steps of the hoist; this file holds the struct and the helpers the moved
// methods share (identity, the keep-alive set and the group registry).
type Service struct {
	cfg   Config
	clock governor.Clock

	mu       sync.Mutex
	fetchers map[string]Fetcher
	observer Observer
	hints    OriginHints
	// commits counts the refreshes stored per (entity, group) since start; an
	// Ensure compares it before and after its fetch to say whether the origin was
	// asked.
	commits map[pairKey]uint64

	writes writeLocks
}

type pairKey struct{ id, group string }

// New returns the service. It validates what the service uses and no more: the
// Spec fields it reads (a non-empty EntityKind, a PrimaryGroup that is one of
// Groups, no duplicate group) and the Config members its methods call; a Spec
// without Views or Roles is accepted, so the full registry validation is not
// repeated here. Expiry has no default in shared code and MUST be positive.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Scheduler == nil, cfg.Describer == nil, cfg.Aliases == nil,
		cfg.KeepAlive == nil, cfg.Errors == nil, cfg.ParseID == nil,
		cfg.NoSuchEntity == nil, cfg.BadRef == nil, cfg.UnknownEntity == nil:
		return nil, errors.New("groupsvc: service needs a store, scheduler, describer, alias table, keep-alive rule, error policy, id parser and the three entity sentinels")
	case len(cfg.Rules.Groups) == 0:
		return nil, errors.New("groupsvc: service needs a rules table")
	case cfg.Expiry <= 0:
		return nil, errors.New("groupsvc: expiry must be positive")
	case cfg.Spec.EntityKind == "":
		return nil, errors.New("groupsvc: spec has no entity kind")
	case !slices.Contains(cfg.Spec.Groups, cfg.Spec.PrimaryGroup):
		return nil, fmt.Errorf("groupsvc: primary group %q is not one of the spec's groups", cfg.Spec.PrimaryGroup)
	}
	seen := map[string]bool{}
	for _, g := range cfg.Spec.Groups {
		if seen[g] {
			return nil, fmt.Errorf("groupsvc: group %q is listed twice in the spec", g)
		}
		seen[g] = true
	}
	s := &Service{cfg: cfg, clock: cfg.Clock, fetchers: map[string]Fetcher{}, commits: map[pairKey]uint64{}}
	if s.clock == nil {
		s.clock = governor.SystemClock{}
	}
	s.writes.init()
	return s, nil
}

// Register installs the executor of the fetcher's group under the scheduler kind
// f.Group(). Registering a group again replaces the fetcher. It panics for the
// primary group (the connector's own executor owns it) and for a name outside
// Spec.Groups: both are wiring mistakes.
func (s *Service) Register(f Fetcher) {
	g := f.Group()
	if g == s.cfg.Spec.PrimaryGroup || !s.isGroup(g) {
		panic(fmt.Sprintf("groupsvc: cannot register a fetcher for group %q", g))
	}
	s.mu.Lock()
	s.fetchers[g] = f
	s.mu.Unlock()
	s.cfg.Scheduler.Register(g, &executor{s: s, f: f})
}

// SetObserver installs the observer; nil means none.
func (s *Service) SetObserver(o Observer) {
	s.mu.Lock()
	s.observer = o
	s.mu.Unlock()
}

// SetOriginHints installs the receiver of the origin hints InvalidateAndQueueAs
// records; nil means none.
func (s *Service) SetOriginHints(h OriginHints) {
	s.mu.Lock()
	s.hints = h
	s.mu.Unlock()
}

// Revalidated forwards one revalidation result to the observer.
func (s *Service) Revalidated(group, result string) {
	if o := s.obs(); o != nil {
		o.Revalidated(group, result)
	}
}

func (s *Service) commitCount(id, group string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits[pairKey{id, group}]
}

func (s *Service) countCommit(id, group string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commits == nil {
		s.commits = map[pairKey]uint64{}
	}
	s.commits[pairKey{id, group}]++
}

func (s *Service) now() time.Time { return s.clock.Now() }

// fold is Spec.FoldRef: the form in which two references of one entity compare
// equal (the identity when the connector sets none).
func (s *Service) fold(ref string) string {
	if f := s.cfg.Spec.FoldRef; f != nil {
		return f(ref)
	}
	return ref
}

// isGroup reports whether name is one of the spec's groups.
func (s *Service) isGroup(name string) bool {
	return slices.Contains(s.cfg.Spec.Groups, name)
}

func (s *Service) fetcher(group string) (Fetcher, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.fetchers[group]
	return f, ok
}

// registered returns the groups that have a fetcher, in the order of
// Spec.Groups.
func (s *Service) registered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, g := range s.cfg.Spec.Groups {
		if _, ok := s.fetchers[g]; ok {
			out = append(out, g)
		}
	}
	return out
}

func (s *Service) obs() Observer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observer
}

func (s *Service) originHints() OriginHints {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hints
}

// canonical maps an id (a store id or a wire reference) to the entity key with
// no origin call. A reference the alias table does not know is returned in its
// folded form (Spec.FoldRef) with known false.
func (s *Service) canonical(ctx context.Context, id string) (key string, known bool, err error) {
	wire, isRef, perr := s.cfg.ParseID(id)
	if !isRef {
		return id, true, nil
	}
	if perr != nil {
		return s.fold(id), false, nil
	}
	found, err := s.cfg.Aliases.Resolve(ctx, []string{wire})
	if err != nil {
		return "", false, err
	}
	a, ok := found[wire]
	if !ok || a.ID == "" {
		return s.fold(id), false, nil
	}
	return a.ID, true, nil
}

// ---- keep-alive set ----------------------------------------------------------

// members returns the store ids that are a member of a configured query (a
// query row's ids may be store ids or wire references).
func (s *Service) members(ctx context.Context) (map[string]bool, error) {
	qs, err := s.cfg.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	// References are resolved together, in one alias read: a lookup by reference
	// can cost a scan of the table, so a canonical() per member would cost one scan
	// each on every call. A reference that does not parse or is not known names no
	// entity and is left out, as canonical reports it.
	var refs []string
	seen := map[string]bool{}
	for _, q := range qs {
		for _, id := range q.IDs {
			wire, isRef, perr := s.cfg.ParseID(id)
			if !isRef {
				set[id] = true
				continue
			}
			if perr != nil || seen[s.fold(wire)] {
				continue
			}
			seen[s.fold(wire)] = true
			refs = append(refs, wire)
		}
	}
	if len(refs) == 0 {
		return set, nil
	}
	known, err := s.cfg.Aliases.Resolve(ctx, refs)
	if err != nil {
		return nil, err
	}
	for _, a := range known {
		if a.ID != "" {
			set[a.ID] = true
		}
	}
	return set, nil
}

// inKeepAlive is the retention rule with the member set already in hand: a
// tombstoned entity is out; a member of a configured query is in; everyone else
// is the connector's call (Config.KeepAlive).
func (s *Service) inKeepAlive(ctx context.Context, row store.EntityRow, members map[string]bool) (bool, error) {
	if !row.TombstonedAt.IsZero() {
		return false, nil
	}
	if members[row.ID] {
		return true, nil
	}
	return s.cfg.KeepAlive.Retain(ctx, row.ID)
}

// classFor is the class of a background task: 3 for a member of a configured
// query, 4 for an entity kept alive by access alone.
func classFor(member bool) governor.Class {
	if member {
		return governor.ClassQuery
	}
	return governor.ClassKeepAlive
}

func purposeOf(class governor.Class) governor.Purpose {
	switch class {
	case governor.ClassInteractive:
		return governor.PurposeInteractive
	case governor.ClassFill:
		return governor.PurposeFill
	default:
		return governor.PurposeRefresh
	}
}

// loadPrimary reads the entity's stored primary content and describes it through
// Config.Describer; have is false when no content is stored.
func (s *Service) loadPrimary(ctx context.Context, id string) (engine.Primary, store.GroupRow, bool, error) {
	row, ok, err := s.cfg.Store.GetGroup(ctx, s.cfg.Spec.EntityKind, id, s.cfg.Spec.PrimaryGroup)
	if err != nil || !ok || len(row.Content) == 0 {
		return engine.Primary{}, row, false, err
	}
	prim, err := s.cfg.Describer.Describe(row.Content)
	if err != nil {
		return engine.Primary{}, row, false, fmt.Errorf("groupsvc: decode primary content of %s: %w", id, err)
	}
	return prim, row, true, nil
}
