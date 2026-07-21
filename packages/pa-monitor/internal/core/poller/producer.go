package poller

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// Producer is the single writer of the corpus-derived state: it owns the corpus
// Monitor (topology + observer projections) and the provider Cache, and on each
// dispatch batch it Assembles one immutable DerivedState and publishes it via an
// atomic pointer (design §3). Assemble runs on a dedicated producer goroutine at
// the ChangeSource cadence (Start); the emit tick only Loads the last published
// DerivedState.
//
// Field ownership (C5): Monitor + Providers + Signalers + BridgeRegistry are
// producer-side; the burn-rate maps stay on the Poller (tick-side).
type Producer struct {
	Monitor        *corpus.Monitor
	Providers      *provider.Cache
	Now            func() time.Time
	Signalers      []signal.Signaler
	BridgeRegistry *bridge.Registry

	// Rec receives the producer-side phase timings (discover/pricer/limits/weekly
	// re-homed off the emit tick, step 6). nil disables recording. Wired by the
	// poller's SetPhaseRecorder. discover is fired by the Monitor itself.
	Rec PhaseRecorder

	// Interval is the SLOW-tier cadence — the periodic full rescan that catches
	// newly-in-window (non-active) files. 0 defaults to 5s. FastInterval is the
	// fast-tier poll that stats the active set + status siblings for size/mtime
	// changes (low-latency); 0 defaults to 1s (capped at Interval). Ignored when a
	// ChangeSource is injected via SetChangeSource.
	Interval     time.Duration
	FastInterval time.Duration

	ref atomic.Pointer[DerivedState]

	// changes drives the loop cadence (two-tier default, or an injected source).
	changes ChangeSource

	// Lifecycle (modeled on service.WriteService): Start seeds one synchronous
	// publish then runs exactly one goroutine (startOnce); Stop cancels + joins.
	started   atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// SetChangeSource injects a ChangeSource (tests / custom transports). Must be
// called before Start.
func (pr *Producer) SetChangeSource(cs ChangeSource) { pr.changes = cs }

// ensureChangeSource builds the two-tier default from Interval/FastInterval when
// none was injected.
func (pr *Producer) ensureChangeSource() ChangeSource {
	if pr.changes != nil {
		return pr.changes
	}
	slow := pr.Interval
	if slow <= 0 {
		slow = 5 * time.Second
	}
	fast := pr.FastInterval
	if fast <= 0 || fast > slow {
		fast = min(slow, time.Second)
	}
	pr.changes = newTwoTierChangeSource(fast, slow, pr.now)
	return pr.changes
}

// Register adds an observer to the Monitor. It MUST be called before Start — the
// observer set is frozen once the producer goroutine owns the Monitor (design
// §3). Calling it after Start panics.
func (pr *Producer) Register(o corpus.Observer) {
	if pr.started.Load() {
		panic("poller: Producer.Register called after Start (observer set is frozen)")
	}
	pr.Monitor.Register(o)
}

// Start seeds one synchronous Assemble+Publish (so the first Load is non-nil
// before any goroutine runs — the tick never assembles) and then spawns the
// single producer goroutine driven by the ChangeSource. Idempotent (sync.Once):
// a second Start is a no-op.
func (pr *Producer) Start(ctx context.Context) {
	pr.startOnce.Do(func() {
		pr.started.Store(true)
		cctx, cancel := context.WithCancel(ctx)
		pr.cancel = cancel
		cs := pr.ensureChangeSource()
		// Seed BEFORE spawning so exactly one goroutine ever mutates the Monitor /
		// providers (single writer): the seed runs on the caller's goroutine with
		// no concurrent assembler, and thereafter only pr.loop assembles.
		if ds, err := pr.Assemble(cctx, pr.now()); err == nil {
			pr.Publish(ds)
			cs.SetWatch(pr.Monitor.WatchPaths())
		}
		pr.wg.Add(1)
		go pr.loop(cctx, cs)
	})
}

// Stop cancels the producer goroutine and joins it (wg.Wait). Idempotent and
// safe to call even if Start was never called.
func (pr *Producer) Stop() {
	pr.stopOnce.Do(func() {
		if pr.cancel != nil {
			pr.cancel()
		}
	})
	pr.wg.Wait()
}

// loop is the single producer goroutine: it waits for the ChangeSource to signal
// a rescan (fast-tier change or slow-tier period), Assembles + Publishes, and
// re-arms the watch set. Exactly this goroutine performs the swap once spawned.
func (pr *Producer) loop(ctx context.Context, cs ChangeSource) {
	defer pr.wg.Done()
	for {
		if !cs.WaitForChange(ctx) {
			return // ctx cancelled (Stop or parent shutdown)
		}
		if ds, err := pr.Assemble(ctx, pr.now()); err == nil {
			pr.Publish(ds)
			cs.SetWatch(pr.Monitor.WatchPaths())
		}
	}
}

// now returns the producer clock (Now, or time.Now when unset).
func (pr *Producer) now() time.Time {
	if pr.Now != nil {
		return pr.Now()
	}
	return time.Now()
}

// Publish stores ds as the current DerivedState (the atomic producer->tick
// hand-off). Exactly one goroutine calls this in async mode.
func (pr *Producer) Publish(ds *DerivedState) { pr.ref.Store(ds) }

// RepoLabelSource adapts a Producer to the detectors.Repo Cache interface
// (RepoLabel(cwd) (string, bool)): it reads the workspace.repo label from the
// most recently published DerivedState (C1). Wiring the Repo detector to this
// instead of provider.Cache keeps the tick's label pipeline off the shared
// provider Cache — the read is an atomic DerivedState Load, so it can never race
// the producer's cwd-node eviction. Before the first Publish it returns
// ("", false), so the label is simply absent for one tick.
type RepoLabelSource struct{ Prod *Producer }

// RepoLabel returns the published workspace.repo label for cwd (false = not a
// repo / not yet published).
func (s RepoLabelSource) RepoLabel(cwd string) (string, bool) {
	if s.Prod == nil {
		return "", false
	}
	return s.Prod.Load().RepoLabel(cwd)
}

// Load returns the most recently published DerivedState (nil before the first
// Publish).
func (pr *Producer) Load() *DerivedState { return pr.ref.Load() }

// Assemble runs one producer batch: it Scans the corpus (discovery + resolution
// + tailing + pricing/limits folds), reads the point-in-time provider lookups
// (subshell, git-branch, terminal-host), resolves PRs, reconciles the provider
// cache, and snapshots the block/limits/weekly projections — returning one
// immutable DerivedState priced at `now`. It performs NO burn-rate sampling,
// status derivation, or DB writes (those stay on the emit tick, buildTree).
func (pr *Producer) Assemble(ctx context.Context, now time.Time) (*DerivedState, error) {
	sessions, err := pr.Monitor.Scan(now)
	if err != nil {
		return nil, err
	}

	// BeginScan resets the per-scan PR live-key set before the per-session PR
	// calls; Reconcile (below) then prunes vanished (cwd,branch) keys.
	pr.Providers.BeginScan()

	projections := make(map[string]SessionProjection, len(sessions))
	for _, s := range sessions {
		path, mtime, ok := pr.Monitor.ResolvedPath(s.SessionID)
		snap, _ := pr.Monitor.SessionSnapshot(s.SessionID)
		shells := pr.Providers.Subshell(s.SessionID, s.PID, path, mtime)
		// Branch + terminal-host are producer-owned session fields.
		s.Branch = pr.Providers.GitBranch(s.Cwd)
		s.TerminalHost = pr.Providers.TerminalHost(s.SessionID, s.PID)
		if s.TerminalHost == "cmux" {
			s.TerminalHost = refineCmuxTerminalHost(pr.Signalers, pr.BridgeRegistry, s.PID)
		}
		subErr, _ := pr.Monitor.SubagentError(s.SessionID)
		projections[s.SessionID] = SessionProjection{
			ResolvedPath:    path,
			TranscriptMTime: mtime,
			ResolvedOK:      ok,
			Snapshot:        snap,
			MaxActivity:     pr.Monitor.MaxActivity(s.SessionID),
			SubagentError:   subErr,
			Subshells:       shells,
		}
	}

	// PR lookups once per directory, keyed by the same first-non-empty-branch
	// logic aggregate.Build uses so the PR matches the displayed branch.
	prByDir := map[string]*session.PRInfo{}
	winningBranch := map[string]string{}
	for _, s := range sessions {
		if s.Branch == "" {
			continue
		}
		if _, already := winningBranch[s.Cwd]; !already {
			winningBranch[s.Cwd] = s.Branch
		}
	}
	for cwd, branch := range winningBranch {
		if info, err := pr.Providers.PR(ctx, cwd, branch); err == nil {
			prByDir[cwd] = info
		}
	}

	// Limits/weekly are cheap projection reads (the fold happened in Scan); the
	// phase timers are re-homed here from the emit tick (step 6, §11 parity).
	limitsStart := time.Now()
	lim := pr.Monitor.Limits()
	pr.phase("limits", limitsStart)

	weeklyStart := time.Now()
	weekly := pr.Monitor.Weekly(now)
	pr.phase("weekly", weeklyStart)

	// Repo labels (C1): compute the workspace.repo label IN THE PRODUCER (it owns
	// the Cache) and publish a cwd->label map, so the tick's label/gauge pipeline
	// reads the map instead of calling provider.Cache.RepoLabel — which would
	// otherwise race the producer's cwd-node eviction. Only positive labels are
	// stored; an absent cwd is a non-repo (RepoLabel returns false).
	repoLabels := map[string]string{}
	for _, s := range sessions {
		if s.Cwd == "" {
			continue
		}
		if _, done := repoLabels[s.Cwd]; done {
			continue
		}
		if v, ok := pr.Providers.RepoLabel(s.Cwd); ok {
			repoLabels[s.Cwd] = v
		}
	}

	// Evict provider-cache nodes for vanished sessions/cwds + prune vanished PR
	// keys — once per batch, after the per-session loop + PR calls.
	pr.Providers.Reconcile(sessions)

	// pricer phase re-homed here from the emit tick (step 6): the pricing fold ran
	// in Scan; this times the block projection read (§11 parity — the phase label
	// is preserved, now fired from the producer).
	pricerStart := time.Now()
	block := pr.Monitor.Block(now)
	costProbed, costProbeErr := pr.Monitor.CostProbed()
	pr.phase("pricer", pricerStart)

	return &DerivedState{
		GeneratedAt:  now,
		Sessions:     sessions,
		Projections:  projections,
		PRByDir:      prByDir,
		RepoLabels:   repoLabels,
		Block:        block,
		CostProbed:   costProbed,
		CostProbeErr: costProbeErr,
		Limits:       lim,
		Weekly:       weekly,
	}, nil
}

// phase records a producer-side phase duration when a Rec is wired (nil-safe).
func (pr *Producer) phase(name string, start time.Time) {
	if pr.Rec != nil {
		pr.Rec.RecordPhase(name, time.Since(start))
	}
}
