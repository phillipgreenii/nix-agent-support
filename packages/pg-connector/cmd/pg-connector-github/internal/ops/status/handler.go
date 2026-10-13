package status

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/auth"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op, on both instances.
const OpName = "status"

// Budgets is the governor as the op reads it. *governor.Governor satisfies it.
type Budgets interface {
	State() governor.State
}

// Scheduler is the scheduler as the op reads it. *scheduler.Scheduler satisfies it.
type Scheduler interface {
	State() scheduler.State
}

// Queries is the part of the store that holds the per-query rows.
// *store.Store satisfies it.
type Queries interface {
	GetQuery(ctx context.Context, key string) (store.QueryRow, bool, error)
}

// Identity is the cached identity and auth state. *identity.Tracker satisfies
// it.
type Identity interface {
	Current() identity.Snapshot
}

var (
	_ Budgets   = (*governor.Governor)(nil)
	_ Scheduler = (*scheduler.Scheduler)(nil)
	_ Queries   = (*store.Store)(nil)
	_ Identity  = (*identity.Tracker)(nil)
)

// Config wires the handler: what a report knows without the daemon (Static)
// plus the daemon's live state.
type Config struct {
	Static

	Budgets   Budgets
	Scheduler Scheduler
	Queries   Queries
	Identity  Identity
	// Started is when the daemon started.
	Started time.Time
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Register installs status under both instances, each stamped with its own
// schema version.
func Register(r *router.Router, cfg Config) error {
	h, err := NewBuilder(cfg)
	if err != nil {
		return err
	}
	r.Register("pr", OpName, h.Handler(schema.PRSchemaVersion))
	r.Register("ci", OpName, h.Handler(schema.CISchemaVersion))
	return nil
}

// Builder builds the daemon's report.
type Builder struct{ cfg Config }

// NewBuilder returns a Builder; the live sources are all required.
func NewBuilder(cfg Config) (*Builder, error) {
	if cfg.Budgets == nil || cfg.Scheduler == nil || cfg.Queries == nil || cfg.Identity == nil {
		return nil, errors.New("status: handler needs the governor, the scheduler, the query store and the identity")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	return &Builder{cfg: cfg}, nil
}

// Handler is the op handler for an instance whose schema version is
// schemaVersion. It never answers an error for a healthy-or-not daemon: an
// unhealthy daemon is still a report.
func (b *Builder) Handler(schemaVersion int) scriptout.OpHandler {
	return scriptout.OpHandler{
		SchemaVersion: schemaVersion,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return b.Build(ctx)
		},
	}
}

// Build is the daemon's report now.
func (b *Builder) Build(ctx context.Context) (Report, error) {
	now := b.cfg.Clock.Now()
	r := b.cfg.Static.base(now)
	r.Source = SourceDaemon

	if !b.cfg.Started.IsZero() {
		started := utc(b.cfg.Started)
		up := max(int64(now.Sub(b.cfg.Started).Seconds()), 0)
		r.StartedAt, r.UptimeSeconds = &started, &up
	}

	for _, bs := range b.cfg.Budgets.State().Buckets {
		r.Budget = append(r.Budget, bucketOf(bs))
	}

	q := &Queue{}
	st := b.cfg.Scheduler.State()
	q.Running = st.Running
	for _, cs := range st.Classes {
		q.Classes = append(q.Classes, QueueClass{
			Class:                int(cs.Class),
			Name:                 className(cs.Class),
			Depth:                cs.Depth,
			OldestOverdueSeconds: int64(cs.OldestOverdue.Seconds()),
		})
	}
	r.Queue = q

	for _, name := range sortedCopy(b.cfg.QueryNames) {
		row, ok, err := b.cfg.Queries.GetQuery(ctx, name)
		if err != nil {
			return Report{}, err
		}
		query := Query{Name: name}
		if ok {
			if !row.LastSuccessAt.IsZero() {
				t := utc(row.LastSuccessAt)
				query.LastSuccessAt = &t
			}
			query.LastError = row.LastError
		}
		r.Queries = append(r.Queries, query)
	}

	snap := b.cfg.Identity.Current()
	as := auth.Status(snap)
	r.Auth = &Auth{State: string(as.State), Detail: as.Detail, Viewer: snap.Login, Unauthenticated: snap.Unauthenticated}

	assess(&r)
	return r, nil
}

func bucketOf(bs governor.BucketState) Bucket {
	b := Bucket{
		Bucket:          string(bs.Bucket),
		Cap:             bs.Cap,
		BackgroundCap:   bs.BackgroundCap,
		Reserve:         bs.Reserve,
		WindowSeconds:   int64(bs.Window.Seconds()),
		Spent:           bs.Spent,
		BackgroundSpent: bs.BackgroundSpent,
		InFlight:        bs.InFlight,
		Paused:          bs.Paused,
	}
	if bs.Remaining >= 0 {
		rem, limit := bs.Remaining, bs.Limit
		b.Remaining, b.Limit = &rem, &limit
		if !bs.ResetAt.IsZero() {
			t := utc(bs.ResetAt)
			b.ResetAt = &t
		}
	}
	if bs.Paused {
		b.PauseReason = bs.PauseReason
		t := utc(bs.PausedUntil)
		b.PausedUntil = &t
	}
	return b
}

// utc is t in UTC at whole-second precision, the report's timestamp form.
func utc(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// storeInfo stats the store file: its own size (the write-ahead log is not
// counted) and how long ago it was last modified.
func storeInfo(path string, now time.Time) Store {
	s := Store{Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return s
	}
	mod := utc(fi.ModTime())
	s.Present, s.SizeBytes, s.ModifiedAt = true, fi.Size(), &mod
	s.AgeSeconds = max(int64(now.Sub(fi.ModTime()).Seconds()), 0)
	return s
}
