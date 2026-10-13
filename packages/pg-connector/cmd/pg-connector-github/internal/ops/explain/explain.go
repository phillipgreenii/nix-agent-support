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
// The pr instance answers the groups of the pull request (today `summary`);
// the ci instance answers the CI groups (`runs`), of which none is stored yet,
// so its answer carries the pull request and no groups, with a note saying so.
package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op, on both instances.
const OpName = "explain"

// ciGroups are the groups the ci instance answers; every other group belongs
// to the pr instance.
var ciGroups = map[string]bool{"runs": true}

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

	groups, err := h.cfg.Store.ListGroups(ctx, entity.KindPR, nodeID)
	if err != nil {
		return nil, err
	}
	now := h.cfg.Clock.Now()
	router.Annotate(ctx, "store", false, 0, 0)
	for _, g := range groups {
		if ciGroups[g.Group] != (h.instance == "ci") {
			continue
		}
		res.Groups = append(res.Groups, h.group(g, nodeID, now))
	}
	if len(res.Groups) == 0 && h.instance == "ci" {
		res.Note = "no CI group is stored for this pull request yet"
	}
	return res, nil
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
