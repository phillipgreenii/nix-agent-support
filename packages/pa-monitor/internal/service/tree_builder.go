package service

import "github.com/phillipgreenii/pa-monitor/internal/store"

// Directory mirrors aggregate.Directory but lives here so the service is
// self-contained. The daemon converts to aggregate.Directory at the proto
// boundary.
type Directory struct {
	Path   string
	Branch string
	// ADR 0024 {working, blocked, idle} counts + the blocked-by-blocker rollup.
	WorkingN           int
	BlockedN           int
	IdleN              int
	BlockedHumanInputN int
	BlockedHumanAuthnN int
	BlockedUsageLimitN int
	BlockedErrorN      int
	TotalTokens        uint64
	TotalCostUSD       float64
	BurnRateSum        float64
	Sessions           []store.SessionWithContribution
}

// BuildDirectories rolls a flat session list into directory groups keyed by Cwd.
// Counts and totals are computed in this pass; PR info and branch resolution
// stay in the daemon layer (file-backed prCache).
//
// TotalTokens and TotalCostUSD aggregate the session-LIFETIME values
// (sc.SessionTokens, sc.CostUSD), matching today's aggregate.Directory
// semantics. Per-block roll-ups live on session_block_contributions; if a
// future consumer needs "dir cost this block", sum sc.BlockCostUSD instead.
//
// Branch resolution is "first non-empty wins" in input order — deterministic
// because the input slice order from SessionStore.List is stable.
func BuildDirectories(sessions []store.SessionWithContribution) []*Directory {
	byCwd := map[string]*Directory{}
	for _, sc := range sessions {
		d, ok := byCwd[sc.Cwd]
		if !ok {
			d = &Directory{Path: sc.Cwd}
			byCwd[sc.Cwd] = d
		}
		// ADR 0024 R9: parse the persisted status string. "blocked" (and legacy
		// "waiting") count as blocked; "dormant" folds into idle; unknown → idle.
		// The persisted blocker column drives the blocked-by-blocker rollup so
		// the DB path can render usage_limit without RateLimitResetsAt.
		switch sc.Status {
		case "working":
			d.WorkingN++
		case "blocked", "waiting":
			d.BlockedN++
			blocker := sc.Blocker
			if blocker == "" && sc.Status == "waiting" {
				blocker = "human_input"
			}
			switch blocker {
			case "human_input":
				d.BlockedHumanInputN++
			case "human_authn":
				d.BlockedHumanAuthnN++
			case "usage_limit":
				d.BlockedUsageLimitN++
			case "error":
				d.BlockedErrorN++
			}
		default:
			// "idle", "dormant", or unknown → idle.
			d.IdleN++
		}
		d.TotalTokens += sc.SessionTokens
		d.TotalCostUSD += sc.CostUSD
		d.BurnRateSum += sc.BurnRateShort
		d.Sessions = append(d.Sessions, sc)
		if d.Branch == "" && sc.Branch != "" {
			d.Branch = sc.Branch
		}
	}
	out := make([]*Directory, 0, len(byCwd))
	for _, d := range byCwd {
		out = append(out, d)
	}
	return out
}
