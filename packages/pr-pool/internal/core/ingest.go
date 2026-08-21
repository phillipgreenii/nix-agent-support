package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// SubcommandIngestEvent is the INTF-CLI callback target a PUSH source — or a pull
// source's DEFERRED query reply — uses to deliver one or more events
// (interfaces.md "Manager→core callback subcommands").
const SubcommandIngestEvent = "ingest-event"

// The message types backing this subcommand (schemas/, checked via package
// conformance — INV-INTF-2).
const (
	IngestRequestSchema = "cli.ingest-event"
	IngestReplySchema   = "cli.ingest-event-reply"
	// eventSchema is the shared per-event shape the envelope's items $ref.
	eventSchema = "event"
)

// errIngestEnvelope classifies a fault in the REQUEST ENVELOPE (as opposed to a
// fault in one event). An envelope fault means no event could be read at all, so
// the reply is the protocol error envelope, not an accepted/rejected tally.
var errIngestEnvelope = errors.New("ingest-event")

// ingestEnvelopeFields is the CLOSED top-level field set of cli.ingest-event —
// the schema artifact sets additionalProperties:false, so an unexpected field is
// a rejection, not a field to ignore.
var ingestEnvelopeFields = map[string]bool{"schemaVersion": true, "id": true, "events": true}

// rejection is one entry of the reply's `rejected` list.
type rejection struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// ingestReply is the cli.ingest-event-reply shape: the tracking id echoed back,
// how many events were accepted, and the per-event rejections.
type ingestReply struct {
	SchemaVersion string      `json:"schemaVersion"`
	ID            string      `json:"id"`
	Accepted      int         `json:"accepted"`
	Rejected      []rejection `json:"rejected"`
}

// ingestRequest is the decoded envelope. Events stay RAW so each one can be
// schema-checked and decoded individually — which is what lets a batch with one
// bad event still deliver the good ones.
type ingestRequest struct {
	ID     string
	Events []json.RawMessage
}

// handleIngestEvent runs the `ingest-event` callback: it validates the envelope,
// then validates and enqueues each event, and replies with the accepted count and
// the per-event `rejected` list (interfaces.md).
//
// Semantics that are easy to get wrong, all from interfaces.md / INV-DISP-3 /
// INV-EVT-*:
//
//   - An event whose `type` NO CONFIGURED BINDING DECLARES is REJECTED to the
//     caller (reason `unknown type: …`, exit 1). It is not enqueued, and the
//     condition is logged and counted. Pre-runtime validation already blocks
//     startup on it (INV-WORKFLOW-1), so at runtime it can only mean a source
//     emitted a `type` its own configuration never declared — an error to report,
//     never a silent drop (INV-DISP-3).
//   - An event whose type IS declared but whose binding is merely INACTIVE this
//     run is ACCEPTED and enqueued. It waits, is offered to nobody, and is dropped
//     unconsumed-expired (INV-EVT-1, INV-EVT-4) — expected, and neither an error
//     nor a warning. Validity is judged against the CONFIGURATION, never against
//     the run's active subset, so this case must never be folded into the one
//     above.
//   - A still-retained DUPLICATE id is ACCEPTED too — de-duplication is the core
//     doing its job, so a source never has to track "did I already emit this"
//     (INV-EVT-3).
//   - `rejected` therefore carries the events that could not enter the queue —
//     malformed ones (reason `malformed: …`) and, rarely, a durable-write failure
//     (reason `enqueue: …`) — plus the events the core REFUSED to queue because
//     their type is unknown to the configuration (reason `unknown type: …`).
//   - The tracking id is echoed, never required to be one the core issued. A push
//     source mints its own id, so requiring a known id would break push ingest
//     outright. (The "unknown tracking id ⇒ acknowledged and ignored" rule in
//     INV-INTF-1 governs callbacks that CORRELATE to a core-issued call; ingest
//     from a push source is not one.)
//
// Exit code: 0 when everything was accepted, 1 when anything was rejected — the
// exit-1 reply carrying a populated `rejected` list is DEC-WIRE-1's own example
// (docs/decisions/wire.md, which owns the wire examples). The busy code (9) is
// never returned: the durable queue does not decline ingest, so this handler has
// no pre-accept decline to signal at all.
func (s *Service) handleIngestEvent(stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply("ingest-event: read request: "+err.Error()))
		return conformance.ExitError
	}
	req, err := decodeIngest(data)
	if err != nil {
		writeBody(stdout, errorReply(err.Error()))
		return conformance.ExitError
	}

	reply := ingestReply{SchemaVersion: schemas.SchemaVersion, ID: req.ID, Rejected: []rejection{}}
	for _, raw := range req.Events {
		// Schema first (INV-INTF-2): the conformance checker is the declared
		// contract, so nothing enters the queue that the suite would reject.
		if err := conformance.CheckBytes(eventSchema, raw); err != nil {
			reply.Rejected = append(reply.Rejected, rejection{ID: rawEventID(raw), Reason: "malformed: " + err.Error()})
			continue
		}
		evt, err := eventqueue.DecodeEvent(raw)
		if err != nil {
			// Past the schema (which already rejects a malformed-SHAPE `at`/
			// `expiresAt` via its `pattern`) but still not convertible — a value
			// that is well-formed RFC3339 syntax yet calendar-invalid (e.g. month
			// 13), which a regex cannot catch and only time.Parse can.
			reply.Rejected = append(reply.Rejected, rejection{ID: rawEventID(raw), Reason: "malformed: " + err.Error()})
			continue
		}
		if !s.bindings.Declares(evt.Type) {
			// UNKNOWN TO THE CONFIGURATION (INV-DISP-3's first case): refuse to queue
			// it and name it in the reply. Queueing it instead would inflate the
			// unconsumed-expired count with a misconfiguration and leave the "no event
			// misses" signal unable to mean a genuine miss. This is deliberately NOT
			// the run-scoped question: a declared type whose binding is disabled this
			// run falls through to Enqueue below.
			slog.Error("core: ingest-event rejected an event whose type no configured binding declares",
				"trackingId", req.ID, "eventId", evt.ID, "type", evt.Type)
			s.observer().OnUnknownTypeRejected(evt.Type)
			reply.Rejected = append(reply.Rejected, rejection{
				ID:     evt.ID,
				Reason: fmt.Sprintf("unknown type: no configured binding declares %q", evt.Type),
			})
			continue
		}
		res, err := s.q.Enqueue(evt)
		if err != nil {
			// A core-side durable-write failure, not a malformed event — but the
			// event did NOT enter the queue, and `rejected` is the only per-event
			// outcome channel the reply schema has. The `enqueue:` prefix keeps the
			// two causes distinguishable to the caller.
			slog.Error("core: ingest-event enqueue failed", "trackingId", req.ID, "eventId", evt.ID, "err", err)
			reply.Rejected = append(reply.Rejected, rejection{ID: evt.ID, Reason: "enqueue: " + err.Error()})
			continue
		}
		if res == eventqueue.Deduped {
			slog.Debug("core: ingest-event absorbed a duplicate event id", "trackingId", req.ID, "eventId", evt.ID)
		}
		reply.Accepted++
	}

	body, err := json.Marshal(reply)
	if err != nil { // unreachable: ingestReply holds only strings, an int and a slice of strings
		writeBody(stdout, errorReply("ingest-event: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	if len(reply.Rejected) > 0 {
		return conformance.ExitError
	}
	return conformance.ExitOK
}

// decodeIngest validates the request ENVELOPE and returns the tracking id with
// each event's raw bytes.
//
// It enforces exactly what cli.ingest-event.schema.json says about the envelope —
// the closed field set, the required fields, the `schemaVersion` const (via the
// shared schemas.CheckSchemaVersion, so "report, don't guess" is one
// implementation), `id` a string, `events` a non-empty array — and deliberately
// does NOT apply the schema's `items` clause to the events. That is the whole
// point: applying the full envelope schema would fail the WHOLE batch on one bad
// event, whereas interfaces.md requires a per-event `rejected` list. Each event is
// checked individually by the caller against the same `event` schema the items
// clause $refs, so nothing skips validation.
//
// TestDecodeIngest_EnvelopeMatchesSchema pins this equivalence, so the two cannot
// drift.
func decodeIngest(data []byte) (ingestRequest, error) {
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return ingestRequest{}, fmt.Errorf("%w: malformed JSON: %v", errIngestEnvelope, err)
	}
	obj, ok := generic.(map[string]any)
	if !ok {
		return ingestRequest{}, fmt.Errorf("%w: request is not an object", errIngestEnvelope)
	}
	// schemaVersion: present, a string, and one this core handles — reported, never
	// guessed (INV-INTF-1).
	if err := schemas.CheckSchemaVersion(obj); err != nil {
		return ingestRequest{}, fmt.Errorf("%w: %w", errIngestEnvelope, err)
	}
	var extra []string
	for k := range obj {
		if !ingestEnvelopeFields[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra) // deterministic message
		return ingestRequest{}, fmt.Errorf("%w: additional properties not allowed: %s", errIngestEnvelope, strings.Join(extra, ", "))
	}
	id, ok := obj["id"].(string)
	if !ok {
		if _, present := obj["id"]; !present {
			return ingestRequest{}, fmt.Errorf("%w: missing required field %q", errIngestEnvelope, "id")
		}
		return ingestRequest{}, fmt.Errorf("%w: id must be a string", errIngestEnvelope)
	}
	rawList, present := obj["events"]
	if !present {
		return ingestRequest{}, fmt.Errorf("%w: missing required field %q", errIngestEnvelope, "events")
	}
	list, ok := rawList.([]any)
	if !ok {
		return ingestRequest{}, fmt.Errorf("%w: events must be an array", errIngestEnvelope)
	}
	if len(list) == 0 {
		return ingestRequest{}, fmt.Errorf("%w: events must carry at least one event", errIngestEnvelope)
	}
	// Re-decode only `events`, to keep each event's ORIGINAL bytes for the shared
	// wire decoder (re-marshalling the generic form could reorder or renumber it).
	var typed struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &typed); err != nil {
		return ingestRequest{}, fmt.Errorf("%w: malformed events: %v", errIngestEnvelope, err)
	}
	return ingestRequest{ID: id, Events: typed.Events}, nil
}

// rawEventID best-effort extracts a malformed event's `id` so the rejection can
// name it (the reply schema requires an `id` on every rejection). An event too
// broken to yield one is reported with an empty id rather than dropped silently.
func rawEventID(raw []byte) string {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.ID
}
