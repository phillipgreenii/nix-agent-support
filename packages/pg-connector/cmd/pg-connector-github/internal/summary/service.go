package summary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// This file is the summary group's refresh service: the executor the scheduler
// runs (a batch of due and fill summaries in one `nodes(ids:)` call), the
// keep-alive set, the periodic refresh on summary_ttl, and expiry. The
// scheduler (pkg/connectord/scheduler) owns the queue, the classes, the worker
// pool, single-flight and aging; this file says what a summary task does and
// who is worth refreshing.

// BatchFetcher is the part of Fetcher the service uses. *Fetcher satisfies it.
type BatchFetcher interface {
	Fetch(ctx context.Context, call ratefeed.Meta, nodeIDs []string) ([]Summary, int64, error)
}

var _ BatchFetcher = (*Fetcher)(nil)

// ServiceStore is the part of the daemon store the service uses.
type ServiceStore interface {
	Store
	ListEntities(ctx context.Context, kind string) ([]store.EntityRow, error)
	ListGroups(ctx context.Context, kind, id string) ([]store.GroupRow, error)
	ListQueries(ctx context.Context, configuredOnly bool) ([]store.QueryRow, error)
	DeleteEntity(ctx context.Context, kind, id string) error
	// TouchEntity and EnsureEntity change only the columns they own, so a read
	// of the row followed by a write of it cannot overwrite what a concurrent
	// refresh stored meanwhile (the carried-forward mergeable value).
	TouchEntity(ctx context.Context, kind, id string, at time.Time) error
	EnsureEntity(ctx context.Context, kind, id string) error
	// SetKeepAlive changes only the keep_alive column, so a keep-alive flip
	// cannot rewind a version a concurrent change write raised.
	SetKeepAlive(ctx context.Context, kind, id string, keep bool) error
}

var _ ServiceStore = (*store.Store)(nil)

// ErrUnknownEntity is returned for a reference the alias table does not know.
var ErrUnknownEntity = errors.New("summary: unknown entity")

// ServiceConfig wires the service.
type ServiceConfig struct {
	Store     ServiceStore
	Persister *Persister
	Fetcher   BatchFetcher
	Identity  *entity.Identity
	Scheduler *scheduler.Scheduler
	// Clock is the time source; nil means the Persister's, then the system's.
	Clock governor.Clock

	// KeepaliveWindow is keepalive_window and Expiry is expiry; zero means the
	// config defaults.
	KeepaliveWindow time.Duration
	Expiry          time.Duration
	// BatchSize is batch_size; zero means the config default, and anything above
	// MaxBatch is lowered to it.
	BatchSize int

	// OnSummaryRefreshed fires after a refreshed summary is stored, with the
	// summary stored before it (nil for a first sighting) and what Persist did
	// with the new one. Phase 3 queues the dependent groups and classifies
	// changes from it; nothing is registered in Phase 2. It runs on the
	// executor's goroutine and MUST NOT block.
	OnSummaryRefreshed func(prev *Summary, next Stored)
	// OnGone runs inside the store transaction that tombstones an entity
	// GitHub no longer shows, and only when the entity was visible before, with
	// the purpose the failed lookup was admitted under. An error rolls the
	// tombstone back and fails the task. It MUST use only tx.
	OnGone GoneHook
	// OnBatchSent fires for every `nodes(ids:)` batch, once with the node ids
	// that were due (role due) and once with those that filled spare slots (role
	// fill), each only when non-empty, before the call is made.
	OnBatchSent func(ids []string, role scheduler.Role)
}

// Service runs the refresh of the summary group.
type Service struct {
	cfg   ServiceConfig
	clock governor.Clock
}

// NewService returns the service and registers its executor with the
// scheduler under the kind GroupName.
func NewService(cfg ServiceConfig) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Persister == nil, cfg.Fetcher == nil, cfg.Identity == nil, cfg.Scheduler == nil:
		return nil, errors.New("summary: service needs a store, persister, fetcher, identity and scheduler")
	}
	if cfg.KeepaliveWindow <= 0 {
		cfg.KeepaliveWindow = config.DefaultKeepaliveWindow
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = config.DefaultExpiry
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = config.DefaultBatchSize
	}
	cfg.BatchSize = min(cfg.BatchSize, MaxBatch)
	s := &Service{cfg: cfg, clock: cfg.Clock}
	if s.clock == nil {
		s.clock = cfg.Persister.Clock
	}
	if s.clock == nil {
		s.clock = governor.SystemClock{}
	}
	cfg.Scheduler.Register(GroupName, executor{s})
	return s, nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// ---- identity ----------------------------------------------------------------

// nodeID accepts a node id or a wire reference owner/repo#n and returns the node
// id (an entity key). A reference the alias table does not know is
// ErrUnknownEntity.
func (s *Service) nodeID(ctx context.Context, id string) (string, error) {
	if !strings.Contains(id, "#") {
		return id, nil
	}
	ref, err := entity.ParseRef(id)
	if err != nil {
		return "", err
	}
	node, _, ok, err := s.cfg.Identity.NodeID(ctx, ref)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownEntity, id)
	}
	return node, nil
}

// ---- keep-alive set ----------------------------------------------------------

// members returns the node ids that are a member of a configured query. A query
// row's ids may be node ids or wire references; both are normalized to node ids
// (a reference the alias table does not know yet cannot name an entity and is
// left out).
func (s *Service) members(ctx context.Context) (map[string]bool, error) {
	qs, err := s.cfg.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	// References are resolved together, in one alias read: the alias lookup by
	// reference scans the table, so a lookup per member would cost one scan each
	// on every call.
	var refs []entity.Ref
	seen := map[string]bool{}
	for _, q := range qs {
		for _, id := range q.IDs {
			if !strings.Contains(id, "#") {
				set[id] = true
				continue
			}
			ref, err := entity.ParseRef(id)
			if err != nil {
				return nil, err
			}
			if !seen[ref.Key()] {
				seen[ref.Key()] = true
				refs = append(refs, ref)
			}
		}
	}
	if len(refs) == 0 {
		return set, nil
	}
	known, err := s.cfg.Identity.NodeIDs(ctx, refs)
	if err != nil {
		return nil, err
	}
	for _, k := range known {
		set[k.NodeID] = true
	}
	return set, nil
}

// accessedWithin reports whether the entity was accessed within keepalive_window
// of now.
func (s *Service) accessedWithin(row store.EntityRow, now time.Time) bool {
	return !row.LastAccess.IsZero() && now.Sub(row.LastAccess) <= s.cfg.KeepaliveWindow
}

// InKeepAlive reports whether the entity is in the keep-alive set: a member of a
// configured query, or accessed within keepalive_window. A tombstoned entity
// (GitHub no longer returns it) is not.
func (s *Service) InKeepAlive(ctx context.Context, id string) (bool, error) {
	node, err := s.nodeID(ctx, id)
	if err != nil {
		return false, err
	}
	row, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
	if err != nil || !ok {
		return false, err
	}
	m, err := s.members(ctx)
	if err != nil {
		return false, err
	}
	return s.inSet(row, m, s.now()), nil
}

func (s *Service) inSet(row store.EntityRow, members map[string]bool, now time.Time) bool {
	if !row.TombstonedAt.IsZero() {
		return false
	}
	return members[row.ID] || s.accessedWithin(row, now)
}

// taskFor is the task that refreshes a summary due at due: class 3 for a member
// of a configured query, class 4 for an entity kept alive by access alone.
func taskFor(node string, member bool, due, access time.Time) scheduler.Task {
	class := governor.ClassKeepAlive
	if member {
		class = governor.ClassQuery
	}
	return scheduler.Task{Kind: GroupName, Entity: node, Group: GroupName, Class: class, Due: due, Access: access}
}

// ---- entry points for the other parts ----------------------------------------

// Access records that a caller read the entity (last_access = now), which keeps
// it in the keep-alive set and raises its refresh priority: a refresh is queued
// (due when its summary goes stale, now when it has none) and a queued one is
// moved up behind the most recently accessed entities. id is a node id or a
// wire reference. An entity the store has no row for gets one.
func (s *Service) Access(ctx context.Context, id string) error {
	node, err := s.nodeID(ctx, id)
	if err != nil {
		return err
	}
	row, err := s.recordAccess(ctx, node)
	if err != nil {
		return err
	}
	return s.queueCycle(ctx, row)
}

// recordAccess sets last_access = now and the keep-alive flag, creating the
// entity row when there is none.
func (s *Service) recordAccess(ctx context.Context, node string) (store.EntityRow, error) {
	if err := s.cfg.Store.TouchEntity(ctx, entity.KindPR, node, s.now()); err != nil {
		return store.EntityRow{}, err
	}
	row, _, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
	return row, err
}

// queueCycle queues the background refresh of an accessed entity: due when its
// summary goes stale (now when it has none), at the access time's priority.
func (s *Service) queueCycle(ctx context.Context, row store.EntityRow) error {
	if !row.TombstonedAt.IsZero() {
		return nil
	}
	due := s.now()
	if g, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, row.ID, GroupName); err != nil {
		return err
	} else if ok && !g.FreshUntil.IsZero() {
		due = g.FreshUntil
	}
	m, err := s.members(ctx)
	if err != nil {
		return err
	}
	return s.cfg.Scheduler.Submit(taskFor(row.ID, m[row.ID], due, row.LastAccess))
}

// Track records entities a query listed. refs are in the wire form owner/repo#n
// and are resolved to node ids through the alias table; each resolved entity
// that has no row gets one, and a class 3 summary task is queued for it, due
// now. A reference the alias table does not know cannot be fetched by node id:
// it is returned in unresolved for the caller to resolve (entity.Resolver) and
// Track again.
func (s *Service) Track(ctx context.Context, refs []string) (unresolved []string, err error) {
	now := s.now()
	for _, r := range refs {
		node, err := s.nodeID(ctx, r)
		if errors.Is(err, ErrUnknownEntity) {
			unresolved = append(unresolved, r)
			continue
		}
		if err != nil {
			return unresolved, err
		}
		if err := s.cfg.Store.EnsureEntity(ctx, entity.KindPR, node); err != nil {
			return unresolved, err
		}
		row, _, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
		if err != nil {
			return unresolved, err
		}
		// A task already queued keeps its Due; a fresh summary is not refetched
		// (the executor skips a task whose group is inside fresh_until).
		due := now
		if g, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, GroupName); err != nil {
			return unresolved, err
		} else if ok && g.FreshUntil.After(now) {
			due = g.FreshUntil
		}
		t := scheduler.Task{
			Kind: GroupName, Entity: node, Group: GroupName, Class: governor.ClassQuery,
			Due: due, Access: row.LastAccess,
		}
		if err := s.cfg.Scheduler.Submit(t); err != nil {
			return unresolved, err
		}
	}
	return unresolved, nil
}

// Position is the rank of the entity's queued summary refresh among the queued
// tasks of its class (1 is next in line); false when none is queued. id is a node
// id or a wire reference.
func (s *Service) Position(ctx context.Context, id string) (int, bool, error) {
	node, err := s.nodeID(ctx, id)
	if err != nil {
		return 0, false, err
	}
	n, ok := s.cfg.Scheduler.Position(node, GroupName)
	return n, ok, nil
}

// InteractiveResult is the outcome for one id of Interactive.
type InteractiveResult struct {
	// ID is the id as the caller gave it; NodeID the entity key.
	ID     string
	NodeID string
	// Fresh is true when the entity's summary is inside fresh_until: it was, or
	// the refresh finished before the caller's deadline and was stored.
	Fresh bool
	// Pending is true when the refresh was still running at the caller's
	// deadline: it is not cancelled and completes in the background.
	Pending bool
	// Err is why the summary is not fresh: the governor's refusal (naming the cap
	// or the reserve), the origin's failure, or ErrUnknownEntity.
	Err error
}

// Interactive makes sure the summary of every id is fresh for a caller who is
// waiting. A fresh summary costs nothing. A stale or missing one is queued as
// class 1, goes out at once in a `nodes(ids:)` call with spare slots filled (the
// batch never waits to fill), and is shared with every other caller asking for
// the same entity. It returns when every refresh has finished or ctx is done; a
// refresh still running then is NOT cancelled, it completes in the background
// and is stored, and its result is Pending. The caller serves the stale content
// (inside expiry) or answers unavailable for a result that is not Fresh. Each id
// is also recorded as an access.
func (s *Service) Interactive(ctx context.Context, ids []string) []InteractiveResult {
	return s.interactive(ctx, ids, false)
}

// InteractiveFresh is Interactive for a caller that asked for fresh: true: every
// id goes to the origin, including one whose summary is still inside fresh_until
// (still single-flight with any caller already fetching it).
func (s *Service) InteractiveFresh(ctx context.Context, ids []string) []InteractiveResult {
	return s.interactive(ctx, ids, true)
}

func (s *Service) interactive(ctx context.Context, ids []string, force bool) []InteractiveResult {
	out := make([]InteractiveResult, len(ids))
	flights := make([]*scheduler.Flight, len(ids))
	for i, id := range ids {
		out[i].ID = id
		node, err := s.nodeID(ctx, id)
		if err != nil {
			out[i].Err = err
			continue
		}
		out[i].NodeID = node
		row, err := s.recordAccess(ctx, node)
		if err != nil {
			out[i].Err = err
			continue
		}
		g, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, GroupName)
		if err != nil {
			out[i].Err = err
			continue
		}
		if ok && !force && g.FreshUntil.After(s.now()) {
			out[i].Fresh = true
			out[i].Err = s.queueCycle(ctx, row)
			continue
		}
		// Not through Access: a background task queued first could be sent out as
		// class 4 before the class 1 request reaches the scheduler.
		f, err := s.cfg.Scheduler.SubmitInteractive(scheduler.Task{
			Kind: GroupName, Entity: node, Group: GroupName, Access: row.LastAccess,
		})
		if err != nil {
			out[i].Err = err
			continue
		}
		flights[i] = f
	}
	for i, f := range flights {
		if f == nil {
			continue
		}
		select {
		case <-f.Done():
			r := f.Result()
			switch {
			case r.Err != nil:
				out[i].Err = r.Err
			default:
				out[i].Fresh = true // refreshed, or found fresh by the executor
			}
		case <-ctx.Done():
			out[i].Pending = true
		}
	}
	return out
}

// Rebuild rebuilds the scheduler's queue from the stored due times, as the
// daemon does on start: every entity in the keep-alive set gets a task due when
// its summary's fresh_until says (now when it has none or it is past). Nothing
// is fetched here; the scheduler asks the governor before every spend, so a
// restart after a long outage catches up at the governor's rate and never
// spends more than the configured budget. It returns how many tasks it queued.
func (s *Service) Rebuild(ctx context.Context) (int, error) {
	rows, err := s.cfg.Store.ListEntities(ctx, entity.KindPR)
	if err != nil {
		return 0, err
	}
	m, err := s.members(ctx)
	if err != nil {
		return 0, err
	}
	now := s.now()
	n := 0
	for _, row := range rows {
		if !s.inSet(row, m, now) {
			continue
		}
		due := time.Time{}
		g, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, row.ID, GroupName)
		if err != nil {
			return n, err
		}
		if ok {
			due = g.FreshUntil
		}
		if err := s.cfg.Scheduler.Submit(taskFor(row.ID, m[row.ID], due, row.LastAccess)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Sweep applies expiry and refreshes the keep-alive flag. An entity outside the
// keep-alive set whose every group's fetched_at is older than expiry is evicted
// with its groups (change-log rows are not touched) and its queued tasks are
// canceled; an entity in the set is never evicted. An entity with no group yet
// is evicted only when it was accessed before and that access is older than
// expiry. It returns how many entities it evicted. The daemon calls it on a
// timer.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	rows, err := s.cfg.Store.ListEntities(ctx, entity.KindPR)
	if err != nil {
		return 0, err
	}
	m, err := s.members(ctx)
	if err != nil {
		return 0, err
	}
	now := s.now()
	evicted := 0
	for _, row := range rows {
		in := s.inSet(row, m, now)
		if in != row.KeepAlive {
			if err := s.cfg.Store.SetKeepAlive(ctx, entity.KindPR, row.ID, in); err != nil {
				return evicted, err
			}
		}
		if in {
			continue
		}
		groups, err := s.cfg.Store.ListGroups(ctx, entity.KindPR, row.ID)
		if err != nil {
			return evicted, err
		}
		if !s.expired(row, groups, now) {
			continue
		}
		s.cfg.Scheduler.CancelEntity(row.ID)
		if err := s.cfg.Store.DeleteEntity(ctx, entity.KindPR, row.ID); err != nil {
			return evicted, err
		}
		evicted++
	}
	return evicted, nil
}

// expired reports whether every group is older than expiry (the newest
// fetched_at decides), or, for an entity with no group, whether its last access
// is.
func (s *Service) expired(row store.EntityRow, groups []store.GroupRow, now time.Time) bool {
	newest := row.LastAccess
	if len(groups) > 0 {
		newest = time.Time{}
		for _, g := range groups {
			if g.FetchedAt.After(newest) {
				newest = g.FetchedAt
			}
		}
	}
	return !newest.IsZero() && now.Sub(newest) > s.cfg.Expiry
}

// ---- the executor ------------------------------------------------------------

// executor adapts the service to scheduler.Executor.
type executor struct{ s *Service }

func (e executor) MaxBatch() int { return e.s.cfg.BatchSize }

// Spend is one point for a batch of 1 to MaxBatch ids (see MaxBatch).
func (e executor) Spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: 1}
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

// Execute runs one batch: it drops the items that need no work (outside the
// keep-alive set, or already fresh), fetches the rest in one `nodes(ids:)` call,
// stores each summary, fires OnSummaryRefreshed and books the next refresh.
func (e executor) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	s := e.s
	res := make([]scheduler.Result, len(items))
	now := s.now()
	members, err := s.members(ctx)
	if err != nil {
		return failAll(res, err)
	}

	// Decide which items go out, and what class the call is admitted as: the
	// best class among the due items.
	var (
		send  []int
		class = governor.ClassFill
		due   []string
		fill  []string
		rows  = map[int]store.EntityRow{}
	)
	for i, it := range items {
		node := it.Task.Entity
		row, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, node)
		if err != nil {
			res[i].Err = err
			continue
		}
		rows[i] = row
		if it.Task.Class >= governor.ClassKeepAlive {
			// A background refresh is for the keep-alive set only.
			if !ok || !s.inSet(row, members, now) {
				res[i].Skipped = true
				continue
			}
		}
		if it.Role == scheduler.RoleDue && it.Task.Class >= governor.ClassQuery {
			// Another path (an interactive read, an earlier batch) may have
			// refreshed it since this task was queued: book the next cycle.
			g, ok, err := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, GroupName)
			if err != nil {
				res[i].Err = err
				continue
			}
			if ok && g.FreshUntil.After(now) {
				res[i].Skipped = true
				_ = s.cfg.Scheduler.Reschedule(taskFor(node, members[node], g.FreshUntil, row.LastAccess))
				continue
			}
		}
		send = append(send, i)
		if it.Role == scheduler.RoleDue {
			class = min(class, it.Task.Class)
			due = append(due, node)
		} else {
			fill = append(fill, node)
		}
	}
	if len(send) == 0 {
		return res
	}
	if class == governor.ClassFill { // only fill riders left: admit as a keep-alive refresh
		class = governor.ClassKeepAlive
	}

	ids := make([]string, len(send))
	for k, i := range send {
		ids[k] = items[i].Task.Entity
	}
	if s.cfg.OnBatchSent != nil {
		if len(due) > 0 {
			s.cfg.OnBatchSent(due, scheduler.RoleDue)
		}
		if len(fill) > 0 {
			s.cfg.OnBatchSent(fill, scheduler.RoleFill)
		}
	}

	got, _, ferr := s.cfg.Fetcher.Fetch(ctx, ratefeed.Meta{Class: class, Purpose: purposeOf(class)}, ids)
	for k, i := range send {
		if k >= len(got) {
			err := ferr
			if err == nil {
				err = fmt.Errorf("summary: no answer for %s", ids[k])
			}
			res[i].Err = err
			s.recordError(ctx, ids[k], err)
			continue
		}
		res[i].Err = s.store(ctx, got[k], rows[i], members, FetchInfo{Class: class, Purpose: purposeOf(class)})
	}
	return res
}

func failAll(res []scheduler.Result, err error) []scheduler.Result {
	for i := range res {
		res[i].Err = err
	}
	return res
}

// store persists one fetched summary and books the next refresh.
func (s *Service) store(ctx context.Context, sum Summary, row store.EntityRow, members map[string]bool, info FetchInfo) error {
	if sum.Gone {
		// GitHub no longer returns the node: stop refreshing it and let expiry
		// collect it.
		if row.ID != "" {
			return s.tombstone(ctx, row.ID, info.Purpose)
		}
		return nil
	}
	st, err := s.cfg.Persister.PersistWith(ctx, sum, info)
	if err != nil {
		return err
	}
	if s.cfg.OnSummaryRefreshed != nil {
		s.cfg.OnSummaryRefreshed(st.Prev, st)
	}
	// Book the next cycle while the entity stays in the set.
	now := s.now()
	cur, ok, err := s.cfg.Store.GetEntity(ctx, entity.KindPR, sum.NodeID)
	if err != nil || !ok {
		return err
	}
	if !s.inSet(cur, members, now) {
		return nil
	}
	due := now.Add(s.cfg.Persister.FreshFor(sum.State))
	return s.cfg.Scheduler.Reschedule(taskFor(sum.NodeID, members[sum.NodeID], due, cur.LastAccess))
}

// tombstone sets tombstoned_at and, only when the entity was visible before,
// runs OnGone, in one transaction. The row is read inside the transaction, so
// the write cannot rewind a version raised meanwhile.
func (s *Service) tombstone(ctx context.Context, node string, purpose governor.Purpose) error {
	return s.cfg.Store.InTx(ctx, func(tx *store.Tx) error {
		row, ok, err := tx.GetEntity(ctx, entity.KindPR, node)
		if err != nil || !ok {
			return err
		}
		visible := row.TombstonedAt.IsZero()
		row.TombstonedAt = s.now()
		if err := tx.PutEntity(ctx, row); err != nil {
			return err
		}
		if visible && s.cfg.OnGone != nil {
			return s.cfg.OnGone(ctx, tx, node, purpose)
		}
		return nil
	})
}

// recordError notes a failed fetch on the stored group, when there is one.
func (s *Service) recordError(ctx context.Context, node string, err error) {
	var refusal *governor.Refusal
	if errors.As(err, &refusal) {
		return // a refusal is the budget speaking, not the origin failing
	}
	g, ok, gerr := s.cfg.Store.GetGroup(ctx, entity.KindPR, node, GroupName)
	if gerr != nil || !ok {
		return
	}
	g.LastError = err.Error()
	_ = s.cfg.Store.PutGroup(ctx, g)
}
