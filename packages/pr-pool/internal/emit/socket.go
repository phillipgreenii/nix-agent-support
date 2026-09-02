package emit

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// trackingPrefix prefixes the `ingest-event` envelope's tracking id so a core log
// line names the front door the event came through. push-inject is
// operator-initiated, so it mints its own tracking id (the core echoes it back and
// never requires one it issued — see core.handleIngestEvent).
const trackingPrefix = "push-inject:"

// Discoverer returns a Locator.Discover that finds the RUNNING core under logDir,
// the same discovery record every other INTF-CLI entry point reads.
//
// It passes core.Discover's error through UNWRAPPED so a caller can still test it
// with errors.Is(err, core.ErrNoRunningCore) and print the one diagnostic the
// operator needs. Discover proves liveness by connecting, so a record left behind
// by a crashed core is reported as "no running core" rather than handed out as a
// ref every later call would fail against. It never starts a core (ADR 0036).
func Discoverer(logDir string) func() (CoreRef, error) {
	return func() (CoreRef, error) {
		ref, err := core.Discover(logDir)
		if err != nil {
			return CoreRef{}, err
		}
		return CoreRef{Socket: ref.Socket, Token: ref.Token, Discovered: true}, nil
	}
}

// SocketEnqueuer performs the core-side enqueue by FORWARDING the event to the
// located core over its unix socket, using the core's own `ingest-event` callback
// target. It is the enqueuer every injected or discovered core needs: that core
// owns the durable queue in ANOTHER process, and the socket is the only way to
// reach it.
//
// # It reports delivery, not dispatch
//
// Enqueue succeeds only when the core's reply says the event entered the queue.
// A transport failure, a protocol refusal, an empty body, a non-zero exit code and
// a populated `rejected` list are all errors — the caller must never be told
// "injected" about an event the core did not take.
//
// # What it cannot tell you
//
// The `cli.ingest-event-reply` schema folds de-duplication into `accepted`,
// because a still-retained duplicate id IS accepted (INV-EVT-3): the reply has no
// field that distinguishes a fresh append from an absorbed re-emit. So Enqueue
// reports eventqueue.Enqueued for "the core accepted it" and can NEVER report
// Deduped over the wire. A caller rendering this outcome to an operator must say
// "accepted", not "enqueued" — claiming a fresh append would be a guess.
type SocketEnqueuer struct{}

// Enqueue forwards evt to the located core and reports whether the core took it.
func (SocketEnqueuer) Enqueue(c CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error) {
	if c.Local {
		return eventqueue.Enqueued, fmt.Errorf(
			"%w: SocketEnqueuer forwards over a socket, but the located core is this process; append to its queue instead (QueueEnqueuer)",
			ErrWrongEnqueuer,
		)
	}
	if c.Socket == "" {
		// A ref that names neither a socket nor the local core locates nothing. Report
		// it as "no running core" so it funnels into the same diagnostic and the same
		// never-auto-start rule as every other locate failure (ADR 0036).
		return eventqueue.Enqueued, fmt.Errorf("%w: located core has no socket to forward to", core.ErrNoRunningCore)
	}
	request, err := ingestEnvelope(evt)
	if err != nil {
		return eventqueue.Enqueued, err
	}
	client, err := core.Dial(core.Ref{Socket: c.Socket, Token: c.Token})
	if err != nil {
		return eventqueue.Enqueued, err
	}
	defer func() { _ = client.Close() }()
	reply, code, err := client.Call(core.SubcommandIngestEvent, request)
	if err != nil {
		return eventqueue.Enqueued, err
	}
	return interpretIngestReply(evt.ID, reply, code)
}

// ingestEnvelope wraps one event in the `cli.ingest-event` request envelope — the
// SAME message a push source's callback sends, which is what makes push-inject the
// operator front door to the same core-side enqueue rather than a second ingest
// path with its own semantics.
//
// The event is re-encoded through the shared eventqueue.EncodeEvent, the exact
// inverse of the decoder the receiving core will run, so the forwarded bytes
// cannot drift from what that core accepts.
func ingestEnvelope(evt eventqueue.Event) ([]byte, error) {
	wire, err := eventqueue.EncodeEvent(evt)
	if err != nil {
		return nil, fmt.Errorf("emit: %w", err)
	}
	req := struct {
		SchemaVersion string            `json:"schemaVersion"`
		ID            string            `json:"id"`
		Events        []json.RawMessage `json:"events"`
	}{
		SchemaVersion: schemas.SchemaVersion,
		ID:            trackingPrefix + evt.ID,
		Events:        []json.RawMessage{wire},
	}
	data, err := json.Marshal(req)
	if err != nil { // unreachable: strings plus already-valid JSON
		return nil, fmt.Errorf("emit: build ingest-event request: %w", err)
	}
	// Validate the envelope we are about to send against its own schema (INV-INTF-2).
	// The core validates it too, but failing here names the fault as OURS instead of
	// coming back as an opaque remote rejection.
	if err := conformance.CheckBytes(core.IngestRequestSchema, data); err != nil {
		return nil, fmt.Errorf("emit: built an invalid %s request: %w", core.IngestRequestSchema, err)
	}
	return data, nil
}

// protocolError is the core's PROTOCOL-level failure envelope (`{schemaVersion,
// error}`) — a transport/lifecycle/auth refusal, deliberately outside the
// per-message reply schemas. A bad token, a core that is not `started` and an
// unknown subcommand all arrive this way.
type protocolError struct {
	Error string `json:"error"`
}

// ingestReplyBody is the `cli.ingest-event-reply` shape this front door reads.
type ingestReplyBody struct {
	ID       string `json:"id"`
	Accepted int    `json:"accepted"`
	Rejected []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"rejected"`
}

// interpretIngestReply turns the core's reply into the enqueue outcome, treating
// every shape that is not "exactly this one event was accepted" as an error.
//
// The ORDER matters: the protocol error envelope is checked before the reply
// schema, because a refusal (bad token, core not started) is not an
// ingest-event-reply at all and reporting it as a schema violation would hide the
// actual cause. The check itself is now against the cli.error SCHEMA ARTIFACT
// (register row bead pg2-o9r6a; Task 3.8 Binding decisions, Step 7) rather than
// an ad hoc "does it have a non-empty `error` field" decode — this was, until
// Task 3.8, the ONE client that discriminated the envelope at all, and
// push-inject is the front door that reuses it (Task 3.8 Files).
func interpretIngestReply(eventID string, reply []byte, code int) (eventqueue.EnqueueResult, error) {
	if code == conformance.ExitBusy {
		// The core's ingest-event never declines: the durable queue does not turn
		// events away, so the common contract's pre-accept busy code should never
		// come back from it. If one ever arrives, it is NOT a delivery — say so
		// rather than shrug.
		return eventqueue.Enqueued, fmt.Errorf("emit: core declined the injection as busy (exit %d); the event was not enqueued", code)
	}
	if len(reply) == 0 {
		return eventqueue.Enqueued, fmt.Errorf("emit: core returned exit %d with no reply body", code)
	}
	if conformance.CheckBytes(core.ErrorReplySchema, reply) == nil {
		var perr protocolError
		if err := json.Unmarshal(reply, &perr); err == nil {
			return eventqueue.Enqueued, fmt.Errorf("emit: core refused the injection: %s", perr.Error)
		}
	}
	if err := conformance.CheckBytes(core.IngestReplySchema, reply); err != nil {
		return eventqueue.Enqueued, fmt.Errorf("emit: core reply is not a valid %s: %w", core.IngestReplySchema, err)
	}
	var body ingestReplyBody
	if err := json.Unmarshal(reply, &body); err != nil { // unreachable: it just passed the schema
		return eventqueue.Enqueued, fmt.Errorf("emit: decode core reply: %w", err)
	}
	if len(body.Rejected) > 0 {
		r := body.Rejected[0]
		return eventqueue.Enqueued, fmt.Errorf("emit: core rejected event %q: %s", r.ID, r.Reason)
	}
	if body.Accepted != 1 {
		return eventqueue.Enqueued, fmt.Errorf("emit: core accepted %d of 1 event and rejected none, so event %q is unaccounted for", body.Accepted, eventID)
	}
	if code != conformance.ExitOK {
		// Accepted with a non-zero code contradicts the callback contract. Refuse to
		// paper over it: the operator would otherwise be told "injected" by a core
		// that signalled a failure.
		return eventqueue.Enqueued, fmt.Errorf("emit: core reported event %q accepted but exited %d", eventID, code)
	}
	return eventqueue.Enqueued, nil
}
