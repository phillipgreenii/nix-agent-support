// Package query is pg-router's typed-union of work SOURCES (producers). Each
// concrete query emits []event.Event; Run errors are propagated, never
// returned as "no work" (pg2-qq9v): a source failure must not masquerade as
// an idle pool.
//
// Under the event model (design 2026-06-25) a query is a PRODUCER: Run returns
// typed events (was []item.Item), the query declares the event type(s) it Emits
// (roles bind to these), and a pluggable Trigger strategy decides when it fires.
// Emits/Trigger are carried by an embedded Meta so every concrete type gets them
// uniformly; the type-specific fields still decode from the query sub-table.
//
// The beads-backed built-in query (query.BeadsReady) and its role pairing
// (roles.BuiltinRoleSet/BuiltinQuerySet) moved OUT of this module entirely
// (docket pg2-oju6w's Task 5.8, ADR 0065's "Source-side boundary" section):
// they now live as a registered kind:"source" participant in
// packages/pg-router-ccpool-handler. ParticipantQuery below is this
// package's replacement seam for reaching such a participant — a real
// INTF-SOURCE wire client, mirroring internal/wireclient.Client's
// handler-side Dispatch (Task 5.3/5.4) — rather than an in-process call.
package query

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/item"
)

type QueryFormat string

const (
	FormatJSONL QueryFormat = "jsonl"
	FormatJSON  QueryFormat = "json"
)

// Commander runs an executable and returns its stdout (one-method interface, like
// beads.Runner / ccpool.Runner — not a bare func field).
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Env carries the capabilities a query needs. The orchestrator builds it from its
// own fields in phase 1 (the Deps bag arrives in phase 2).
//
// BD (a beads.Runner, the in-process bd capability) was deleted here (Task
// 5.8): its one consumer, query.BeadsReady, moved out of this module
// entirely — see this file's package doc comment. A query that needs a
// registered participant's own capability reaches it over the wire
// (ParticipantQuery), never through this bag.
type Env struct {
	RepoRoot string
	Cmd      Commander
}

// Query is a producer of typed events. The critical inversion (design M2): Run
// returns []event.Event (was []item.Item), and the query declares the event
// type(s) it Emits plus its firing Trigger. A role and a query are wired ONLY
// through a shared event-type string (the producer/consumer decoupling).
type Query interface {
	Validate() error
	// Emits returns the event type(s) this query produces (for wiring +
	// orphan-emit validation).
	Emits() []string
	// Trigger is the query's firing strategy (Q1) — Strategy pattern. A nil
	// return is treated as PeriodTrigger by the driver.
	Trigger() Trigger
	// FailureBackoff is this query's pull-source failure backoff (INV-FAIL-3,
	// pg2-0c8yz): the retry cadence discover.Produce consults when Run fails,
	// distinct from Trigger's success-path interval. The zero value (Retries: 0)
	// means fail fast — exactly the original pg2-qq9v behavior — so a query that
	// has not opted in is unaffected.
	FailureBackoff() FailureBackoff
	// BackingCommand returns the executable this source needs in order to run,
	// or "" when it needs none (a pure in-process producer). It is a STATIC
	// declaration, resolved pre-runtime by config.Validate's absent-backing-command
	// check, so every concrete query states it explicitly rather than inheriting a
	// silent default — a new query type that shells out and forgets it would
	// otherwise escape that check.
	BackingCommand() string
	// Run produces zero or more events for the bus to publish.
	Run(ctx context.Context, env Env) ([]event.Event, error)
}

// Meta carries the event-model wiring common to every concrete query: the event
// type(s) it emits and its firing trigger. It is embedded (value) so a query
// gets Emits()/Trigger() for free; config sets it from the [[query]]-level
// emits/trigger keys (NOT the type-specific sub-table), and the built-in Go
// query set constructs it inline.
type Meta struct {
	EmitTypes []string
	Trig      Trigger
	// FB is this query's pull-source failure backoff (INV-FAIL-3). The zero
	// value (Retries: 0) reproduces today's fail-fast behavior exactly, so an
	// unconfigured query is unaffected — opting in is [query.failure_backoff] or
	// the pool-level default (config.Registry.buildQuery).
	FB FailureBackoff
}

// Emits returns the configured emit type(s).
func (m Meta) Emits() []string { return m.EmitTypes }

// Trigger returns the configured trigger, defaulting to PeriodTrigger{} (fire on
// every tick) when unset so an unconfigured query reproduces today's behavior.
func (m Meta) Trigger() Trigger {
	if m.Trig == nil {
		return PeriodTrigger{}
	}
	return m.Trig
}

// FailureBackoff returns the configured pull-source failure backoff (INV-FAIL-3).
func (m Meta) FailureBackoff() FailureBackoff { return m.FB }

// setMeta lets the factory/config install the [[query]]-level wiring onto a
// concrete query decoded from its sub-table. A pointer to an embedded Meta
// satisfies this, so *BeadsReady et al. get it via promotion.
func (m *Meta) setMeta(x Meta) { *m = x }

// metaSetter is the seam the factory uses to install Meta post-decode.
type metaSetter interface{ setMeta(Meta) }

// Source is a named producer: a query with the config name it was registered
// under (the [[query]].name). The name is provenance (Event.Source) and the
// handle run-query resolves. Emits/Trigger live on the embedded Query.
type Source struct {
	Name  string
	Query Query
}

// SourceSet is the ordered set of producers a drain fires (config order).
type SourceSet []Source

// FromIssue (the beads.Issue -> item.Item adapter) is DELETED here (Task
// 5.8): its only reason to live in this leaf package was the beads-backed
// query, which moved out entirely (this file's package doc comment), and
// internal/beads itself no longer exists in this module for a signature
// here to reference. cmd/pg-router/runrole.go's own call site
// (query.FromIssue(iss), buildRunRoleEvent) is left referencing a symbol
// that no longer exists — that file already fails to build independently
// (it imports the equally-moved internal/beads and internal/ccpool
// packages directly), so this is not new breakage; reconciling it is
// outside this task's own Files list.

// firstEmit returns the query's primary emit type, or "" if it declares none.
// The built-in single-emit queries wrap every item under this type.
func firstEmit(q Query) string {
	e := q.Emits()
	if len(e) == 0 {
		return ""
	}
	return e[0]
}

// declaresEmit reports whether emit is among q's declared Emits() — the
// per-record `emit` selector validation a command source's rawItem uses for
// multi-emit (Task 1.4): an emit value MUST be one of the query's declared
// types, never an arbitrary string.
func declaresEmit(q Query, emit string) bool {
	for _, e := range q.Emits() {
		if e == emit {
			return true
		}
	}
	return false
}

// IsStub reports whether a query type is a not-yet-implemented stub. No query
// types are stubs currently; the seam is retained so the drain pre-flight can
// keep warning if a future type lands as a decode/validate-only stub.
func IsStub(Query) bool { return false }

// --- ParticipantQuery: the source-side wire client (Task 5.8) ---

// ParticipantQuery is a Query that reaches a REGISTERED SOURCE PARTICIPANT
// over the wire (INTF-SOURCE's `query` message, DEC-WIRE-1's default CLI
// transport) instead of running any work in-process. It mirrors
// internal/wireclient.Client's handler-side Dispatch exactly: a fresh
// tracking id per call, an inline `{events}` reply decoded straight into
// []event.Event, or a `{deferred: true}` reply that owes Run nothing this
// tick — the events land later on the SAME tracking id via the existing
// `ingest-event` callback path (cmd/pg-router/ingest_event.go), unchanged by
// this type.
//
// Which command to invoke, and what ready-to-run `ingest-event` command the
// request's own `callback` field should carry, are deliberately NOT resolved
// here — mirrors wireclient.CommandFor's own doc comment: a deployment/
// wiring concern. That wiring is still unbuilt for the handler side too
// (cmd/pg-router's bootCore never assigns Orchestrator.Handler), so leaving
// it unbuilt here as well is consistent with that precedent rather than a
// gap unique to this type. Command/Callback/Runner are the injected seams a
// caller supplies once real production wiring lands.
//
// Not TOML-configurable (no factory.go entry): the retired query.BeadsReady
// was never TOML-configurable either — it only ever backed the in-Go
// built-in default query set, which is now deleted (Task 5.8 Step 2). A
// caller constructs a ParticipantQuery directly in Go.
type ParticipantQuery struct {
	Meta `toml:"-"`
	// Command is the argv PREFIX to invoke (before "query" is appended) — the
	// registered source participant's own resolved command.
	Command []string
	// Callback is the ready-to-run `ingest-event` command handed to the
	// participant for a deferred reply (interfaces.md's "Callback"). Empty is
	// schema-legal (source.query's `callback` is an unconstrained string) and
	// is what an unwired deployment sends today.
	Callback string
	// Runner executes the invocation; defaults to OSParticipantRunner{}.
	Runner ParticipantRunner
}

func (q ParticipantQuery) Validate() error {
	if len(q.Command) == 0 {
		return fmt.Errorf("participant query: command is required")
	}
	return nil
}

// BackingCommand: the invoked participant's own command is this source's
// backing command (config.Validate's absent-backing-command check, same as
// every other Query implementation).
func (q ParticipantQuery) BackingCommand() string {
	if len(q.Command) == 0 {
		return ""
	}
	return q.Command[0]
}

// ErrParticipantBusy is returned when the invoked participant's query
// subcommand exits busy (code 9 — DEC-WIRE-1's "coarse exit codes"),
// mirroring wireclient.ErrBusy on the handler side.
var ErrParticipantBusy = errors.New("query: source participant busy")

// ParticipantRunner executes one query subcommand invocation: argv (the
// participant's own CommandFor-resolved command, with "query" appended) fed
// stdin, returning its stdout and coarse exit code. Mirrors
// internal/wireclient.Runner exactly; duplicated rather than imported to
// keep this task's diff confined to this package (this package must not
// import internal/wireclient — see its own doc comment on the deployment
// concerns this type deliberately leaves unresolved).
type ParticipantRunner interface {
	Run(ctx context.Context, argv []string, stdin []byte) (stdout []byte, exitCode int, err error)
}

// OSParticipantRunner is the production ParticipantRunner: a real
// subprocess, matching DEC-WIRE-1's default transport.
type OSParticipantRunner struct{}

func (OSParticipantRunner) Run(ctx context.Context, argv []string, stdin []byte) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, 0, errors.New("query: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out.Bytes(), 0, nil
	case errors.As(err, &exitErr):
		// The reply body (if any) is still on stdout even on a non-zero exit
		// (DEC-WIRE-1: "the rich outcome is in the JSON reply"). Return it
		// alongside the exit code; Run below decides what, if anything, it
		// means.
		return out.Bytes(), exitErr.ExitCode(), nil
	default:
		return nil, 0, fmt.Errorf("query: run %v: %w", argv, err)
	}
}

func (q ParticipantQuery) runner() ParticipantRunner {
	if q.Runner != nil {
		return q.Runner
	}
	return OSParticipantRunner{}
}

// sourceQueryRequest / sourceQueryReply / wireEvent mirror
// packages/pg-router/schemas/source.query{,-reply}.schema.json /
// event.schema.json — docs/decisions/wire.md's illustrative shapes, realized
// here as the concrete Go types this client encodes/decodes.
type sourceQueryRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Callback      string `json:"callback"`
}

type sourceQueryReply struct {
	SchemaVersion string      `json:"schemaVersion"`
	ID            string      `json:"id"`
	Deferred      bool        `json:"deferred,omitempty"`
	Events        []wireEvent `json:"events,omitempty"`
	Error         string      `json:"error,omitempty"`
}

type wireEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	At        string         `json:"at,omitempty"`
	ExpiresAt string         `json:"expiresAt,omitempty"`
	Payload   map[string]any `json:"payload"`
}

// querySchemaVersion is the wire envelope version this client stamps on
// every request it builds (DEC-WIRE-1's "schemaVersion") — matches
// wireclient.SchemaVersion's own value.
const querySchemaVersion = "1"

// Run implements Query: it sends INTF-SOURCE's `query` message to the
// registered participant's own invoked command (Command, with "query"
// appended), using a freshly minted qry-<...> tracking id, and returns
// UNMODIFIED events decoded from an inline reply, or nil (no error) for a
// deferred one — the events land later via `ingest-event`, correlated by
// this same tracking id.
func (q ParticipantQuery) Run(ctx context.Context, env Env) ([]event.Event, error) {
	if len(q.Command) == 0 {
		return nil, errors.New("participant query: no command configured")
	}
	id, err := newQueryID()
	if err != nil {
		return nil, fmt.Errorf("participant query: mint tracking id: %w", err)
	}
	req := sourceQueryRequest{SchemaVersion: querySchemaVersion, ID: id, Callback: q.Callback}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("participant query: encode request: %w", err)
	}

	stdout, code, err := q.runner().Run(ctx, append(q.Command, "query"), body)
	if err != nil {
		return nil, err
	}
	switch code {
	case 0:
		if len(stdout) == 0 {
			return nil, fmt.Errorf("participant query: empty reply on exit 0")
		}
		var reply sourceQueryReply
		if err := json.Unmarshal(stdout, &reply); err != nil {
			return nil, fmt.Errorf("participant query: decode reply: %w", err)
		}
		if reply.Error != "" {
			return nil, fmt.Errorf("participant query: %s", reply.Error)
		}
		if reply.Deferred {
			// Owes nothing now (interfaces.md's "Deferred replies" —
			// INTF-SOURCE's query "owes events later"): the events arrive on
			// the ingest-event callback, correlated by this SAME id.
			return nil, nil
		}
		events := make([]event.Event, 0, len(reply.Events))
		for _, we := range reply.Events {
			events = append(events, eventFromWire(we))
		}
		return events, nil
	case 9: // DEC-WIRE-1's "coarse exit codes": 9 is busy.
		return nil, ErrParticipantBusy
	default:
		if len(stdout) > 0 {
			var reply sourceQueryReply
			if err := json.Unmarshal(stdout, &reply); err == nil && reply.Error != "" {
				return nil, fmt.Errorf("participant query exited %d: %s", code, reply.Error)
			}
		}
		return nil, fmt.Errorf("participant query exited %d", code)
	}
}

// eventFromWire decodes one wire event (event.schema.json) into this
// package's own Event, reconstructing Item from the payload's id/type/title/
// metadata keys — the SAME flat shape discover.ToQueueEvent writes (docket
// pg2-oju6w's Task 5.6) and the handler module's own itemFromPayload reads
// back on the dispatch side (Task 5.6's "same shape, different side"
// framing). at/expiresAt ride onto Attributes, mirroring CommandQuery.Run's
// own handling of a command source's optional at/expiresAt fields.
func eventFromWire(we wireEvent) event.Event {
	evt := event.Event{ID: we.ID, Type: we.Type, Item: itemFromWirePayload(we.Payload)}
	attrs := make(map[string]any, 2)
	if we.At != "" {
		if t, err := time.Parse(time.RFC3339, we.At); err == nil {
			attrs["at"] = t
		}
	}
	if we.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, we.ExpiresAt); err == nil {
			attrs["expiresAt"] = t
		}
	}
	if len(attrs) > 0 {
		evt.Attributes = attrs
	}
	return evt
}

// itemFromWirePayload reconstructs an item.Item from a wire event's opaque
// payload object — this package's own local twin of
// packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler's
// itemFromPayload (Go's internal-package visibility rule means neither side
// may import the other's), reading the same flat id/type/title/metadata
// shape. A payload missing an expected field yields a zero value for it
// rather than an error, matching that function's own "absent path is a
// non-match, not an error" posture.
func itemFromWirePayload(payload map[string]any) item.Item {
	var it item.Item
	if v, ok := payload["id"].(string); ok {
		it.ID = v
	}
	if v, ok := payload["type"].(string); ok {
		it.Type = v
	}
	if v, ok := payload["title"].(string); ok {
		it.Title = v
	}
	if v, ok := payload["metadata"].(map[string]any); ok {
		it.Metadata = v
	}
	return it
}

// newQueryID mints a fresh query tracking id: "qry-" + 12 hex chars of
// crypto/rand entropy, the same shape convention
// internal/wireclient.newDispatchID uses for its own "dsp-" ids.
func newQueryID() (string, error) {
	b := make([]byte, 6) // 6 bytes -> 12 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "qry-" + hex.EncodeToString(b), nil
}
