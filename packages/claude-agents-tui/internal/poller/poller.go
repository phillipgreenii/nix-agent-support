package poller

import (
	"context"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/burnrate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
	"github.com/phillipgreenii/claude-agents-tui/internal/subshell"
	"github.com/phillipgreenii/claude-agents-tui/internal/transcript"
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
	SessionsDir      string
	ClaudeHome       string
	PidAlive         func(int) bool
	PlanTier         string
	WorkingThreshold time.Duration
	IdleThreshold    time.Duration
	BurnWindowShort  time.Duration
	BurnWindowLong   time.Duration
	Now              func() time.Time
	CCUsageFn        func(ctx context.Context) ([]byte, error)
	CCUsageStateFn   func() (probed bool, err error)
	PRLookupFn       func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
	Signalers        []signal.Signaler

	burnShort       map[string]*burnrate.Buffer
	burnLong        map[string]*burnrate.Buffer
	prevTotalTokens map[string]int

	terminalHostCache map[int]string
	transcriptCache   map[string]cachedTranscript
}

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
		s.Status = session.Classify(now, s.TranscriptMTime, p.WorkingThreshold, p.IdleThreshold)
		s.Branch = session.GitBranch(s.Cwd)
		if s.Status == session.Working {
			anyWorking = true
		}

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

		// TerminalHost cache: detect once per PID lifetime.
		if host, hit := p.terminalHostCache[s.PID]; hit {
			s.TerminalHost = host
		} else {
			s.TerminalHost = detectTerminalHost(p.Signalers, s.PID)
			p.terminalHostCache[s.PID] = s.TerminalHost
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

		enriched[s.SessionID] = aggregate.SessionEnrichment{
			ContextTokens:     snap.ContextTokens,
			SessionTokens:     snap.TotalTokens,
			Model:             snap.Model,
			FirstPrompt:       snap.FirstPrompt,
			SubagentCount:     snap.SubagentCount,
			SubshellCount:     shells,
			AwaitingInput:     snap.AwaitingInput,
			RateLimitResetsAt: rlReset,
			BurnRateShort:     p.burnShort[s.SessionID].Rate(now),
			BurnRateLong:      p.burnLong[s.SessionID].Rate(now),
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
		if s.Status == session.Dormant && p.PidAlive != nil && p.PidAlive(s.PID) && pidActiveSID[s.PID] == s.SessionID {
			s.Status = session.Idle
		}
	}

	var block *ccusage.Block
	if p.CCUsageFn != nil {
		if body, err := p.CCUsageFn(ctx); err == nil && body != nil {
			block, _ = ccusage.ParseActiveBlock(body)
		}
	}

	var ccUsageProbed bool
	var ccUsageErr error
	if p.CCUsageStateFn != nil {
		ccUsageProbed, ccUsageErr = p.CCUsageStateFn()
	}

	tree := aggregate.Build(sessions, enriched, prByDir, block, p.PlanTier)
	tree.CCUsageProbed = ccUsageProbed
	tree.CCUsageErr = ccUsageErr
	return tree, anyWorking, nil
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
