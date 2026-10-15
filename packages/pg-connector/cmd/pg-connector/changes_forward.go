// changes_forward.go: the forward-only mode of the "changes" verb, for a
// backend that declares owns_changes in its capabilities (a daemon-backed
// backend, ADR 0090). Such a backend keeps its own change log, so the umbrella
// neither diffs repeated list calls for it nor keeps any entity cache or
// ledger index for it (INV-CACHE-9). For every owning backend of the queried
// type, changes forwards the call, writes what the backend answered, and only
// after that output is flushed acknowledges it.
//
// Per backend, one forwarded call does the following:
//
//  1. Send changes {consumer, query, cached, reset} through invokeOne, so the
//     backend's registered config block travels with it as for every other op.
//     A failed call (unavailable, cursor_expired: invalid_argument, anything)
//     leaves that source degraded with the error as its reason, delivers
//     nothing, and sends no acknowledgement. The ledger diff never runs for it.
//  2. Decode the answer with the entity of each change kept as raw JSON, so pr
//     and ci entities pass through unchanged, and build the result the existing
//     output step already consumes (changesBackendResult).
//  3. Stamp the ledger key (type, backend, query) from the answer
//     (INV-LEDGER-FRESH-5), before the output is written. See stampForwarded.
//  4. After the caller's output is written and flushed, newChangesCmd's RunE
//     sends changes_ack {consumer, query, next_seq} per source that answered
//     (commitForwardedAcks); a backend that did not answer is never
//     acknowledged.
//
// Choices the design left open, each pinned by a test in
// changes_forward_test.go:
//
//   - Registry names: the umbrella OVERWRITES sources[].backend and every
//     changes[].source with the registered backend name (the daemon fills them
//     with the instance's registry name, which schema.ChangeSource lets the
//     umbrella replace), and reports the source's status as succeeded. One
//     answer's names therefore always agree, and agree with the ledger key.
//   - An answer must carry exactly one sources[] row (the polled key's own);
//     anything else is a malformed answer: degraded, no delivery, no ack.
//   - Only the POLLED query's key is stamped from sources_freshness[]; the
//     other entries (one per configured query) are ignored, so a poll never
//     creates a ledger file for a query no consumer has polled.
//   - A failed call stamps the consumer's last_seen and records the failure's
//     wire error code as the key's last_error, except for invalid_argument (a
//     caller-state answer such as cursor_expired:), unknown_op and
//     query_not_recognized (not failures of a source, INV-LEDGER-FRESH-3).
//   - last_error mapping: sources_freshness[].last_error is free text, so the
//     ledger records the code "unavailable" (the taxonomy's code for an origin
//     that could not be reached). Its at is the time the umbrella first saw
//     that failure: an unchanged, still-current failure (the ledger's last_error
//     is newer than its refreshed_at) keeps its earlier at, so "failing since"
//     stays readable; an empty last_error leaves the recorded one in place
//     (retained after a success, INV-LEDGER-FRESH-3).
//   - refreshed_at takes the backend's last_success_at verbatim (the backend's
//     time, never the umbrella's), also on a --cached call.
//   - An acknowledgement that fails is not retried and does not change the
//     answer already written: one line goes to stderr, the exit code is
//     unchanged, and the next poll redelivers (at-least-once).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// vocabularyOwnsChangesKey is the capabilities.vocabulary key a backend sets
// to declare that it owns its change log (ADR 0090).
const vocabularyOwnsChangesKey = "owns_changes"

// ledgerErrorForwarded is the LedgerError.Code recorded for a non-empty
// sources_freshness[].last_error: that field is free text, so the ledger keeps
// the taxonomy code for an origin that could not be reached.
const ledgerErrorForwarded = "unavailable"

// backendOwnsChanges asks backend's capabilities (through
// Registry.InvokeCapabilities) whether its vocabulary carries owns_changes,
// with the same presence-and-bool convention backendOptedOut uses: a bool is
// taken as is, any other present value counts as declared. A capabilities call
// that fails returns (false, err): a backend that declared nothing is not
// treated as owning (the fail-open rule of cacheEnabled, INV-CACHE-8).
func backendOwnsChanges(ctx context.Context, reg *Registry, backend string) (bool, error) {
	resp, err := reg.InvokeCapabilities(ctx, backend)
	if err != nil {
		return false, err
	}
	v, ok := resp.Vocabulary[vocabularyOwnsChangesKey]
	if !ok {
		return false, nil
	}
	if b, isBool := v.(bool); isBool {
		return b, nil
	}
	return true, nil
}

// forwardedChange is one changes[] entry of a forwarded answer, with the entity
// kept as raw JSON (schema.PRChange and schema.CIChange differ in the entity's
// shape only).
type forwardedChange struct {
	Change string          `json:"change"`
	Source string          `json:"source"`
	Entity json.RawMessage `json:"entity"`
	Seq    int64           `json:"seq"`
	Kinds  []string        `json:"kinds"`
	Fields json.RawMessage `json:"fields"`
}

// forwardedChangesResult is the answer of a forwarded changes op for either
// entity type (schema.PRChangesResult / schema.CIChangesResult).
type forwardedChangesResult struct {
	Sources          []schema.ChangeSource    `json:"sources"`
	Changes          []forwardedChange        `json:"changes"`
	SourcesFreshness []schema.SourceFreshness `json:"sources_freshness"`
}

// forwardChangesOne is the per-backend forward path fanOutChangesOne calls for
// an owning backend; entityType is pr or ci. It never reads or writes the
// entity cache, and never touches the ledger's entity index, version or
// cursor. See this file's header comment.
func forwardChangesOne(ctx context.Context, reg *Registry, entityType, backend, query, consumerID string, cached, reset bool) changesBackendResult {
	key := LedgerKey{Type: entityType, Backend: backend, Query: query, Instance: ledgerInstanceDiscriminator(entityType)}
	// skipAdvance and a nil ledger keep every ledger-path commit step
	// (commitAdvances, commitCacheWrites, commitCacheTombstones) away from it.
	res := changesBackendResult{backend: backend, key: key, forwarded: true, skipAdvance: true}

	fail := func(err error) changesBackendResult {
		sr := classifyListSource(backend, err)
		res.status = sr.Status
		res.reason = sr.Reason
		if serr := stampForwarded(reg, key, consumerID, time.Now(), nil, err); serr != nil {
			res.notes = append(res.notes, serr.Error())
		}
		return res
	}

	resp, err := invokeOne(ctx, reg, backend, "changes", map[string]any{
		"consumer": consumerID, "query": query, "cached": cached, "reset": reset,
	})
	if err != nil {
		return fail(err)
	}
	var ans forwardedChangesResult
	if err := scriptout.Decode(resp.Result, &ans); err != nil {
		return fail(scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("%s: invalid changes answer: %v", backend, err)))
	}
	if len(ans.Sources) != 1 {
		return fail(scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("%s: changes answer carries %d sources rows, want exactly 1", backend, len(ans.Sources))))
	}

	src := ans.Sources[0]
	nextSeq := src.NextSeq
	res.status = SourceSucceeded
	res.reason = src.Reason
	res.truncated = src.Truncated
	res.version = src.Version
	res.nextSeq = &nextSeq
	res.answered = true
	res.freshness = ans.SourcesFreshness
	if res.freshness == nil {
		res.freshness = []schema.SourceFreshness{}
	}
	res.entries = make([]changesEntry, 0, len(ans.Changes))
	for _, c := range ans.Changes {
		seq := c.Seq
		kinds, _ := json.Marshal(nonNilStrings(c.Kinds))
		res.entries = append(res.entries, changesEntry{
			Change: ChangeKind(c.Change),
			Source: backend, // registered name, see this file's header comment
			Entity: c.Entity,
			Seq:    &seq,
			Kinds:  kinds,
			Fields: nonNullArray(c.Fields),
		})
	}

	var fresh *schema.SourceFreshness
	for i := range ans.SourcesFreshness {
		if ans.SourcesFreshness[i].Query == query {
			fresh = &ans.SourcesFreshness[i]
			break
		}
	}
	if serr := stampForwarded(reg, key, consumerID, time.Now(), fresh, nil); serr != nil {
		res.notes = append(res.notes, serr.Error())
	}
	return res
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nonNullArray returns raw, or an empty JSON array when raw is absent or null,
// so a forwarded change's fields is an array and never null.
func nonNullArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}

// stampForwarded records one forwarded call on the ledger key (INV-LEDGER-FRESH-5,
// INV-CACHE-9): the consumer's last_seen is set to now (its cursor left alone),
// and
//   - on an answer (callErr == nil), refreshed_at and last_error are set from
//     fresh, the sources_freshness[] entry of the polled query: refreshed_at
//     takes its last_success_at (nothing is recorded when there is none or it
//     does not parse), and a non-empty last_error is recorded;
//   - on a failed call (callErr != nil), the failure's wire error code is
//     recorded as last_error unless it is not a failure of a source.
//
// The entity index, version and cursor are never touched, and the ledger is
// saved even when the key had no file before, so the (type, backend, query) key
// is never a ghost row of the consumer's freshness view. A failure to stamp is
// returned for the caller to report; it never invalidates the answer.
func stampForwarded(reg *Registry, key LedgerKey, consumerID string, now time.Time, fresh *schema.SourceFreshness, callErr error) error {
	l, err := loadLedgerAdopting(key, reg.PreviousNames(key.Backend))
	if err != nil {
		return fmt.Errorf("changes: stamp ledger for backend %q: %w", key.Backend, err)
	}
	cs := l.Consumers[consumerID]
	cs.LastSeen = now
	l.Consumers[consumerID] = cs

	switch {
	case callErr != nil:
		if forwardedFailureRecorded(callErr) {
			recordForwardedError(l, now, scriptout.CodeForError(callErr))
		}
	case fresh != nil:
		if t, ok := parseFreshnessTime(fresh.LastSuccessAt); ok {
			l.RefreshedAt = &t
		}
		if fresh.LastError != "" {
			recordForwardedError(l, now, ledgerErrorForwarded)
		}
	}
	if err := saveLedger(key, l); err != nil {
		return fmt.Errorf("changes: stamp ledger for backend %q: %w", key.Backend, err)
	}
	return nil
}

// forwardedFailureRecorded reports whether a failed forwarded call is a failure
// of the source and so recorded as the key's last_error: not invalid_argument
// (the caller's own state, such as cursor_expired:), and not the two answers
// RecordFetchOutcome also treats as "not a source".
func forwardedFailureRecorded(err error) bool {
	return !errors.Is(err, scriptout.ErrInvalidArgument) &&
		!errors.Is(err, scriptout.ErrUnknownOp) &&
		!errors.Is(err, scriptout.ErrQueryNotRecognized)
}

// recordForwardedError records code as the key's last_error. A failure that is
// still current (same code, newer than refreshed_at) keeps its earlier at, so a
// reader still tells "failing since" from "recovered" by comparing at with
// refreshed_at.
func recordForwardedError(l *Ledger, now time.Time, code string) {
	if cur := l.LastError; cur != nil && cur.Code == code && (l.RefreshedAt == nil || cur.At.After(*l.RefreshedAt)) {
		return
	}
	l.RecordFetchError(now, code)
}

func parseFreshnessTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// commitForwardedAcks is the post-flush step of a forwarded call: for every
// forwarded source that answered, send changes_ack {consumer, query, next_seq}
// with THAT source's own next_seq, also when it returned no change. A source
// that did not answer is never acknowledged. The ack's own payload is ignored.
// An ack that fails is not retried and is reported as one line on errOut only:
// the rows were already delivered, and the next poll redelivers them.
func commitForwardedAcks(ctx context.Context, reg *Registry, results []changesBackendResult, consumerID, query string, errOut io.Writer) {
	for _, r := range results {
		if !r.forwarded || !r.answered || r.nextSeq == nil {
			continue
		}
		_, err := invokeOne(ctx, reg, r.backend, "changes_ack", map[string]any{
			"consumer": consumerID, "query": query, "next_seq": *r.nextSeq,
		})
		if err != nil {
			fmt.Fprintf(errOut, "pg-connector: changes: acknowledging position %d of consumer %q query %q on backend %q failed (the changes were delivered; the next poll redelivers them): %v\n",
				*r.nextSeq, consumerID, query, r.backend, err)
		}
	}
}

// reportForwardNotes writes the notes a forwarded call collected (a ledger stamp
// that could not be written) to errOut, one line each.
func reportForwardNotes(results []changesBackendResult, errOut io.Writer) {
	for _, r := range results {
		for _, n := range r.notes {
			fmt.Fprintf(errOut, "pg-connector: %s\n", n)
		}
	}
}
