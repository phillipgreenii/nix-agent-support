// Package explain is the `explain` op of both instances: why is this entity
// stale, answered from the store alone (no origin call, no budget).
//
// # Request
//
// The wire argument is {"id": "owner/repo#n"} (a node id is accepted too). The
// id is looked up in the alias table; an id the store has never seen answers
// not_found, a missing or empty id invalid_argument.
//
// # Result
//
// Result carries, for each stored group of the pull request that belongs to
// the instance asked, the group's fetched_at, fresh_until, max_age_at (when
// the group has a hard age), last_error, the head and base the group was
// fetched for, whether it is still fresh at the time of the answer, and its
// queue position (1 is next in line among the queued tasks of its class;
// absent when the group has no queued task). The fields are additive: later
// phases add the pending settle rows, the settle deadline and the last change
// rows, and the groups they bring.
//
// # Verdicts
//
// When the handler is given the freshness rules (Config.Rules) each group also
// carries a verdict: status (fresh, stale or hard_stale), the reason text when
// it is not fresh, stamps_match for a group stamped with a head and base (does
// the head and base it was fetched for equal the stored summary's), and, for the
// runs group, the active and failing verdicts of runs.Active and runs.Failing
// against the summary's head and the number of cached job rows of the runs on
// that head (Config.Jobs). The verdict is the one groups.Rules.Fresh gives, so
// explain and the read path agree. These fields are additive over the Phase 2
// document; a handler built without Rules answers the Phase 2 document.
//
// The pr instance answers every stored group of the pull request but runs; the
// ci instance answers the pull request's summary and its runs group (the CI
// view). When no runs group is stored its answer carries a note saying so.
package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op, on both instances.
const OpName = "explain"

// inView reports whether the instance's explain answers the group: the ci view
// is the summary and the runs group, the pr view is every group but runs.
func inView(instance, group string) bool {
	if instance == "ci" {
		return group == groups.Summary || group == groups.Runs
	}
	return group != groups.Runs
}

// Status values of a group's verdict.
const (
	StatusFresh     = "fresh"
	StatusStale     = "stale"
	StatusHardStale = "hard_stale"
)

// JobReader reads the job table's cached rows. *store.Store satisfies it.
type JobReader interface {
	GetJobs(ctx context.Context, runID string, attempt int) (store.JobRow, bool, error)
}

var _ JobReader = (*store.Store)(nil)

// Store is the part of the daemon store the op reads. *store.Store satisfies
// it (its alias, entity and group tables).
type Store interface {
	entity.AliasStore
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
	ListGroups(ctx context.Context, kind, id string) ([]store.GroupRow, error)
}

var _ Store = (*store.Store)(nil)

// Queue is the scheduler's position accessor. *scheduler.Scheduler satisfies
// it.
type Queue interface {
	Position(entity, group string) (int, bool)
}

var _ Queue = (*scheduler.Scheduler)(nil)

// Config wires the handlers.
type Config struct {
	Store Store
	Queue Queue
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// Rules, when set, adds each group's verdict (status, reason, stamps_match
	// and the runs facts). Nil answers the Phase 2 document.
	Rules *groups.Rules
	// Jobs, when set, adds the number of cached job rows to the runs group.
	Jobs JobReader
}

// Result is the answer.
type Result struct {
	// Instance is the instance that answered.
	Instance string `json:"instance"`
	// ID is the id as asked; Ref is the pull request's reference as GitHub
	// spells it and NodeID its GraphQL node id.
	ID     string `json:"id"`
	Ref    string `json:"ref"`
	NodeID string `json:"node_id"`

	LastAccess   *time.Time `json:"last_access,omitempty"`
	KeepAlive    bool       `json:"keep_alive"`
	TombstonedAt *time.Time `json:"tombstoned_at,omitempty"`

	Groups []Group `json:"groups"`
	Note   string  `json:"note,omitempty"`
}

// Group is one stored group of the entity.
type Group struct {
	Group           string     `json:"group"`
	FetchedAt       *time.Time `json:"fetched_at,omitempty"`
	FreshUntil      *time.Time `json:"fresh_until,omitempty"`
	MaxAgeAt        *time.Time `json:"max_age_at,omitempty"`
	Fresh           bool       `json:"fresh"`
	ExpiredByMaxAge bool       `json:"expired_by_max_age"`
	LastError       string     `json:"last_error,omitempty"`
	FetchedForHead  string     `json:"fetched_for_head,omitempty"`
	FetchedForBase  string     `json:"fetched_for_base,omitempty"`
	QueuePosition   *int       `json:"queue_position,omitempty"`

	// Status is fresh, stale or hard_stale and Reason says why a group is not
	// fresh; StampsMatch is set for a group stamped with a head and base (files,
	// commits, runs): does what it was fetched for equal the stored summary's.
	Status      string `json:"status,omitempty"`
	Reason      string `json:"reason,omitempty"`
	StampsMatch *bool  `json:"stamps_match,omitempty"`

	// The runs group only: Active and Failing are the verdicts of runs.Active
	// and runs.Failing on the summary's head, CachedJobs the number of job rows
	// the job table holds for the runs on that head.
	Active     *bool `json:"active,omitempty"`
	Failing    *bool `json:"failing,omitempty"`
	CachedJobs *int  `json:"cached_jobs,omitempty"`
}

// Register installs explain under both instances, each stamped with its own
// schema version.
func Register(r *router.Router, cfg Config) error {
	pr, err := Handler(cfg, "pr", schema.PRSchemaVersion)
	if err != nil {
		return err
	}
	ci, err := Handler(cfg, "ci", schema.CISchemaVersion)
	if err != nil {
		return err
	}
	r.Register("pr", OpName, pr)
	r.Register("ci", OpName, ci)
	return nil
}

// Handler returns the op handler of one instance.
func Handler(cfg Config, instance string, schemaVersion int) (scriptout.OpHandler, error) {
	if cfg.Store == nil || cfg.Queue == nil {
		return scriptout.OpHandler{}, errors.New("explain: handler needs a store and the scheduler")
	}
	if cfg.Clock == nil {
		cfg.Clock = governor.SystemClock{}
	}
	h := &handler{cfg: cfg, instance: instance, identity: entity.NewIdentity(cfg.Store, cfg.Clock)}
	return scriptout.OpHandler{SchemaVersion: schemaVersion, Handle: h.handle}, nil
}

type handler struct {
	cfg      Config
	instance string
	identity *entity.Identity
}

type wireArgs struct {
	ID string `json:"id"`
}

func (h *handler) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var a wireArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode explain args: "+err.Error())
	}
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, `explain needs the argument "id" (owner/repo#n)`)
	}
	nodeID, ref, ok, err := h.resolve(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("no stored pull request %q", a.ID))
	}

	res := Result{Instance: h.instance, ID: a.ID, Ref: ref.String(), NodeID: nodeID, Groups: []Group{}}
	if row, found, err := h.cfg.Store.GetEntity(ctx, entity.KindPR, nodeID); err != nil {
		return nil, err
	} else if found {
		res.KeepAlive = row.KeepAlive
		res.LastAccess, res.TombstonedAt = tp(row.LastAccess), tp(row.TombstonedAt)
	}

	stored, err := h.cfg.Store.ListGroups(ctx, entity.KindPR, nodeID)
	if err != nil {
		return nil, err
	}
	now := h.cfg.Clock.Now()
	router.Annotate(ctx, "store", false, 0, 0)
	cur, haveSummary := currentSummary(stored)
	hasRuns := false
	for _, g := range stored {
		if !inView(h.instance, g.Group) {
			continue
		}
		hasRuns = hasRuns || g.Group == groups.Runs
		out := h.group(g, nodeID, now)
		if err := h.verdict(ctx, &out, g, cur, haveSummary, now); err != nil {
			return nil, err
		}
		res.Groups = append(res.Groups, out)
	}
	sort.SliceStable(res.Groups, func(i, j int) bool { return groupRank(res.Groups[i].Group) < groupRank(res.Groups[j].Group) })
	if !hasRuns && h.instance == "ci" {
		res.Note = "no CI group is stored for this pull request yet"
	}
	return res, nil
}

// groupRank orders groups as the spec's field-group table does.
func groupRank(group string) int {
	for i, g := range groups.All() {
		if g == group {
			return i
		}
	}
	return len(groups.All())
}

// currentSummary decodes the stored summary among the rows: the stamps every
// stamped group is judged against.
func currentSummary(rows []store.GroupRow) (summary.Summary, bool) {
	for _, g := range rows {
		if g.Group != groups.Summary || len(g.Content) == 0 {
			continue
		}
		var s summary.Summary
		if err := json.Unmarshal(g.Content, &s); err != nil {
			return summary.Summary{}, false
		}
		return s, true
	}
	return summary.Summary{}, false
}

// verdict fills the verdict fields of out from the freshness rules (see the
// package comment); it does nothing without Config.Rules.
func (h *handler) verdict(ctx context.Context, out *Group, g store.GroupRow, cur summary.Summary, haveSummary bool, now time.Time) error {
	rules := h.cfg.Rules
	if rules == nil {
		return nil
	}
	stamps := groups.StampsOf(cur)
	f := rules.Fresh(g, stamps, now)
	switch f.Level {
	case groups.LevelFresh:
		out.Status = StatusFresh
	case groups.LevelHardStale:
		out.Status = StatusHardStale
	default:
		out.Status = StatusStale
	}
	out.Reason = f.Reason
	if rules.Stamped(g.Group) {
		match := haveSummary && g.FetchedForHead == stamps.Head && g.FetchedForBase == stamps.Base
		out.StampsMatch = &match
		if !haveSummary && out.Status == StatusFresh {
			out.Status, out.Reason = StatusStale, "no stored summary to compare the head and base with"
		}
	}
	if g.Group != groups.Runs {
		return nil
	}
	content, err := runs.Decode(g.Content)
	if err != nil {
		return nil // an empty or unreadable runs row carries no verdicts
	}
	active, failing := runs.Active(content, stamps.Head), runs.Failing(content, stamps.Head)
	out.Active, out.Failing = &active, &failing
	if h.cfg.Jobs == nil {
		return nil
	}
	n := 0
	for _, r := range content.Runs {
		if stamps.Head == "" || r.HeadSHA != stamps.Head {
			continue
		}
		attempt := r.Attempt
		if attempt <= 0 {
			attempt = 1
		}
		_, ok, err := h.cfg.Jobs.GetJobs(ctx, r.ID, attempt)
		if err != nil {
			return err
		}
		if ok {
			n++
		}
	}
	out.CachedJobs = &n
	return nil
}

// resolve finds the node id and reference of id: an owner/repo#n through the
// alias table, else id as a node id.
func (h *handler) resolve(ctx context.Context, id string) (nodeID string, ref entity.Ref, ok bool, err error) {
	if parsed, perr := entity.ParseRef(id); perr == nil {
		nodeID, ref, ok, err = h.identity.NodeID(ctx, parsed)
		return nodeID, ref, ok, err
	}
	ref, ok, err = h.identity.Ref(ctx, id)
	return id, ref, ok, err
}

func (h *handler) group(g store.GroupRow, nodeID string, now time.Time) Group {
	out := Group{
		Group:          g.Group,
		FetchedAt:      tp(g.FetchedAt),
		FreshUntil:     tp(g.FreshUntil),
		MaxAgeAt:       tp(g.MaxAgeAt),
		LastError:      g.LastError,
		FetchedForHead: g.FetchedForHead,
		FetchedForBase: g.FetchedForBase,
	}
	out.ExpiredByMaxAge = !g.MaxAgeAt.IsZero() && !now.Before(g.MaxAgeAt)
	out.Fresh = !g.FreshUntil.IsZero() && now.Before(g.FreshUntil) && !out.ExpiredByMaxAge
	if pos, queued := h.cfg.Queue.Position(nodeID, g.Group); queued {
		out.QueuePosition = &pos
	}
	return out
}

// tp is t as a UTC whole-second pointer, nil for the zero time.
func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC().Truncate(time.Second)
	return &u
}
