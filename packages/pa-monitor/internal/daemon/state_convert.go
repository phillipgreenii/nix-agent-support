package daemon

import (
	"sort"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/service"
	"github.com/phillipgreenii/pa-monitor/internal/store"
)

// convertStateToAggregateTree converts a *service.State (DB-materialised
// snapshot) into the *aggregate.Tree shape that consumers (proto layer,
// nudger, lifecycle metrics) expect. This is the boundary translation that
// keeps all downstream code compiling unchanged.
//
// PRInfo is intentionally left nil — it is file-backed and is not available
// from the DB; the proto layer is already nil-safe on PRInfo.
func convertStateToAggregateTree(st *service.State) *aggregate.Tree {
	if st == nil {
		return nil
	}

	tree := &aggregate.Tree{
		GeneratedAt: st.Now,
		// CCUsageProbed is true on the DB path by definition: if the DB had no
		// data, GetState would return nil and we would never reach this function.
		CCUsageProbed: true,
	}

	// Convert block.
	if st.Block != nil {
		tree.ActiveBlock = storeBlockToCCUsageBlock(st.Block)
		tree.PlanCapUSD = st.Block.PlanCapUSD
	}

	// Convert week.
	if st.Week != nil {
		tree.ActiveWeek = storeWeekToCCUsageWeek(st.Week)
		tree.WeekCapUSD = st.Week.WeekCapUSD
	}

	// Convert directories.
	for _, dir := range st.Dirs {
		ad := convertDirectory(dir)
		if ad.Path == "" {
			continue
		}
		tree.Dirs = append(tree.Dirs, ad)
	}
	sort.Slice(tree.Dirs, func(i, j int) bool {
		return tree.Dirs[i].Path < tree.Dirs[j].Path
	})

	// Fix #1: prefer the block-level RateLimitResetsAt over per-session
	// aggregation. The block column is authoritative for the global window.
	if st.Block != nil && st.Block.RateLimitResetsAt != nil {
		tree.WindowResetsAt = *st.Block.RateLimitResetsAt
	}

	return tree
}

// convertDirectory converts one service.Directory (DB-backed) into an
// aggregate.Directory.
func convertDirectory(dir *service.Directory) *aggregate.Directory {
	ad := &aggregate.Directory{
		Path:         dir.Path,
		Branch:       dir.Branch,
		WorkingN:     dir.WorkingN,
		IdleN:        dir.IdleN,
		DormantN:     dir.DormantN,
		WaitingN:     dir.WaitingN,
		TotalTokens:  int(dir.TotalTokens),
		TotalCostUSD: dir.TotalCostUSD,
		BurnRateSum:  dir.BurnRateSum,
	}

	for i := range dir.Sessions {
		sv := convertSessionWithContribution(&dir.Sessions[i])
		if sv != nil {
			ad.Sessions = append(ad.Sessions, sv)
		}
	}

	// Sort sessions newest-first, matching aggregate.Build's ordering.
	sort.Slice(ad.Sessions, func(i, j int) bool {
		return ad.Sessions[i].StartedAt.After(ad.Sessions[j].StartedAt)
	})

	return ad
}

// convertSessionWithContribution converts a store.SessionWithContribution
// into an aggregate.SessionView. The resulting *session.Session and
// SessionEnrichment are populated from all fields that sessionViewToProto
// reads (see internal/proto/translate.go).
func convertSessionWithContribution(sc *store.SessionWithContribution) *aggregate.SessionView {
	if sc == nil {
		return nil
	}

	// Build the core session.Session.
	pid := 0
	if sc.PID != nil {
		pid = *sc.PID
	}

	sess := &session.Session{
		PID:             pid,
		SessionID:       sc.SessionID,
		Cwd:             sc.Cwd,
		Kind:            sc.Kind,
		Entrypoint:      sc.Entrypoint,
		Name:            sc.Name,
		Branch:          sc.Branch,
		StartedAt:       sc.StartedAt,
		TerminalHost:    sc.TerminalHost,
		TranscriptMTime: sc.TranscriptMTime,
		Status:          parseSessionStatus(sc.Status),
		// Env is not stored in the DB; consumers that need it (label detectors)
		// operate on the live-process path. Left nil here — the proto layer
		// checks sv.Env != nil before forwarding env keys.
	}

	// Build the enrichment.
	en := aggregate.SessionEnrichment{
		ContextTokens: int(sc.ContextTokens),
		Model:         sc.Model,
		FirstPrompt:   sc.FirstPrompt,
		SubagentCount: int(sc.SubagentCount),
		SubshellCount: int(sc.SubshellCount),
		SessionTokens: int(sc.SessionTokens),
		BurnRateShort: sc.BurnRateShort,
		BurnRateLong:  sc.BurnRateLong,
		// CostUSD is always the block-scoped contribution. The session's
		// lifetime cost (sc.CostUSD) is not used here — it lives in its own
		// column and is not the right value for per-block display.
		CostUSD:       sc.BlockCostUSD,
		AwaitingInput: sc.AwaitingInput,
	}

	// Reconstruct LastError from the stored fields. The escalation-aware
	// retryable verdict is stored separately and now lives on the enrichment,
	// not the shared record.
	if sc.LastErrorKind != "" {
		en.LastError = &transcript.ErrorRecord{
			Kind:         transcript.ErrorKind(sc.LastErrorKind),
			Text:         sc.LastErrorText,
			At:           sc.LastErrorAt,
			IsTerminal:   sc.LastErrorTerminal,
			FromSubagent: sc.LastErrorFromSubagent,
		}
		en.LastErrorRetryable = sc.LastErrorRetryable
	}

	return &aggregate.SessionView{
		Session:           sess,
		SessionEnrichment: en,
	}
}

// storeBlockToCCUsageBlock converts a *store.Block into a *ccusage.Block.
// The BurnRate and Projection fields are not stored in the DB; they are
// set to zero — callers that need them (ProjectedExhaust, proto render)
// must recompute them if needed. For the "all sessions" view that Task 20
// enables, zero burn-rate means the projection simply doesn't render.
func storeBlockToCCUsageBlock(b *store.Block) *ccusage.Block {
	if b == nil {
		return nil
	}
	return &ccusage.Block{
		ID:        b.BlockID,
		StartTime: b.StartedAt,
		EndTime:   b.EndedAt,
		IsActive:  true, // GetActive only returns the active block
		CostUSD:   b.TotalCostUSD,
		CapHitAt:  b.CapHitAt,
		// BurnRate and Projection are not persisted; zero-value is safe
		// for proto serialisation and nil-checks.
	}
}

// storeWeekToCCUsageWeek converts a *store.Week into a *ccusage.WeeklyEntry.
func storeWeekToCCUsageWeek(w *store.Week) *ccusage.WeeklyEntry {
	if w == nil {
		return nil
	}
	return &ccusage.WeeklyEntry{
		Period:    w.WeekID, // "YYYY-MM-DD" Monday anchor stored as WeekID
		TotalCost: w.TotalCostUSD,
		CapHitAt:  w.CapHitAt,
	}
}

// parseSessionStatus maps the stored status string to session.Status.
func parseSessionStatus(s string) session.Status {
	switch s {
	case "working":
		return session.Working
	case "idle":
		return session.Idle
	case "waiting":
		return session.WaitingForHuman
	default:
		return session.Dormant
	}
}
