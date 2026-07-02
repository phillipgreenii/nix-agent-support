package poller

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/bridge"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/burnrate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/subshell"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// stalePauseGrace bounds how far past the rate-limit reset the TUI will still
// treat a session as paused. Beyond this, the session was likely abandoned
// during the window; auto-resume should not fire to every such session on
// toggle. 5 minutes is large enough to avoid races with the natural fire path
// and small enough that abandoned sessions are quickly cleared.
const stalePauseGrace = 5 * time.Minute

type cachedTranscript struct {
	path          string
	mtime         time.Time
	snap          transcript.Snapshot
	subshellCount int
}

type Poller struct {
	SessionsDir string
	ClaudeHome  string
	PidAlive    func(int) bool
	PlanTier    string
	// BlockCapUSD is the per-5h-block soft cap (0 = unknown) sourced from the
	// Account at wiring time and threaded into aggregate.Build for the
	// display-layer projection. Replaces the former inline ccusage.PlanCapUSD
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
	// Pricer is the CostPricer port supplying the active 5h block cost and its
	// probe state. Nil disables cost folding (the block is simply absent). The
	// ccusage adapter is its production implementation; tests inject a fake.
	Pricer     CostPricer
	PRLookupFn func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
	Signalers  []signal.Signaler
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

	// DB is used to look up the surrogate session id (SELECT id FROM sessions
	// WHERE session_id = ?) so contribution rows can reference the correct
	// parent. This is intentional short-term coupling; a later refactor will
	// extract this lookup into a Service interface.
	DB *sql.DB

	// ActiveBlockID is the surrogate id of the current block in the DB.
	// Set by the daemon main loop after ccusage poller upserts the block.
	// 0 means no active block yet — contributions are skipped this tick.
	ActiveBlockID int64
	ActiveWeekID  int64

	burnShort       map[string]*burnrate.Buffer
	burnLong        map[string]*burnrate.Buffer
	prevTotalTokens map[string]int

	terminalHostCache map[int]string
	transcriptCache   map[string]cachedTranscript
}

// SetActiveBlockID implements daemon.BlockWeekIDSetter. Called by the daemon
// main loop after upserting the active block so that the next Snapshot can
// attach per-session contributions to the correct block row.
func (p *Poller) SetActiveBlockID(id int64) { p.ActiveBlockID = id }

// SetActiveWeekID implements daemon.BlockWeekIDSetter.
func (p *Poller) SetActiveWeekID(id int64) { p.ActiveWeekID = id }

func (p *Poller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
	now := p.Now()
	disc := &session.Discoverer{SessionsDir: p.SessionsDir, PidAlive: p.PidAlive}
	sessions, err := disc.Discover()
	if err != nil {
		return nil, false, err
	}

	subshellCounter := &subshell.Counter{}

	// Lazy-init stateful maps.
	if p.burnShort == nil {
		p.burnShort = make(map[string]*burnrate.Buffer)
		p.burnLong = make(map[string]*burnrate.Buffer)
		p.prevTotalTokens = make(map[string]int)
		p.transcriptCache = make(map[string]cachedTranscript)
		p.terminalHostCache = make(map[int]string)
	}

	enriched := map[string]aggregate.SessionEnrichment{}
	anyWorking := false

	for _, s := range sessions {
		path, mtime, ok := session.ResolveTranscript(p.ClaudeHome, s)
		if ok {
			s.TranscriptMTime = mtime
		}
		s.Branch = session.GitBranch(s.Cwd)

		// Transcript cache: re-read only when path or mtime changed.
		var snap transcript.Snapshot
		var shells int
		if cached, hit := p.transcriptCache[s.SessionID]; hit &&
			path != "" && cached.path == path && cached.mtime.Equal(mtime) {
			snap = cached.snap
			shells = cached.subshellCount
		} else {
			snap, _ = transcript.Scan(path)
			shells, _ = subshellCounter.Count(s.PID)
			if path != "" {
				p.transcriptCache[s.SessionID] = cachedTranscript{
					path: path, mtime: mtime, snap: snap, subshellCount: shells,
				}
			}
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
			s.TerminalHost = detectTerminalHost(p.Signalers, s.PID)
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
			if subErr, ok := transcript.LastSubagentError(path); ok {
				e := subErr
				snap.LastError = &e
				// LastError was replaced by a subagent error; re-derive the
				// auto-resume verdict from the (possibly different) record.
				snap.LastErrorRetryable = transcript.Retryable(snap.LastError)
			}
		}

		// Registry-driven activity verdict (§4.2/§4.3). Supersedes the old
		// mtime-only Classify as the PRIMARY signal. busy is TRUSTED and never
		// demoted on transcript staleness — that demotion is what reintroduced
		// the incident bug. Subagent mtime is load-bearing ONLY for the
		// "waiting"-freshness cross-check and the display/age (dormant) bucket.
		if !s.PidAlive {
			// Dead pid: keep last-known (the poller persists state until GC).
			// Fall back to the mtime age bucket, but never report Working — a
			// dead process is not actively working.
			s.Status = session.Classify(now, s.TranscriptMTime, p.WorkingThreshold, p.IdleThreshold)
			if s.Status == session.Working {
				s.Status = session.Idle
			}
		} else {
			lastActivity := maxActivity(s.TranscriptMTime, path)
			reg := claudetranscript.RegistrySession{
				Status:          s.RegistryStatus,
				WaitingFor:      s.WaitingFor,
				StatusUpdatedAt: s.StatusUpdatedAt,
			}
			verdict := claudetranscript.ClassifyActivity(reg, snap.AwaitingInput, lastActivity, p.waitingFreshWindow())
			switch verdict.Activity {
			case claudetranscript.Active:
				s.Status = session.Working
			case claudetranscript.WaitingForHuman:
				s.Status = session.WaitingForHuman
			default:
				s.Status = session.Idle
			}
			// Display-only age bucket, orthogonal to the verdict: an Idle
			// session whose last activity is older than IdleThreshold renders
			// as Dormant. Working/WaitingForHuman are NOT demoted (busy is
			// trusted; a fresh wait is a real wait).
			if s.Status == session.Idle && !lastActivity.IsZero() &&
				now.Sub(lastActivity) > p.IdleThreshold {
				s.Status = session.Dormant
			}
		}
		if s.Status == session.Working {
			anyWorking = true
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
			delete(p.transcriptCache, id)
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
			if info, err := p.PRLookupFn(ctx, cwd, branch); err == nil {
				prByDir[cwd] = info
			}
		}
	}

	// PID clamp: if a PID is alive and this session has the freshest transcript
	// for that PID, clamp Dormant → Idle. Sessions superseded by /resume stay Dormant.
	pidActiveSID := make(map[int]string)
	for _, s := range sessions {
		cur, ok := pidActiveSID[s.PID]
		if !ok || s.TranscriptMTime.After(sessionMtime(sessions, cur)) {
			pidActiveSID[s.PID] = s.SessionID
		}
	}
	for _, s := range sessions {
		if s.Status == session.Dormant && s.PidAlive && pidActiveSID[s.PID] == s.SessionID {
			s.Status = session.Idle
		}
	}

	var block *ccusage.Block
	var ccUsageProbed bool
	var ccUsageErr error
	if p.Pricer != nil {
		block, _ = p.Pricer.ActiveBlock(ctx)
		ccUsageProbed, ccUsageErr = p.Pricer.Probed()
	}

	tree := aggregate.Build(sessions, enriched, prByDir, block, p.BlockCapUSD)
	tree.CCUsageProbed = ccUsageProbed
	tree.CCUsageErr = ccUsageErr

	if p.WriteService != nil {
		nowUTC := now.UTC()
		for _, sv := range tree.Sessions() {
			ss := store.Session{
				SessionID:       sv.SessionID,
				PID:             pidPtrIfAlive(sv.PID, sv.Session),
				Cwd:             sv.Cwd,
				Name:            sv.Name,
				Kind:            sv.Kind,
				Entrypoint:      sv.Entrypoint,
				Model:           sv.SessionEnrichment.Model,
				TerminalHost:    sv.TerminalHost,
				Branch:          sv.Branch,
				Status:          sv.Session.Status.String(),
				FirstPrompt:     sv.SessionEnrichment.FirstPrompt,
				Labels:          nil, // populated when label pipeline runs in daemon
				TranscriptMTime: sv.TranscriptMTime,
				StartedAt:       sv.StartedAt,
				ContextTokens:   uint64(sv.SessionEnrichment.ContextTokens),
				SessionTokens:   uint64(sv.SessionEnrichment.SessionTokens),
				SubagentCount:   uint32(sv.SessionEnrichment.SubagentCount),
				SubshellCount:   uint32(sv.SessionEnrichment.SubshellCount),
				BurnRateShort:   sv.SessionEnrichment.BurnRateShort,
				BurnRateLong:    sv.SessionEnrichment.BurnRateLong,
				CostUSD:         sv.SessionEnrichment.CostUSD,
				AwaitingInput:   sv.SessionEnrichment.AwaitingInput,
				LastProcessedAt: nowUTC,
				UpdatedAt:       nowUTC,
			}
			// fold LastError if present
			if sv.SessionEnrichment.LastError != nil {
				le := sv.SessionEnrichment.LastError
				ss.LastErrorKind = string(le.Kind)
				ss.LastErrorText = le.Text
				ss.LastErrorAt = le.At
				ss.LastErrorTerminal = le.IsTerminal
				ss.LastErrorRetryable = sv.SessionEnrichment.LastErrorRetryable
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
							CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionEnrichment.SessionTokens), UpdatedAt: nowUTC,
						})
					}
					if p.ActiveWeekID > 0 {
						_ = p.WriteService.UpsertWeekContribution(ctx, store.Contribution{
							SessionID: sessRowID, ParentID: p.ActiveWeekID,
							CostUSD: sv.CostUSD, Tokens: uint64(sv.SessionEnrichment.SessionTokens), UpdatedAt: nowUTC,
						})
					}
				}
			}
		}
	}

	return tree, anyWorking, nil
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

// maxActivity returns the latest of the main transcript's mtime and the max
// mtime over <sessionid>/subagents/agent-*.jsonl. The subagents directory is
// derived from the resolved main transcript path exactly as
// claude-transcript.LastSubagentError does ("<dir>/<sid>.jsonl" ->
// "<dir>/<sid>/subagents"); for a resumed/forked session whose resolved
// transcript basename differs from the spawning session id, that directory
// won't exist and only the main mtime is used. This feeds ONLY the
// "waiting"-freshness check and the display/age bucket — it is intentionally
// NOT load-bearing for the busy keep-awake (busy is trusted, never demoted).
func maxActivity(mainMTime time.Time, mainPath string) time.Time {
	best := mainMTime
	if mainPath == "" {
		return best
	}
	subDir := strings.TrimSuffix(mainPath, ".jsonl") + "/subagents"
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return best
	}
	for _, e := range entries {
		if e.IsDir() ||
			!strings.HasPrefix(e.Name(), "agent-") ||
			filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(best) {
			best = info.ModTime()
		}
	}
	return best
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
