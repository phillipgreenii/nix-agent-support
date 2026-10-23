package feedop

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Deps is what the handlers need. The feed's own Queries is built by the
// assembly; the handlers read the stored configured query rows themselves.
type Deps struct {
	Feed  *feed.Feed
	Store *store.Store
	// View is the view this instance serves: a name of Feed.Spec().Views.
	View string
	// Source is the registry name written to sources[].backend and
	// changes[].source.
	Source   string
	Renderer Renderer
	// AuthErrors are the errors that map to unauthenticated.
	AuthErrors    []error
	SchemaVersion int
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers builds the two ops of the change feed for one view, keyed "changes"
// and "changes_ack": changes {consumer, query?, cached, reset} and changes_ack
// {consumer, query?, next_seq}, each stamped with Deps.SchemaVersion and acting on
// the consumer's one cursor (bound to Deps.View and the query it names). The query
// is required for a registration that delivers by membership and optional for
// one that delivers by watched marker. Every answer carries the store's identity
// (store_identity).
func Handlers(d Deps) map[string]scriptout.OpHandler {
	if d.Clock == nil {
		d.Clock = governor.SystemClock{}
	}
	h := &handler{deps: d}
	ops := make(map[string]scriptout.OpHandler, 2)
	ops["changes"] = scriptout.OpHandler{SchemaVersion: d.SchemaVersion, Handle: h.changes}
	ops["changes_ack"] = scriptout.OpHandler{SchemaVersion: d.SchemaVersion, Handle: h.ack}
	return ops
}

type handler struct{ deps Deps }

// queryRequired reports whether a call must name a query: a registration that
// delivers by membership has no rows without one.
func (h *handler) queryRequired() bool {
	return h.deps.Feed.Spec().Delivery == registry.DeliverByMembership
}

// view is the registered view the instance serves; a name the Spec does not
// register is the zero view (the feed itself refuses such a poll).
func (h *handler) view() registry.View {
	for _, v := range h.deps.Feed.Spec().Views {
		if v.Name == h.deps.View {
			return v
		}
	}
	return registry.View{}
}

func (h *handler) changes(ctx context.Context, raw json.RawMessage) (any, error) {
	m, err := args(raw)
	if err != nil {
		return nil, err
	}
	if err := noTimeBound(m, "since"); err != nil {
		return nil, err
	}
	if err := noTimeBound(m, "before"); err != nil {
		return nil, err
	}
	consumer, query, err := keyArgs(m, h.queryRequired())
	if err != nil {
		return nil, err
	}
	cached, err := boolArg(m, "cached")
	if err != nil {
		return nil, err
	}
	reset, err := boolArg(m, "reset")
	if err != nil {
		return nil, err
	}
	router.Annotate(ctx, "store", false, 0, 0)

	res, err := h.deps.Feed.Poll(ctx, feed.PollRequest{View: h.deps.View, Consumer: consumer, Scope: query, Cached: cached, Reset: reset})
	if err != nil {
		return nil, h.mapError("changes", err)
	}
	configured, err := h.deps.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, h.mapError("changes", err)
	}
	out := schema.ChangesResult{
		Sources: []schema.ChangeSource{{
			Backend: h.deps.Source, Status: schema.ChangeSourceSucceeded,
			Version: res.Position, NextSeq: res.NextSeq,
		}},
		Changes:          []schema.Change{},
		SourcesFreshness: freshness(configured),
		StoreIdentity:    res.StoreID,
	}

	if reset {
		for _, ref := range res.Replay {
			entity, err := h.deps.Renderer.Replay(ctx, ref)
			if err != nil {
				return nil, h.mapError("changes", err)
			}
			out.Changes = append(out.Changes, schema.Change{
				Change: feed.ChangeAdded, Source: h.deps.Source, Entity: entity,
				Kinds: []string{}, Fields: json.RawMessage(`[]`),
			})
		}
		return out, nil
	}

	spec, view := h.deps.Feed.Spec(), h.view()
	members := memberSet(configured, query, spec.FoldRef)
	for _, row := range res.Rows {
		deliver, change := feed.Deliver(spec, view, row, query, members(row.ID))
		if !deliver {
			continue
		}
		entity, err := h.deps.Renderer.Row(ctx, row)
		if err != nil {
			return nil, h.mapError("changes", err)
		}
		kinds := row.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		fields := row.Fields
		if len(fields) == 0 || string(fields) == "null" {
			fields = json.RawMessage(`[]`)
		}
		out.Changes = append(out.Changes, schema.Change{
			Change: change, Source: h.deps.Source, Entity: entity, Seq: row.Seq, Kinds: kinds, Fields: fields,
		})
	}
	return out, nil
}

// noTimeBound refuses the time-bound argument name: the feed has a position, not a
// time window.
func noTimeBound(m map[string]json.RawMessage, name string) error {
	if _, ok := m[name]; ok {
		return invalid("changes takes no time bound; the argument %q is not supported", name)
	}
	return nil
}

func (h *handler) ack(ctx context.Context, raw json.RawMessage) (any, error) {
	m, err := args(raw)
	if err != nil {
		return nil, err
	}
	consumer, query, err := keyArgs(m, h.queryRequired())
	if err != nil {
		return nil, err
	}
	rawSeq, ok := m["next_seq"]
	if !ok {
		return nil, invalid(`the argument "next_seq" is required`)
	}
	var next int64
	if err := json.Unmarshal(rawSeq, &next); err != nil || string(rawSeq) == "null" || next < 0 {
		return nil, invalid(`the argument "next_seq" must be a JSON integer, zero or more`)
	}
	router.Annotate(ctx, "store", false, 0, 0)

	res, err := h.deps.Feed.Ack(ctx, feed.AckRequest{View: h.deps.View, Consumer: consumer, Scope: query, NextSeq: next})
	if err != nil {
		return nil, h.mapError("changes_ack", err)
	}
	return schema.ChangesAckResult{Consumer: consumer, Query: query, AckedSeq: res.AckedSeq, StoreIdentity: res.StoreID}, nil
}

func (h *handler) mapError(op string, err error) error {
	return MapError(op, err, h.deps.AuthErrors)
}

// freshness is the sources_freshness of the configured queries' stored rows:
// every query with the time of its last whole-query origin success (empty when
// it never had one) and its last error.
func freshness(configured []store.QueryRow) []schema.SourceFreshness {
	out := make([]schema.SourceFreshness, 0, len(configured))
	for _, q := range configured {
		f := schema.SourceFreshness{Query: q.Key, LastError: q.LastError}
		if !q.LastSuccessAt.IsZero() {
			f.LastSuccessAt = q.LastSuccessAt.UTC().Format(time.RFC3339)
		}
		out = append(out, f)
	}
	return out
}

// memberSet returns the membership test of the stored members of the configured
// query named name, by folded reference. A query with no stored row has no
// members.
func memberSet(configured []store.QueryRow, name string, fold func(string) string) func(ref string) bool {
	if fold == nil {
		fold = func(s string) string { return s }
	}
	set := map[string]bool{}
	for _, q := range configured {
		if q.Key != name {
			continue
		}
		for _, id := range q.IDs {
			set[fold(id)] = true
		}
	}
	return func(ref string) bool { return set[fold(ref)] }
}
