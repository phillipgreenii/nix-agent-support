// Package inspect is the `inspect` op of the pr instance: for each pull request
// named, is it tracked, is it watched, and how did it become watched, answered
// from the store alone (no origin call, no budget).
//
// # Why it exists
//
// Two properties of a pull request are kept apart (INV-GH-FRESH-3,
// INV-GH-CHG-10). It is TRACKED while the daemon keeps it fresh in the
// background (it is in the keep-alive set: listed by a configured query, or
// told about by a read or a watch and not yet closed or merged). It is WATCHED
// when its stored watched flag is set, which is what makes the daemon append and
// deliver change rows for it. A PR read once is tracked and not watched; a PR
// watched stays watched until its row is deleted. The operator and the marking
// step of the PR lane (which marks every PR with an open anchor as watched and
// then checks the daemon's count against the tracker's) both need to read the
// difference, and neither `status` nor `explain` says it.
//
// # Request
//
// The wire argument is {"ids": ["owner/repo#n", ...]}; the single form
// {"id": "owner/repo#n"} is the same with one id. An id is a wire reference or a
// node id. At least one id is required and at most MaxIDs are accepted
// (invalid_argument otherwise, naming the limit); a malformed reference is
// invalid_argument and nothing is answered. The same PR named twice is answered
// once, under the first spelling.
//
// # Result
//
// results has one entry per distinct PR named, in request order: id as asked,
// ref and node_id when the daemon knows the PR, known, tracked, watched,
// watched_at and watched_via (RFC 3339 and the cause, "query:<name>", "direct" or
// "adhoc:<name>", present only for a watched PR) and tombstoned. An id the daemon has
// never seen is answered known=false, tracked=false, watched=false and is not an
// error: it is a count, not a lookup. counts sums them: requested (distinct
// PRs), known, unknown, tracked and watched.
package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op, on the pr instance.
const OpName = "inspect"

// MaxIDs is the most ids one request may name. A caller with more asks in
// chunks and adds the counts.
const MaxIDs = 2000

// Store is the part of the daemon store the op reads. *store.Store satisfies it.
type Store interface {
	entity.AliasStore
	GetEntity(ctx context.Context, kind, id string) (store.EntityRow, bool, error)
}

var _ Store = (*store.Store)(nil)

// Tracker answers the keep-alive question for a list of node ids in one pass.
// *summary.Service satisfies it (InKeepAliveSet).
type Tracker interface {
	InKeepAliveSet(ctx context.Context, ids []string) (map[string]bool, error)
}

// Config wires the handler.
type Config struct {
	Store   Store
	Tracker Tracker
}

// Item is the answer for one pull request.
type Item struct {
	// ID is the id as asked.
	ID string `json:"id"`
	// Ref and NodeID are set when the daemon knows the PR.
	Ref    string `json:"ref,omitempty"`
	NodeID string `json:"node_id,omitempty"`

	Known   bool `json:"known"`
	Tracked bool `json:"tracked"`
	Watched bool `json:"watched"`
	// WatchedAt and WatchedVia say when the flag was first set and by what
	// cause; they are present only while Watched.
	WatchedAt  *time.Time `json:"watched_at,omitempty"`
	WatchedVia string     `json:"watched_via,omitempty"`
	Tombstoned bool       `json:"tombstoned,omitempty"`
}

// Counts sums the answer.
type Counts struct {
	// Requested is the number of distinct PRs named.
	Requested int `json:"requested"`
	Known     int `json:"known"`
	Unknown   int `json:"unknown"`
	Tracked   int `json:"tracked"`
	Watched   int `json:"watched"`
}

// Result is the answer.
type Result struct {
	Instance string `json:"instance"`
	Results  []Item `json:"results"`
	Counts   Counts `json:"counts"`
}

// Register installs inspect under the pr instance.
func Register(r *router.Router, cfg Config) error {
	h, err := Handler(cfg)
	if err != nil {
		return err
	}
	r.Register("pr", OpName, h)
	return nil
}

// Handler returns the op handler.
func Handler(cfg Config) (scriptout.OpHandler, error) {
	if cfg.Store == nil || cfg.Tracker == nil {
		return scriptout.OpHandler{}, errors.New("inspect: handler needs a store and a tracker")
	}
	h := &handler{cfg: cfg}
	return scriptout.OpHandler{SchemaVersion: schema.PRSchemaVersion, Handle: h.handle}, nil
}

type handler struct{ cfg Config }

type wireArgs struct {
	ID  string   `json:"id"`
	IDs []string `json:"ids"`
}

// target is one distinct PR named by the request.
type target struct {
	asked string
	ref   entity.Ref // set when the id was a reference
	isRef bool
	node  string // set once resolved
	known bool   // an alias (a reference) or a node id to look the entity up by
	canon entity.Ref
}

func (h *handler) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var a wireArgs
	if err := scriptout.Decode(raw, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode inspect args: "+err.Error())
	}
	asked := a.IDs
	if a.ID != "" {
		asked = append([]string{a.ID}, asked...)
	}
	if len(asked) == 0 {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, `inspect needs the argument "ids" (owner/repo#n, ...) or "id"`)
	}
	if len(asked) > MaxIDs {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("inspect takes at most %d ids, got %d; ask in chunks", MaxIDs, len(asked)))
	}

	targets, err := h.parse(asked)
	if err != nil {
		return nil, err
	}
	if err := h.resolve(ctx, targets); err != nil {
		return nil, err
	}
	nodes := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.known {
			nodes = append(nodes, t.node)
		}
	}
	tracked, err := h.cfg.Tracker.InKeepAliveSet(ctx, nodes)
	if err != nil {
		return nil, err
	}

	router.Annotate(ctx, "store", false, 0, 0)
	res := Result{Instance: "pr", Results: make([]Item, 0, len(targets))}
	seen := map[string]bool{}
	for _, t := range targets {
		it := Item{ID: t.asked}
		if t.known {
			if seen[t.node] { // two spellings of one PR: counted once
				continue
			}
			seen[t.node] = true
			row, ok, err := h.cfg.Store.GetEntity(ctx, entity.KindPR, t.node)
			if err != nil {
				return nil, err
			}
			it.NodeID = t.node
			if t.canon != (entity.Ref{}) {
				it.Ref = t.canon.String()
			}
			if ok {
				it.Known = true
				it.Tombstoned = !row.TombstonedAt.IsZero()
				it.Tracked = tracked[t.node]
				if !row.WatchedAt.IsZero() {
					at := row.WatchedAt.UTC().Truncate(time.Second)
					it.Watched, it.WatchedAt, it.WatchedVia = true, &at, row.WatchedVia
				}
			}
		}
		res.Results = append(res.Results, it)
	}
	for _, it := range res.Results {
		res.Counts.Requested++
		switch {
		case !it.Known:
			res.Counts.Unknown++
		default:
			res.Counts.Known++
		}
		if it.Tracked {
			res.Counts.Tracked++
		}
		if it.Watched {
			res.Counts.Watched++
		}
	}
	return res, nil
}

// parse turns each asked id into a target, rejecting a malformed reference.
// Two identical spellings collapse to one target.
func (h *handler) parse(asked []string) ([]*target, error) {
	var out []*target
	seen := map[string]bool{}
	for _, raw := range asked {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "inspect: an id is empty")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t := &target{asked: id}
		if strings.Contains(id, "#") {
			ref, err := entity.ParseRef(id)
			if err != nil {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("inspect: %q is not owner/repo#n or a node id: %v", id, err))
			}
			t.ref, t.isRef = ref, true
		} else {
			t.node = id
		}
		out = append(out, t)
	}
	return out, nil
}

// resolve fills node, canon and known: the references in one batched alias
// read, the node ids one by one (an indexed read each).
func (h *handler) resolve(ctx context.Context, targets []*target) error {
	identity := entity.NewIdentity(h.cfg.Store, nil)
	var refs []entity.Ref
	for _, t := range targets {
		if t.isRef {
			refs = append(refs, t.ref)
		}
	}
	var byRef map[string]entity.KnownAlias
	if len(refs) > 0 {
		var err error
		if byRef, err = identity.NodeIDs(ctx, refs); err != nil {
			return err
		}
	}
	for _, t := range targets {
		if t.isRef {
			if k, ok := byRef[t.ref.Key()]; ok {
				t.node, t.canon, t.known = k.NodeID, k.Canonical, true
			}
			continue
		}
		// A node id is looked up in the entity table; the alias only supplies the
		// reference to print, and an entity with no alias answers without one.
		t.known = true
		ref, ok, err := identity.Ref(ctx, t.node)
		if err != nil {
			return err
		}
		if ok {
			t.canon = ref
		}
	}
	return nil
}
