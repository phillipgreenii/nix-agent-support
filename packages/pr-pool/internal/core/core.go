// Package core is pr-pool's socket service: the long-lived process the behavior
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
// callback, because nothing in pr-pool consumes a post-accept outcome. Acceptance
// arrives in the dispatch REPLY — an inline outcome, or `{"deferred": true}`
// (interfaces.md) — not on a callback. See Serve's default branch. This is
// distinct from SELF-status, which survives and — as of bead pg2-zaghi — has its
// own wire mechanism: see SelfStatus in registry.go and SubcommandSelfStatus in
// selfstatus.go.
//
// # Booted in production by cmd/pr-pool's run / run-until-idle (pg2-f3mcb.2)
//
// cmd/pr-pool's `run` (long-running daemon) and `run-until-idle` (discover once,
// drain to idle, exit) subcommands call Listen + Accept, giving this Service a
// live socket for ingest-event and self-status outside a test binary — the
// multi-bead convergence (epic pg2-f3mcb) onto "the queue is the universal
// intermediary" (pg2-f3mcb.2, ADR 0056) that also retired internal/eventbus and
// internal/orchestrator's discover-then-dispatch loop over it. See
// cmd/pr-pool/run.go (bootCore) for the wiring: a queue->executor Listener per
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

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// Service is a running core: the socket listener plus the state behind it.
var _ conformance.Participant = (*Service)(nil)

// DefaultCommand is the command name used when assembling the callback strings
// the core hands to participants.
const DefaultCommand = "pr-pool"

// Options configures Listen.
type Options struct {
	// LogDir is where the socket and discovery record live — pr-pool's existing
	// runtime state directory (Config.LogDir / PR_POOL_LOG_DIR). Required.
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
	// internal/metrics.NewReadableProvider's doc). cmd/pr-pool's bootCore is
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
	// cmd/pr-pool's bootCore is the production wiring site.
	ActivityRing *activity.Ring
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
	configPath   string
	startedAt    time.Time

	// tick and gates are the two published-state cells Serve's handlers (this
	// package) read with no cross-package import (Task 3.5 Objective):
	// tick is written by PublishTick (status.go) and gates is written by
	// ObserveGateFromTick/ObserveGateFromSocketVerb (status.go, its own small
	// mutex — never mu above).
	tick  atomic.Pointer[TickSnapshot]
	gates gateState
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
func Listen(opts Options) (*Service, error) {
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
		return nil, fmt.Errorf("core: socket path %s is %d bytes, over the platform limit of %d; set PR_POOL_LOG_DIR to a shorter directory",
			sock, len(sock), maxSocketPathLen)
	}
	if err := clearStaleSocket(sock); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
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
		state:          conformance.Starting,
		q:              opts.Queue,
		bindings:       opts.Bindings,
		obs:            opts.Observer,
		reg:            NewRegistry(now),
		ln:             ln,
		ref:            ref,
		logDir:         opts.LogDir,
		command:        command,
		metricsReader:  opts.MetricsReader,
		monitorSubsets: opts.MonitorSubsets,
		activityRing:   opts.ActivityRing,
		configPath:     opts.ConfigPath,
		startedAt:      now(),
	}, nil
}

// clearStaleSocket removes a leftover socket file so Listen can re-bind, but only
// after proving nothing answers on it — unlinking a LIVE core's socket would
// leave two cores fighting over one discovery record.
func clearStaleSocket(sock string) error {
	if _, err := os.Stat(sock); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("core: stat socket %s: %w", sock, err)
	}
	if err := probe(sock); err == nil {
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
	return reg, nil
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
			s.handleConn(conn)
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

// handleConn serves one transport frame: authenticate, run the subcommand through
// the participant boundary, return the reply and coarse exit code.
func (s *Service) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// A socket deadline is WALL-clock, so it deliberately uses time.Now rather than
	// the injectable clock seam (which stamps domain timestamps): a mock clock must
	// not be able to make a real connection hang forever.
	if err := conn.SetDeadline(time.Now().Add(callTimeout)); err != nil {
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
	var out bytes.Buffer
	code := s.Serve(req.Subcommand, bytes.NewReader(req.Payload), &out)
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
		return s.handleMonRead(stdin, stdout)
	case SubcommandStatus:
		return s.handleStatus(stdin, stdout)
	default:
		// `session-status` deliberately lands HERE, as an unknown subcommand. It was
		// dropped 2026-07-28: pr-pool consumes no post-accept session outcome, so the
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

// SubcommandStatus is the INTF-CLI verb (Task 3.8) an operator (`pr-pool
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
// itself, which is what cmd/pr-pool's discriminateReply does with this
// constant).
const ErrorReplySchema = "cli.error"

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
func (s *Service) handleStatus(stdin io.Reader, stdout io.Writer) int {
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

	body, err := json.Marshal(s.composeStatusReply(req.Since))
	if err != nil { // unreachable: composeStatusReply holds only JSON-safe scalars/slices/maps
		writeBody(stdout, errorReply("status: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// composeStatusReply builds the cli.status-reply body. Lock ordering (Task
// 3.8 Binding decisions, Step 8): DepthByType/UnmatchedBindings are already
// lock-free reads of eventqueue's own published depthCell (Task 3.2), so no
// q.mu is ever taken here — Registry.List() (its own mutex) and
// s.activityRing.Read (the ring's own mutex) each run independently, with no
// lock held across either call.
//
// tick may be nil (the boot window, before the drive loop's first
// PublishTick) — every tick-derived field is simply omitted from the reply
// rather than guessed at, and nothing here panics on that nil (Task 3.8
// Acceptance).
func (s *Service) composeStatusReply(since uint64) map[string]any {
	regs := s.reg.List()
	tick := s.CurrentTick()
	gates, gatesObservedAt := s.GateSnapshot()

	legacySources, legacyHandlers := 0, 0
	if tick != nil {
		legacySources = tick.Config.ActiveQueries
		legacyHandlers = tick.Config.ActiveRoles
	}

	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"deliveries":    []any{}, // see handleStatus's doc: no tracking-id source this docket phase
		"queues":        statusQueues(s.q.DepthByType()),
		"config":        map[string]any{"sources": legacySources, "handlers": legacyHandlers},
		"core": map[string]any{
			"state":      s.State().String(),
			"pid":        os.Getpid(),
			"startedAt":  s.startedAt.UTC().Format(time.RFC3339Nano),
			"configPath": s.configPath,
		},
		"registry":  statusRegistrations(regs),
		"listeners": statusRegistrationsOfKind(regs, KindHandler),
		"gates":     statusGates(gates),
		"asOf":      time.Now().UTC().Format(time.RFC3339Nano),
		"sources":   []any{},
		"activity":  []any{},
		// activityDropped defaults false (no ring, or since==0's "no cursor, no
		// gap to report" case per Ring.Read's own doc) and is set true below
		// only when the ring itself reports a gap (bead pg2-vtuou).
		"activityDropped": false,
	}
	if unmatched := s.q.UnmatchedBindings(s.declaredTypesSorted()); len(unmatched) > 0 {
		reply["unmatchedBindings"] = unmatched
	} else {
		reply["unmatchedBindings"] = []any{}
	}
	if !gatesObservedAt.IsZero() {
		reply["gatesObservedAt"] = gatesObservedAt.UTC().Format(time.RFC3339Nano)
	}
	if s.activityRing != nil {
		buf := make([]activity.Entry, activityReadWindow)
		n, dropped := s.activityRing.Read(since, buf)
		reply["activity"] = statusActivity(buf[:n])
		reply["activityDropped"] = dropped
	}
	if tick != nil {
		core := reply["core"].(map[string]any)
		core["version"] = tick.Version
		reply["mode"] = tick.RunMode
		reply["resolvedConfig"] = statusResolvedConfig(tick.Config)
		reply["sources"] = statusSources(tick.Sources)
		reply["lastTickAt"] = tick.LastTickAt.UTC().Format(time.RFC3339Nano)
		reply["snapshotAt"] = tick.SnapshotAt.UTC().Format(time.RFC3339Nano)
		if tick.Config.PollInterval != nil {
			reply["tickIntervalMs"] = tick.Config.PollInterval.Milliseconds()
		}
	}
	return reply
}

// statusQueues renders DepthByType's map as the legacy `queues` array,
// sorted by type for a deterministic reply.
func statusQueues(depth map[string]int) []map[string]any {
	types := make([]string, 0, len(depth))
	for t := range depth {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]map[string]any, 0, len(types))
	for _, t := range types {
		out = append(out, map[string]any{"type": t, "depth": depth[t]})
	}
	return out
}

// statusGates renders GateSnapshot's map as the `gates` array, sorted by
// name; `mtime`/`owner` are omitted per-entry when the gate carries none
// (an unset gate has no mtime, and no writer here ever sets Owner today).
func statusGates(gates map[string]GateInfo) []map[string]any {
	names := make([]string, 0, len(gates))
	for n := range gates {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		g := gates[n]
		entry := map[string]any{"name": n, "set": g.Set}
		if !g.Mtime.IsZero() {
			entry["mtime"] = g.Mtime.UTC().Format(time.RFC3339Nano)
		}
		if g.Owner != "" {
			entry["owner"] = g.Owner
		}
		out = append(out, entry)
	}
	return out
}

// statusRegistrations renders Registry.List() entries as the `registry`
// array's shape — also reused, filtered, for `listeners`.
func statusRegistrations(regs []Registration) []map[string]any {
	out := make([]map[string]any, 0, len(regs))
	for _, r := range regs {
		out = append(out, map[string]any{
			"id":    r.ID,
			"kind":  string(r.Kind),
			"state": r.State.String(),
			"self":  string(r.Self),
		})
	}
	return out
}

// statusRegistrationsOfKind filters regs to one Kind before rendering —
// `listeners` is the Kind==KindHandler subset of the full `registry` dump
// (interfaces.md: a handler is the participant that "listens" for dispatch).
func statusRegistrationsOfKind(regs []Registration, kind Kind) []map[string]any {
	var filtered []Registration
	for _, r := range regs {
		if r.Kind == kind {
			filtered = append(filtered, r)
		}
	}
	return statusRegistrations(filtered)
}

// statusSources renders a TickSnapshot's Sources as the `sources` array.
func statusSources(sources []SourceReport) []map[string]any {
	out := make([]map[string]any, 0, len(sources))
	for _, sr := range sources {
		out = append(out, map[string]any{"name": sr.Name, "rejected": sr.Rejected})
	}
	return out
}

// statusActivity renders Ring.Read's output as the `activity` array, oldest
// first (Ring.Read's own return order).
func statusActivity(entries []activity.Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"seq":       e.Seq,
			"startedAt": e.StartedAt.UTC().Format(time.RFC3339Nano),
			"type":      e.Type,
			"outcome":   e.Outcome,
		})
	}
	return out
}

// statusResolvedConfig renders a TickSnapshot's ResolvedConfig as the
// `resolvedConfig` object; `pollIntervalMs` is omitted when PollInterval is
// nil (drain-and-exit mode has no polling cadence to report — Task 3.5
// Step 7).
func statusResolvedConfig(cfg ResolvedConfig) map[string]any {
	out := map[string]any{
		"repoRoot":      cfg.RepoRoot,
		"beadsPrefix":   cfg.BeadsPrefix,
		"activeRoles":   cfg.ActiveRoles,
		"activeQueries": cfg.ActiveQueries,
	}
	if cfg.PollInterval != nil {
		out["pollIntervalMs"] = cfg.PollInterval.Milliseconds()
	}
	return out
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
