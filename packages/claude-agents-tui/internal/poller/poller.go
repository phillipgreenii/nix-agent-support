package poller

import (
	"context"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/burnrate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
	"github.com/phillipgreenii/claude-agents-tui/internal/subshell"
	"github.com/phillipgreenii/claude-agents-tui/internal/transcript"
)

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

	burnShort       map[string]*burnrate.Buffer
	burnLong        map[string]*burnrate.Buffer
	prevTotalTokens map[string]int
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

		fp, _ := transcript.FirstPrompt(path)
		ctxSnap, _ := transcript.LatestContext(path)
		subs, _ := transcript.OpenSubagents(path)
		waiting, _ := transcript.IsAwaitingInput(path)
		shells, _ := subshellCounter.Count(s.PID)

		// Burn rate: add delta (tokens generated since last poll) to ring buffers.
		prev := p.prevTotalTokens[s.SessionID]
		delta := max(ctxSnap.TotalTokens-prev, 0)
		p.prevTotalTokens[s.SessionID] = ctxSnap.TotalTokens

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

		enriched[s.SessionID] = aggregate.SessionEnrichment{
			ContextTokens: ctxSnap.ContextTokens,
			SessionTokens: ctxSnap.TotalTokens,
			Model:         ctxSnap.Model,
			FirstPrompt:   fp,
			SubagentCount: subs,
			SubshellCount: shells,
			AwaitingInput: waiting,
			BurnRateShort: p.burnShort[s.SessionID].Rate(now),
			BurnRateLong:  p.burnLong[s.SessionID].Rate(now),
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
