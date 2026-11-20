// Package core is pg-router's socket service: the long-lived process the behavior
// docs call "the core" (INV-LIFE-1, interfaces.md). It owns
//
//   - the LISTENER — a unix-domain socket under Config.LogDir, its auth token, and
//     the discovery record a CLI reads to find it (socket.go);
//   - the SERVICE state — the durable event queue plus the participant registry
//     (this file, registry.go);
//   - the CALLBACK targets — the INTF-CLI subcommands the core hands out with the
//     socket and token already baked in. Two exist today: `ingest-event`
//     (ingest.go, a SOURCE's event-delivery callback) and `self-status`
//     (selfstatus.go, every participant's own health-report callback).
//
// # Transport
//
// Service implements conformance.Participant, the SAME (subcommand, stdin,
// stdout) → exit-code boundary the CLI transport and the conformance suite use.
// The socket is only a carrier for that boundary: Accept reads a transport frame,
// hands the payload to Serve, and returns what Serve wrote. interfaces.md allows
// exactly this ("a participant MAY instead speak a gRPC or in-code transport
// contract over the socket and still conform, so long as it carries the same
// message schema"), and it means every message is schema-checked in ONE place, no
// matter which transport delivered it.
//
// # Two decisions recorded in code here
//
// AUTO-START (the former OQ-AUTOSTART, resolved 2026-07-28, ADR 0036): a callback
// or operator command that finds no running core FAILS with ErrNoRunningCore. The
// CLI never spawns a core. Rationale is on ErrNoRunningCore in socket.go.
//
// `session-status` IS DROPPED (2026-07-28): the core exposes no per-session status
// callback, because nothing in pg-router consumes a post-accept outcome. Acceptance
// arrives in the dispatch REPLY — an inline outcome, or `{"deferred": true}`
// (interfaces.md) — not on a callback. See Serve's default branch. This is
// distinct from SELF-status, which survives and — as of bead pg2-zaghi — has its
// own wire mechanism: see SelfStatus in registry.go and SubcommandSelfStatus in
// selfstatus.go.
//
// # Booted in production by cmd/pg-router's run / run-until-idle (pg2-f3mcb.2)
//
// cmd/pg-router's `run` (long-running daemon) and `run-until-idle` (discover once,
// drain to idle, exit) subcommands call Listen + Accept, giving this Service a
// live socket for ingest-event and self-status outside a test binary — the
// multi-bead convergence (epic pg2-f3mcb) onto "the queue is the universal
// intermediary" (pg2-f3mcb.2, ADR 0056) that also retired internal/eventbus and
// internal/orchestrator's discover-then-dispatch loop over it. See
// cmd/pg-router/run.go (bootCore) for the wiring: a queue->executor Listener per
// enabled role (internal/orchestrator.NewListener) is registered on the SAME
// *eventqueue.Queue this Service routes through.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/kvstore"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
	"github.com/phillipgreenii/pg-router/schemas"
)

// Service is a running core: the socket listener plus the state behind it.
var _ conformance.Participant = (*Service)(nil)

// DefaultCommand is the command name used when assembling the callback strings
// the core hands to participants.
const DefaultCommand = "pg-router"

// Options configures Listen.
type Options struct {
	// LogDir is where the socket and discovery record live — pg-router's existing
	// runtime state directory (Config.LogDir / PG_ROUTER_LOG_DIR). Required.
	LogDir string
	// Queue is the durable event queue the core routes through. Required: the
	// queue IS the core's delivery guarantee (INV-EVT-1), so a core without one
	// could accept an event it cannot keep.
	Queue *eventqueue.Queue
	// Bindings is the set of event types the CONFIGURATION declares a binding for
	// (every binding, including the ones disabled for this run). Required for the
	// same reason Queue is: it is the core's only way to tell an event type UNKNOWN
	// to the configuration — which INV-DISP-3 requires it to reject — from one
	// merely inactive this run, which it must accept. Without it the core would
	// have to treat every event as one or the other, and either answer breaks the
	// invariant.
	Bindings Bindings
	// Observer receives the ingest-time conditions the core records to metrics
	// (INV-OBS-1 / INTF-MON). Optional: nil means the conditions are logged only.
	Observer IngestObserver
	// MetricsReader is the value-read-back handle Task 3.6's `mon.read`
	// composes its replies from (INTF-MON pull, Task 3.6-prereq). Optional:
	// nil means no read-back capability is wired — e.g. when the deployment
	// configured its own external MeterProvider (Config.MeterProvider),
	// which owns its own reader set fixed at its own construction and
	// cannot have a second reader retrofitted here (see
	// internal/metrics.NewReadableProvider's doc). cmd/pg-router's bootCore is
	// the production wiring site.
	MetricsReader MetricsReader
	// MonitorSubsets resolves a kind=monitor registration id to the metric
	// catalog subset (by INTF-MON name) it may read, looked up BEFORE the
	// caller ever calls register (Task 3.6 Binding decisions: "resolved...
	// looked up from config by registration id, not carried on the mon.read
	// request itself"). Optional: nil resolves every id to an empty subset —
	// no production caller sets this yet.
	MonitorSubsets MonitorSubsetResolver
	// ActivityRing is the dispatch-outcome ring buffer (Task 3.4,
	// internal/activity) the `status` verb reads LIVE and directly (Task
	// 3.8) — the ring package's own doc: "meant to be read live and
	// directly by the status verb's handler, not embedded in any periodic
	// snapshot". Optional: nil means the `activity` field of a status reply
	// is always empty, the same nil-means-absent idiom MetricsReader uses.
	// cmd/pg-router's bootCore is the production wiring site.
	ActivityRing *activity.Ring
	// Levels is the level projection the level verbs and the status reply read
	// and write. Optional: nil means the level verbs reply with an error and the
	// status reply carries an empty levels array.
	Levels *LevelProjection
	// LevelRefresher runs a sensor's pull query once for level-refresh.
	// Optional: nil means level-refresh replies with an error.
	LevelRefresher LevelRefresher
	// SourceInFlight reports which configured pull sources currently have a
	// fetch in progress (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2's per-source
	// "processing now" signal) — read live by composeStatusReply, never
	// cached. Optional: nil means every source's `inFlight` reports false,
	// the same nil-means-absent idiom MetricsReader/ActivityRing use.
	// cmd/pg-router's bootCore is the production wiring site (its
	// activityObserver implements this interface too).
	SourceInFlight SourceInFlightReader
	// ConfigPath is the resolved config file path (internal/config.Config.
	// ConfigPath) the `status` verb echoes back under its `core.configPath`
	// field (Task 3.8) — informational only; the core itself never reads
	// the file at this path. Optional: "" means the field is empty.
	ConfigPath string
	// Command is the program name baked into the callback strings handed to
	// participants (default DefaultCommand). Injectable so a test — or a
	// deployment that installs the binary under another name — hands out a command
	// that actually resolves.
	Command string
	// Now is the clock seam for the registry and the discovery record.
	Now func() time.Time

	// StoreCommand resolves the argv prefix to invoke a registered
	// KindStorage participant's own get/put/delete verbs (Task 6.7, Binding
	// decision 4) — the storage-side counterpart to
	// internal/wireclient.CommandFor (INTF-HANDLER's own resolver),
	// injected the same way: a deployment/wiring concern, never resolved by
	// this package on its own initiative. Optional, and left unset by every
	// caller today: no production wiring can supply one yet
	// (internal/core/storeclient.go's package doc records the BLOCKING GAP
	// this seam exists ahead of — internal/roles/roles.go and
	// internal/config/config.go both document that a role no longer
	// declares a backing command at all). nil means Service.Register's
	// KindStorage branch still swaps Service.store to the wire-forwarding
	// implementation (Binding decision 4's "resolved once... never
	// re-resolved per call"), but calling it reports the missing resolver
	// plainly rather than silently succeeding or panicking — see
	// storeclient.go's wireStore.call.
	StoreCommand StoreCommandFor

	// DeclaredRoles is the FULL, pre-selector configured role list
	// (roles.Role{Name, Binds, Enabled} — Enabled here is the CONFIG-level
	// flag, never a run-scoped selector's flip) — Task 4.1's listeners[]
	// renders one row per entry, over the "full configured participant
	// set" interfaces.md's widening requires (never the already-filtered
	// active set — that is exactly what makes a selector-excluded
	// participant observable at all). cmd/pg-router's bootCore captures this
	// BEFORE applySelectors runs, since applySelectors flips Role.Enabled
	// for a selector-excluded role too (Binding Decision 4) — the
	// post-selector value can no longer distinguish "config-disabled" from
	// "selector-excluded". Optional: nil renders an empty listeners[].
	DeclaredRoles []roles.Role
	// ExcludedRoles/ExcludedSources are the role/source names
	// applySelectors excluded THIS RUN (Binding Decision 4, Task 4.1) —
	// captured at applySelectors' own call site, before it mutates
	// Role.Enabled or removes a query.Source from the active set, so
	// listeners[]/sources[] can report `excluded` independently of
	// `enabled`. Optional: nil means nothing was selector-excluded this
	// run.
	ExcludedRoles   []string
	ExcludedSources []string
	// SourceIntervalsMs is source name -> expected tick cadence in
	// milliseconds (this task): resolved once by the caller
	// (cmd/pg-router's bootCore, via config.ExpectedIntervalMsFor) from the
	// FULL configured query set, so statusSources can report an
	// interval-aware staleness state instead of the pool-wide tick. A
	// missing or zero entry means "unknown" — statusSources renders that
	// source's ExpectedIntervalMs as 0, and the TUI renders N/A.
	SourceIntervalsMs map[string]int64
	// SourceDescriptions is source name -> operator-authored free-text
	// description (pg2-ec754): resolved once by the caller (cmd/pg-router's
	// bootCore) from the FULL configured query set's own query.Source.
	// Description, mirroring SourceIntervalsMs's own resolve-once-at-boot
	// pattern. A missing entry (or this map being nil) means "unset" —
	// statusSources renders that source's description as "" (always
	// present, never omitted).
	SourceDescriptions map[string]string
	// ListenerCounts is delivered/declined per role name (Task 4.1 Step 5):
	// bumped by the deployment's own eventqueue.Observer wiring, at the
	// SAME (event, handler) acceptance/decline sites INV-EVT-1 already
	// records (cmd/pg-router's fanOutObserver, bootCore) — never a second,
	// independently-tracked count. Zero-lock-cost (perf-F4's pattern,
	// mirroring eventqueue.Queue's own depthCell atomic.Pointer): the SAME
	// *ListenerCounts value is shared between the observer that writes it
	// and this Service, which only ever reads it, so composeStatusReply
	// needs no lock of its own. Optional: nil (or a missing role-name key)
	// reads as delivered=0/declined=0.
	ListenerCounts map[string]*ListenerCounts
	// RoleProgress reads the per-role progress state the three role-progress
	// gauges read (metrics.Emitter.RoleProgress), so the `status` reply's
	// per-listener `progress` object and the gauges can never disagree. Called
	// once per status read. Optional: nil, or a nil result, reports every
	// listener's progress as null.
	RoleProgress func() map[string]metrics.RoleProgress
}

// ListenerCounts is one role's delivered/declined tally (Task 4.1 Step 5),
// atomic so a writer (the deployment's eventqueue.Observer wiring) and a
// reader (composeStatusReply, on a different goroutine) never race without
// either taking a lock. The zero value is a valid, freshly-zeroed counter.
type ListenerCounts struct {
	Delivered atomic.Int64
	Declined  atomic.Int64
	// LastDeliveredAtNanos is UnixNano of the most recent successful
	// delivery (this task) -- 0 means never delivered. Set by
	// listenerCountObserver.OnAccept, the same call site Delivered already
	// increments at.
	LastDeliveredAtNanos atomic.Int64
	// HandlerFailures counts genuine business-logic rejections
	// (HandlerFailureObserver) -- NOT a decline, since the item was
	// accepted. Bumped by cmd/pg-router's handlerFailureCountObserver.
	HandlerFailures atomic.Int64
	// DeclinedByReason breaks Declined down by the SAME reason string
	// eventqueue.Observer.OnDeclined already carries (bead pg2-j4uwg widens
	// this from "wired through, discarded" to "actually recorded"):
	// DeclineReason's own coarse text ("busy"/"unavailable"/"none") by
	// default, or a Listener-supplied eventqueue.OfferResult.DeclineDetail
	// when one was given (e.g. pg-router-ccpool-handler's "at-capacity" /
	// "capacity-unknown"). A sync.Map, not a plain map+mutex: the read side
	// (statusListeners, via DeclinedByReasonSnapshot) must never block on a
	// concurrent writer — the SAME zero-lock-cost intent Delivered/Declined
	// already state, one level deeper than a single atomic.Int64 reaches.
	// Values are always *atomic.Int64, inserted at most once per distinct
	// reason string via LoadOrStore.
	DeclinedByReason sync.Map
}

// BumpDeclinedReason increments c's DeclinedByReason tally for reason by 1,
// creating that reason's own counter on first use (LoadOrStore, safe for
// concurrent callers racing the SAME new reason). Exported so the
// deployment's own eventqueue.Observer wiring (cmd/pg-router/run.go's
// listenerCountObserver) can call it alongside c.Declined.Add(1) — the SAME
// call site, never a second, independently-tracked count.
func (c *ListenerCounts) BumpDeclinedReason(reason string) {
	v, _ := c.DeclinedByReason.LoadOrStore(reason, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

// DeclinedByReasonSnapshot reads back a plain map[string]int64 from c's
// live sync.Map — for statusListeners to render into the wire reply. It is
// a snapshot COPY, never the live map, matching this being a one-shot
// render rather than a handle a caller could keep mutating underneath.
func (c *ListenerCounts) DeclinedByReasonSnapshot() map[string]int64 {
	out := map[string]int64{}
	c.DeclinedByReason.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}

// Service holds the core's live state.
type Service struct {
	mu       sync.Mutex
	state    conformance.Lifecycle
	closing  bool
	q        *eventqueue.Queue
	bindings Bindings
	obs      IngestObserver
	reg      *Registry
	ln       net.Listener
	ref      Ref
	logDir   string
	command  string
	inflight sync.WaitGroup
	// callDeadline overrides serverCallDeadline for this Service when non-zero
	// (set only before Accept starts, by tests that must not wait out 5 s).
	callDeadline time.Duration

	// metricsReader and monitorSubsets are the two Task 3.6-prereq seams:
	// the value-read-back handle and the config-resolved
	// registration-id->subset lookup mon.read's Serve handler (Task 3.6)
	// composes its replies from. Both optional; see Options' docs.
	metricsReader  MetricsReader
	monitorSubsets MonitorSubsetResolver

	// activityRing is the Task 3.4 dispatch-outcome ring the `status` verb
	// (Task 3.8) reads live via handleStatus; nil when Options.ActivityRing
	// was not set. configPath and startedAt are two more Task 3.8 status
	// fields with no other reader today: the resolved config path handed in
	// at Listen, and this Service's own construction time (its own `now`
	// seam, not time.Now, so a test can control it the same way NewRegistry
	// already does).
	activityRing *activity.Ring
	// sourceInFlight is Options.SourceInFlight (DEC-OBS-2, bead pg2-ugcrb);
	// nil when unset, in which case statusSources reports every source's
	// `inFlight` as false — see composeStatusReply's own call site.
	sourceInFlight SourceInFlightReader
	configPath     string
	startedAt      time.Time

	// declaredRoles, excludedRoles, excludedSources and listenerCounts are
	// Task 4.1's listeners[]/sources[] widening seams — see Options'
	// matching fields for what each carries and why. All four are set once
	// at Listen and never mutated afterward, so composeStatusReply reads
	// them with no lock of Service's own mu.
	declaredRoles      []roles.Role
	excludedRoles      []string
	excludedSources    []string
	sourceIntervalsMs  map[string]int64
	sourceDescriptions map[string]string
	levels             *LevelProjection
	levelRefresher     LevelRefresher
	listenerCounts     map[string]*ListenerCounts
	roleProgress       func() map[string]metrics.RoleProgress

	// tick is the published-state cell Serve's handlers (this package) read with
	// no cross-package import (Task 3.5 Objective): it is written by PublishTick
	// (status.go).
	//
	// tick's single-writer discipline survives Task 6.2's concurrent
	// phase-2 fan-out in internal/eventqueue unchanged (Task 6.4, bead
	// pg2-3brwx.4 — a Confirm step, no code change needed here): each
	// phase-2 goroutine writes only into its own pass-local
	// pending[i].result, and phase 3 — still fully serialized under
	// eventqueue's own q.mu — writes only into q.custody/q.inFlight, the
	// per-listener/pool-wide delivery counters, the durable store, and the
	// queued observer signals. None of that ever reaches this cell.
	// cmd/pg-router/run.go's drive loop remains the ONLY caller of
	// tick.Store (via PublishTick), exactly as before Task 6.2.
	tick atomic.Pointer[TickSnapshot]

	// readSem is the verb-classed admission semaphore (Task 3.10 Step 4): a
	// non-blocking counting semaphore over exactly the {status, mon.read, get}
	// read verbs (get joined the allowlist at Task 6.7), gated in handleConn
	// AFTER frame decode (the verb lives inside the frame, so it can never sit
	// around Accept). Every other verb — write/lifecycle verbs, unknown verbs,
	// and any pre-Serve outcome (malformed frame, bad token, liveness probe) —
	// never touches this semaphore.
	readSem chan struct{}

	// storeMu guards store — a small, DEDICATED mutex (never mu above,
	// mirroring gates' own "its own small mutex" precedent): store is
	// written at most once per registered KindStorage participant, from
	// Service.Register's KindStorage branch (Binding decision 4: "resolved
	// ONCE at Listen/register time... never re-resolved per call"), and read
	// on every get/put/delete socket call via getStore().
	storeMu sync.RWMutex
	// store is Service's kvstore.Store field (Task 6.7, Binding decision 4):
	// kvstore.NewInMemory() from Listen until a KindStorage participant
	// registers, the wire-forwarding implementation (storeclient.go)
	// thereafter. No participant ever deregisters today, so there is no
	// reverse-swap path (a documented known limitation if deregistration is
	// ever added).
	store kvstore.Store
	// storeCommand / storeRunner are the wire-forwarding store's two
	// construction seams (Options.StoreCommand and — test-only, no Options
	// field, since no real deployment has any legitimate reason to replace
	// the transport itself — a stub wireclient.Runner). Both stay nil in
	// production; storeclient.go's newWireStore defaults a nil runner to
	// wireclient.OSRunner{} the same way wireclient.Client does.
	storeCommand StoreCommandFor
	storeRunner  wireclient.Runner
}

// readSemCapacity bounds how many concurrent status/mon.read calls the core
// admits at once. The design fixes the BEHAVIOR (an immediate exit-9 decline
// when saturated, never block-then-succeed) and the allowlist membership, not
// this number or the synchronization primitive (Task 3.10 Binding decisions,
// "Freedom boundary") — 8 is a generous ceiling for a read-only inspection verb:
// large enough that a legitimate burst (an operator's `status`, Task 4.0's
// future poller, a monitoring sink's `mon.read`) never collides in practice,
// small enough that a runaway caller cannot pin unbounded goroutines open.
const readSemCapacity = 8

// readRefusalMessage is the human-readable text the admission semaphore's
// refusal carries in the cli.error envelope (Task 3.10 Binding decisions,
// Step 5) — the manual-CLI rendering an operator sees is never a bare exit
// code with no message.
const readRefusalMessage = "too many concurrent status/mon.read calls in flight; retry"

// PollerBackoffCap documents (Task 3.10 Step 6) the poller-side contract a
// caller of the {status, mon.read} verbs MUST honor on seeing exit 9 from the
// admission semaphore above: a 9 advances that caller's own backoff ladder,
// capped at this duration, but SUPPRESSES the poll-error zone — it is routine
// admission control, never a fault, so staleness surfaces only through the
// reply's own asOf age (exactly as the boot window, before the first tick,
// already does today). The poller ITSELF — its ladder shape, its own
// single-in-flight enforcement — is Task 4.0's TUI, out of this task's Files;
// this constant exists only so the contract this task's admission control
// creates has one citable, testable home instead of living solely in a design
// doc.
const PollerBackoffCap = 5 * time.Second

// isReadVerb reports whether subcommand is one of the admission-controlled
// read verbs — exactly {status, mon.read, get} (get joined the allowlist at
// Task 6.7, Binding decision 3: it is INTF-STORE's own read operation, so it
// is admitted and refusable with exit 9 when saturated exactly like status/
// mon.read), never register (Task 3.10 Binding decisions: register is a
// same-process Go method, unreachable via handleConn's switch, so it is
// vacuous to name here) and never a write/lifecycle verb — put/delete stay
// OUT of this allowlist, admitted unconditionally, same as ingest-event.
func isReadVerb(subcommand string) bool {
	switch subcommand {
	case SubcommandStatus, SubcommandMonRead, SubcommandGet:
		return true
	default:
		return false
	}
}

// acquireReadSlot tries to admit one more concurrent read-verb call,
// returning false immediately (never blocking) when readSemCapacity calls are
// already in flight — the (N+1)th caller is refused with exit 9 AT ONCE, not
// after waiting for a slot to free, which is the entire point of admission
// control (Task 3.10 Binding decisions, Step 2): a poller's backoff ladder
// depends on a prompt refusal signal, not a delayed success.
func (s *Service) acquireReadSlot() bool {
	select {
	case s.readSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseReadSlot returns one admitted slot.
func (s *Service) releaseReadSlot() {
	<-s.readSem
}

// IngestObserver receives the ingest-time conditions the core records to METRICS,
// alongside the log line each one already writes (INV-DISP-3 requires both). It is
// the seam that keeps the metric emitter out of the core: the core states WHAT
// happened and the deployment's sink decides how it is counted, so no concrete
// monitoring backend is visible here (INV-OBS-1 / INTF-MON).
type IngestObserver interface {
	// OnUnknownTypeRejected fires once per event rejected because no configured
	// binding declares its `type` — INV-DISP-3's first case, which is an error to
	// report and never a silent drop.
	OnUnknownTypeRejected(eventType string)
	// OnDeduped fires once per event id ingest-event absorbed as a duplicate
	// still retained in the queue (INV-EVT-3, register gap bead pg2-cz31d) —
	// the metrics half of the Debug log line handleIngestEvent already writes
	// at that same res == eventqueue.Deduped branch.
	OnDeduped(eventType string)
}

// MetricsReader is a value-read-back handle over the metric catalog's
// current counter values (INTF-MON pull; Task 3.6-prereq) — the read-side
// counterpart to IngestObserver's write side, kept as a narrow interface for
// the same reason: this package states only OTel's own neutral snapshot
// shape (metricdata.ResourceMetrics — see internal/metrics's package doc,
// "a neutral standard, not a mandated backend"), never a concrete
// monitoring backend. internal/metrics.Reader is the production
// implementation (see its NewReadableProvider).
type MetricsReader interface {
	// Snapshot returns the catalog's current values. Task 3.6's mon.read
	// handler filters the result to the caller's registered subset; this
	// method itself returns everything the underlying MeterProvider has
	// collected.
	Snapshot(ctx context.Context) (metricdata.ResourceMetrics, error)
}

// MonitorSubsetResolver resolves a kind=monitor registration id to the
// metric catalog subset (by INTF-MON name) it may read via mon.read — see
// Options.MonitorSubsets.
type MonitorSubsetResolver func(id string) []string

// SourceInFlightReader reports which configured pull sources currently have
// a fetch in progress (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2's per-source
// "processing now" signal) — see Options.SourceInFlight. Read live at
// composeStatusReply time, the same posture eventqueue.Queue.InFlightListeners
// already takes for the listener side.
type SourceInFlightReader interface {
	// InFlightSources returns the names of every source currently between
	// an OnSourceFetchStart and its matching OnSourceFetchEnd.
	InFlightSources() []string
}

// noopObserver is what a Service without an Observer uses, so the ingest path
// never branches on nil.
type noopObserver struct{}

func (noopObserver) OnUnknownTypeRejected(string) {}
func (noopObserver) OnDeduped(string)             {}

// observer returns the Service's IngestObserver, defaulting to the no-op.
func (s *Service) observer() IngestObserver {
	if s.obs == nil {
		return noopObserver{}
	}
	return s.obs
}

// Listen binds the core's socket under opts.LogDir, mints its auth token, and
// publishes the discovery record — everything a CLI needs to find the core. The
// returned Service is in `starting`: the socket exists and connections queue in
// the listen backlog, but nothing is served until Accept runs (INV-INTF-1:
// messages cross only in `started`).
//
// A live core already answering on the socket is ErrAlreadyRunning; a stale socket
// file left by a crashed core is removed and re-bound.
func Listen(ctx context.Context, opts Options) (*Service, error) {
	if opts.LogDir == "" {
		return nil, errors.New("core: Listen requires a LogDir")
	}
	if opts.Queue == nil {
		return nil, errors.New("core: Listen requires a Queue (the core's delivery guarantee)")
	}
	if opts.Bindings == nil {
		return nil, errors.New("core: Listen requires Bindings (the configured binding set); without it the core cannot tell an event type unknown to the configuration from one merely inactive this run (INV-DISP-3)")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	command := opts.Command
	if command == "" {
		command = DefaultCommand
	}
	if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("core: create log dir: %w", err)
	}
	sock := SocketPath(opts.LogDir)
	if len(sock) > maxSocketPathLen {
		return nil, fmt.Errorf("core: socket path %s is %d bytes, over the platform limit of %d; set PG_ROUTER_LOG_DIR to a shorter directory",
			sock, len(sock), maxSocketPathLen)
	}
	if err := clearStaleSocket(ctx, sock); err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("core: listen on %s: %w", sock, err)
	}
	token, err := newToken()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	ref := Ref{Socket: sock, Token: token}
	rec := record{
		SchemaVersion: "1",
		Socket:        sock,
		Token:         token,
		PID:           os.Getpid(),
		StartedAt:     now(),
	}
	if err := writeRecord(RecordPath(opts.LogDir), rec); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &Service{
		state:              conformance.Starting,
		q:                  opts.Queue,
		bindings:           opts.Bindings,
		obs:                opts.Observer,
		reg:                NewRegistry(now),
		ln:                 ln,
		ref:                ref,
		logDir:             opts.LogDir,
		command:            command,
		metricsReader:      opts.MetricsReader,
		monitorSubsets:     opts.MonitorSubsets,
		activityRing:       opts.ActivityRing,
		sourceInFlight:     opts.SourceInFlight,
		configPath:         opts.ConfigPath,
		startedAt:          now(),
		readSem:            make(chan struct{}, readSemCapacity),
		declaredRoles:      opts.DeclaredRoles,
		excludedRoles:      opts.ExcludedRoles,
		excludedSources:    opts.ExcludedSources,
		sourceIntervalsMs:  opts.SourceIntervalsMs,
		sourceDescriptions: opts.SourceDescriptions,
		levels:             opts.Levels,
		levelRefresher:     opts.LevelRefresher,
		listenerCounts:     opts.ListenerCounts,
		roleProgress:       opts.RoleProgress,
		store:              kvstore.NewInMemory(),
		storeCommand:       opts.StoreCommand,
	}, nil
}

// clearStaleSocket removes a leftover socket file so Listen can re-bind, but only
// after proving nothing answers on it — unlinking a LIVE core's socket would
// leave two cores fighting over one discovery record.
func clearStaleSocket(ctx context.Context, sock string) error {
	if _, err := os.Stat(sock); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("core: stat socket %s: %w", sock, err)
	}
	if err := probe(ctx, sock, DefaultProbeTimeout); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyRunning, sock)
	}
	if err := os.Remove(sock); err != nil {
		return fmt.Errorf("core: remove stale socket %s: %w", sock, err)
	}
	slog.Info("core: removed stale socket left by a previous core", "socket", sock)
	return nil
}

// Ref returns the socket + token a CLI needs to reach this core.
func (s *Service) Ref() Ref { return s.ref }

// Queue returns the durable event queue the core routes through.
func (s *Service) Queue() *eventqueue.Queue { return s.q }

// Registry returns the participant registry.
func (s *Service) Registry() *Registry { return s.reg }

// MetricsReader returns the Service's value-read-back handle (Task
// 3.6-prereq), or nil when none is wired — see Options.MetricsReader.
func (s *Service) MetricsReader() MetricsReader { return s.metricsReader }

// monitorSubsetResolver returns the Service's MonitorSubsetResolver,
// defaulting to one that resolves every id to an empty subset, so Register
// never branches on nil (the same idiom observer() already uses for obs).
func (s *Service) monitorSubsetResolver() MonitorSubsetResolver {
	if s.monitorSubsets == nil {
		return func(string) []string { return nil }
	}
	return s.monitorSubsets
}

// State returns the core's own lifecycle state.
func (s *Service) State() conformance.Lifecycle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// CallbackCommand assembles the single callback command string for one
// subcommand, with the socket and token already baked in — what interfaces.md
// requires the core to hand out so "the participant appends its own arguments and
// runs it; it never assembles the socket or token itself".
func (s *Service) CallbackCommand(subcommand string) string {
	return fmt.Sprintf("%s %s --socket %s --token %s",
		s.command, subcommand, shellQuote(s.ref.Socket), shellQuote(s.ref.Token))
}

// Register adds a participant to the registry and hands it the callback
// commands for its kind (interfaces.md: registering is what "makes its callback
// reachable") — its kind-specific callback (ingestCallbackFor), and the
// self-status callback every kind gets (interfaces.md "Self-status": "Any
// participant MAY push its own status").
//
// For kind == KindMonitor, it also resolves and records the caller's metric
// catalog subset from the configured MonitorSubsetResolver (Task 3.6 Binding
// decisions: "resolved BEFORE it ever calls register... looked up from
// config by registration id, not carried on the mon.read request itself") —
// a plain follow-up field update via Registry.SetSubset, the same shape
// SetLifecycle/SetSelfStatus already use, rather than a Register argument
// every OTHER kind would have to pass as empty.
func (s *Service) Register(id string, kind Kind) (Registration, error) {
	reg, err := s.reg.Register(id, kind, s.ingestCallbackFor(kind), s.CallbackCommand(SubcommandSelfStatus))
	if err != nil {
		return Registration{}, err
	}
	if kind == KindMonitor {
		if err := s.reg.SetSubset(id, s.monitorSubsetResolver()(id)); err != nil {
			return Registration{}, err
		}
		reg, _ = s.reg.Get(id) // re-fetch: SetSubset just updated it
	}
	if kind == KindStorage {
		// Resolved ONCE here, never re-resolved per call (Binding decision 4):
		// in-memory (Listen's default) until the FIRST KindStorage participant
		// registers, wire-forwarding from then on. s.storeCommand/s.storeRunner
		// stay nil in every real deployment today (see Options.StoreCommand's
		// doc and storeclient.go's package doc for the BLOCKING GAP this seam
		// exists ahead of) — the swap still happens, but a nil storeCommand
		// makes the resulting store report that plainly on first use rather
		// than silently keeping the in-memory fallback.
		s.setStore(newWireStore(id, s.storeCommand, s.storeRunner))
	}
	return reg, nil
}

// getStore returns Service's current kvstore.Store — the in-memory default,
// or the wire-forwarding implementation once a KindStorage participant has
// registered (setStore, called only from Register's KindStorage branch).
func (s *Service) getStore() kvstore.Store {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	return s.store
}

// setStore swaps Service's kvstore.Store field.
func (s *Service) setStore(store kvstore.Store) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	s.store = store
}

// ingestCallbackFor returns the ONE event-delivery callback command a
// participant of this kind gets — distinct from the self-status callback every
// kind gets (Register above).
//
// Only a SOURCE has this callback target: `ingest-event`. A HANDLER has none —
// `session-status` was dropped (see the package doc), and a handler's acceptance
// already arrives in its dispatch reply (an inline outcome, or
// `{"deferred": true}`), so there is nothing left for a handler to call back
// about for THIS purpose. A monitoring sink pulls or is pushed to over INTF-MON,
// and storage is core-initiated; neither calls back for event delivery either.
func (s *Service) ingestCallbackFor(kind Kind) string {
	if kind == KindSource {
		return s.CallbackCommand(SubcommandIngestEvent)
	}
	return ""
}

// Accept serves the socket until ctx is cancelled or Close is called: it enters
// `started` (messages may now cross, INV-INTF-1), then handles each connection in
// its own goroutine. It returns nil on an orderly shutdown, having waited for
// in-flight requests to finish, and the accept error otherwise.
func (s *Service) Accept(ctx context.Context) error {
	s.setState(conformance.Started)
	stopped := make(chan struct{})
	defer close(stopped)
	// In-flight requests are drained, not aborted, when ctx is cancelled to stop
	// accepting, so they run on a context that survives that cancellation.
	reqCtx := context.WithoutCancel(ctx)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close() // cancelling ctx is an orderly shutdown request
		case <-stopped:
		}
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if s.isClosing() {
				s.inflight.Wait() // drain: `stopping` completes only once nothing is in flight
				s.setState(conformance.Stopped)
				return nil
			}
			return fmt.Errorf("core: accept on %s: %w", s.ref.Socket, err)
		}
		s.inflight.Add(1)
		go func() {
			defer s.inflight.Done()
			s.handleConn(reqCtx, conn)
		}()
	}
}

// Close begins an orderly shutdown: `stopping`, then the listener and the
// discovery artifacts go away so no new caller can find or reach the core. It is
// idempotent — ctx cancellation and an explicit Close can both fire.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.state = conformance.Stopping
	s.mu.Unlock()

	// Propagate the orderly-shutdown signal to every registered participant
	// (Task 2.1 Step 2.1.6) — the same lifecycle diagram interfaces.md declares
	// for the core's own state, now driven onto the registry too: stopping (no
	// new requests), then stopped (drained). Best-effort — SetLifecycle only
	// fails for an id already gone from the registry, which Close does not
	// treat as a shutdown error.
	for _, reg := range s.reg.List() {
		_ = s.reg.SetLifecycle(reg.ID, conformance.Stopping)
	}

	// Unpublish BEFORE closing the listener: a CLI that reads the record must not
	// be handed a socket that is about to vanish.
	var errs []error
	if err := os.Remove(RecordPath(s.logDir)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("core: remove discovery record: %w", err))
	}
	if err := s.ln.Close(); err != nil {
		errs = append(errs, fmt.Errorf("core: close listener: %w", err))
	}
	// Go's unix listener unlinks the socket on Close; tolerate it being gone.
	if err := os.Remove(s.ref.Socket); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("core: remove socket: %w", err))
	}
	for _, reg := range s.reg.List() {
		_ = s.reg.SetLifecycle(reg.ID, conformance.Stopped)
	}
	return errors.Join(errs...)
}

func (s *Service) setState(state conformance.Lifecycle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Never walk backwards out of shutdown: a late Accept must not reopen a
	// closing core.
	if s.closing && state == conformance.Started {
		return
	}
	s.state = state
}

func (s *Service) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// serverCallDeadline is the core's OWN accept-to-response deadline in
// handleConn — a SEPARATE, server-side constant, independent of a client's
// CallOptions (Task 3.10 Interfaces): nothing on the wire carries a client's
// chosen timeout to the server process, so both sides simply default to the
// same duration (DefaultCallTimeout in socket.go) by CONVENTION, not by
// propagation.
const serverCallDeadline = DefaultCallTimeout

// baseCallDeadline is the per-connection base deadline: the test override when
// set, else serverCallDeadline.
func (s *Service) baseCallDeadline() time.Duration {
	if s.callDeadline > 0 {
		return s.callDeadline
	}
	return serverCallDeadline
}

// serverDeadlineFor is the accept-to-response deadline for one authenticated
// subcommand: serverCallDeadline for every verb except log-compact, which does
// real file work (fold, write, fsync, rename of a log up to max_log_bytes) and so
// gets LogCompactCallTimeout. It is applied only AFTER the token check, so an
// unauthenticated peer can never hold a connection open that long.
func serverDeadlineFor(subcommand string) time.Duration {
	if subcommand == SubcommandLogCompact {
		return LogCompactCallTimeout
	}
	return serverCallDeadline
}

// handleConn serves one transport frame: authenticate, admission-control the
// two read verbs, run the subcommand through the participant boundary, return
// the reply and coarse exit code.
func (s *Service) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// A socket deadline is WALL-clock, so it deliberately uses time.Now rather than
	// the injectable clock seam (which stamps domain timestamps): a mock clock must
	// not be able to make a real connection hang forever.
	if err := conn.SetDeadline(time.Now().Add(s.baseCallDeadline())); err != nil {
		slog.Warn("core: set connection deadline failed", "err", err)
		return
	}
	var req wireRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		// A clean EOF with nothing sent is a LIVENESS PROBE, not a fault: Discover
		// connects and closes to prove a core is reachable (and so does any port
		// scanner). Answering it would write to an already-closed peer, so this
		// path must stay silent at WARN or every discovery would log twice.
		if errors.Is(err, io.EOF) {
			slog.Debug("core: connection closed without a request (liveness probe)")
			return
		}
		slog.Warn("core: malformed transport frame", "err", err)
		s.respond(conn, conformance.ExitError, errorReply("malformed transport frame: "+err.Error()))
		return
	}
	if !authorized(req.Token, s.ref.Token) {
		// Do not log the presented token: it is attacker-controlled input that
		// would land verbatim in the log.
		slog.Warn("core: rejected socket request with a bad token", "subcommand", req.Subcommand)
		s.respond(conn, conformance.ExitError, errorReply("unauthorized"))
		return
	}
	// Verb-classed admission control (Task 3.10 Step 4), positioned HERE —
	// after frame decode AND after the token check — so a malformed frame or a
	// bad token is NEVER refused by this semaphore (it is a pre-Serve outcome,
	// not a read-verb admission decision): only a genuinely authorized
	// {status, mon.read} call can be declined this way, and it is declined
	// IMMEDIATELY (never blocked until a slot frees, Step 2) with a
	// human-readable cli.error envelope (Step 5) at exit 9 — the exact signal
	// Task 4.0's poller backs off on (PollerBackoffCap's doc).
	if isReadVerb(req.Subcommand) {
		if !s.acquireReadSlot() {
			slog.Debug("core: read semaphore saturated, refusing", "subcommand", req.Subcommand)
			s.respond(conn, conformance.ExitBusy, errorReply(readRefusalMessage))
			return
		}
		defer s.releaseReadSlot()
	}
	if d := serverDeadlineFor(req.Subcommand); d != serverCallDeadline && d != s.baseCallDeadline() {
		if err := conn.SetDeadline(time.Now().Add(d)); err != nil {
			slog.Warn("core: extend connection deadline failed", "subcommand", req.Subcommand, "err", err)
		}
	}
	var out bytes.Buffer
	code := s.serve(ctx, req.Subcommand, bytes.NewReader(req.Payload), &out)
	s.respond(conn, code, out.Bytes())
}

// respond writes one transport reply frame.
func (s *Service) respond(w io.Writer, code int, reply []byte) {
	body := json.RawMessage(reply)
	if len(body) == 0 {
		body = jsonNull // a body-less reply (the legal busy case) is null, not invalid JSON
	}
	if err := json.NewEncoder(w).Encode(wireResponse{ExitCode: code, Reply: body}); err != nil {
		slog.Warn("core: write reply failed", "err", err)
	}
}

// Serve is the participant boundary (conformance.Participant): it runs one
// subcommand over the JSON-in / JSON-out contract and returns a coarse exit code
// (0 ok / 1 error / 2 usage / 9 busy). Every transport funnels through here, so
// the message schema is enforced exactly once.
//
// Messages are accepted ONLY while the core is `started` (INV-INTF-1); before or
// after that the request is refused with the protocol error envelope rather than
// silently dropped, so the caller's exit code tells it what happened.
func (s *Service) Serve(subcommand string, stdin io.Reader, stdout io.Writer) int {
	return s.serve(context.Background(), subcommand, stdin, stdout)
}

// serve is Serve with the context of the request being served, threaded to the
// verbs that call out (status and mon.read metric snapshots, level refresh).
func (s *Service) serve(ctx context.Context, subcommand string, stdin io.Reader, stdout io.Writer) int {
	if st := s.State(); st != conformance.Started {
		writeBody(stdout, errorReply(fmt.Sprintf("not accepting: core is %s", st)))
		return conformance.ExitError
	}
	switch subcommand {
	case SubcommandIngestEvent:
		return s.handleIngestEvent(stdin, stdout)
	case SubcommandSelfStatus:
		return s.handleSelfStatus(stdin, stdout)
	case SubcommandMonRead:
		return s.handleMonRead(ctx, stdin, stdout)
	case SubcommandStatus:
		return s.handleStatus(ctx, stdin, stdout)
	case SubcommandRegister:
		return s.handleRegister(stdin, stdout)
	case SubcommandLogCompact:
		return s.handleLogCompact(stdin, stdout)
	case SubcommandLevelIngest:
		return s.handleLevelIngest(stdin, stdout)
	case SubcommandLevelList:
		return s.handleLevelList(stdin, stdout)
	case SubcommandLevelSet:
		return s.handleLevelSet(stdin, stdout)
	case SubcommandLevelClear:
		return s.handleLevelClear(stdin, stdout)
	case SubcommandLevelRefresh:
		return s.handleLevelRefresh(ctx, stdin, stdout)
	case SubcommandGet:
		return s.handleGet(stdin, stdout)
	case SubcommandPut:
		return s.handlePut(stdin, stdout)
	case SubcommandDelete:
		return s.handleDelete(stdin, stdout)
	default:
		// `session-status` deliberately lands HERE, as an unknown subcommand. It was
		// dropped 2026-07-28: pg-router consumes no post-accept session outcome, so the
		// callback had no consumer left. Acceptance arrives in the dispatch reply
		// (inline outcome or `{"deferred": true}`), and a participant's own health
		// travels on the self-status channel (SelfStatus, registry.go). Do not add it
		// back without a consumer.
		writeBody(stdout, errorReply(fmt.Sprintf("unknown subcommand %q", subcommand)))
		return conformance.ExitError
	}
}

// writeBody writes a reply body, logging rather than failing on a write error —
// there is nothing else to do with it, and the exit code already carries the
// coarse outcome.
func writeBody(w io.Writer, body []byte) {
	if _, err := w.Write(body); err != nil {
		slog.Warn("core: write reply body failed", "err", err)
	}
}

// SubcommandStatus is the INTF-CLI verb (Task 3.8) an operator (`pg-router
// status`) — or, later, Task 4.0's TUI via its `since` long-poll affordance —
// uses to inspect a running core: its resolved configuration, live
// deliveries, and per-type queue depths (interfaces.md "Inspecting a running
// core"; register row bead pg2-xa44k).
const SubcommandStatus = "status"

// The message types backing this subcommand (schemas/, checked via package
// conformance — INV-INTF-2).
const (
	StatusRequestSchema = "cli.status"
	StatusReplySchema   = "cli.status-reply"
)

// ErrorReplySchema is errorReply's own protocol-level failure envelope shape
// (`{schemaVersion, error}`) given a schema artifact, so a CLI-facing client
// can discriminate it from the verb's own reply schema BEFORE trusting
// either (register row bead pg2-o9r6a; Task 3.8 Binding decisions, Step 7:
// "creating the cli.error schema alone does not close it" — every
// CLI-facing client that reads a core reply must apply the discrimination
// itself, which is what cmd/pg-router's discriminateReply does with this
// constant).
const ErrorReplySchema = "cli.error"

// SubcommandGet / SubcommandPut / SubcommandDelete are INTF-STORE's core-
// initiated wire subcommands (Task 6.7): three socket verbs, each carrying
// the existing store.request/store.reply schema pair (whose `op` field MUST
// equal the subcommand — validated at decode below), mirroring
// SubcommandStatus/SubcommandPause's shape exactly (Binding decision 3).
// This — not one "store" subcommand branching internally on op — is what
// lets isReadVerb classify SubcommandGet alone with a one-line addition to
// its existing string switch, with no signature change and no payload
// inspection added to admission control. No human ever runs `pg-router get`
// (these are core<->participant wire subcommands, never a NEW
// operator-typed CLI subcommand — the Global Constraints'
// Operator-command-surface rule does not apply here).
const (
	SubcommandGet    = "get"
	SubcommandPut    = "put"
	SubcommandDelete = "delete"
)

// The message types backing the three INTF-STORE subcommands above
// (schemas/, checked via package conformance — INV-INTF-2). Both schemas
// pre-date this task (Task 0.7); this file only wires the three handlers,
// plus store.reply's Task 6.7 widening (schemas/store.reply.schema.json:
// `value` may now be null).
const (
	StoreRequestSchema = "store.request"
	StoreReplySchema   = "store.reply"
)

// storeRequest is the decoded store.request envelope common to all three
// verbs. Value is only ever populated for `put` — package conformance's own
// storeRequestRule cross-field check ("value required IFF op==put") already
// enforces that a `get`/`delete` request never carries one, so decoding into
// a plain string here (rather than a pointer) loses nothing.
type storeRequest struct {
	ID    string `json:"id"`
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// decodeStoreRequest reads+validates one store.request (the conformance
// schema check, which also runs storeRequestRule), then confirms the
// request's own `op` field matches subcommand — SubcommandGet/Put/Delete are
// three DISTINCT socket verbs, never one "store" verb branching on op
// (Binding decision 3), so a mismatch (whichever way) is reported as a
// request-shaped error, never a silent misroute to the wrong store method.
func decodeStoreRequest(stdin io.Reader, stdout io.Writer, subcommand string) (storeRequest, bool) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply(subcommand+": read request: "+err.Error()))
		return storeRequest{}, false
	}
	if err := conformance.CheckBytes(StoreRequestSchema, data); err != nil {
		writeBody(stdout, errorReply(subcommand+": "+err.Error()))
		return storeRequest{}, false
	}
	var req storeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		// Unreachable once CheckBytes has passed — see handleStatus's identical note.
		writeBody(stdout, errorReply(subcommand+": malformed request: "+err.Error()))
		return storeRequest{}, false
	}
	if req.Op != subcommand {
		writeBody(stdout, errorReply(fmt.Sprintf("%s: request op %q does not match the dispatched subcommand", subcommand, req.Op)))
		return storeRequest{}, false
	}
	return req, true
}

// writeStoreReply marshals+writes one store.reply body, mirroring
// handleStatus's identical marshal-error handling.
func writeStoreReply(stdout io.Writer, reply map[string]any, verb string) int {
	body, err := json.Marshal(reply)
	if err != nil { // unreachable: reply holds only JSON-safe scalars
		writeBody(stdout, errorReply(verb+": marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// handleGet runs the `get` socket verb (Task 6.7): a read verb (isReadVerb),
// resolved against Service.store (the in-memory default, or the
// wire-forwarding implementation once a KindStorage participant has
// registered — Binding decision 4). A missing key is reported as
// `{ok:false, value:null}` — kvstore.Store's own documented "absent" idiom
// (its Get doc: "ok=false and a nil error... is how absent is
// represented"), never an error, and never an OMITTED `value` field: the
// widened store.reply.schema.json (this task) is what lets `value` carry an
// explicit null here rather than one caller having to infer absence from a
// missing key.
func (s *Service) handleGet(stdin io.Reader, stdout io.Writer) int {
	req, ok := decodeStoreRequest(stdin, stdout, SubcommandGet)
	if !ok {
		return conformance.ExitError
	}
	value, found, err := s.getStore().Get(req.Key)
	if err != nil {
		writeBody(stdout, errorReply("get: "+err.Error()))
		return conformance.ExitError
	}
	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"ok":            found,
	}
	if found {
		reply["value"] = value
	} else {
		reply["value"] = nil
	}
	return writeStoreReply(stdout, reply, "get")
}

// handlePut runs the `put` socket verb (Task 6.7): a write verb (isReadVerb
// excludes it, admitted unconditionally same as ingest-event), resolved
// against Service.store exactly like handleGet.
func (s *Service) handlePut(stdin io.Reader, stdout io.Writer) int {
	req, ok := decodeStoreRequest(stdin, stdout, SubcommandPut)
	if !ok {
		return conformance.ExitError
	}
	if err := s.getStore().Put(req.Key, req.Value); err != nil {
		writeBody(stdout, errorReply("put: "+err.Error()))
		return conformance.ExitError
	}
	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"ok":            true,
	}
	return writeStoreReply(stdout, reply, "put")
}

// handleDelete runs the `delete` socket verb (Task 6.7): a write verb
// (isReadVerb excludes it), resolved against Service.store exactly like
// handleGet/handlePut. Deleting an absent key is a no-op, not an error
// (kvstore.Store's own documented Delete contract), so this always reports
// ok=true once the underlying store call itself does not error.
func (s *Service) handleDelete(stdin io.Reader, stdout io.Writer) int {
	req, ok := decodeStoreRequest(stdin, stdout, SubcommandDelete)
	if !ok {
		return conformance.ExitError
	}
	if err := s.getStore().Delete(req.Key); err != nil {
		writeBody(stdout, errorReply("delete: "+err.Error()))
		return conformance.ExitError
	}
	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"ok":            true,
	}
	return writeStoreReply(stdout, reply, "delete")
}

// activityReadWindow bounds one status reply's activity[] slice. It matches
// the ring's own DefaultSize (Task 3.4) rather than its smaller
// defaultReadWindow: a caller-supplied `since` can legitimately ask for more
// than the newest-min(64,held) default returns, and Ring.Read itself
// truncates to whichever of (requested window, buffer length) is smaller, so
// sizing the buffer to the ring's full capacity is what lets a since-scoped
// request actually get everything the ring still holds.
const activityReadWindow = activity.DefaultSize

// handleStatus runs the `status` verb (Task 3.8): it composes the
// cli.status-reply body — resolved configuration, live deliveries, and
// per-type queue depths (the three INTF-CLI inspection MUSTs, register row
// bead pg2-xa44k) plus the additive Task 3.8 field set — strictly from what
// is already published on this Service, with no cross-package reach (Task
// 3.8 Files).
//
// `deliveries` stays the legacy shape but is always empty: nothing this
// docket phase's Contract lists as consumed (Task 3.2/3.4/3.5/3.6/3.7,
// Registry.List()) produces a per-(event,handler) delivery record keyed by a
// dispatch tracking id. That is core-internal accepted-map state
// (eventqueue's entry.accepted, unexported, no public accessor), and adding
// one is outside this task's Files — a documented realization gap, not a
// silent guess. The resolved-configuration and per-type-queue-depth MUSTs
// are fully realized below.
func (s *Service) handleStatus(ctx context.Context, stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply("status: read request: "+err.Error()))
		return conformance.ExitError
	}
	if err := conformance.CheckBytes(StatusRequestSchema, data); err != nil {
		writeBody(stdout, errorReply("status: "+err.Error()))
		return conformance.ExitError
	}
	var req struct {
		Since uint64 `json:"since"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		// Unreachable once CheckBytes has passed — see mon.go's identical note.
		writeBody(stdout, errorReply("status: malformed request: "+err.Error()))
		return conformance.ExitError
	}

	body, err := status.Encode(s.composeStatusReply(ctx, req.Since))
	if err != nil { // unreachable: composeStatusReply holds only JSON-safe scalars/slices/maps
		writeBody(stdout, errorReply("status: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// declaredTypesSorted returns every type SOME configured binding declares
// (s.bindings' own keys), sorted, for UnmatchedBindings' `declared` argument.
func (s *Service) declaredTypesSorted() []string {
	out := make([]string, 0, len(s.bindings))
	for t := range s.bindings {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// shellQuote single-quotes a value for the callback command string, so a socket
// path containing a space (or any shell metacharacter) survives the participant
// running the command through a shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
