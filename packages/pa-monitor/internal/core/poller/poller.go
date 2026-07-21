package poller

import (
	"context"
	"database/sql"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/provider"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// PhaseRecorder receives per-tick phase/scan/subprocess timings. *otel.Emitter
// satisfies it; nil disables recording. Defined here (not imported from otel) so
// internal/core/poller has no dependency on internal/otel.
type PhaseRecorder interface {
	RecordPhase(phase string, d time.Duration)
	RecordScan(mode string, d time.Duration, bytes int64)
	RecordSubprocess(kind string, d time.Duration)
}

// stalePauseGrace bounds how far past the rate-limit reset the TUI will still
// treat a session as paused. Beyond this, the session was likely abandoned
// during the window; auto-resume should not fire to every such session on
// toggle. 5 minutes is large enough to avoid races with the natural fire path
// and small enough that abandoned sessions are quickly cleared.
const stalePauseGrace = 5 * time.Minute

type Poller struct {
	SessionsDir string
	ClaudeHome  string
	PidAlive    func(int) bool
	PlanTier    string
	// BlockCapUSD is the per-5h-block soft cap (0 = unknown) sourced from the
	// Account at wiring time and threaded into aggregate.Build for the
	// display-layer projection. Replaces the former inline usage.PlanCapUSD
	// lookup so the poller depends only on the port/Account, not the provider.
	BlockCapUSD      float64
	WorkingThreshold time.Duration
	IdleThreshold    time.Duration
	// WaitingFreshWindow bounds the registry-"waiting" freshness cross-check
	// (claude-transcript.ClassifyActivity). Zero falls back to a small default.
	WaitingFreshWindow time.Duration
	BurnWindowShort    time.Duration
	BurnWindowLong     time.Duration
	Now                func() time.Time
	Signalers          []signal.Signaler
	// BridgeRegistry, if non-nil, refines a "cmux" TerminalHost into one of
	// "cmux" / "cmux (no bridge)" / "cmux (bridge disconnected)" based on
	// whether a cmux-bridge has registered for the session's cmux server PID.
	// Nil disables the enrichment; sessions in cmux are reported as plain
	// "cmux".
	BridgeRegistry *bridge.Registry

	// WriteService is optional. When non-nil, every tick UPSERTs all
	// discovered sessions and their current-block contributions into the DB.
	// The in-memory aggregate.Tree path also remains until Task 19 cuts over.
	WriteService *service.WriteService

	// Labeler, when non-nil, computes the label set (workspace.scope, agent.*,
	// etc.) for a session so Snapshot can PERSIST it on the session row instead
	// of nil. The daemon wires this to its label pipeline (detectors +
	// decorators); the same pipeline feeds OTEL, so the DB and OTEL agree.
	// Nil (e.g. the TUI's read-only poller, or tests) persists no labels.
	Labeler func(sv *aggregate.SessionView) map[string]string

	// DB is used to look up the surrogate session id (SELECT id FROM sessions
	// WHERE session_id = ?) so contribution rows can reference the correct
	// parent. This is intentional short-term coupling; a later refactor will
	// extract this lookup into a Service interface.
	DB *sql.DB

	// ActiveBlockID is the surrogate id of the current block in the DB.
	// Set by the daemon main loop after the cost poller upserts the block.
	// 0 means no active block yet — contributions are skipped this tick.
	ActiveBlockID int64
	ActiveWeekID  int64

	// Rec, when non-nil, receives per-tick phase/scan/subprocess timings
	// (pg2-sewtz OTel instrumentation). Wired by the daemon via
	// SetPhaseRecorder; nil (e.g. the TUI's read-only poller, or most tests)
	// disables recording.
	Rec PhaseRecorder

	// Monitor is the single owner of the corpus read (discovery, resolution,
	// transcript + subagent tailing, and the UsagePricing + Limits folds).
	// Snapshot reads all corpus projections from it (block, resolution, snapshot,
	// subagent error, maxActivity). It is REQUIRED — the old inline
	// ResolveTranscript / ScanIncremental / LastSubagentError / maxActivity / pricer
	// path was removed in pg2-66h9g. Still synchronous on the tick goroutine.
	Monitor *corpus.Monitor

	// Providers is the nested cache of point-in-time PULL lookups (git-branch,
	// subshell, terminal-host, env, repo-label, PR). Snapshot reads branch /
	// terminal-host / subshell / PR from it; env is injected into the Discoverer's
	// ReadEnv and repo-label into the label detector at the composition root. It
	// records the git_branch/subshell/terminal_host/pr_lookup subprocess metrics
	// from their new home (on miss). Lazily defaulted when nil (bare/test poller).
	// Still synchronous on the tick goroutine in Phase 2.
	Providers *provider.Cache

	// burn is the tick-side burn-rate sampler (Δtokens ring buffers +
	// prevTotalTokens). Its windows are seeded from BurnWindowShort/Long on first
	// use. Tick-owned per C5 (burn-rate is a stateful sample over published token
	// counts).
	burn burnSampler

	// producer owns the Monitor + provider Cache and assembles the immutable
	// DerivedState the emit tick reads (Phase 3). Lazily built from p.Monitor /
	// p.Providers on first Snapshot; SynchronousMode=true keeps Scan on the tick
	// until Phase 4 introduces the producer goroutine.
	producer *Producer
}

// Producer returns the poller's Producer, building it lazily from p.Monitor /
// p.Providers (defaulting both for a bare/test poller). Exposed so the daemon
// can drive the producer goroutine (Phase 4) and tests can exercise the
// assemble/publish/Load seam directly.
func (p *Poller) Producer() *Producer { return p.ensureProducer() }

// ensureProducer lazily constructs the Producer, defaulting the Monitor +
// provider Cache exactly as the pre-Phase-3 Snapshot did for a bare/test poller.
func (p *Poller) ensureProducer() *Producer {
	if p.producer != nil {
		return p.producer
	}
	if p.Monitor == nil {
		m := corpus.New(p.ClaudeHome, &session.Discoverer{SessionsDir: p.SessionsDir, PidAlive: p.PidAlive})
		m.Register(corpus.NewSessionSnapshotObserver())
		m.Register(corpus.NewSubagentErrorObserver())
		m.Register(corpus.NewUsagePricingObserver(usage.PriceTable{}))
		m.Register(corpus.NewLimitsObserver())
		m.SetPhaseRecorder(p.Rec)
		p.Monitor = m
	}
	if p.Providers == nil {
		p.Providers = provider.New(p.Now)
		p.Providers.SetRecorder(p.Rec)
	}
	p.producer = &Producer{
		Monitor:         p.Monitor,
		Providers:       p.Providers,
		Now:             p.Now,
		Signalers:       p.Signalers,
		BridgeRegistry:  p.BridgeRegistry,
		Rec:             p.Rec,
		SynchronousMode: true,
	}
	return p.producer
}

// SetActiveBlockID implements daemon.BlockWeekIDSetter. Called by the daemon
// main loop after upserting the active block so that the next Snapshot can
// attach per-session contributions to the correct block row.
func (p *Poller) SetActiveBlockID(id int64) { p.ActiveBlockID = id }

// MonitorLimits returns the account-global rate_limits projection from the last
// PUBLISHED DerivedState (C5). The daemon lifecycle reads limits from here; the
// read is an atomic Load of producer-owned state, so the emit tick never touches
// the Monitor (which the producer owns off-tick in Phase 4). Nil before the first
// publish (e.g. a bare test poller that never Snapshotted).
func (p *Poller) MonitorLimits() *limits.Limits {
	if p.producer == nil {
		return nil
	}
	ds := p.producer.Load()
	if ds == nil {
		return nil
	}
	return ds.Limits
}

// MonitorWeekly returns the current-week cost from the last PUBLISHED
// DerivedState (C5). The `now` argument is retained for signature stability (the
// weekly value is priced at the producer's GeneratedAt); the WeeklyEvery
// DB-upsert cadence gate stays on the tick (lifecycle.go). Nil before the first
// publish.
func (p *Poller) MonitorWeekly(now time.Time) *usage.WeeklyEntry {
	if p.producer == nil {
		return nil
	}
	ds := p.producer.Load()
	if ds == nil {
		return nil
	}
	return ds.Weekly
}

// SetActiveWeekID implements daemon.BlockWeekIDSetter.
func (p *Poller) SetActiveWeekID(id int64) { p.ActiveWeekID = id }

// SetLabeler implements daemon.SessionLabeler. The daemon wires its label
// pipeline here so Snapshot persists each session's computed labels (pg2-4xbrm).
func (p *Poller) SetLabeler(fn func(sv *aggregate.SessionView) map[string]string) { p.Labeler = fn }

// SetPhaseRecorder wires a PhaseRecorder. Takes any so the daemon can call it
// through an anonymous interface without importing this package. It also fans
// the recorder out to the Monitor (when delegating), so the Monitor's scan
// metrics (transcript.scan.duration modes + the discover phase) reach the same
// emitter from their new home — the daemon keeps a single wiring site.
func (p *Poller) SetPhaseRecorder(r any) {
	if pr, ok := r.(PhaseRecorder); ok {
		p.Rec = pr
	}
	if p.Monitor != nil {
		p.Monitor.SetPhaseRecorder(r)
	}
	if p.Providers != nil {
		p.Providers.SetRecorder(r)
	}
	if p.producer != nil {
		p.producer.Rec = p.Rec
	}
}

// phase records a once-per-tick phase duration when a Rec is wired, keeping
// call sites terse (`defer p.phase("name", time.Now())` is NOT used here since
// that would capture time.Now() eagerly; call sites instead do
// `t0 := time.Now(); ...; p.phase("name", t0)`).
func (p *Poller) phase(name string, start time.Time) {
	if p.Rec != nil {
		p.Rec.RecordPhase(name, time.Since(start))
	}
}

// Snapshot builds the per-tick aggregate.Tree. It obtains one immutable
// DerivedState from the Producer — assembled + published inline in
// SynchronousMode (Scan on the tick), or Loaded from the last producer publish
// otherwise (Phase 4) — then builds the tree from it (buildTree). The producer /
// tick split is field-owned per C5: Monitor + providers are producer-side; the
// burn-rate maps + prevTotalTokens are tick-side.
func (p *Poller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	now := p.Now()
	prod := p.ensureProducer()

	var ds *DerivedState
	if prod.SynchronousMode {
		var err error
		ds, err = prod.Assemble(ctx, now)
		if err != nil {
			return nil, false, err
		}
		prod.Publish(ds)
	} else {
		// Async: the producer goroutine owns Assemble+Publish. The tick ONLY
		// Loads — it must NEVER assemble here, or it would race the goroutine's
		// concurrent Assemble (two writers of the Monitor/providers). Producer.
		// Start seeds one publish before spawning, so ds is non-nil by the first
		// tick; a nil Load is a brief pre-seed window → emit an empty tree.
		ds = prod.Load()
		if ds == nil {
			return aggregate.Build(nil, nil, nil, nil, p.BlockCapUSD), false, nil
		}
	}

	return p.buildTree(ctx, ds, now)
}

// StartProducer switches the poller to the decoupled (async) mode and starts the
// single producer goroutine: it owns Monitor+provider assembly and publishes the
// DerivedState the emit tick Loads. The emit tick becomes a thin reader. Wired by
// the daemon (RunWith); the read-only TUI poller and tests that never call this
// stay synchronous (SynchronousMode=true).
func (p *Poller) StartProducer(ctx context.Context) {
	prod := p.ensureProducer()
	prod.SynchronousMode = false
	prod.Start(ctx)
}

// StopProducer stops + joins the producer goroutine (no-op if never started).
func (p *Poller) StopProducer() {
	if p.producer != nil {
		p.producer.Stop()
	}
}

// buildTree is the emit-tick reader: it Loads no state itself but takes an
// already-assembled DerivedState and derives the tree — burn-rate sampling
// (tick-stateful), Status/Blocker/LongIdle derivation, aggregate.Build, and the
// DB upserts. It NEVER touches the Monitor or the providers, and never mutates
// the published DerivedState (it derives Status into tick-owned session copies).
func (p *Poller) buildTree(ctx context.Context, ds *DerivedState, now time.Time) (*aggregate.Tree, bool, error) {
	sessions := ds.Sessions

	// Seed the burn-sampler windows from config (idempotent).
	p.burn.winShort = p.BurnWindowShort
	p.burn.winLong = p.BurnWindowLong

	enriched := map[string]aggregate.SessionEnrichment{}
	anyKeepAwake := false

	// built holds tick-owned copies of the producer's sessions; Status/Blocker/
	// LongIdle are derived onto the copies so the published DerivedState stays
	// immutable (load-bearing once the producer runs off the tick, Phase 4).
	built := make([]*session.Session, 0, len(sessions))

	for _, s := range sessions {
		proj := ds.Projections[s.SessionID]
		path := proj.ResolvedPath
		// snap is a local value copy of the projection's Snapshot — the resurfacing
		// below mutates it without touching the published DerivedState.
		snap := proj.Snapshot
		shells := proj.Subshells

		sc := *s
		built = append(built, &sc)

		// Burn rate: add the Δ since last poll to the tick-owned ring buffers.
		burnShort, burnLong := p.burn.sample(sc.SessionID, now, snap.TotalTokens)

		// Drop a rate-limit reset that's already in the past beyond a small
		// grace window. The session was paused but never resumed (likely
		// abandoned); without this filter, enabling auto-resume would fire
		// real keystrokes to every dormant session.
		rlReset := snap.RateLimitResetsAt
		if !rlReset.IsZero() && now.After(rlReset.Add(stalePauseGrace)) {
			rlReset = time.Time{}
		}

		// Subagent disrupt surfacing: a stream-idle-timeout (or any disrupt)
		// inside a subagent lands only in subagents/agent-*.jsonl. When the main
		// session has no terminal error of its own, surface the most recent
		// terminal subagent error (tagged FromSubagent) so it shows in the TUI.
		// The producer captured it as proj.SubagentError.
		if path != "" &&
			(snap.LastError == nil || !snap.LastError.IsTerminal) {
			if subErr := proj.SubagentError; subErr != nil {
				e := *subErr
				snap.LastError = &e
				// LastError was replaced by a subagent error; re-derive the
				// auto-resume verdict from the (possibly different) record.
				snap.LastErrorRetryable = transcript.Retryable(snap.LastError)
			}
		}

		// Status + blocker derivation (ADR 0024 D1). Registry-driven activity
		// verdict (§4.2/§4.3) supplies the "has work" question — busy is TRUSTED
		// and never demoted on transcript staleness — but a CURRENT terminal
		// blocking condition (auth 401 / usage-limit / other terminal error, or a
		// still-in-future rate-limit reset) OVERRIDES busy to Blocked + the
		// appropriate blocker (deriveStatusBlocker). Subagent mtime is load-
		// bearing ONLY for the "waiting"-freshness cross-check and the LongIdle
		// age refinement.
		if !sc.PidAlive {
			// Dead pid: never Working (a dead process is not actively working)
			// and never Blocked (it is gone, not waiting). Idle, refined by age.
			sc.Status = session.Idle
			sc.Blocker = session.NoBlocker
			sc.LongIdle = session.IsLongIdle(now, sc.TranscriptMTime, p.IdleThreshold)
		} else {
			lastActivity := proj.MaxActivity
			reg := claudetranscript.RegistrySession{
				Status:          sc.RegistryStatus,
				WaitingFor:      sc.WaitingFor,
				StatusUpdatedAt: sc.StatusUpdatedAt,
			}
			verdict := claudetranscript.ClassifyActivity(reg, snap.AwaitingInput, lastActivity, p.waitingFreshWindow())
			sc.Status, sc.Blocker = deriveStatusBlocker(verdict.Activity, snap.LastError, rlReset)
			// Dormant → idle age refinement (ADR 0024). Only an Idle session can
			// be long-idle; Working/Blocked are never age-demoted (busy is
			// trusted; a blocker is a real, current condition).
			sc.LongIdle = sc.Status == session.Idle &&
				session.IsLongIdle(now, lastActivity, p.IdleThreshold)
		}
		if session.KeepAwake(sc.Status, sc.Blocker, snap.LastErrorRetryable) {
			anyKeepAwake = true
		}

		enriched[sc.SessionID] = aggregate.SessionEnrichment{
			ContextTokens:      snap.ContextTokens,
			SessionTokens:      snap.TotalTokens,
			Model:              snap.Model,
			FirstPrompt:        snap.FirstPrompt,
			SubagentCount:      snap.SubagentCount,
			SubshellCount:      shells,
			AwaitingInput:      snap.AwaitingInput,
			RateLimitResetsAt:  rlReset,
			BurnRateShort:      burnShort,
			BurnRateLong:       burnLong,
			LastError:          snap.LastError,
			LastErrorRetryable: snap.LastErrorRetryable,
		}
	}

	// Prune stale burn buffers for sessions no longer alive. Provider-cache
	// eviction is the producer's Reconcile (in Assemble).
	activeIDs := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		activeIDs[s.SessionID] = true
	}
	p.burn.prune(activeIDs)

	// PR lookups were resolved by the producer; read them back.
	prByDir := ds.PRByDir

	// PID clamp: if a PID is alive and this session has the freshest transcript
	// for that PID, clear the LongIdle age refinement (formerly the Dormant →
	// Idle clamp; ADR 0024 preserves it as idle-age). Sessions superseded by
	// /resume keep their LongIdle flag. Operates over the tick-owned copies.
	pidActiveSID := make(map[int]string)
	for _, s := range built {
		cur, ok := pidActiveSID[s.PID]
		if !ok || s.TranscriptMTime.After(sessionMtime(built, cur)) {
			pidActiveSID[s.PID] = s.SessionID
		}
	}
	for _, s := range built {
		if s.LongIdle && s.PidAlive && pidActiveSID[s.PID] == s.SessionID {
			s.LongIdle = false
		}
	}

	// Block/CostProbed are read back from the DerivedState (the producer folded
	// them and fired the "pricer" phase off-tick, step 6).
	block := ds.Block
	costProbed, costProbeErr := ds.CostProbed, ds.CostProbeErr

	aggregateBuildStart := time.Now()
	tree := aggregate.Build(built, enriched, prByDir, block, p.BlockCapUSD)
	p.phase("aggregate_build", aggregateBuildStart)
	tree.CostProbed = costProbed
	tree.CostProbeErr = costProbeErr

	if p.WriteService != nil {
		dbWriteSessionsStart := time.Now()
		nowUTC := now.UTC()
		for _, sv := range tree.Sessions() {
			var svLabels map[string]string
			if p.Labeler != nil {
				svLabels = p.Labeler(sv)
			}
			ss := store.Session{
				SessionID:       sv.SessionID,
				PID:             pidPtrIfAlive(sv.PID, sv.Session),
				Cwd:             sv.Cwd,
				Name:            sv.Name,
				Kind:            sv.Kind,
				Entrypoint:      sv.Entrypoint,
				Model:           sv.Model,
				TerminalHost:    sv.TerminalHost,
				Branch:          sv.Branch,
				Status:          sv.Status.String(),
				Blocker:         sv.Blocker.String(),
				FirstPrompt:     sv.FirstPrompt,
				Labels:          svLabels, // computed by p.Labeler (daemon label pipeline); nil when unwired
				TranscriptMTime: sv.TranscriptMTime,
				StartedAt:       sv.StartedAt,
				ContextTokens:   uint64(sv.ContextTokens),
				SessionTokens:   uint64(sv.SessionTokens),
				SubagentCount:   uint32(sv.SubagentCount),
				SubshellCount:   uint32(sv.SubshellCount),
				BurnRateShort:   sv.BurnRateShort,
				BurnRateLong:    sv.BurnRateLong,
				CostUSD:         sv.CostUSD,
				AwaitingInput:   sv.AwaitingInput,
				LastProcessedAt: nowUTC,
				UpdatedAt:       nowUTC,
			}
			// fold LastError if present
			if sv.LastError != nil {
				le := sv.LastError
				ss.LastErrorKind = string(le.Kind)
				ss.LastErrorText = le.Text
				ss.LastErrorAt = le.At
				ss.LastErrorTerminal = le.IsTerminal
				ss.LastErrorRetryable = sv.LastErrorRetryable
				ss.LastErrorFromSubagent = le.FromSubagent
			}
			// Best-effort write — DB failures must not abort the tick.
			_ = p.WriteService.UpsertSession(ctx, ss)

			if p.DB != nil && (p.ActiveBlockID > 0 || p.ActiveWeekID > 0) {
				var sessRowID int64
				if err := p.DB.QueryRowContext(ctx,
					"SELECT id FROM sessions WHERE session_id = ?", sv.SessionID).Scan(&sessRowID); err == nil {
					if p.ActiveBlockID > 0 {
						_ = p.WriteService.UpsertBlockContribution(ctx, store.Contribution{
							SessionID: sessRowID, ParentID: p.ActiveBlockID,
							CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionTokens), UpdatedAt: nowUTC,
						})
					}
					if p.ActiveWeekID > 0 {
						_ = p.WriteService.UpsertWeekContribution(ctx, store.Contribution{
							SessionID: sessRowID, ParentID: p.ActiveWeekID,
							CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionTokens), UpdatedAt: nowUTC,
						})
					}
				}
			}
		}
		p.phase("db_write_sessions", dbWriteSessionsStart)
	}

	return tree, anyKeepAwake, nil
}

// deriveStatusBlocker maps the registry activity verdict plus the current
// per-session terminal blocking signals to the observable (Status, Blocker)
// pair (ADR 0024 D1 / R2). A CURRENT terminal blocking condition overrides the
// activity verdict (including busy → Working); "current" is encoded by
// LastError.IsTerminal (the snapshot tail-walk clears IsTerminal once newer
// user/assistant activity supersedes the error). usage_limit is derived from
// per-session inputs ONLY (a terminal ErrRateLimit, or a still-relevant
// RateLimitResetsAt) — NEVER the account-global FiveHourPct.
//
// A FromSubagent terminal error MUST NOT override the parent's status: it is
// surfaced only for display (poller resurfaces the newest subagent error when
// the main session has none of its own), and a subagent's one-shot
// agent-*.jsonl file never receives superseding activity, so its IsTerminal
// stays true for the life of the parent process — treating it as a blocking
// condition would pin an alive, working/idle parent to Blocked forever (and
// hold the Mac awake for a retryable disrupt that is never nudged). This
// mirrors the nudge/keep-awake path, which already excludes FromSubagent
// (see hasUnattemptedNudgeableDisrupt and the disrupt producer).
//
// Precedence (highest first): terminal auth 401 → human_authn; terminal
// rate-limit or live reset → usage_limit; any other terminal error → error;
// then the activity verdict (waiting → human_input; active → working; else
// idle). A terminal error therefore wins over a registry "waiting" flag: an
// API error is the concrete "cannot proceed on its own" signal.
func deriveStatusBlocker(act claudetranscript.Activity, lastErr *transcript.ErrorRecord, rlReset time.Time) (session.Status, session.Blocker) {
	terminal := lastErr != nil && lastErr.IsTerminal && !lastErr.FromSubagent
	switch {
	case terminal && lastErr.Kind == transcript.ErrAuthFailed:
		return session.Blocked, session.HumanAuthn
	case terminal && lastErr.Kind == transcript.ErrRateLimit:
		return session.Blocked, session.UsageLimit
	case !rlReset.IsZero():
		return session.Blocked, session.UsageLimit
	case terminal:
		return session.Blocked, session.ErrorBlocker
	case act == claudetranscript.WaitingForHuman:
		return session.Blocked, session.HumanInput
	case act == claudetranscript.Active:
		return session.Working, session.NoBlocker
	default:
		return session.Idle, session.NoBlocker
	}
}

// waitingFreshWindow returns the configured WaitingFreshWindow, falling back
// to 2*WorkingThreshold (or 60s when neither is set).
func (p *Poller) waitingFreshWindow() time.Duration {
	if p.WaitingFreshWindow > 0 {
		return p.WaitingFreshWindow
	}
	if p.WorkingThreshold > 0 {
		return 2 * p.WorkingThreshold
	}
	return 60 * time.Second
}

// sessionMtime returns the TranscriptMTime of the session with the given ID,
// or zero time if not found. Used for PID-active-session heuristic.
func sessionMtime(sessions []*session.Session, id string) time.Time {
	for _, s := range sessions {
		if s.SessionID == id {
			return s.TranscriptMTime
		}
	}
	return time.Time{}
}

// pidPtrIfAlive returns a pointer to pid when the session's PID is alive,
// nil otherwise. Used to write a NULL pid to the DB for dead processes.
func pidPtrIfAlive(pid int, s *session.Session) *int {
	if s != nil && !s.PidAlive {
		return nil
	}
	p := pid
	return &p
}

// refineCmuxTerminalHost takes a "cmux" detection and refines it against the
// bridge registry, returning one of:
//   - "cmux" — a bridge is registered and recently seen
//   - "cmux (bridge disconnected)" — a bridge was registered but is stale
//   - "cmux (no bridge)" — no bridge has ever registered for this server
//
// If br is nil or the CmuxSignaler cannot resolve a server ancestor (e.g.
// because the ps cache expired between Detect and this call), falls back to
// the bare "cmux" string — never worse than the pre-refinement value.
func refineCmuxTerminalHost(signalers []signal.Signaler, br *bridge.Registry, pid int) string {
	if br == nil {
		return "cmux"
	}
	for _, s := range signalers {
		cs, ok := s.(*signal.CmuxSignaler)
		if !ok {
			continue
		}
		serverPID, ok := cs.FindCmuxServerAncestor(pid)
		if !ok {
			return "cmux"
		}
		switch br.StatusForServer(serverPID) {
		case bridge.Alive:
			return "cmux"
		case bridge.Stale:
			return "cmux (bridge disconnected)"
		case bridge.Unknown:
			return "cmux (no bridge)"
		}
	}
	return "cmux"
}
