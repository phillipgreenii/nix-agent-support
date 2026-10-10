package daemon

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// compose builds the engine's observer: the catalog, the log lines, an extra
// observer when one is set, and a guard around all of them. The engine calls
// an observer while it serializes writes, and a panic in one is re-raised
// after the commit's remaining notifications are skipped, so a client push for
// that version could be missed; the guard keeps any defect in a callback
// inside the daemon (operator input 4 of the sub-project 1 hand-over).
func (d *Daemon) compose() engine.Observer {
	m := d.metrics.Observer(func(method string, r any) {
		d.log.Error("metrics observer panicked", "method", method, "panic", fmt.Sprint(r))
	})
	return guarded{inner: &logObserver{EngineObserver: m, d: d}, d: d}
}

// logObserver adds the startup log lines and the extra observer to the
// catalog's.
type logObserver struct {
	*obs.EngineObserver
	d *Daemon
}

func (o *logObserver) Replayed(n int, dur time.Duration) {
	o.EngineObserver.Replayed(n, dur)
	o.d.log.Info("replay end", "events", n, "duration_ms", float64(dur.Microseconds())/1000)
	o.d.mu.Lock()
	o.d.replay = server.ReplayInfo{Events: n, Duration: dur}
	o.d.mu.Unlock()
	if x := o.d.p.ExtraObserver; x != nil {
		x.Replayed(n, dur)
	}
}

func (o *logObserver) Recovered(r store.Recovery) {
	o.EngineObserver.Recovered(r)
	if r.TornTail || r.UncommittedBatches > 0 {
		o.d.log.Warn("recovered the end of the log", "torn_tail", r.TornTail, "uncommitted_batches", r.UncommittedBatches,
			"truncated_bytes", r.TruncatedBytes, "sidecar", r.Sidecar)
	}
	o.d.mu.Lock()
	o.d.recovery = wire.HealthRecover{TornTail: r.TornTail, UncommittedBatches: r.UncommittedBatches}
	o.d.mu.Unlock()
	if x := o.d.p.ExtraObserver; x != nil {
		x.Recovered(r)
	}
}

func (o *logObserver) Appended(t event.Type, s store.AppendStats) {
	o.EngineObserver.Appended(t, s)
	if x := o.d.p.ExtraObserver; x != nil {
		x.Appended(t, s)
	}
}

func (o *logObserver) AppendFailed(stage string) {
	o.EngineObserver.AppendFailed(stage)
	o.d.log.Error("append failed", "stage", stage)
	if x := o.d.p.ExtraObserver; x != nil {
		x.AppendFailed(stage)
	}
}

func (o *logObserver) Rejected(r command.Reason) {
	o.EngineObserver.Rejected(r)
	if x := o.d.p.ExtraObserver; x != nil {
		x.Rejected(r)
	}
}

func (o *logObserver) Corrected(kind string) {
	o.EngineObserver.Corrected(kind)
	if x := o.d.p.ExtraObserver; x != nil {
		x.Corrected(kind)
	}
}

// guarded recovers a panic in any method of the observer it wraps and logs it.
type guarded struct {
	inner engine.Observer
	d     *Daemon
}

func (g guarded) guard(method string) {
	if r := recover(); r != nil {
		g.d.log.Error("engine observer panicked", "method", method, "panic", fmt.Sprint(r))
	}
}

func (g guarded) Replayed(n int, dur time.Duration) {
	defer g.guard("Replayed")
	g.inner.Replayed(n, dur)
}

func (g guarded) Recovered(r store.Recovery) {
	defer g.guard("Recovered")
	g.inner.Recovered(r)
}

func (g guarded) Appended(t event.Type, s store.AppendStats) {
	defer g.guard("Appended")
	g.inner.Appended(t, s)
}

func (g guarded) AppendFailed(stage string) {
	defer g.guard("AppendFailed")
	g.inner.AppendFailed(stage)
}

func (g guarded) Rejected(r command.Reason) {
	defer g.guard("Rejected")
	g.inner.Rejected(r)
}

func (g guarded) Corrected(kind string) {
	defer g.guard("Corrected")
	g.inner.Corrected(kind)
}
