package service

import (
	"context"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// State is the full snapshot returned by ReadService.GetState.
type State struct {
	Sessions []store.SessionWithContribution
	Dirs     []*Directory
	Block    *store.Block
	Week     *store.Week
	Toggles  map[string]bool
	Now      time.Time
}

// ReadDeps bundles every read-side Store.
type ReadDeps struct {
	Sessions store.SessionStore
	Blocks   store.BlockStore
	Weeks    store.WeekStore
	Toggles  store.ToggleStore
	Nudges   store.NudgeStore
}

// ReadService materialises a State from the stores per request.
type ReadService struct {
	deps      ReadDeps
	freshness store.FreshnessWindow
	now       func() time.Time
}

func NewReadService(deps ReadDeps) *ReadService {
	return &ReadService{deps: deps, freshness: store.DefaultFreshness(), now: time.Now}
}

// SetClock allows tests to inject a deterministic now.
func (r *ReadService) SetClock(fn func() time.Time) { r.now = fn }

func (r *ReadService) GetState(ctx context.Context, filter store.Filter) (*State, error) {
	now := r.now().UTC()
	st := &State{Now: now}

	block, err := r.deps.Blocks.GetActive(ctx, now, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Block = block

	week, err := r.deps.Weeks.GetActive(ctx, now, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Week = week

	var activeBlockID int64
	if block != nil {
		activeBlockID = block.ID
	}
	sessions, err := r.deps.Sessions.List(ctx, filter, activeBlockID, r.freshness)
	if err != nil {
		return nil, err
	}
	st.Sessions = sessions
	st.Dirs = BuildDirectories(sessions)

	toggles, err := r.deps.Toggles.All(ctx)
	if err != nil {
		return nil, err
	}
	st.Toggles = toggles

	return st, nil
}

// SessionDetail wraps a Session with its latest nudge event (if any).
type SessionDetail struct {
	Session     store.Session
	LatestNudge *store.NudgeEvent
}

func (r *ReadService) GetSessionByID(ctx context.Context, sessionID string) (*SessionDetail, error) {
	sess, err := r.deps.Sessions.GetByID(ctx, sessionID, r.freshness)
	if err != nil || sess == nil {
		return nil, err
	}
	det := &SessionDetail{Session: *sess}
	// Look up the surrogate id for the nudge join.
	// (A small extension to SessionStore would return id; for now skip nudge
	// lookup if we can't get it.)
	det.LatestNudge = nil
	return det, nil
}

func (r *ReadService) Toggles(ctx context.Context) (map[string]bool, error) {
	return r.deps.Toggles.All(ctx)
}
