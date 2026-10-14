package changes

import (
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// LogKindPR is the kind of the change rows and pending rows of a pull request.
const LogKindPR = "pr"

// The result words Complete reports to the observer.
const (
	// ResultSettled: every awaited group was refreshed.
	ResultSettled = "settled"
	// ResultFailed: the barrier ended because a refresh failed.
	ResultFailed = "failed"
	// ResultTimeout: the barrier ended at its deadline.
	ResultTimeout = "timeout"
)

// hintTTL is how long a recorded origin hint stays usable.
const hintTTL = 10 * time.Minute

// Observer receives the facts the change metrics report.
type Observer interface {
	// Detected is called once per kind of every appended row, with delay the
	// row's at minus its origin_updated_at; it is not called when the origin
	// time is unknown.
	Detected(kind, origin string, delay time.Duration)
	// Settled is called when a pending row ends (Complete, or the append that
	// completed the barrier), with the result word.
	Settled(result string)
}

// Config configures an Engine.
type Config struct {
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// SettleTimeout is how long a pending row waits for its dependents; zero
	// means config.DefaultSettleTimeout.
	SettleTimeout time.Duration
	// BodyCap is the byte cap of a comment or review body recorded in a field;
	// zero means config.DefaultChangeBodyCap.
	BodyCap int
}

type hintKey struct{ node, group string }

type hint struct {
	origin string
	at     time.Time
}

// Engine detects changes and appends them. It is safe for concurrent use; the
// store transaction a call runs in serialises the store work.
type Engine struct {
	clock   governor.Clock
	settle  time.Duration
	bodyCap int

	mu    sync.Mutex
	obs   Observer
	hints map[hintKey]hint
}

// New builds an Engine from cfg, applying the defaults.
func New(cfg Config) (*Engine, error) {
	if cfg.SettleTimeout < 0 {
		return nil, fmt.Errorf("changes: settle timeout %s is negative", cfg.SettleTimeout)
	}
	if cfg.BodyCap < 0 {
		return nil, fmt.Errorf("changes: body cap %d is negative", cfg.BodyCap)
	}
	e := &Engine{
		clock:   cfg.Clock,
		settle:  cfg.SettleTimeout,
		bodyCap: cfg.BodyCap,
		hints:   map[hintKey]hint{},
	}
	if e.clock == nil {
		e.clock = governor.SystemClock{}
	}
	if e.settle == 0 {
		e.settle = config.DefaultSettleTimeout
	}
	if e.bodyCap == 0 {
		e.bodyCap = config.DefaultChangeBodyCap
	}
	return e, nil
}

func (e *Engine) now() time.Time { return e.clock.Now() }

// SetObserver installs the observer (nil removes it).
func (e *Engine) SetObserver(o Observer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.obs = o
}

func (e *Engine) observer() Observer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.obs
}

// NoteOrigin records the origin of the refresh of (node, group) the group
// engine is about to queue; the next detection for that pair uses it once. It
// satisfies groupsvc.OriginHints.
func (e *Engine) NoteOrigin(node, group, origin string) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, h := range e.hints {
		if now.Sub(h.at) > hintTTL {
			delete(e.hints, k)
		}
	}
	e.hints[hintKey{node, group}] = hint{origin: origin, at: now}
}

// originFor returns the origin of a detection for (node, group) admitted under
// purpose, consuming a recorded hint.
func (e *Engine) originFor(node, group string, purpose governor.Purpose) string {
	now := e.now()
	e.mu.Lock()
	h, ok := e.hints[hintKey{node, group}]
	delete(e.hints, hintKey{node, group})
	e.mu.Unlock()
	if ok && now.Sub(h.at) <= hintTTL && h.origin != "" {
		return h.origin
	}
	if purpose == governor.PurposeInteractive {
		return groupsvc.OriginRead
	}
	return groupsvc.OriginRefresh
}
