package changesop

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The ops, as the router keys them under the pr and ci instances.
const (
	OpChanges    = "changes"
	OpChangesAck = "changes_ack"
)

// sourceSucceeded is the ChangeSource.Status of every answer: a poll that fails
// is an error, not a degraded row.
const sourceSucceeded = "succeeded"

// Deps is what the handlers need. The feed's own Queries is built by the
// assembly; the handlers read the stored configured query rows themselves.
type Deps struct {
	Feed    *feed.Feed
	Store   *store.Store
	Aliases *entity.Identity
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

// Handlers returns the handlers of the change feed, keyed by instance and then
// op: for pr the ops changes {consumer, query, cached, reset} and changes_ack
// {consumer, query, next_seq}, each stamped with schema.PRSchemaVersion, and for
// ci the ops changes and changes_ack with the same arguments, each stamped with
// schema.CISchemaVersion and acting on the key (consumer, ci, query).
func Handlers(deps Deps) map[string]map[string]scriptout.OpHandler {
	if deps.Clock == nil {
		deps.Clock = governor.SystemClock{}
	}
	h := &handler{deps: deps}
	return map[string]map[string]scriptout.OpHandler{
		"pr": {
			OpChanges:    {SchemaVersion: schema.PRSchemaVersion, Handle: h.changes},
			OpChangesAck: {SchemaVersion: schema.PRSchemaVersion, Handle: h.ack},
		},
		"ci": {
			OpChanges:    {SchemaVersion: schema.CISchemaVersion, Handle: h.ciChanges},
			OpChangesAck: {SchemaVersion: schema.CISchemaVersion, Handle: h.ciAck},
		},
	}
}

type handler struct{ deps Deps }

// args decodes the arguments of an op into their keys; an absent or null
// argument object is no keys.
func args(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := scriptout.Decode(raw, &m); err != nil {
		return nil, invalid("decode arguments: %v", err)
	}
	return m, nil
}

// stringArg reads an optional string argument (absent or null is empty); a value
// of another type is invalid_argument.
func stringArg(m map[string]json.RawMessage, name string) (string, error) {
	raw, ok := m[name]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", invalid("argument %q must be a string", name)
	}
	return strings.TrimSpace(s), nil
}

// boolArg reads an optional boolean argument (absent or null is false); a value
// of another type is invalid_argument.
func boolArg(m map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := m[name]
	if !ok || string(raw) == "null" {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, invalid("argument %q must be a boolean", name)
	}
	return b, nil
}

// keyArgs reads the two arguments every op of the feed requires.
func keyArgs(m map[string]json.RawMessage) (consumer, query string, err error) {
	if consumer, err = stringArg(m, "consumer"); err != nil {
		return "", "", err
	}
	if query, err = stringArg(m, "query"); err != nil {
		return "", "", err
	}
	if consumer == "" {
		return "", "", invalid(`the argument "consumer" is required`)
	}
	if query == "" {
		return "", "", invalid(`the argument "query" is required`)
	}
	return consumer, query, nil
}

func (h *handler) changes(ctx context.Context, raw json.RawMessage) (any, error) {
	m, err := args(raw)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"since", "before"} {
		if _, ok := m[k]; ok {
			return nil, invalid("changes takes no time bound; the argument %q is not supported", k)
		}
	}
	consumer, query, err := keyArgs(m)
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

	res, err := h.deps.Feed.Poll(ctx, feed.PollRequest{Kind: feed.KindPR, Consumer: consumer, Query: query, Cached: cached, Reset: reset})
	if err != nil {
		return nil, mapError(OpChanges, err)
	}
	configured, err := h.deps.Store.ListQueries(ctx, true)
	if err != nil {
		return nil, mapError(OpChanges, err)
	}
	out := schema.PRChangesResult{
		Sources: []schema.ChangeSource{{
			Backend: status.RegistryNamePR, Status: sourceSucceeded,
			Version: res.Position, NextSeq: res.NextSeq,
		}},
		Changes:          []schema.PRChange{},
		SourcesFreshness: freshness(configured),
	}

	if reset {
		for _, ref := range res.Replay {
			pr, err := h.currentEntity(ctx, ref)
			if err != nil {
				return nil, mapError(OpChanges, err)
			}
			out.Changes = append(out.Changes, schema.PRChange{
				Change: feed.ChangeAdded, Source: status.RegistryNamePR, Entity: pr,
				Kinds: []string{}, Fields: json.RawMessage(`[]`),
			})
		}
		return out, nil
	}

	members := memberSet(configured, query)
	for _, row := range res.Rows {
		deliver, change := feed.DeliveryFor(row, query, members[foldRef(row.ID)])
		if !deliver {
			continue
		}
		pr, err := h.rowEntity(ctx, row.ID, row.Version, row.HeadSHA)
		if err != nil {
			return nil, mapError(OpChanges, err)
		}
		kinds := row.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		fields := row.Fields
		if len(fields) == 0 || string(fields) == "null" {
			fields = json.RawMessage(`[]`)
		}
		out.Changes = append(out.Changes, schema.PRChange{
			Change: change, Source: status.RegistryNamePR, Entity: pr, Seq: row.Seq, Kinds: kinds, Fields: fields,
		})
	}
	return out, nil
}

func (h *handler) ack(ctx context.Context, raw json.RawMessage) (any, error) {
	return h.ackKind(ctx, raw, feed.KindPR)
}

// ackKind acknowledges the key (consumer, kind, query) of the request.
func (h *handler) ackKind(ctx context.Context, raw json.RawMessage, kind string) (any, error) {
	m, err := args(raw)
	if err != nil {
		return nil, err
	}
	consumer, query, err := keyArgs(m)
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

	if err := h.deps.Feed.Ack(ctx, kind, consumer, query, next); err != nil {
		return nil, mapError(OpChangesAck, err)
	}
	// The key's stored position after the acknowledgement. The position is the
	// stored consumer row's acknowledged sequence number, which is what
	// Feed.Positions reports for the key, without that call's scan of every key.
	row, found, err := h.deps.Store.GetConsumer(ctx, consumer, kind, query)
	if err != nil {
		return nil, mapError(OpChangesAck, err)
	}
	acked := next
	if found {
		acked = row.AckedSeq
	}
	return schema.ChangesAckResult{Consumer: consumer, Query: query, AckedSeq: acked}, nil
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

// memberSet is the set of the stored members of the configured query named name,
// by folded reference. A query with no stored row has no members.
func memberSet(configured []store.QueryRow, name string) map[string]bool {
	set := map[string]bool{}
	for _, q := range configured {
		if q.Key != name {
			continue
		}
		for _, id := range q.IDs {
			set[foldRef(id)] = true
		}
	}
	return set
}

// foldRef folds the owner/repo of a wire reference to lower case and keeps the
// number exact: the form two references of one pull request compare equal in.
func foldRef(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}
