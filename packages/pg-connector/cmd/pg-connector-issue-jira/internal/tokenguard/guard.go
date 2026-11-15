package tokenguard

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

// Degraded reasons.
const (
	// ReasonTokenRejected: the keychain holds the token Jira already rejected.
	ReasonTokenRejected = "token_rejected"
	// ReasonTokenUnavailable: the keychain could not be read.
	ReasonTokenUnavailable = "token_unavailable"
	// ReasonTokenFile: the token file could not be prepared or written.
	ReasonTokenFile = "token_file"
)

// Read reasons recorded on a token_read event.
const (
	ReadStart   = "start"
	ReadRepull  = "repull"
	ReadRewrite = "rewrite"
	ReadBackoff = "backoff"
)

const (
	// KeychainFloor is the least time between two keychain reads (the start
	// read is exempt from waiting, but it counts as a read).
	KeychainFloor = 60 * time.Second
	// ReadTimeout bounds one keychain read.
	ReadTimeout = 10 * time.Second
)

// DefaultBackoff is the wait before each retry of a failed keychain read.
var DefaultBackoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}

// Event is one thing the guard reports to its Recorder. It never carries the
// token or a digest.
type Event struct {
	// Name is token_read, token_file_write or token_degraded.
	Name string
	// Reason is the read reason (token_read) or the degraded reason.
	Reason string
	// Result is "ok", a keychain error kind, or "failed".
	Result string
	// Unchanged is set on a successful token_read whose token equals the one
	// last written to the file (no digest is carried, only the comparison).
	Unchanged bool
	// Err is the OS error text of a failed token_file_write. The store's
	// errors never contain the token. It is for the log only, never a label.
	Err string
}

// Recorder receives the guard's events; part of the telemetry wiring
// implements it. The default drops everything.
type Recorder interface{ Record(Event) }

type nopRecorder struct{}

func (nopRecorder) Record(Event) {}

// Store is what the guard needs of the token file store (*tokenfile.Store).
type Store interface {
	Start() error
	Write(token []byte) error
}

// State is the guard state the status report reads. It holds no secret.
type State struct {
	Degraded bool
	// Reason is "" when healthy, else a Reason constant.
	Reason string
	// ReadErrKind is the kind of the latest failed keychain read, "" if none.
	ReadErrKind keychain.Kind
	Generation  uint64
}

// Config configures New.
type Config struct {
	Keychain keychain.Keychain
	Store    Store
	// Service and Account name the keychain item.
	Service, Account string
	Clock            governor.Clock // nil: governor.SystemClock
	Timer            Timer          // nil: SystemTimer
	Recorder         Recorder       // nil: drops events
	Backoff          []time.Duration
}

// Guard is the token lifecycle owner. It implements origin.Refresher.
type Guard struct {
	kc       keychain.Keychain
	store    Store
	svc, acc string
	clock    governor.Clock
	timer    Timer
	rec      Recorder
	backoff  []time.Duration

	cancel context.CancelFunc
	ctx    context.Context
	wg     sync.WaitGroup

	// refreshMu serialises refreshes (the singleflight); mu guards the state.
	refreshMu sync.Mutex
	mu        sync.Mutex
	gen       uint64
	reason    string
	closing   bool
	readErr   keychain.Kind
	hasRead   bool
	lastRead  time.Time
	written   [sha256.Size]byte
	hasWrit   bool
	rejected  [sha256.Size]byte
	hasRej    bool
	looping   bool
}

var _ origin.Refresher = (*Guard)(nil)

// New returns a guard. It does nothing until Start.
func New(c Config) *Guard {
	g := &Guard{
		kc: c.Keychain, store: c.Store, svc: c.Service, acc: c.Account,
		clock: c.Clock, timer: c.Timer, rec: c.Recorder, backoff: c.Backoff,
	}
	if g.clock == nil {
		g.clock = governor.SystemClock{}
	}
	if g.timer == nil {
		g.timer = SystemTimer{}
	}
	if g.rec == nil {
		g.rec = nopRecorder{}
	}
	if g.backoff == nil {
		g.backoff = DefaultBackoff
	}
	g.ctx, g.cancel = context.WithCancel(context.Background())
	return g
}

// Start prepares the token store (removing leftovers), makes the one start
// keychain read and writes the token file. It never returns an error: a
// failure degrades the guard (the daemon still starts and serves stored
// content). Call it after the instance lock is held.
func (g *Guard) Start(ctx context.Context) {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()
	if err := g.store.Start(); err != nil {
		g.degradeFile()
		return
	}
	tok, err := g.read(ctx, ReadStart)
	if err != nil {
		g.noteReadError(err)
		g.startBackoff()
		return
	}
	g.install(tok)
}

// Close stops the backoff loop and marks the guard closing, so a call failing
// on the removed file never reads the keychain. It does not remove the file.
func (g *Guard) Close() {
	g.SetClosing()
	g.cancel()
	g.wg.Wait()
}

// SetClosing marks shutdown: no further keychain reads happen.
func (g *Guard) SetClosing() {
	g.mu.Lock()
	g.closing = true
	g.mu.Unlock()
}

// Degraded reports whether calls must be refused without launching pjira.
func (g *Guard) Degraded() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason != ""
}

// Generation is the count of token file writes.
func (g *Guard) Generation() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen
}

// State returns the guard state for the status report.
func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return State{Degraded: g.reason != "", Reason: g.reason, ReadErrKind: g.readErr, Generation: g.gen}
}

// Refresh implements origin.Refresher. It reports whether the failed call
// should be retried once.
func (g *Guard) Refresh(ctx context.Context, used uint64, why string) bool {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()

	g.mu.Lock()
	switch {
	case g.closing, g.reason != "":
		g.mu.Unlock()
		return false
	case used < g.gen:
		// Another call already rewrote the file: retry without a read.
		g.mu.Unlock()
		return true
	case g.hasRead && g.clock.Now().Sub(g.lastRead) < KeychainFloor:
		// Inside the floor: no comparison, no retry, no state change.
		g.mu.Unlock()
		return false
	}
	g.mu.Unlock()

	reason := ReadRepull
	if why == origin.ReasonTokenFileMissing {
		reason = ReadRewrite
	}
	tok, err := g.read(ctx, reason)
	if err != nil {
		g.noteReadError(err)
		g.startBackoff()
		return false
	}
	d := sha256.Sum256([]byte(tok))
	g.mu.Lock()
	same := g.hasWrit && g.written == d
	g.mu.Unlock()
	if same && why != origin.ReasonTokenFileMissing {
		g.mu.Lock()
		g.rejected, g.hasRej = d, true
		g.mu.Unlock()
		g.degrade(ReasonTokenRejected)
		return false
	}
	return g.install(tok)
}

// RejectedAfterRetry implements origin.Refresher: the token written by the
// last refresh was rejected too.
func (g *Guard) RejectedAfterRetry() {
	g.mu.Lock()
	if g.hasWrit {
		g.rejected, g.hasRej = g.written, true
	}
	g.mu.Unlock()
	g.degrade(ReasonTokenRejected)
}

// read makes one bounded keychain read and stamps the floor clock.
func (g *Guard) read(ctx context.Context, reason string) (string, error) {
	rctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	tok, err := g.kc.Password(rctx, g.svc, g.acc)
	g.mu.Lock()
	g.hasRead, g.lastRead = true, g.clock.Now()
	g.mu.Unlock()
	ev := Event{Name: "token_read", Reason: reason, Result: "ok"}
	if err != nil {
		ev.Result = string(keychain.KindOf(err))
	} else {
		d := sha256.Sum256([]byte(tok))
		g.mu.Lock()
		ev.Unchanged = g.hasWrit && g.written == d
		g.mu.Unlock()
	}
	g.rec.Record(ev)
	return tok, err
}

// install writes tok to the file, bumps the generation and clears an
// unavailable or file degrade. It reports whether the file now holds tok.
func (g *Guard) install(tok string) bool {
	b := []byte(tok)
	err := g.store.Write(b)
	for i := range b {
		b[i] = 0
	}
	if err != nil {
		g.rec.Record(Event{Name: "token_file_write", Result: "failed", Err: err.Error()})
		g.degradeFile()
		return false
	}
	g.rec.Record(Event{Name: "token_file_write", Result: "ok"})
	d := sha256.Sum256([]byte(tok))
	g.mu.Lock()
	g.written, g.hasWrit = d, true
	g.gen++
	if g.reason == ReasonTokenUnavailable || g.reason == ReasonTokenFile {
		g.reason = ""
	}
	g.readErr = ""
	g.mu.Unlock()
	return true
}

func (g *Guard) degradeFile() { g.degrade(ReasonTokenFile) }

// degrade sets the reason unless the guard is already degraded for a stronger
// one: token_rejected is never replaced.
func (g *Guard) degrade(reason string) {
	g.mu.Lock()
	if g.reason == ReasonTokenRejected || g.reason == reason {
		g.mu.Unlock()
		return
	}
	g.reason = reason
	g.mu.Unlock()
	g.rec.Record(Event{Name: "token_degraded", Reason: reason})
}

// noteReadError records a failed read. An existing token_rejected reason and
// the rejected digest are kept.
func (g *Guard) noteReadError(err error) {
	g.mu.Lock()
	g.readErr = keychain.KindOf(err)
	g.mu.Unlock()
	g.degrade(ReasonTokenUnavailable)
}

// startBackoff starts the bounded retry loop once. Caller holds refreshMu.
func (g *Guard) startBackoff() {
	g.mu.Lock()
	if g.looping || g.closing {
		g.mu.Unlock()
		return
	}
	g.looping = true
	g.mu.Unlock()
	g.wg.Add(1)
	go g.backoffLoop()
}

func (g *Guard) backoffLoop() {
	defer g.wg.Done()
	defer func() {
		g.mu.Lock()
		g.looping = false
		g.mu.Unlock()
	}()
	for _, d := range g.backoff {
		select {
		case <-g.timer.After(d):
		case <-g.ctx.Done():
			return
		}
		g.refreshMu.Lock()
		g.mu.Lock()
		stop := g.closing || g.reason != ReasonTokenUnavailable
		g.mu.Unlock()
		if stop {
			g.refreshMu.Unlock()
			return
		}
		tok, err := g.read(g.ctx, ReadBackoff)
		if err != nil {
			g.noteReadError(err)
			g.refreshMu.Unlock()
			continue
		}
		g.install(tok)
		g.refreshMu.Unlock()
		return
	}
}

// RetryFile retries the token file after a failed write or a refused temp
// directory (degraded token_file): it prepares the store again, makes one
// keychain read (subject to the floor) and writes the file. It does nothing in
// any other state. The caller owns the timing: the guard has no retry timer
// for a disk error, because a disk error is not a keychain read error.
func (g *Guard) RetryFile(ctx context.Context) {
	g.refreshMu.Lock()
	defer g.refreshMu.Unlock()

	g.mu.Lock()
	skip := g.closing || g.reason != ReasonTokenFile ||
		(g.hasRead && g.clock.Now().Sub(g.lastRead) < KeychainFloor)
	g.mu.Unlock()
	if skip {
		return
	}
	if err := g.store.Start(); err != nil {
		return
	}
	tok, err := g.read(ctx, ReadRewrite)
	if err != nil {
		g.noteReadError(err)
		g.startBackoff()
		return
	}
	g.install(tok)
}
