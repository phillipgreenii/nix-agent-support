package engine

import (
	"slices"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// Observer is told what the engine does, for logs and metrics. Its methods
// are called while the engine serializes writes, so they MUST be quick and
// MUST NOT call back into the engine's write path (Do, SetConfig, Close).
// None of them is ever given free text.
type Observer interface {
	// Replayed is called once by Open with the number of committed events
	// replayed and how long the replay took.
	Replayed(n int, d time.Duration)
	// Recovered is called once by Open, before Replayed, with what the store
	// did to the end of the log; the zero value means nothing was recovered.
	Recovered(store.Recovery)
	// Appended is called once per appended event, in log order, with the
	// event's type, after the new state is adopted and before OnCommit, so a
	// panic in it leaves the engine consistent (see Engine.Do). The stats of
	// the append are split across its calls: each call carries Events 1 and
	// the event's own Bytes, and only the first call of an append (the first
	// event of a batch) carries the append's Duration; the later ones carry
	// zero, so summing the calls of one append gives its totals. A daemon that
	// records the append latency MUST observe Duration only when it is
	// non-zero, or every later event of a batch counts as a zero-length append.
	Appended(t event.Type, s store.AppendStats)
	// AppendFailed is called when a request reached the store and failed
	// there: "write" (the write failed), "fsync" (its sync failed) or
	// "project" (the events are durable but the new state could not be
	// adopted).
	AppendFailed(stage string)
	// Rejected is called once for every refusal Do returns, with its reason,
	// the engine's own id_conflict and store_unavailable included. A no-op,
	// a replayed retry and a dry run's preview are not refusals.
	Rejected(r command.Reason)
	// Corrected is called after a correction ("correct") or a retraction
	// ("retract") is committed.
	Corrected(kind string)
}

// The stages AppendFailed names.
const (
	stageWrite   = "write"
	stageFsync   = "fsync"
	stageProject = "project"
)

// The kinds Corrected names.
const (
	kindCorrect = "correct"
	kindRetract = "retract"
)

// nopObserver is the Observer of an engine opened without one.
type nopObserver struct{}

func (nopObserver) Replayed(int, time.Duration)            {}
func (nopObserver) Recovered(store.Recovery)               {}
func (nopObserver) Appended(event.Type, store.AppendStats) {}
func (nopObserver) AppendFailed(string)                    {}
func (nopObserver) Rejected(command.Reason)                {}
func (nopObserver) Corrected(string)                       {}

// callbacks holds the functions registered with OnCommit and OnHealthChange.
// Registration may happen from any goroutine; the engine calls them, in the
// order they were registered, while it serializes writes.
type callbacks struct {
	mu       sync.Mutex
	onCommit []func(Version)
	onHealth []func(Health)
}

func (c *callbacks) addCommit(f func(Version)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onCommit = append(c.onCommit, f)
}

func (c *callbacks) addHealth(f func(Health)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onHealth = append(c.onHealth, f)
}

func (c *callbacks) commit(v Version) {
	c.mu.Lock()
	fs := slices.Clone(c.onCommit)
	c.mu.Unlock()
	for _, f := range fs {
		f(v)
	}
}

func (c *callbacks) health(h Health) {
	c.mu.Lock()
	fs := slices.Clone(c.onHealth)
	c.mu.Unlock()
	for _, f := range fs {
		f(h)
	}
}

// OnCommit registers f, which is called with the new state version after each
// successful commit and after each successful reload (SetConfig); never for a
// no-op, a dry run or a refusal. f runs while the engine serializes writes,
// after the new state is adopted, so Snapshot, State and Version already show
// it; f MUST NOT call Do, SetConfig or Close.
func (e *Engine) OnCommit(f func(Version)) { e.callbacks.addCommit(f) }

// OnHealthChange registers f, which is called with the store's health when
// the store enters read-only mode (after a failed append or an adoption
// failure), and again should the reported health ever change; never for a
// healthy store. f runs while the engine serializes writes and MUST NOT call
// Do, SetConfig or Close.
func (e *Engine) OnHealthChange(f func(Health)) { e.callbacks.addHealth(f) }
