package groupsvc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
)

// writeLocks is the in-process lock of the write rule: writes to ONE entity
// (keyed by store id) run one at a time, writes to different entities run in
// parallel.
type writeLocks struct {
	mu   sync.Mutex
	keys map[string]*keyLock
	// inflight counts SerializeWrite calls from entry to return, the waiting ones
	// included; idle is closed when it drops to zero while a DrainWrites waits.
	inflight int
	idle     chan struct{}
}

type keyLock struct {
	ch   chan struct{} // a token in ch means the lock is held
	refs int
}

func (w *writeLocks) init() { w.keys = map[string]*keyLock{} }

func (w *writeLocks) enter() {
	w.mu.Lock()
	w.inflight++
	w.mu.Unlock()
}

func (w *writeLocks) leave() {
	w.mu.Lock()
	w.inflight--
	if w.inflight == 0 && w.idle != nil {
		close(w.idle)
		w.idle = nil
	}
	w.mu.Unlock()
}

// ref returns the key's lock, counting one more user of it.
func (w *writeLocks) ref(key string) *keyLock {
	w.mu.Lock()
	defer w.mu.Unlock()
	kl := w.keys[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		w.keys[key] = kl
	}
	kl.refs++
	return kl
}

func (w *writeLocks) unref(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if kl := w.keys[key]; kl != nil {
		if kl.refs--; kl.refs <= 0 {
			delete(w.keys, key)
		}
	}
}

// SerializeWrite runs fn while holding the in-process lock of the entity: writes
// to one entity (keyed by store id) run one at a time, writes to different
// entities run in parallel. id is a store id or a wire reference; a reference is
// canonicalized through the alias table with no origin call, and an unknown
// reference is keyed by its folded form (Spec.FoldRef). The wait for the lock
// honors ctx's deadline: when ctx ends first, SerializeWrite returns ctx's error
// and fn does not run.
func (s *Service) SerializeWrite(ctx context.Context, id string, fn func(ctx context.Context) error) error {
	key, _, err := s.canonical(ctx, id)
	if err != nil {
		return err
	}
	w := &s.writes
	w.enter()
	defer w.leave()
	if err := ctx.Err(); err != nil {
		return err
	}
	kl := w.ref(key)
	defer w.unref(key)
	select {
	case kl.ch <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-kl.ch }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

// DrainWrites returns once every SerializeWrite call in flight has finished, or
// with ctx's error when ctx ends first. The daemon calls it on SIGTERM.
func (s *Service) DrainWrites(ctx context.Context) error {
	w := &s.writes
	for {
		w.mu.Lock()
		if w.inflight == 0 {
			w.mu.Unlock()
			return nil
		}
		if w.idle == nil {
			w.idle = make(chan struct{})
		}
		ch := w.idle
		w.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// groupKnown reports whether tasks of the group can run: the primary group (its
// executor is the connector's own) or a registered fetcher.
func (s *Service) groupKnown(group string) error {
	if group == s.cfg.Spec.PrimaryGroup {
		return nil
	}
	if !s.isGroup(group) {
		return fmt.Errorf("%w %q", ErrUnknownGroup, group)
	}
	if _, ok := s.fetcher(group); !ok {
		return fmt.Errorf("%w: %s", ErrNoFetcher, group)
	}
	return nil
}

// InvalidateAndQueue is InvalidateAndQueueAs with OriginWrite.
func (s *Service) InvalidateAndQueue(ctx context.Context, id, group string) error {
	return s.InvalidateAndQueueAs(ctx, id, group, OriginWrite)
}

// InvalidateAndQueueAs marks what a successful write changed: it calls
// groupkit.Invalidate for the group (so a refresh begun earlier loses and the
// next read goes to origin) and queues the group's refresh at class 2, due now.
// group is any group name including the primary group; for the primary group the
// store decides what an invalidation of a missing row does and the refresh is
// queued. id is canonicalized like SerializeWrite; an unseen entity (an unknown
// reference, or an entity the store has no row for) is a no-op. Before it
// invalidates, it tells the origin hints (when set) the origin of the refresh it
// is about to queue; an unseen entity notes nothing.
func (s *Service) InvalidateAndQueueAs(ctx context.Context, id, group, origin string) error {
	if err := s.groupKnown(group); err != nil {
		return err
	}
	node, known, err := s.canonical(ctx, id)
	if err != nil || !known {
		return err
	}
	kind := s.cfg.Spec.EntityKind
	ent, ok, err := s.cfg.Store.GetEntity(ctx, kind, node)
	if err != nil || !ok || !ent.TombstonedAt.IsZero() {
		return err
	}
	if h := s.originHints(); h != nil {
		h.NoteOrigin(node, group, origin)
	}
	if err := groupkit.Invalidate(ctx, s.cfg.Store, kind, node, group, s.now()); err != nil {
		return err
	}
	return s.cfg.Scheduler.Submit(scheduler.Task{
		Kind: group, Entity: node, Group: group, Class: governor.ClassInvalidated, Access: ent.LastAccess,
	})
}

// knownNode canonicalizes id for the queue helpers: an unknown reference cannot
// name an entity and is an error.
func (s *Service) knownNode(ctx context.Context, id string) (string, error) {
	node, known, err := s.canonical(ctx, id)
	if err != nil {
		return "", err
	}
	if !known {
		return "", fmt.Errorf("%w: %s", s.cfg.UnknownEntity, id)
	}
	return node, nil
}

// access is the entity's last access, zero when it has no row.
func (s *Service) access(ctx context.Context, node string) (time.Time, error) {
	ent, ok, err := s.cfg.Store.GetEntity(ctx, s.cfg.Spec.EntityKind, node)
	if err != nil || !ok {
		return time.Time{}, err
	}
	return ent.LastAccess, nil
}

// QueueGroup queues the refresh of one group of one entity at the class, due at
// due (zero is now). It merges with a queued task the way scheduler.Submit does:
// the better class and the earlier due time win. group may be the primary group.
func (s *Service) QueueGroup(ctx context.Context, id, group string, class governor.Class, due time.Time) error {
	if !class.Valid() {
		return fmt.Errorf("groupsvc: invalid class %d", int(class))
	}
	if err := s.groupKnown(group); err != nil {
		return err
	}
	node, err := s.knownNode(ctx, id)
	if err != nil {
		return err
	}
	acc, err := s.access(ctx, node)
	if err != nil {
		return err
	}
	return s.cfg.Scheduler.Submit(scheduler.Task{Kind: group, Entity: node, Group: group, Class: class, Due: due, Access: acc})
}

// RescheduleGroup REPLACES the queued background task of (entity, group) in full
// (scheduler.Reschedule, so a later due time is possible), with class 3 for a
// member of a configured query and class 4 otherwise, and queues one when
// nothing is queued. It NEVER downgrades or postpones a task of class 1 or 2
// that is queued or running for the pair: it does nothing then.
func (s *Service) RescheduleGroup(ctx context.Context, id, group string, due time.Time) error {
	if err := s.groupKnown(group); err != nil {
		return err
	}
	node, err := s.knownNode(ctx, id)
	if err != nil {
		return err
	}
	if c, queued, running := s.cfg.Scheduler.Inspect(node, group); (queued || running) && c <= governor.ClassInvalidated {
		return nil
	}
	members, err := s.members(ctx)
	if err != nil {
		return err
	}
	acc, err := s.access(ctx, node)
	if err != nil {
		return err
	}
	return s.cfg.Scheduler.Reschedule(scheduler.Task{
		Kind: group, Entity: node, Group: group, Class: classFor(members[node]), Due: due, Access: acc,
	})
}

// Inspect reports the BEST class among the queued and the running tasks of the
// pair, and whether one is queued and whether one is running. id is canonicalized
// like SerializeWrite (an unknown reference reports no task).
func (s *Service) Inspect(ctx context.Context, id, group string) (class governor.Class, queued, running bool, err error) {
	node, _, err := s.canonical(ctx, id)
	if err != nil {
		return 0, false, false, err
	}
	class, queued, running = s.cfg.Scheduler.Inspect(node, group)
	return class, queued, running, nil
}
