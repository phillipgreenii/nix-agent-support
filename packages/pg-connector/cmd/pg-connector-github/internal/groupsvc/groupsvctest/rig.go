package groupsvctest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// T0 is the manual clock's start.
var T0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// RigOptions configure NewRig.
type RigOptions struct {
	// Gov, when set, gates the scheduler and is handed to the service and to the
	// fake origin; the fake fetchers admit through it when their Gov is set.
	Gov *governor.Governor
	// Workers is the scheduler's worker count; zero means 4.
	Workers int
	// BatchSize is the summary service's batch size; zero means the maximum.
	BatchSize int
	// Settings replaces the default settings (TTLs and so on).
	Settings *config.Settings
	// Expiry overrides the settings' expiry.
	Expiry time.Duration
	// AliasStore, when set, is what the alias table is read and written through
	// (a memo over Store, for a test that makes millions of lookups); nil means
	// the store itself.
	AliasStore entity.AliasStore
	// Store and Clock reuse an existing store and clock (a simulated restart).
	Store *store.Store
	Clock *governor.ManualClock
	// OnGroupRefreshed is the service's hook.
	OnGroupRefreshed func(prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo)
	// OnGroupStoring and OnGone are the service's transactional hooks.
	OnGroupStoring func(ctx context.Context, tx *store.Tx, prev *store.GroupRow, next store.GroupRow, info groupsvc.RefreshInfo) error
	OnGone         func(ctx context.Context, tx *store.Tx, node string, purpose governor.Purpose) error
	// OnSummaryRefreshed and SummaryOnGone are the summary service's hooks.
	OnSummaryRefreshed func(prev *summary.Summary, next summary.Stored)
	SummaryOnGone      summary.GoneHook
	// Identity receives the authentication markers.
	Identity groupsvc.AuthMarker
	// PollInterval is the scheduler's poll interval; zero means 5 ms.
	PollInterval time.Duration
}

// Rig is a real store, scheduler and summary service on a manual clock, with the
// fake origin and resolver and a groupsvc.Service over them.
type Rig struct {
	T        *testing.T
	Store    *store.Store
	Clock    *governor.ManualClock
	Sched    *scheduler.Scheduler
	Summary  *summary.Service
	Svc      *groupsvc.Service
	Origin   *Origin
	Resolver *Resolver
	Aliases  *entity.Identity
	Persist  *summary.Persister
	Rules    *groups.Rules
	Settings config.Settings

	runOnce sync.Once
}

// NewRig builds the rig; nothing is dispatched until Start (or a Drain).
func NewRig(t *testing.T, o RigOptions) *Rig {
	t.Helper()
	st, clk := o.Store, o.Clock
	if st == nil {
		var err error
		st, _, err = store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	if clk == nil {
		clk = governor.NewManualClock(T0)
	}
	settings := config.Defaults().Settings
	if o.Settings != nil {
		settings = *o.Settings
	}
	if o.Workers == 0 {
		o.Workers = 4
	}
	if o.BatchSize == 0 {
		o.BatchSize = summary.MaxBatch
	}
	if o.PollInterval == 0 {
		o.PollInterval = 5 * time.Millisecond
	}
	expiry := settings.Expiry.Std()
	if o.Expiry > 0 {
		expiry = o.Expiry
	}
	var aliasStore entity.AliasStore = st
	if o.AliasStore != nil {
		aliasStore = o.AliasStore
	}
	aliases := entity.NewIdentity(aliasStore, clk)
	origin := NewOrigin()
	origin.Gov = o.Gov
	pers := &summary.Persister{
		Store: st, Identity: aliases, TTL: settings.SummaryTTL.Std(), TerminalTTL: settings.TerminalTTL.Std(), Clock: clk,
	}
	opt := scheduler.Options{Clock: clk, Workers: o.Workers, FillHorizon: settings.FillHorizon.Std(), PollInterval: o.PollInterval}
	if o.Gov != nil {
		opt.Gate = o.Gov
	}
	sched := scheduler.New(opt)
	sum, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: pers, Fetcher: origin, Identity: aliases, Scheduler: sched, Clock: clk,
		KeepaliveWindow: settings.KeepaliveWindow.Std(), Expiry: expiry, BatchSize: o.BatchSize,
		OnSummaryRefreshed: o.OnSummaryRefreshed, OnGone: o.SummaryOnGone,
	})
	if err != nil {
		t.Fatal(err)
	}
	rules := groups.NewRules(settings)
	resolver := NewResolver(aliases)
	svc, err := groupsvc.New(groupsvc.Config{
		Store: st, Scheduler: sched, Summary: sum, Rules: rules, Aliases: aliases, Resolver: resolver,
		Identity: o.Identity, Clock: clk, Governor: o.Gov, Expiry: expiry, OnGroupRefreshed: o.OnGroupRefreshed,
		OnGroupStoring: o.OnGroupStoring, OnGone: o.OnGone,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Rig{
		T: t, Store: st, Clock: clk, Sched: sched, Summary: sum, Svc: svc, Origin: origin, Resolver: resolver,
		Aliases: aliases, Persist: pers, Rules: rules, Settings: settings,
	}
}

// Start runs the scheduler's loop until the test ends, for tests that wait on
// Ensure. A test that steps the scheduler itself (Drain) does not call it.
func (r *Rig) Start() {
	r.runOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Sched.Run(ctx)
		}()
		r.T.Cleanup(func() {
			cancel()
			<-done
		})
	})
}

// Register installs the fake fetcher's executor.
func (r *Rig) Register(f *Fetcher) *Fetcher {
	r.Svc.Register(f)
	return f
}

// SeedSummary stores the summary the origin currently answers for the node id, as
// a refresh at the manual clock's now would, and returns it. The entity row is
// created with the given last access (zero means none).
func (r *Rig) SeedSummary(nodeID string, lastAccess time.Time) summary.Summary {
	r.T.Helper()
	ctx := context.Background()
	st, err := r.Persist.Persist(ctx, r.Origin.Summary(nodeID))
	if err != nil {
		r.T.Fatal(err)
	}
	if !lastAccess.IsZero() {
		if err := r.Store.TouchEntity(ctx, entity.KindPR, nodeID, lastAccess); err != nil {
			r.T.Fatal(err)
		}
	}
	return st.Summary
}

// SeedGroup stores a group row of the node id.
func (r *Rig) SeedGroup(row store.GroupRow) {
	r.T.Helper()
	row.Kind = entity.KindPR
	if err := r.Store.PutGroup(context.Background(), row); err != nil {
		r.T.Fatal(err)
	}
}

// Group reads a stored group row.
func (r *Rig) Group(nodeID, group string) (store.GroupRow, bool) {
	r.T.Helper()
	g, ok, err := r.Store.GetGroup(context.Background(), entity.KindPR, nodeID, group)
	if err != nil {
		r.T.Fatal(err)
	}
	return g, ok
}

// Entity reads a stored entity row.
func (r *Rig) Entity(nodeID string) (store.EntityRow, bool) {
	r.T.Helper()
	e, ok, err := r.Store.GetEntity(context.Background(), entity.KindPR, nodeID)
	if err != nil {
		r.T.Fatal(err)
	}
	return e, ok
}

// PutQuery stores a configured query holding the ids.
func (r *Rig) PutQuery(key string, ids ...string) {
	r.T.Helper()
	if err := r.Store.PutQuery(context.Background(), store.QueryRow{Key: key, Configured: true, IDs: ids, Complete: true}); err != nil {
		r.T.Fatal(err)
	}
}
