package service

import "github.com/phillipgreenii/pa-monitor/internal/store"

// Directory mirrors aggregate.Directory but lives here so the service is
// self-contained. The daemon converts to aggregate.Directory at the proto
// boundary.
type Directory struct {
	Path         string
	Branch       string
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  uint64
	TotalCostUSD float64
	BurnRateSum  float64
	Sessions     []store.SessionWithContribution
}

// BuildDirectories rolls a flat session list into directory groups keyed by Cwd.
// Counts and totals are computed in this pass; PR info and branch resolution
// stay in the daemon layer (file-backed prCache).
func BuildDirectories(sessions []store.SessionWithContribution) []*Directory {
	byCwd := map[string]*Directory{}
	for _, sc := range sessions {
		d, ok := byCwd[sc.Cwd]
		if !ok {
			d = &Directory{Path: sc.Cwd}
			byCwd[sc.Cwd] = d
		}
		switch sc.Status {
		case "working":
			d.WorkingN++
		case "idle":
			d.IdleN++
		default:
			d.DormantN++
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
