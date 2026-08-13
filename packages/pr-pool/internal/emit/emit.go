// Package emit implements the operator command source behind the `push-inject`
// INTF-CLI subcommand (interfaces.md). It is the operator-facing front door to
// the push-ingest path: it parses an operator-supplied event JSON, validates it
// against the push-inject message schema, locates the running core exactly as
// other INTF-CLI operator subcommands do (an injected socket/token, else
// discovery), and performs the SAME core-side enqueue as the ingest-event manager
// callback — durable via the queue, delivered at-least-once and deduped
// (INV-EVT-*).
//
// The subcommand's name is `push-inject`. There is no `pr-pool-emit` subcommand;
// the package name is historical.
//
// # The located core decides the enqueuer
//
// A located core may be THIS process or another one, and the two need different
// transports. That choice belongs to the CoreRef, not to whoever wired the call:
// see Enqueuer, QueueEnqueuer (in-process) and SocketEnqueuer (over the socket),
// each of which refuses a ref it cannot reach.
//
// This is bead pg2-hvlyj.17 (plan item 5.5), extended with the socket transport by
// bead pg2-f3mcb.3. Its statement coverage is gated at >=85% by the
// `pr-pool-go-tests` flake check (bead pg2-hvlyj.19).
package emit

import (
	"errors"
	"fmt"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// pushInjectSchema is the message type the operator input is validated against
// (interfaces.md INTF-CLI push-inject == the event shape).
const pushInjectSchema = "cli.push-inject"

// ErrNoCore is returned when no core can be located (nothing injected and no
// discovery available).
var ErrNoCore = errors.New("emit: no running core located (no injected socket, no discovery)")

// ErrWrongEnqueuer is returned when an Enqueuer is handed a CoreRef it cannot
// possibly reach: QueueEnqueuer given a core in ANOTHER process, or
// SocketEnqueuer given the in-process core. It exists because the failure it
// replaces was SILENT — see QueueEnqueuer.Enqueue.
var ErrWrongEnqueuer = errors.New("emit: this enqueuer cannot reach the located core")

// CoreRef identifies a located running core: its socket path and auth token, and
// whether it was injected (env/arg) or discovered.
//
// Local is what distinguishes "the core is THIS process" from "the core is over
// there". It is an EXPLICIT flag rather than "an empty Socket means local"
// precisely so a zero-value CoreRef — the thing a half-written locate path
// returns — names no core at all and is refused by every Enqueuer, instead of
// defaulting to a local enqueue that goes nowhere the operator can see.
type CoreRef struct {
	Socket     string
	Token      string
	Discovered bool
	// Local marks the IN-PROCESS core: this process owns the durable queue, so
	// there is no socket to cross. It is the ONLY ref QueueEnqueuer accepts.
	Local bool
}

// Locator resolves the running core the same way as the other INTF-CLI operator
// subcommands: an INJECTED socket/token (env or arg) wins; otherwise it falls
// back to discovering the running socket service. When neither locates a core the
// result is ErrNoCore — the CLI NEVER auto-starts one (ADR 0036; the former
// OQ-AUTOSTART, resolved 2026-07-28 as "error, do not spawn").
type Locator struct {
	InjectedSocket string
	InjectedToken  string
	// Discover finds a running socket service when nothing is injected. May be
	// nil (no discovery configured).
	Discover func() (CoreRef, error)
}

// Locate returns the core to enqueue against, preferring an injected socket.
func (l Locator) Locate() (CoreRef, error) {
	if l.InjectedSocket != "" {
		return CoreRef{Socket: l.InjectedSocket, Token: l.InjectedToken, Discovered: false}, nil
	}
	if l.Discover != nil {
		return l.Discover()
	}
	return CoreRef{}, ErrNoCore
}

// LocalLocator locates the IN-PROCESS core: the caller IS the core and owns the
// durable queue, so nothing is injected and nothing is discovered. It is the only
// locator whose result QueueEnqueuer accepts, and it exists so that path has to be
// stated rather than fallen into.
func LocalLocator() Locator {
	return Locator{Discover: func() (CoreRef, error) { return CoreRef{Local: true}, nil }}
}

// Enqueuer performs the core-side enqueue against a located core — the same
// enqueue the ingest-event callback target performs. There are exactly two
// implementations, and WHICH ONE IS CORRECT IS DECIDED BY THE CoreRef, never by
// the caller's preference:
//
//   - QueueEnqueuer — the core is THIS process; append straight to its queue.
//   - SocketEnqueuer — the core is another process; forward over its socket.
//
// Both REFUSE a ref they cannot reach (ErrWrongEnqueuer). An Enqueuer that
// quietly accepted the wrong ref would report success for an event that was never
// delivered, which is the one failure mode this boundary must not have.
type Enqueuer interface {
	Enqueue(core CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error)
}

// QueueEnqueuer enqueues directly into a durable queue — the IN-PROCESS front
// door, usable only by a caller that is itself the core.
type QueueEnqueuer struct{ Q *eventqueue.Queue }

// Enqueue appends evt to the LOCAL durable queue, and REFUSES any CoreRef that
// names a core in another process.
//
// The refusal is the point. This method used to ignore the CoreRef outright, with
// the comment "the queue is local" — so wiring an operator front door through it
// against a DISCOVERED core reported success while appending to a queue that died
// with the CLI process. The event was never delivered and nothing surfaced the
// loss. A located-but-unreachable core is now an error at the boundary that knows
// it cannot reach it, which is the only place it can still be reported honestly.
func (q QueueEnqueuer) Enqueue(core CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error) {
	if !core.Local {
		return eventqueue.Enqueued, fmt.Errorf(
			"%w: QueueEnqueuer only reaches the in-process queue, but the located core is remote (socket %q, discovered=%t); forward over the socket instead (SocketEnqueuer)",
			ErrWrongEnqueuer, core.Socket, core.Discovered,
		)
	}
	return q.Q.Enqueue(evt)
}

// Result reports the outcome of an Emit.
type Result struct {
	Core  CoreRef
	Event eventqueue.Event
	// Status is the enqueue outcome, but HOW MUCH IT CAN RESOLVE depends on which
	// Enqueuer ran — it is not a uniform property of Emit:
	//
	//   - QueueEnqueuer (in-process) passes the durable queue's own EnqueueResult
	//     through, so a re-emit of a still-retained id genuinely reports Deduped
	//     (INV-EVT-3).
	//   - SocketEnqueuer (over the wire) can only ever report Enqueued, and it means
	//     "the core accepted it", NOT "freshly appended". See SocketEnqueuer's
	//     "What it cannot tell you" for why the reply cannot carry the distinction,
	//     and for the wording an operator-facing caller must use.
	//
	// So Deduped is reachable in-process and UNOBSERVABLE over the socket. A caller
	// that does not know which Enqueuer ran MUST NOT read Enqueued as "freshly
	// appended"; only a caller holding a QueueEnqueuer may treat the two values as a
	// real distinction.
	Status eventqueue.EnqueueResult
}

// Emit parses operator-supplied JSON, validates it against the push-inject
// message schema, locates the core, and enqueues the event. A malformed or
// non-schema-valid input is rejected with a clear error before any core is
// located; a valid event enters the durable queue (at-least-once, deduped).
func Emit(jsonArg []byte, loc Locator, enq Enqueuer) (Result, error) {
	// 1. Validate the operator input against the push-inject message schema
	//    (rejects malformed JSON, missing/wrong-typed fields).
	if err := conformance.CheckBytes(pushInjectSchema, jsonArg); err != nil {
		return Result{}, fmt.Errorf("emit: rejected: %w", err)
	}
	// 2. Parse into the core event (the wire instants are RFC3339 strings).
	evt, err := parseEvent(jsonArg)
	if err != nil {
		return Result{}, err
	}
	// 3. Locate the core (injected socket/token, else discovery).
	core, err := loc.Locate()
	if err != nil {
		return Result{}, err
	}
	// 4. Enqueue — the same core-side enqueue as the ingest-event callback.
	status, err := enq.Enqueue(core, evt)
	if err != nil {
		return Result{}, fmt.Errorf("emit: enqueue: %w", err)
	}
	return Result{Core: core, Event: evt, Status: status}, nil
}

// parseEvent decodes the validated JSON into a core event. The wire→core
// conversion (the optional RFC3339 `at` / `expiresAt`) is delegated to the
// SHARED decoder eventqueue.DecodeEvent, which the `ingest-event` manager
// callback (internal/core) uses too — the operator front door and the callback
// front door MUST agree on how a wire event becomes an Event.
func parseEvent(data []byte) (eventqueue.Event, error) {
	evt, err := eventqueue.DecodeEvent(data)
	if err != nil {
		return eventqueue.Event{}, fmt.Errorf("emit: %w", err)
	}
	return evt, nil
}
