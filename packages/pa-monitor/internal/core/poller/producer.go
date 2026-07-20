package poller

import (
	"context"
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
// atomic pointer (design §3). Phase 4 runs Assemble on a dedicated goroutine at
// the ChangeSource cadence; through Phase 1-3 the emit tick drives it inline
// (SynchronousMode).
//
// Field ownership (C5): Monitor + Providers + Signalers + BridgeRegistry are
// producer-side; the burn-rate maps stay on the Poller (tick-side).
type Producer struct {
	Monitor        *corpus.Monitor
	Providers      *provider.Cache
	Now            func() time.Time
	Signalers      []signal.Signaler
	BridgeRegistry *bridge.Registry

	// SynchronousMode drives the equivalence gate (C2). When true (Phase 1-3 and
	// the default), the emit tick Assembles + publishes inline before reading the
	// state back — Scan-on-tick. When false (Phase 4), a producer goroutine owns
	// Assemble+publish and the tick only Loads the last published DerivedState.
	SynchronousMode bool

	ref atomic.Pointer[DerivedState]
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

	block := pr.Monitor.Block(now)
	costProbed, costProbeErr := pr.Monitor.CostProbed()

	return &DerivedState{
		GeneratedAt:  now,
		Sessions:     sessions,
		Projections:  projections,
		PRByDir:      prByDir,
		RepoLabels:   repoLabels,
		Block:        block,
		CostProbed:   costProbed,
		CostProbeErr: costProbeErr,
		Limits:       pr.Monitor.Limits(),
		Weekly:       pr.Monitor.Weekly(now),
	}, nil
}
