package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The origins a change row can carry: where the refresh that produced it came
// from.
const (
	OriginQuery   = "query"
	OriginRefresh = "refresh"
	OriginWrite   = "write"
	OriginRead    = "read"
)

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

// RefreshInfo says how a refreshed group was fetched.
type RefreshInfo struct {
	// Started is when the fetch began.
	Started time.Time
	// Class and Purpose are what the call was admitted as.
	Class   governor.Class
	Purpose governor.Purpose
}

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

// Config configures an Engine. Every field but Clock is required.
type Config struct {
	// Spec is the connector's registration; it is validated by New.
	Spec registry.Spec
	// Classifier is the connector's change classifier.
	Classifier Classifier
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
	// SettleTimeout is how long a pending row waits for its dependents. It is
	// required and positive: the shared code has no default.
	SettleTimeout time.Duration
	// BodyCap is the byte cap of a long text value recorded in a field. It is
	// required and positive.
	BodyCap int
}

type hintKey struct{ id, group string }

type hint struct {
	origin string
	at     time.Time
}

// Engine detects changes and appends them. It is safe for concurrent use; the
// store transaction a call runs in serialises the store work.
//
// The engine appends a row for every tracked entity (Spec.Emission is read as
// registry.EmitAll: the emission gate is not part of this engine yet).
type Engine struct {
	spec    registry.Spec
	cls     Classifier
	clock   governor.Clock
	settle  time.Duration
	bodyCap int

	mu    sync.Mutex
	obs   Observer
	hints map[hintKey]hint
}

// New builds an Engine from cfg. It fails when the Spec is invalid, when the
// primary group is not store.SummaryGroupName (the store hard-codes that word),
// when there is no Classifier, or when SettleTimeout or BodyCap is not positive.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Spec.Validate(); err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	if cfg.Spec.PrimaryGroup != store.SummaryGroupName {
		return nil, fmt.Errorf("engine: primary group %q is not %q, the one name the store supports",
			cfg.Spec.PrimaryGroup, store.SummaryGroupName)
	}
	if cfg.Classifier == nil {
		return nil, errors.New("engine: no classifier")
	}
	if cfg.SettleTimeout <= 0 {
		return nil, fmt.Errorf("engine: settle timeout %s is not positive", cfg.SettleTimeout)
	}
	if cfg.BodyCap <= 0 {
		return nil, fmt.Errorf("engine: body cap %d is not positive", cfg.BodyCap)
	}
	e := &Engine{
		spec:    cfg.Spec,
		cls:     cfg.Classifier,
		clock:   cfg.Clock,
		settle:  cfg.SettleTimeout,
		bodyCap: cfg.BodyCap,
		hints:   map[hintKey]hint{},
	}
	if e.clock == nil {
		e.clock = governor.SystemClock{}
	}
	return e, nil
}

func (e *Engine) now() time.Time { return e.clock.Now() }

// fold is Spec.FoldRef, the identity when it is nil.
func (e *Engine) fold(ref string) string {
	if e.spec.FoldRef == nil {
		return ref
	}
	return e.spec.FoldRef(ref)
}

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

// NoteOrigin records the origin of the refresh of (id, group) the group
// engine is about to queue; the next detection for that pair uses it once.
func (e *Engine) NoteOrigin(id, group, origin string) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, h := range e.hints {
		if now.Sub(h.at) > hintTTL {
			delete(e.hints, k)
		}
	}
	e.hints[hintKey{id, group}] = hint{origin: origin, at: now}
}

// originFor returns the origin of a detection for (id, group) admitted under
// purpose, consuming a recorded hint.
func (e *Engine) originFor(id, group string, purpose governor.Purpose) string {
	now := e.now()
	e.mu.Lock()
	h, ok := e.hints[hintKey{id, group}]
	delete(e.hints, hintKey{id, group})
	e.mu.Unlock()
	if ok && now.Sub(h.at) <= hintTTL && h.origin != "" {
		return h.origin
	}
	if purpose == governor.PurposeInteractive {
		return OriginRead
	}
	return OriginRefresh
}
