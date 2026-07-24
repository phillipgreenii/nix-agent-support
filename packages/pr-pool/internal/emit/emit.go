// Package emit implements the operator push-inject command source — the
// pr-pool-emit / `push-inject` INTF-CLI subcommand (interfaces.md). It is the
// operator-facing front door to the push-ingest path: it parses an
// operator-supplied event JSON, validates it against the push-inject message
// schema, locates the running core exactly as other INTF-CLI operator
// subcommands do (an injected socket/token, else discovery), and performs the
// SAME core-side enqueue as the ingest-event manager callback — durable via the
// #2 queue, delivered at-least-once and deduped (INV-EVT-*).
//
// This is bead pg2-hvlyj.17 (plan item 5.5). Its statement coverage is gated at
// >=85% by the `pr-pool-go-tests` flake check (bead pg2-hvlyj.19).
package emit

import (
	"encoding/json"
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

// CoreRef identifies a located running core: its socket path and auth token, and
// whether it was injected (env/arg) or discovered.
type CoreRef struct {
	Socket     string
	Token      string
	Discovered bool
}

// Locator resolves the running core the same way as the other INTF-CLI operator
// subcommands: an INJECTED socket/token (env or arg) wins; otherwise it falls
// back to discovering the running socket service. (Whether it auto-starts a core
// when none is found is OQ-AUTOSTART — out of scope here.)
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

// Enqueuer performs the core-side enqueue against a located core — the same
// enqueue the ingest-event callback target performs. The in-process
// implementation (QueueEnqueuer) enqueues directly into the durable queue; a
// deployment MAY forward it over the located socket instead.
type Enqueuer interface {
	Enqueue(core CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error)
}

// QueueEnqueuer enqueues directly into a durable queue (the in-process front
// door). It ignores the CoreRef because the queue is local.
type QueueEnqueuer struct{ Q *eventqueue.Queue }

// Enqueue appends evt to the durable queue.
func (q QueueEnqueuer) Enqueue(_ CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error) {
	return q.Q.Enqueue(evt)
}

// Result reports the outcome of an Emit.
type Result struct {
	Core   CoreRef
	Event  eventqueue.Event
	Status eventqueue.EnqueueResult // Enqueued or Deduped
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
	// 2. Parse into the core event (ttl is a duration string on the wire).
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

// wireEvent is the on-the-wire event shape (ttl as a duration string).
type wireEvent struct {
	SchemaVersion string         `json:"schemaVersion"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	TTL           string         `json:"ttl"`
	Payload       map[string]any `json:"payload"`
}

// parseEvent decodes the validated JSON into a core event, converting the ttl
// string to a duration.
func parseEvent(data []byte) (eventqueue.Event, error) {
	var w wireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return eventqueue.Event{}, fmt.Errorf("emit: parse event: %w", err)
	}
	ttl, err := eventqueue.ParseTTL(w.TTL)
	if err != nil {
		return eventqueue.Event{}, fmt.Errorf("emit: %w", err)
	}
	return eventqueue.Event{
		SchemaVersion: w.SchemaVersion,
		ID:            w.ID,
		Type:          w.Type,
		TTL:           ttl,
		Payload:       w.Payload,
	}, nil
}
