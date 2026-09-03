package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// pollerBackoffInitial / pollerBackoffCap bound the reconnect/retry backoff
// ladder a failed Snapshot advances (pa-monitor's own precedent:
// internal/rpcclient/streaming_poller.go's streamInitialBackoff/
// streamMaxBackoff — 1s seed, doubling, capped at 5s. pollerBackoffCap is 5s,
// NOT 30s — a prior draft mis-stated pa-monitor's actual cap
// [design: Task 4.4 Interfaces]).
//
// pollerRPCDeadline bounds one Client.Call round trip (Snapshot's status
// call and ToggleGate's pause/resume call alike), so a core that never
// replies cannot pin the caller forever.
const (
	pollerBackoffInitial = 1 * time.Second
	pollerBackoffCap     = 5 * time.Second
	pollerRPCDeadline    = 5 * time.Second
)

// ErrBusy is Snapshot's typed sentinel for an exit-9 (conformance.ExitBusy)
// reply: the core is momentarily saturated, not actually failing. It
// advances the backoff ladder like any other poll failure, but the CALLER
// (the Model, Task 4.5) suppresses the poll-error zone for this outcome
// specifically — staleness is surfaced by asOf age alone — which requires
// telling it apart from every other failure, hence a distinct sentinel
// rather than a generic error [design: Task 4.4 (Poller semantics)].
var ErrBusy = errors.New("tui: core is busy (exit 9)")

// Poller is internal/tui's one channel to a running core: exactly one RPC
// attempt per Snapshot call (the livelock bound this design calls pr-pool's
// "one concurrency experiment"), plus ToggleGate for the pause/resume socket
// verb [design: Task 4.4 Objective; Task 4.4 Interfaces].
type Poller interface {
	// Snapshot performs at most one RPC attempt per call. since is the
	// activity-ring cursor from the PREVIOUS successful reply (0 on the
	// first call, or after an epoch reset).
	Snapshot(ctx context.Context, since uint64) (StatusReply, error)

	// ToggleGate issues the pause/resume socket verb by performing its OWN
	// Discover+Dial+Call+Close cycle against the same ref SocketPoller
	// already tracks — NEVER a live connection object reused across calls
	// (core.Client is single-use per connection: one request, one reply,
	// then the server closes it — internal/core/socket.go's Client doc;
	// internal/core/core.go's handleConn decodes exactly one wireRequest
	// then defer-closes). ToggleGate does NOT interact with Snapshot's
	// backoff ladder — a toggle failure is reported to the caller directly,
	// never folded into poll-error bookkeeping [design: Task 4.4
	// Interfaces (ToggleGate)].
	ToggleGate(ctx context.Context, verb string) (effective string, err error)
}

// SocketPoller is the production Poller: it locates a running core via
// core.Discover/core.Dial and speaks the status/pause/resume socket verbs
// over a fresh, single-use core.Client connection per call.
//
// SocketPoller is NOT safe for concurrent Snapshot calls from more than one
// caller in the sense of USEFUL concurrent polling — the TUI's own MVU tick
// loop is the single caller by construction [design: Task 4.4 Interfaces].
// It nonetheless serializes internally (mu) so that a caller which DOES
// invoke it concurrently by accident gets a bounded, race-free result
// (never a data race, never an unbounded pile of in-flight Discover+Dial
// attempts) rather than undefined behavior — this is what
// TestPoller_LivelockBoundUnderRace exercises.
type SocketPoller struct {
	logDir string

	mu      sync.Mutex
	ref     core.Ref
	backoff time.Duration

	// inflight is an independent (lock-external) witness that at most one
	// Discover+Dial+Call attempt is ever outstanding at a time — mu already
	// enforces this; inflight lets a test verify the enforcement rather than
	// assume it.
	inflight int32
}

// NewSocketPoller constructs a SocketPoller against logDir (used for any
// future re-Discover) and an already-resolved ref (which may be the zero
// value, Ref{}, when the caller has not yet discovered a core).
func NewSocketPoller(logDir string, ref core.Ref) *SocketPoller {
	return &SocketPoller{logDir: logDir, ref: ref}
}

// Snapshot implements Poller. Poller semantics, restated precisely
// [design: Task 4.4 (Poller semantics)]:
//
//   - At most one RPC attempt per call. If not currently dialed (no cached
//     ref), it attempts Discover+Dial at most once per call — never loops
//     internally on repeated failure (the livelock bound). A Dial failure
//     here returns the wrapped core.ErrNoRunningCore and advances the
//     backoff ladder; it does not retry Discover again within this same
//     call.
//   - If a ref IS already cached (a prior call's successful Discover), a
//     Dial failure against it is the realistic core-restart signal: it
//     triggers exactly one re-Discover attempt within this same failed
//     call (never more), then one retry Dial against the freshly
//     discovered ref. If that also fails, the call returns the wrapped
//     core.ErrNoRunningCore and advances the backoff ladder.
//   - On a successful Dial, it issues Client.Call(ctx, core.SubcommandStatus,
//     {"schemaVersion":"1","since":since}, CallOptions{}) bounded by
//     pollerRPCDeadline.
//   - Exit code 9 (ExitBusy) is not a poll failure for backoff-suppression
//     purposes at the CALLER: it advances the backoff ladder (the core is
//     momentarily saturated) but Snapshot returns the typed sentinel
//     ErrBusy so the Model can tell this apart from every other failure.
//   - A non-9, non-nil Call error, or a reply that fails
//     core.DiscriminateReply (a malformed reply, or an auth failure — a
//     successful Dial, but the core replies "unauthorized"): a generic poll
//     error, never a panic, advancing the backoff ladder. This path never
//     triggers re-Discover — only an actual Dial failure does.
//   - Success resets the backoff ladder to pollerBackoffInitial.
func (p *SocketPoller) Snapshot(ctx context.Context, since uint64) (StatusReply, error) {
	p.mu.Lock()
	// inflight is incremented strictly AFTER acquiring mu and decremented
	// strictly BEFORE releasing it (a single deferred closure, not two
	// separate defers — deferred calls run LIFO, and two separate defers
	// here would decrement inflight AFTER unlocking, letting a witness read
	// briefly see 2 even though the critical sections never overlapped).
	// This keeps inflight a faithful witness that at most one Discover+
	// Dial+Call attempt is ever outstanding, rather than a false-positive
	// generator.
	atomic.AddInt32(&p.inflight, 1)
	defer func() {
		atomic.AddInt32(&p.inflight, -1)
		p.mu.Unlock()
	}()

	client, err := p.dialLocked()
	if err != nil {
		p.advanceBackoffLocked()
		return StatusReply{}, fmt.Errorf("tui: poll: %w", err)
	}
	defer func() { _ = client.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, pollerRPCDeadline)
	defer cancel()

	payload, err := json.Marshal(struct {
		SchemaVersion string `json:"schemaVersion"`
		Since         uint64 `json:"since"`
	}{SchemaVersion: schemas.SchemaVersion, Since: since})
	if err != nil { // unreachable: two JSON-safe scalars always marshal
		p.advanceBackoffLocked()
		return StatusReply{}, fmt.Errorf("tui: poll: build request: %w", err)
	}

	reply, code, err := client.Call(callCtx, core.SubcommandStatus, payload, core.CallOptions{CallTimeout: pollerRPCDeadline})
	if err != nil {
		p.advanceBackoffLocked()
		return StatusReply{}, fmt.Errorf("tui: poll: %w", err)
	}
	if code == conformance.ExitBusy {
		p.advanceBackoffLocked()
		return StatusReply{}, ErrBusy
	}

	var out StatusReply
	if err := core.DiscriminateReply(reply, core.StatusReplySchema, &out); err != nil {
		p.advanceBackoffLocked()
		return StatusReply{}, fmt.Errorf("tui: poll: %w", err)
	}

	p.backoff = pollerBackoffInitial
	return out, nil
}

// dialLocked resolves a *core.Client, called with mu already held. It
// implements the "currently dialed / not currently dialed" branch of
// Snapshot's own doc, shared verbatim by nothing else (ToggleGate performs
// its own, deliberately separate, resolution below — it must never advance
// Snapshot's backoff ladder, so it cannot call this method, which is always
// followed by advanceBackoffLocked on the caller's error path).
func (p *SocketPoller) dialLocked() (*core.Client, error) {
	ref := p.ref
	discoveredThisCall := false
	if ref.Socket == "" {
		r, err := core.Discover(p.logDir)
		if err != nil {
			return nil, err
		}
		ref = r
		p.ref = r
		discoveredThisCall = true
	}

	client, dialErr := core.Dial(ref, core.DefaultProbeTimeout)
	if dialErr == nil {
		return client, nil
	}

	// Dial failure. A ref we just freshly discovered failing to dial is not
	// retried again this call (that would exceed "at most once"). A CACHED
	// ref failing to dial is the realistic core-restart signal — invalidate
	// it and try exactly one re-Discover + Dial before giving up.
	p.ref = core.Ref{}
	if discoveredThisCall {
		return nil, dialErr
	}
	r, err := core.Discover(p.logDir)
	if err != nil {
		return nil, dialErr
	}
	client, err = core.Dial(r, core.DefaultProbeTimeout)
	if err != nil {
		return nil, err
	}
	p.ref = r
	return client, nil
}

// ToggleGate implements Poller. It reuses SocketPoller's existing
// dial/reconnect machinery (dialLocked: dials the cached ref if any,
// Discovers first if not currently dialed) but performs its OWN fresh
// Discover+Dial+Call+Close cycle — never a live connection reused across
// calls — and never advances or resets Snapshot's backoff ladder, on
// success or failure alike [design: Task 4.4 Interfaces (ToggleGate)].
//
// verb selects the socket subcommand: core.SubcommandPause ("pause") or
// core.SubcommandResume ("resume"). effective reports the ACTUAL resulting
// gate state the core replied with (never an optimistic echo of verb), so
// the caller (the sibling packet covering Task 4.8) never has to guess or
// locally flip a toggle ahead of the core's own answer.
func (p *SocketPoller) ToggleGate(ctx context.Context, verb string) (effective string, err error) {
	var replySchema string
	switch verb {
	case core.SubcommandPause:
		replySchema = core.PauseReplySchema
	case core.SubcommandResume:
		replySchema = core.ResumeReplySchema
	default:
		return "", fmt.Errorf("tui: toggle gate: unknown verb %q", verb)
	}

	p.mu.Lock()
	client, dialErr := p.dialLocked()
	p.mu.Unlock()
	if dialErr != nil {
		return "", fmt.Errorf("tui: toggle gate: %w", dialErr)
	}
	defer func() { _ = client.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, pollerRPCDeadline)
	defer cancel()

	payload, err := json.Marshal(struct {
		SchemaVersion string `json:"schemaVersion"`
	}{SchemaVersion: schemas.SchemaVersion})
	if err != nil { // unreachable: one JSON-safe scalar always marshals
		return "", fmt.Errorf("tui: toggle gate: build request: %w", err)
	}

	reply, _, callErr := client.Call(callCtx, verb, payload, core.CallOptions{CallTimeout: pollerRPCDeadline})
	if callErr != nil {
		return "", fmt.Errorf("tui: toggle gate: %w", callErr)
	}

	var out struct {
		Set bool `json:"set"`
	}
	if err := core.DiscriminateReply(reply, replySchema, &out); err != nil {
		return "", fmt.Errorf("tui: toggle gate: %w", err)
	}
	if out.Set {
		return "paused", nil
	}
	return "resumed", nil
}

// advanceBackoffLocked advances the backoff ladder on a poll failure, called
// with mu already held: pollerBackoffInitial from a reset/zero state,
// doubling thereafter, capped at pollerBackoffCap.
func (p *SocketPoller) advanceBackoffLocked() {
	switch {
	case p.backoff <= 0:
		p.backoff = pollerBackoffInitial
	default:
		next := p.backoff * 2
		if next > pollerBackoffCap {
			next = pollerBackoffCap
		}
		p.backoff = next
	}
}
