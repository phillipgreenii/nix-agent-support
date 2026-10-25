package notice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// instancePrefix keys the hold of a create's instance name, which has no issue
// id yet; no Jira key contains a colon, so the two never meet.
const instancePrefix = "INSTANCE:"

// TableConfig builds a Table.
type TableConfig struct {
	// Store is the RAW daemon store the expiry bumps generations in (never the
	// GuardStore, which refuses while held). Nil means an expiry bumps nothing
	// and queues nothing.
	Store *store.Store
	// Clock is the time source for the bumps; nil means the system clock.
	Clock governor.Clock
	// Lease is write_lease: how long a hold lasts unless a Begin names another;
	// zero or negative means config.DefaultWriteLease.
	Lease time.Duration
	// AfterFunc starts the expiry timer of a hold: it calls f once, after d, on
	// a goroutine of its own, and the returned stop cancels it. Nil means
	// time.AfterFunc. A test passes a fake it fires by hand.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
	// OnError, when set, is told about an error the expiry could not return.
	OnError func(err error)
}

// hold is one entity's hold; seq tells a stale timer from the current one.
type hold struct {
	seq  uint64
	stop func() bool
}

// Table is the hold table: the entities a write is in flight for. It is safe
// for concurrent use. Build it with NewTable; a zero Table is not usable.
type Table struct {
	cfg TableConfig

	mu      sync.Mutex
	holds   map[string]*hold
	seq     uint64
	summary *summary.Service
	detail  *detail.Service
}

// NewTable returns a Table. Build it BEFORE the summary and detail services (they
// are constructed over the GuardStore that reads it), then call Bind.
func NewTable(cfg TableConfig) *Table {
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	if cfg.Lease <= 0 {
		cfg.Lease = config.DefaultWriteLease
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		}
	}
	return &Table{cfg: cfg, holds: map[string]*hold{}}
}

// Bind gives the table the services whose re-pulls an expiry queues. Call it
// once, after both exist; a hold that expires before Bind re-queues nothing.
func (t *Table) Bind(s *summary.Service, d *detail.Service) {
	t.mu.Lock()
	t.summary, t.detail = s, d
	t.mu.Unlock()
}

// Lease is the configured hold length.
func (t *Table) Lease() time.Duration { return t.cfg.Lease }

func key(id string) string { return groups.FoldRef(id) }

// Begin holds id for lease (zero or negative means the configured lease). A Begin
// on an id that is already held extends the hold: the old expiry timer is
// cancelled and a new one starts.
func (t *Table) Begin(_ context.Context, id string, lease time.Duration) {
	if lease <= 0 {
		lease = t.cfg.Lease
	}
	k := key(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	if old := t.holds[k]; old != nil && old.stop != nil {
		old.stop()
	}
	t.seq++
	seq := t.seq
	h := &hold{seq: seq}
	h.stop = t.cfg.AfterFunc(lease, func() { t.expire(k, seq) })
	t.holds[k] = h
}

// Release ends the hold of id and its timer; an id that is not held is a no-op.
func (t *Table) Release(_ context.Context, id string) {
	k := key(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	if h := t.holds[k]; h != nil {
		if h.stop != nil {
			h.stop()
		}
		delete(t.holds, k)
	}
}

// Held reports whether id is held now.
func (t *Table) Held(id string) bool {
	if t == nil {
		return false
	}
	k := key(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.holds[k] != nil
}

// expire is the timer of the hold seq of k: it bumps the generations once more
// while the hold is still in force, releases the hold and queues one re-pull. A
// stale timer (the hold was released or extended since) does nothing.
func (t *Table) expire(k string, seq uint64) {
	ctx := context.Background()
	t.mu.Lock()
	h := t.holds[k]
	if h == nil || h.seq != seq {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	isIssue := !strings.HasPrefix(k, instancePrefix)
	var errs []error
	if isIssue && t.cfg.Store != nil {
		errs = append(errs, bumpBoth(ctx, t.cfg.Store, t.cfg.Clock.Now(), k))
	}

	t.mu.Lock()
	cur := t.holds[k]
	if cur == nil || cur.seq != seq {
		// Extended or released while the bump ran: its own end handles the rest.
		t.mu.Unlock()
		t.report(errors.Join(errs...))
		return
	}
	delete(t.holds, k)
	sum, det := t.summary, t.detail
	t.mu.Unlock()

	if isIssue && t.cfg.Store != nil {
		p := puller{st: t.cfg.Store, sum: sum, det: det}
		errs = append(errs, p.queue(ctx, k))
	}
	t.report(errors.Join(errs...))
}

func (t *Table) report(err error) {
	if err != nil && t.cfg.OnError != nil {
		t.cfg.OnError(err)
	}
}

// instanceKey is the table key of a create's instance name.
func instanceKey(name string) string { return key(instancePrefix + name) }

// bumpBoth bumps the generation of both groups of id, keeping their content.
func bumpBoth(ctx context.Context, st groupkit.GenStore, now time.Time, id string) error {
	var errs []error
	for _, g := range []string{groups.Summary, groups.Detail} {
		errs = append(errs, groupkit.Invalidate(ctx, st, groups.EntityKind, id, g, now))
	}
	return errors.Join(errs...)
}

// puller queues the re-pulls of an issue after its write.
type puller struct {
	st      *store.Store
	sum     *summary.Service
	det     *detail.Service
	queries func() []string
}

// queue queues exactly one re-pull of the summary and of the detail group at
// class 2, due now. For an issue the daemon does not know (no entity row) it
// queues the summary fetch that tracks it and one re-run of every configured
// query instead. An unbound service (nil) queues nothing.
func (p puller) queue(ctx context.Context, id string) error {
	_, known, err := p.st.GetEntity(ctx, groups.EntityKind, id)
	if err != nil {
		return err
	}
	var errs []error
	if p.sum != nil {
		errs = append(errs, p.sum.Queue(ctx, id, governor.ClassInvalidated, time.Time{}))
	}
	if !known {
		if p.queries != nil {
			p.queries()
		}
		return errors.Join(errs...)
	}
	if p.det != nil {
		errs = append(errs, p.det.Group().QueueGroup(ctx, id, groups.Detail, governor.ClassInvalidated, time.Time{}))
	}
	return errors.Join(errs...)
}
