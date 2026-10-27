package gather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnavailable is the sentinel (errors.Is) for pg-connector's wire error
// "unavailable": the backend's daemon cannot answer right now. A caller
// treats it as a degraded run and retries; it never concludes not_found from
// it.
var ErrUnavailable = errors.New("pg-connector: unavailable")

// cursorExpiredPrefix starts a source reason when the backend dropped the
// feed key a consumer's cursor pointed at.
const cursorExpiredPrefix = "cursor_expired:"

// wireErrorCode is the error code of a failed call's stdout (a wireEnvelope
// error branch), or "" when the stdout is not such an envelope.
func wireErrorCode(stdout []byte) string {
	var env wireEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil && env.Error != nil {
		return env.Error.Code
	}
	return ""
}

// unavailableError keeps the call's own error text unchanged (callers print it)
// while matching ErrUnavailable under errors.Is.
type unavailableError struct{ msg string }

func (e *unavailableError) Error() string        { return e.msg }
func (e *unavailableError) Is(target error) bool { return target == ErrUnavailable }

// exitError is the error for a pg-connector call that exited with a code the
// caller treats as failure. A wire error "unavailable" matches ErrUnavailable
// under errors.Is; the message is the same either way.
func exitError(args []string, code int, stdout []byte) error {
	msg := fmt.Sprintf("pg-connector %v: exit %d: %s", args, code, wireErrorMessage(stdout))
	if wireErrorCode(stdout) == "unavailable" {
		return &unavailableError{msg: msg}
	}
	return errors.New(msg)
}

// FeedRow is one row of an owning backend's change feed: the log position,
// the change kinds and changed field names the backend reported, and the
// entity the row is about (raw, as the backend sent it).
type FeedRow struct {
	Seq      int64
	Kinds    []string
	Fields   json.RawMessage
	EntityID string
	Entity   json.RawMessage
}

// FeedSource is one sources[] row of a feed answer. NextSeq is the log
// position scanned up to (it can pass the last returned row) and nil when the
// source reported none.
type FeedSource struct {
	Backend       string
	Status        string
	Reason        string
	NextSeq       *int64
	StoreIdentity string
	// CursorExpired is true when Reason starts with "cursor_expired:" (the
	// backend dropped the feed key the consumer's cursor pointed at).
	CursorExpired bool
}

// FeedFreshness is one sources_freshness[] row: when a query last answered
// and its last error.
type FeedFreshness struct {
	Query         string
	LastSuccessAt string
	LastError     string
}

// FeedResult is what one ListFeed call returns.
type FeedResult struct {
	Sources   []FeedSource
	Rows      []FeedRow
	Freshness []FeedFreshness
}

type feedWireOut struct {
	Sources []struct {
		Backend       string `json:"backend"`
		Status        string `json:"status"`
		Reason        string `json:"reason"`
		NextSeq       *int64 `json:"next_seq"`
		StoreIdentity string `json:"store_identity"`
	} `json:"sources"`
	Changes []struct {
		Seq    int64           `json:"seq"`
		Kinds  []string        `json:"kinds"`
		Fields json.RawMessage `json:"fields"`
		Entity json.RawMessage `json:"entity"`
	} `json:"changes"`
	Freshness []struct {
		Query         string `json:"query"`
		LastSuccessAt string `json:"last_success_at"`
		LastError     string `json:"last_error"`
	} `json:"sources_freshness"`
	Error *wireError `json:"error"`
}

// ListFeed runs `pg-connector <type> changes [--query <query>] --consumer
// <consumer> --backend <backend> --no-ack [--reset] --output json` through
// the exec chokepoint: the feed is read pinned to one backend and is NOT
// acknowledged (AckFeed does that). An empty query passes no --query (the
// daemon keeps one cursor per consumer, so a caller reads each owning
// instance with an empty query). Exit 0, 2 and 3 with a parseable JSON
// document are answers and are RETURNED with their sources (so CursorExpired
// is visible); an unparseable answer, any other exit, or the wire error
// unavailable (wrapping ErrUnavailable) is an error.
func (g *Gatherer) ListFeed(ctx context.Context, entityType, query, consumer, backend string, reset bool) (FeedResult, error) {
	if entityType == "" || consumer == "" || backend == "" {
		return FeedResult{}, fmt.Errorf("gather: list feed: type, consumer and backend are required")
	}
	args := []string{entityType, "changes"}
	if query != "" {
		args = append(args, "--query", query)
	}
	args = append(args, "--consumer", consumer, "--backend", backend, "--no-ack")
	if reset {
		args = append(args, "--reset")
	}
	args = append(args, "--output", "json")
	res, err := g.run(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return FeedResult{}, err
	}
	switch res.exitCode {
	case 0, 2, 3:
	default:
		return FeedResult{}, exitError(args, res.exitCode, res.stdout)
	}
	if wireErrorCode(res.stdout) == "unavailable" {
		return FeedResult{}, exitError(args, res.exitCode, res.stdout)
	}
	var wire feedWireOut
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return FeedResult{}, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	if wire.Error != nil {
		return FeedResult{}, exitError(args, res.exitCode, res.stdout)
	}
	out := FeedResult{
		Sources:   make([]FeedSource, 0, len(wire.Sources)),
		Rows:      make([]FeedRow, 0, len(wire.Changes)),
		Freshness: make([]FeedFreshness, 0, len(wire.Freshness)),
	}
	for _, s := range wire.Sources {
		out.Sources = append(out.Sources, FeedSource{
			Backend: s.Backend, Status: s.Status, Reason: s.Reason,
			NextSeq: s.NextSeq, StoreIdentity: s.StoreIdentity,
			CursorExpired: strings.HasPrefix(s.Reason, cursorExpiredPrefix),
		})
	}
	for _, c := range wire.Changes {
		var ent changesEntityWire
		if err := json.Unmarshal(c.Entity, &ent); err != nil {
			return FeedResult{}, fmt.Errorf("decode pg-connector %v entity: %w", args, err)
		}
		if ent.ID == "" {
			return FeedResult{}, fmt.Errorf("pg-connector %v reported feed row %d for an entity with no id", args, c.Seq)
		}
		out.Rows = append(out.Rows, FeedRow{Seq: c.Seq, Kinds: c.Kinds, Fields: c.Fields, EntityID: ent.ID, Entity: c.Entity})
	}
	for _, f := range wire.Freshness {
		out.Freshness = append(out.Freshness, FeedFreshness{Query: f.Query, LastSuccessAt: f.LastSuccessAt, LastError: f.LastError})
	}
	return out, nil
}

// AckFeed runs `pg-connector <type> changes-ack [--query <query>] --consumer
// <consumer> --next-seq <n> --backend <backend> --output json` and returns the
// backend's acked_seq. An empty query passes no --query. Exit 0 is the only
// success; the wire error unavailable wraps ErrUnavailable. The answer is read
// whether or not it is wrapped in a result envelope.
func (g *Gatherer) AckFeed(ctx context.Context, entityType, query, consumer, backend string, nextSeq int64) (ackedSeq int64, err error) {
	if entityType == "" || consumer == "" || backend == "" {
		return 0, fmt.Errorf("gather: ack feed: type, consumer and backend are required")
	}
	if nextSeq < 0 {
		return 0, fmt.Errorf("gather: ack feed: next seq %d is negative", nextSeq)
	}
	args := []string{entityType, "changes-ack"}
	if query != "" {
		args = append(args, "--query", query)
	}
	args = append(args, "--consumer", consumer, "--next-seq", fmt.Sprint(nextSeq), "--backend", backend, "--output", "json")
	res, err := g.run(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return 0, err
	}
	if res.exitCode != 0 {
		return 0, exitError(args, res.exitCode, res.stdout)
	}
	var wire struct {
		AckedSeq *int64 `json:"acked_seq"`
		Result   *struct {
			AckedSeq *int64 `json:"acked_seq"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return 0, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	switch {
	case wire.AckedSeq != nil:
		return *wire.AckedSeq, nil
	case wire.Result != nil && wire.Result.AckedSeq != nil:
		return *wire.Result.AckedSeq, nil
	}
	return 0, fmt.Errorf("pg-connector %v: answer has no acked_seq", args)
}

func (g *Gatherer) feedEnv(entityType string) []string {
	if entityType == "issue" {
		return g.issueBeadsDirEnv()
	}
	return nil
}

// HoldsIssue reports whether backend holds the issue id: `issue show <id>
// --fresh --backend <backend>` exiting 0 is held, exiting 4 is not held with a
// nil error, and the wire error unavailable (wrapping ErrUnavailable) or any
// other failure is an error. --fresh keeps a umbrella cache entry from
// answering for the backend.
func (g *Gatherer) HoldsIssue(ctx context.Context, id, backend string) (held bool, err error) {
	if id == "" || backend == "" {
		return false, fmt.Errorf("gather: holds issue: id and backend are required")
	}
	args := []string{"issue", "show", id, "--fresh", "--backend", backend}
	res, err := g.run(ctx, args, g.issueBeadsDirEnv())
	if err != nil {
		return false, err
	}
	switch res.exitCode {
	case 0:
		return true, nil
	case 4:
		return false, nil
	default:
		return false, exitError(args, res.exitCode, res.stdout)
	}
}

// MemberRef is one entity of a query listing: its id and the stated
// entity_meta.watched (nil = not stated).
type MemberRef struct {
	ID      string
	Watched *bool
}

// MemberList is a query's current listing on one backend; Truncated means the
// listing is incomplete.
type MemberList struct {
	Members   []MemberRef
	Truncated bool
}

// ListQueryMembers runs `pg-connector issue list --query <query> --backend
// <backend> --output json` and decodes entities[].id, each entity's stated
// entity_meta.watched and the truncated flag. Exit 0 and 2 are answers; the
// wire error unavailable wraps ErrUnavailable.
func (g *Gatherer) ListQueryMembers(ctx context.Context, query, backend string) (MemberList, error) {
	if query == "" || backend == "" {
		return MemberList{}, fmt.Errorf("gather: list query members: query and backend are required")
	}
	args := []string{"issue", "list", "--query", query, "--backend", backend, "--output", "json"}
	res, err := g.run(ctx, args, g.issueBeadsDirEnv())
	if err != nil {
		return MemberList{}, err
	}
	switch res.exitCode {
	case 0, 2:
	default:
		return MemberList{}, exitError(args, res.exitCode, res.stdout)
	}
	var wire struct {
		Entities []struct {
			ID         string `json:"id"`
			EntityMeta struct {
				Watched *bool `json:"watched"`
			} `json:"entity_meta"`
		} `json:"entities"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return MemberList{}, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	out := MemberList{Members: make([]MemberRef, 0, len(wire.Entities)), Truncated: wire.Truncated}
	for _, e := range wire.Entities {
		if e.ID == "" {
			return MemberList{}, fmt.Errorf("pg-connector %v listed an entity with no id", args)
		}
		out.Members = append(out.Members, MemberRef{ID: e.ID, Watched: e.EntityMeta.Watched})
	}
	return out, nil
}

// EntityMetaWatched reads entity_meta.watched from a raw issue show result:
// stated is false when entity_meta or its watched member is absent (or null),
// so "false" and "not stated" stay distinct.
func EntityMetaWatched(issueShow json.RawMessage) (watched, stated bool) {
	var doc struct {
		EntityMeta *struct {
			Watched *bool `json:"watched"`
		} `json:"entity_meta"`
	}
	if err := json.Unmarshal(issueShow, &doc); err != nil || doc.EntityMeta == nil || doc.EntityMeta.Watched == nil {
		return false, false
	}
	return *doc.EntityMeta.Watched, true
}

// degradedReason names why an issue show answer is not a trustworthy fresh
// read ("" = trustworthy): stale: true, or served_from other than origin
// (that is, cache). A missing served_from is not degraded.
func degradedReason(issueShow json.RawMessage) string {
	var doc struct {
		Stale      bool   `json:"stale"`
		ServedFrom string `json:"served_from"`
	}
	if err := json.Unmarshal(issueShow, &doc); err != nil {
		return ""
	}
	switch {
	case doc.Stale:
		return "issue answer is stale"
	case doc.ServedFrom != "" && doc.ServedFrom != "origin":
		return "issue answer served from " + doc.ServedFrom
	}
	return ""
}
