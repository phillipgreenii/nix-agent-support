package poller

import (
	"context"
	"database/sql"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/burnrate"
	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/subshell"
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
	PRLookupFn         func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
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

	burnShort       map[string]*burnrate.Buffer
	burnLong        map[string]*burnrate.Buffer
	prevTotalTokens map[string]int

	terminalHostCache map[int]string
	// subshellCache backs the poller-side subshell count. Subshell counting stays
	// a poller/provider concern (Phase 2), so it keeps its own (path,mtime)-keyed
	// cache — without it, each tick would recount subshells (a pgrep spawn).
	subshellCache map[string]subshellCacheEntry
}

type subshellCacheEntry struct {
	path  string
	mtime time.Time
	count int
}

// SetActiveBlockID implements daemon.BlockWeekIDSetter. Called by the daemon
// main loop after upserting the active block so that the next Snapshot can
// attach per-session contributions to the correct block row.
func (p *Poller) SetActiveBlockID(id int64) { p.ActiveBlockID = id }

// UsesCorpusMonitor reports whether this poller reads its corpus projections from
// a Monitor. Always true in production (buildPoller wires one). The daemon
// lifecycle reads this to route its limits/weekly reads to the Monitor rather than
// the opts.Limits/opts.WeeklyFn fallback (which now serves only non-Monitor test
// pollers). The UseCorpusMonitor flag + the inline poller path were removed in
// pg2-66h9g; the Monitor is mandatory for Snapshot.
func (p *Poller) UsesCorpusMonitor() bool { return p.Monitor != nil }

// MonitorLimits returns the Monitor's account-global rate_limits projection (nil
// when no Monitor, e.g. a bare test poller). The daemon lifecycle reads limits
// from here so the whole-corpus walk happens once, in the Monitor.
func (p *Poller) MonitorLimits() *limits.Limits {
	if p.Monitor == nil {
		return nil
	}
	return p.Monitor.Limits()
}

// MonitorWeekly returns the Monitor's current-week cost at now (nil when no
// Monitor). The daemon lifecycle reads weekly from here.
func (p *Poller) MonitorWeekly(now time.Time) *usage.WeeklyEntry {
	if p.Monitor == nil {
		return nil
	}
	return p.Monitor.Weekly(now)
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

func (p *Poller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	now := p.Now()
	// The Monitor owns discovery + resolution + transcript/subagent tailing + the
	// UsagePricing/Limits folds; it records the "discover" phase around its own
	// Discover() call. The inline path was removed in pg2-66h9g, so a Monitor is
	// required. Production wires one explicitly (buildPoller); a poller without one
	// (tests, read-only pollers) gets a default Monitor over its own
	// SessionsDir/ClaudeHome with the standard observer set.
	if p.Monitor == nil {
		m := corpus.New(p.ClaudeHome, &session.Discoverer{SessionsDir: p.SessionsDir, PidAlive: p.PidAlive})
		m.Register(corpus.NewSessionSnapshotObserver())
		m.Register(corpus.NewSubagentErrorObserver())
		m.Register(corpus.NewUsagePricingObserver(usage.PriceTable{}))
		m.Register(corpus.NewLimitsObserver())
		m.SetPhaseRecorder(p.Rec)
		p.Monitor = m
	}
	sessions, err := p.Monitor.Scan(now)
	if err != nil {
		return nil, false, err
	}

	subshellCounter := &subshell.Counter{}

	// Lazy-init stateful maps.
	if p.burnShort == nil {
		p.burnShort = make(map[string]*burnrate.Buffer)
		p.burnLong = make(map[string]*burnrate.Buffer)
		p.prevTotalTokens = make(map[string]int)
		p.terminalHostCache = make(map[int]string)
		p.subshellCache = make(map[string]subshellCacheEntry)
	}

	enriched := map[string]aggregate.SessionEnrichment{}
	anyKeepAwake := false

	for _, s := range sessions {
		// Resolution + the transcript fold were done by Monitor.Scan (which also
		// set s.TranscriptMTime). Read the projections; subshell counting stays here
		// with its own (path,mtime) cache (Phase-2 provider).
		path, mtime, _ := p.Monitor.ResolvedPath(s.SessionID)
		snap, _ := p.Monitor.SessionSnapshot(s.SessionID)
		shells := p.countSubshellCached(subshellCounter, s.SessionID, s.PID, path, mtime)
		// git branch provider (stays in the poller; Phase 2). Independent of the
		// transcript read, so its position relative to acquisition is immaterial.
		gitBranchStart := time.Now()
		s.Branch = session.GitBranch(s.Cwd)
		if p.Rec != nil {
			p.Rec.RecordSubprocess("git_branch", time.Since(gitBranchStart))
		}

		// TerminalHost cache: detect once per PID lifetime. The cached value
		// is the bare signaler.Name() ("tmux", "cmux", "ghostty", "unknown");
		// the cmux subcase is then refined every poll against BridgeRegistry
		// (cheap, in-memory) so users see live "cmux (bridge disconnected)"
		// transitions without having to wait for the session PID to recycle.
		//
		// Special case: cached "unknown" results are re-probed each tick.
		// Detection can transiently fail (e.g. tmux pane created between
		// `ps` and `list-panes`, or the tmux server briefly absent), and
		// caching "unknown" for the PID lifetime locks in that wrong answer.
		// Re-probing is cheap: the signaler-level cache (tmuxCacheTTL=2s,
		// CmuxSignaler.surfaceCacheTTL similar) absorbs the cost across
		// sessions in a single tick.
		if host, hit := p.terminalHostCache[s.PID]; hit && host != "unknown" {
			s.TerminalHost = host
		} else {
			terminalHostStart := time.Now()
			s.TerminalHost = detectTerminalHost(p.Signalers, s.PID)
			if p.Rec != nil {
				p.Rec.RecordSubprocess("terminal_host", time.Since(terminalHostStart))
			}
			p.terminalHostCache[s.PID] = s.TerminalHost
		}
		if s.TerminalHost == "cmux" {
			s.TerminalHost = refineCmuxTerminalHost(p.Signalers, p.BridgeRegistry, s.PID)
		}

		// Burn rate: add delta (tokens generated since last poll) to ring buffers.
		prev := p.prevTotalTokens[s.SessionID]
		delta := max(snap.TotalTokens-prev, 0)
		p.prevTotalTokens[s.SessionID] = snap.TotalTokens

		winShort := p.BurnWindowShort
		if winShort == 0 {
			winShort = 60 * time.Second
		}
		winLong := p.BurnWindowLong
		if winLong == 0 {
			winLong = 300 * time.Second
		}
		if _, ok := p.burnShort[s.SessionID]; !ok {
			p.burnShort[s.SessionID] = burnrate.New(winShort)
			p.burnLong[s.SessionID] = burnrate.New(winLong)
		}
		p.burnShort[s.SessionID].Add(now, delta)
		p.burnLong[s.SessionID].Add(now, delta)

		// Drop a rate-limit reset that's already in the past beyond a small
		// grace window. The session was paused but never resumed (likely
		// abandoned); without this filter, enabling auto-resume would fire
		// real keystrokes to every dormant session.
		rlReset := snap.RateLimitResetsAt
		if !rlReset.IsZero() && now.After(rlReset.Add(stalePauseGrace)) {
			rlReset = time.Time{}
		}

		// Subagent disrupt surfacing: a stream-idle-timeout (or any disrupt)
		// inside a subagent lands only in subagents/agent-*.jsonl, which Scan
		// does not read. When the main session has no terminal error of its
		// own, surface the most recent terminal subagent error (tagged
		// FromSubagent) so it shows in the TUI. Scanned outside the transcript
		// cache because subagent files change independently of the main one.
		if path != "" &&
			(snap.LastError == nil || !snap.LastError.IsTerminal) {
			if subErr, ok := p.Monitor.SubagentError(s.SessionID); ok && subErr != nil {
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
		if !s.PidAlive {
			// Dead pid: never Working (a dead process is not actively working)
			// and never Blocked (it is gone, not waiting). Idle, refined by age.
			s.Status = session.Idle
			s.Blocker = session.NoBlocker
			s.LongIdle = session.IsLongIdle(now, s.TranscriptMTime, p.IdleThreshold)
		} else {
			lastActivity := p.Monitor.MaxActivity(s.SessionID)
			reg := claudetranscript.RegistrySession{
				Status:          s.RegistryStatus,
				WaitingFor:      s.WaitingFor,
				StatusUpdatedAt: s.StatusUpdatedAt,
			}
			verdict := claudetranscript.ClassifyActivity(reg, snap.AwaitingInput, lastActivity, p.waitingFreshWindow())
			s.Status, s.Blocker = deriveStatusBlocker(verdict.Activity, snap.LastError, rlReset)
			// Dormant → idle age refinement (ADR 0024). Only an Idle session can
			// be long-idle; Working/Blocked are never age-demoted (busy is
			// trusted; a blocker is a real, current condition).
			s.LongIdle = s.Status == session.Idle &&
				session.IsLongIdle(now, lastActivity, p.IdleThreshold)
		}
		if session.KeepAwake(s.Status, s.Blocker, snap.LastErrorRetryable) {
			anyKeepAwake = true
		}

		enriched[s.SessionID] = aggregate.SessionEnrichment{
			ContextTokens:      snap.ContextTokens,
			SessionTokens:      snap.TotalTokens,
			Model:              snap.Model,
			FirstPrompt:        snap.FirstPrompt,
			SubagentCount:      snap.SubagentCount,
			SubshellCount:      shells,
			AwaitingInput:      snap.AwaitingInput,
			RateLimitResetsAt:  rlReset,
			BurnRateShort:      p.burnShort[s.SessionID].Rate(now),
			BurnRateLong:       p.burnLong[s.SessionID].Rate(now),
			LastError:          snap.LastError,
			LastErrorRetryable: snap.LastErrorRetryable,
		}
	}

	// Prune stale burn buffers for sessions no longer alive.
	activeIDs := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		activeIDs[s.SessionID] = true
	}
	for id := range p.burnShort {
		if !activeIDs[id] {
			delete(p.burnShort, id)
			delete(p.burnLong, id)
			delete(p.prevTotalTokens, id)
			delete(p.subshellCache, id)
		}
	}
	// Prune terminalHostCache by PID (different key space from session ID).
	activePIDs := make(map[int]bool, len(sessions))
	for _, s := range sessions {
		activePIDs[s.PID] = true
	}
	for pid := range p.terminalHostCache {
		if !activePIDs[pid] {
			delete(p.terminalHostCache, pid)
		}
	}

	// Look up PRs once per directory using the same first-non-empty-branch
	// logic as aggregate.Build, ensuring the PR matches the displayed branch.
	prByDir := map[string]*session.PRInfo{}
	if p.PRLookupFn != nil {
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
			prLookupStart := time.Now()
			info, err := p.PRLookupFn(ctx, cwd, branch)
			if p.Rec != nil {
				p.Rec.RecordSubprocess("pr_lookup", time.Since(prLookupStart))
			}
			if err == nil {
				prByDir[cwd] = info
			}
		}
	}

	// PID clamp: if a PID is alive and this session has the freshest transcript
	// for that PID, clear the LongIdle age refinement (formerly the Dormant →
	// Idle clamp; ADR 0024 preserves it as idle-age). Sessions superseded by
	// /resume keep their LongIdle flag.
	pidActiveSID := make(map[int]string)
	for _, s := range sessions {
		cur, ok := pidActiveSID[s.PID]
		if !ok || s.TranscriptMTime.After(sessionMtime(sessions, cur)) {
			pidActiveSID[s.PID] = s.SessionID
		}
	}
	for _, s := range sessions {
		if s.LongIdle && s.PidAlive && pidActiveSID[s.PID] == s.SessionID {
			s.LongIdle = false
		}
	}

	var block *usage.Block
	var costProbed bool
	var costProbeErr error
	// The Monitor's UsagePricing observer already folded the in-window records
	// during Scan; Block/CostProbed are a cheap projection read priced at the same
	// `now`. The "pricer" phase timer stays (metric parity) — it now measures the
	// projection read, not the whole-corpus WalkDir.
	pricerStart := time.Now()
	block = p.Monitor.Block(now)
	costProbed, costProbeErr = p.Monitor.CostProbed()
	p.phase("pricer", pricerStart)

	aggregateBuildStart := time.Now()
	tree := aggregate.Build(sessions, enriched, prByDir, block, p.BlockCapUSD)
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

// countSubshellCached returns the subshell count for a session when delegating
// to the Monitor, reusing a cached value while the resolved transcript
// (path,mtime) is unchanged — mirroring the inline transcript cache's subshell
// reuse, so moving the corpus read into the Monitor does NOT turn subshell
// counting into a per-tick pgrep storm. Subshell stays a poller/provider concern
// (Phase 2), outside the Monitor.
func (p *Poller) countSubshellCached(counter *subshell.Counter, sessionID string, pid int, path string, mtime time.Time) int {
	if e, ok := p.subshellCache[sessionID]; ok && e.path == path && e.mtime.Equal(mtime) {
		return e.count
	}
	subshellStart := time.Now()
	n, _ := counter.Count(pid)
	if p.Rec != nil {
		p.Rec.RecordSubprocess("subshell", time.Since(subshellStart))
	}
	if path != "" {
		p.subshellCache[sessionID] = subshellCacheEntry{path: path, mtime: mtime, count: n}
	}
	return n
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

// detectTerminalHost returns the Name() of the first Signaler whose Detect returns true,
// or "unknown" if none match.
func detectTerminalHost(signalers []signal.Signaler, pid int) string {
	for _, s := range signalers {
		if s.Detect(pid) {
			return s.Name()
		}
	}
	return "unknown"
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
