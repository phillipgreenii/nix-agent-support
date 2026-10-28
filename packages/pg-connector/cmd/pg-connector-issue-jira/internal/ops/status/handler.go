package status

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Op is the wire name of the op.
const Op = "status"

// Governor is the governor as the op reads it. *governor.Governor satisfies it.
type Governor interface {
	State() governor.State
}

// Scheduler is the scheduler as the op reads it. *scheduler.Scheduler
// satisfies it.
type Scheduler interface {
	State() scheduler.State
}

// Settle is the settle lifecycle as the op reads it. *engine.Lifecycle
// satisfies it.
type Settle interface {
	Stats(ctx context.Context) (engine.Stats, error)
}

// Feed is the change feed as the op reads it. *feed.Feed satisfies it.
type Feed interface {
	Positions(ctx context.Context) ([]feed.Position, error)
}

// AuthMarker is the authentication marker. *origin.AuthState satisfies it.
type AuthMarker interface {
	Unauthenticated() (bool, error)
}

var (
	_ Governor   = (*governor.Governor)(nil)
	_ Scheduler  = (*scheduler.Scheduler)(nil)
	_ Settle     = (*engine.Lifecycle)(nil)
	_ Feed       = (*feed.Feed)(nil)
	_ AuthMarker = (*origin.AuthState)(nil)
)

// Config wires the op: what a report knows without the daemon (Daemon,
// Version, BuildID) plus the daemon's live state. Every live source is
// required except Requests, Bucket, Clock and Probe.
type Config struct {
	// RouterInstance is the router instance the op is registered on (the
	// assembly passes serve.Instance).
	RouterInstance string
	// Identity returns the acting identity (the assembly passes
	// AuthState.Identity); nil reports none.
	Identity func() string

	Daemon  *config.Config
	Version string
	BuildID string
	Started time.Time

	Store     *store.Store
	Governor  Governor
	Scheduler Scheduler
	Settle    Settle
	Feed      Feed
	Auth      AuthMarker

	// Readings is the holder of the origin's last rate-limit reading; nil
	// reports the floor alone.
	Readings *origin.Readings
	// Bucket is the governor bucket the origin spends from; empty means
	// origin.Bucket.
	Bucket governor.Bucket
	// Requests is the cumulative request counter; nil reports the governor's
	// window spend as the total.
	Requests func() int64
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Probe answers the supervisor probe; nil uses proc.SupervisorProbe.
	Probe func(label string) proc.SupervisorState
}

func (c Config) validate() error {
	switch {
	case c.Daemon == nil:
		return errors.New("status: the daemon config is required")
	case c.Store == nil || c.Governor == nil || c.Scheduler == nil || c.Settle == nil || c.Feed == nil || c.Auth == nil:
		return errors.New("status: the store, governor, scheduler, settle lifecycle, feed and authentication marker are required")
	}
	return nil
}

// Register installs the status op on the router instance cfg.RouterInstance,
// stamped with the issue schema version.
func Register(rt *router.Router, cfg Config) error {
	if cfg.RouterInstance == "" {
		return errors.New("status: the router instance is required")
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	rt.Register(cfg.RouterInstance, Op, scriptout.OpHandler{
		SchemaVersion: schema.IssueSchemaVersion,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			router.Annotate(ctx, "store", false, 0, 0)
			return Collect(ctx, cfg)
		},
	})
	return nil
}

// rateLimit builds the report's rate_limit block: the floor always and, while a
// reading is current, the reading and what it says about the floor.
func rateLimit(cfg Config, now time.Time) *RateLimit {
	rl := &RateLimit{Floor: int64(cfg.Daemon.RateReserveRequests)}
	if cfg.Readings == nil {
		return rl
	}
	l, ok := cfg.Readings.Current(now)
	if !ok {
		return rl
	}
	age := int64(l.Age(now).Seconds())
	rl.LastStatus, rl.ReadingAgeSeconds = l.Status, &age
	rl.Limit, rl.Remaining, rl.NearLimit, rl.Reason = l.Limit, l.Remaining, l.NearLimit, l.Reason
	if l.Remaining != nil {
		h := *l.Remaining - rl.Floor
		rl.Headroom, rl.BelowFloor = &h, h < 0
	}
	if at, ok := l.ResetAt(); ok {
		u := utc(at)
		rl.ResetAt = &u
	}
	return rl
}

// Collect fills the daemon's report from the live state. It reads the store
// and the daemon's own state and calls no origin. A source that fails fails
// the collection: a partial report would pass for a complete one.
func Collect(ctx context.Context, cfg Config) (Report, error) {
	if err := cfg.validate(); err != nil {
		return Report{}, err
	}
	clk := cfg.Clock
	if clk == nil {
		clk = governor.SystemClock{}
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = origin.Bucket
	}
	now := clk.Now()
	env := Env{Probe: cfg.Probe, Now: func() time.Time { return now }}
	r := env.base(cfg.Daemon, cfg.Version)
	r.Source = SourceDaemon
	if cfg.BuildID != "" {
		r.BuildID = cfg.BuildID
	}

	var uptime time.Duration
	if !cfg.Started.IsZero() {
		started := utc(cfg.Started)
		uptime = max(now.Sub(cfg.Started), 0)
		up := int64(uptime.Seconds())
		r.StartedAt, r.UptimeSeconds = &started, &up
	}

	// The store: its identity, schema version and the change-log head.
	r.Store.SchemaVersion = cfg.Store.Version()
	if id, err := cfg.Store.Identity(ctx); err == nil {
		r.Store.Identity = id
	} else if !errors.Is(err, store.ErrNoIdentity) {
		return Report{}, err
	}
	head, err := cfg.Store.ChangeHead(ctx)
	if err != nil {
		return Report{}, err
	}

	// The governor: the request counter and the pause.
	r.Requests = &Requests{}
	if bs, ok := cfg.Governor.State().Bucket(bucket); ok {
		r.Requests = &Requests{
			WindowSpent: bs.Spent, BackgroundSpent: bs.BackgroundSpent, InFlight: bs.InFlight,
			Cap: bs.Cap, WindowSeconds: int64(bs.Window.Seconds()),
		}
		r.Requests.Total = bs.Spent
		if bs.Paused {
			r.Pause = &Pause{Reason: bs.PauseReason, Until: utc(bs.PausedUntil)}
		}
	}
	if cfg.Requests != nil {
		r.Requests.Total = cfg.Requests()
	}

	// The origin's rate-limit reading and the floor.
	r.RateLimit = rateLimit(cfg, now)

	// The scheduler.
	st := cfg.Scheduler.State()
	q := &Queue{Running: st.Running}
	for _, cs := range st.Classes {
		q.Classes = append(q.Classes, QueueClass{
			Class: int(cs.Class), Name: className(cs.Class.String()),
			Depth: cs.Depth, OldestOverdueSeconds: int64(cs.OldestOverdue.Seconds()),
		})
	}
	r.Queue = q

	// The configured queries.
	rows, err := cfg.Store.ListQueries(ctx, true)
	if err != nil {
		return Report{}, err
	}
	byKey := make(map[string]store.QueryRow, len(rows))
	for _, row := range rows {
		byKey[row.Key] = row
	}
	var stale []string
	for _, name := range sortedNames(cfg.Daemon) {
		row := byKey[name]
		qr := Query{Name: name, LastError: row.LastError}
		since := cfg.Started
		if !row.LastSuccessAt.IsZero() {
			t := utc(row.LastSuccessAt)
			age := max(int64(now.Sub(row.LastSuccessAt).Seconds()), 0)
			qr.LastSuccessAt, qr.LastSuccessAgeSeconds = &t, &age
			if row.LastSuccessAt.After(since) {
				since = row.LastSuccessAt
			}
		}
		if !row.BaselineAt.IsZero() {
			t := utc(row.BaselineAt)
			qr.BaselineAt = &t
		} else if !cfg.Started.IsZero() {
			age := int64(uptime.Seconds())
			qr.BaselineOpenAgeSeconds = &age
		}
		if !since.IsZero() {
			if msg, bad := queryStale(name, since, now, time.Duration(cfg.Daemon.QueryTTLFor(name))); bad {
				stale = append(stale, msg)
			}
		}
		r.Queries = append(r.Queries, qr)
	}

	// Authentication.
	unauth, aerr := cfg.Auth.Unauthenticated()
	r.Auth = &Auth{State: AuthOK}
	if cfg.Identity != nil {
		r.Auth.Identity = cfg.Identity()
	}
	if unauth {
		r.Auth.State = AuthUnauthenticated
		if aerr != nil {
			r.Auth.Detail = aerr.Error()
		}
	}

	// The change engine.
	ss, err := cfg.Settle.Stats(ctx)
	if err != nil {
		return Report{}, err
	}
	positions, err := cfg.Feed.Positions(ctx)
	if err != nil {
		return Report{}, err
	}
	r.Changes = &Changes{
		HeadSeq: head, PendingSettle: ss.Pending,
		SettleTimeoutsLastHour: ss.Timeouts, SettleFailuresLastHour: ss.Failures,
	}
	for _, p := range positions {
		c := Consumer{
			Consumer: p.Consumer, View: p.View, Scope: p.Scope,
			AckedSeq: p.AckedSeq, HeadSeq: p.Head, LagRows: p.LagRows, SeenAt: utc(p.SeenAt),
		}
		if !p.OldestUnacked.IsZero() {
			c.OldestUnackedAgeSeconds = max(int64(now.Sub(p.OldestUnacked).Seconds()), 0)
		}
		r.Changes.Consumers = append(r.Changes.Consumers, c)
	}

	// The tracked classes.
	members, err := query.Members(ctx, cfg.Store)
	if err != nil {
		return Report{}, err
	}
	counts, err := summary.Classes(ctx, cfg.Store, members, now, time.Duration(cfg.Daemon.TerminalTTL))
	if err != nil {
		return Report{}, err
	}
	r.Tracked = &Tracked{Members: counts.Members, OtherOpen: counts.OtherOpen, ClosedGrace: counts.ClosedGrace, Watched: counts.Watched}

	assess(&r, stale)
	return r, nil
}
