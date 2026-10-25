package detail

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Config wires the service. Everything not listed here the package supplies.
type Config struct {
	// Store is the daemon store; the assembly passes the same one as the
	// guard store of the other groups. *store.Store satisfies it.
	Store     groupsvc.Store
	Scheduler *scheduler.Scheduler
	// Rules is the freshness table (groups.Rules): detail_ttl, detail_max_age
	// and terminal_ttl are its values.
	Rules groupkit.Table
	// Aliases is the alias table (summary.NewAliases).
	Aliases query.Aliases
	// KeepAlive is the retention rule (summary.KeepAlive); the rule is the
	// summary package's, this package only passes it through.
	KeepAlive groupsvc.KeepAlive
	// Governor is the governor the origin runner pauses; nil pauses nothing.
	Governor *governor.Governor
	// Auth is the daemon's one origin.AuthState.
	Auth groupsvc.AuthMarker
	// Origin runs the detail search (*origin.Runner).
	Origin Origin
	// Chg is the change engine: its DetectGroup and DetectGone are the store
	// seams of the group service. Required.
	Chg *changes.Engine
	// Facts computes the two operator facts (Backend.OperatorFacts); nil
	// stores none.
	Facts Facts
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Expiry is how long a stored detail may be served stale; zero means
	// config.DefaultExpiry.
	Expiry time.Duration
	// FallbackPause is the pause of a rate-limit answer without a stated
	// delay; zero means config.DefaultDefaultPause. The origin runner has
	// already paused for a 429, so the policy here never acts on it.
	FallbackPause time.Duration
	// BodyCap is change_body_cap, the byte cap of a stored comment body; zero
	// means config.DefaultChangeBodyCap.
	BodyCap int
	// Bucket is the governor bucket one search spends from; empty means
	// origin.Bucket.
	Bucket governor.Bucket
	// OnError, when set, is told about an error Revalidate could not return
	// (it has no result); the remaining work of the revalidation still runs.
	OnError func(err error)
}

// Service is the detail group of the daemon: the shared group refresh service
// with the Jira detail fetcher registered, and the revalidation rule.
type Service struct {
	cfg Config
	svc *groupsvc.Service
	clk governor.Clock
}

// Spec is the registration the shared service reads: the issue entity, its
// primary (summary) group and the closed set of groups.
func spec() registry.Spec {
	return registry.Spec{
		EntityKind:   groups.EntityKind,
		PrimaryGroup: groups.Summary,
		Groups:       groups.All(),
		FoldRef:      groups.FoldRef,
	}
}

// NewService builds the shared group service for entity kind issue and
// registers the detail Fetcher with the scheduler. It is the only group it
// registers: the summary group has its own executor and children is never
// stored.
func NewService(cfg Config) (*Service, error) {
	switch {
	case cfg.Store == nil, cfg.Scheduler == nil, cfg.Aliases == nil, cfg.KeepAlive == nil:
		return nil, errors.New("detail: service needs a store, scheduler, alias table and keep-alive rule")
	case cfg.Origin == nil:
		return nil, errors.New("detail: service needs an origin")
	case cfg.Chg == nil:
		return nil, errors.New("detail: service needs the change engine")
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = config.DefaultExpiry
	}
	if cfg.FallbackPause <= 0 {
		cfg.FallbackPause = config.DefaultDefaultPause
	}
	if cfg.BodyCap <= 0 {
		cfg.BodyCap = config.DefaultChangeBodyCap
	}
	clk := cfg.Clock
	if clk == nil {
		clk = governor.SystemClock{}
	}
	sc := groupsvc.Config{
		Spec:           spec(),
		Describer:      describer{store: cfg.Store, rules: cfg.Rules, clock: clk},
		Store:          cfg.Store,
		Scheduler:      cfg.Scheduler,
		Rules:          cfg.Rules,
		Aliases:        cfg.Aliases,
		KeepAlive:      cfg.KeepAlive,
		Errors:         ErrorPolicy{},
		Identity:       cfg.Auth,
		Clock:          clk,
		Governor:       cfg.Governor,
		Expiry:         cfg.Expiry,
		FallbackPause:  cfg.FallbackPause,
		NoSuchEntity:   scriptout.ErrNotFound,
		BadRef:         scriptout.ErrInvalidArgument,
		UnknownEntity:  scriptout.ErrNotFound,
		ParseID:        parseID,
		OnGroupStoring: cfg.Chg.DetectGroup,
		OnGone:         cfg.Chg.DetectGone,
	}
	shared, err := groupsvc.New(sc)
	if err != nil {
		return nil, err
	}
	shared.Register(&Fetcher{
		Origin: cfg.Origin, Store: cfg.Store, Rules: cfg.Rules, Facts: cfg.Facts,
		Clock: clk, BodyCap: cfg.BodyCap, Bucket: cfg.Bucket,
	})
	return &Service{cfg: cfg, svc: shared, clk: clk}, nil
}

// Group is the shared group refresh service (Ensure, Rebuild, QueueGroup,
// InvalidateAndQueue, SerializeWrite, DrainWrites and the rest).
func (s *Service) Group() *groupsvc.Service { return s.svc }

// Ensure is the shared Ensure (the spec's "Entity read"): id is a Jira key in
// any case. A key the origin says does not exist is scriptout.ErrNotFound and
// a malformed one scriptout.ErrInvalidArgument.
func (s *Service) Ensure(ctx context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error) {
	return s.svc.Ensure(ctx, id, want, opt)
}

// parseID is the Jira reference rule: a Jira key is the reference (its folded
// form, which is also the store id); anything else is malformed.
func parseID(id string) (wire string, isRef bool, err error) {
	if !internal.IsJiraKey(id) {
		return id, false, errors.New("detail: malformed issue key")
	}
	return groups.FoldRef(id), true, nil
}

// describer is groups.Describer with Terminal set from the entity row's
// closed-at: the shared executor schedules a fetch of a terminal issue with the
// terminal TTL and no maximum age, as the Fetcher's own FreshUntil does.
type describer struct {
	store groupsvc.Store
	rules groupkit.Table
	clock governor.Clock
}

var _ groupsvc.Describer = describer{}

func (d describer) Describe(content []byte) (engine.Primary, error) {
	p, err := groups.Describer{}.Describe(content)
	if err != nil || p.Gone || p.Ref == "" {
		return p, err
	}
	ent, ok, err := d.store.GetEntity(context.Background(), groups.EntityKind, groups.FoldRef(p.Ref))
	if err != nil {
		return engine.Primary{}, err
	}
	if ok {
		p.Terminal = groups.Terminal(ent.ClosedAt, d.clock.Now(), d.rules.TerminalTTL)
	}
	return p, nil
}
