package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
)

type CheckDeps struct {
	PN       pn.Client
	BD       bd.Client
	PatchID  patchid.Client
	Discover func(paths []string, root string) ([]discover.DB, error) // nil → discover.DistinctDBs
}

type CheckParams struct {
	WorkspaceDir string
	DryRun       bool
	Strict       bool
	LastN        int
	StaleHandler string
	StaleAfter   time.Duration
	Now          time.Time
}

type Skip struct {
	GateID string `json:"gate-id"`
	Repo   string `json:"repo"`
	Reason string `json:"reason"`
}

type StaleAction struct {
	GateID string `json:"gate-id"`
	Action string `json:"action"`
}

type CheckResult struct {
	Resolved     []string      `json:"resolved"`
	WouldResolve []string      `json:"would_resolve,omitempty"`
	Skipped      []Skip        `json:"skipped"`
	StaleActions []StaleAction `json:"stale_actions"`
}

func parseAwaitID(s string) (wsid, repo, patchID string, ok bool) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

func Check(ctx context.Context, d CheckDeps, p CheckParams) (CheckResult, error) {
	if p.LastN == 0 {
		p.LastN = 100
	}
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return CheckResult{}, err
	}
	disc := d.Discover
	if disc == nil {
		disc = discover.DistinctDBs
	}
	paths := make([]string, 0, len(info.Repos))
	for _, r := range info.Repos {
		paths = append(paths, r.Path)
	}
	dbs, err := disc(paths, info.Root)
	if err != nil {
		return CheckResult{}, err
	}

	var result CheckResult
	for _, db := range dbs {
		gates, err := d.BD.ListGates(ctx, db.Dir)
		if err != nil {
			return result, err
		}
		for _, g := range gates {
			if g.AwaitType != "pn:applied" {
				continue
			}
			wsid, repoName, patchID, ok := parseAwaitID(g.AwaitID)
			if !ok || wsid != info.Wsid {
				continue // not ours / malformed → leave alone
			}
			repo, known := info.RepoByName(repoName)
			if !known {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "unknown repo"})
				continue
			}
			// Stale eligibility (resolve takes precedence below).
			stale := false
			if p.StaleAfter > 0 {
				if ts, err := time.Parse(time.RFC3339, g.CreatedAt); err == nil {
					if p.Now.Sub(ts) > p.StaleAfter {
						stale = true
					}
				}
			}
			if repo.AppliedRef == "" {
				// never applied → leave blocked; stale-handle if eligible
				if stale {
					d.applyStale(ctx, db.Dir, g.ID, p, &result)
				}
				continue
			}
			if repo.Dirty && p.Strict {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "dirty (--strict)"})
				continue
			}
			// Choose scan range: baseline..applied_ref when baseline is an
			// ancestor, else the last-N commits of applied_ref.
			rng := fmt.Sprintf("-n %d %s", p.LastN, repo.AppliedRef)
			if base := g.Metadata["applied_baseline"]; base != "" && d.PatchID.IsAncestor(ctx, repo.Path, base, repo.AppliedRef) {
				rng = base + ".." + repo.AppliedRef
			}
			set, err := d.PatchID.ScanPatchIDs(ctx, repo.Path, rng)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "scan failed: " + err.Error()})
				continue
			}
			if set[patchID] {
				if p.DryRun {
					result.WouldResolve = append(result.WouldResolve, g.ID)
				} else if err := d.BD.ResolveGate(ctx, db.Dir, g.ID, ""); err != nil {
					result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "resolve failed: " + err.Error()})
				} else {
					result.Resolved = append(result.Resolved, g.ID)
				}
				continue
			}
			// Not found → leave blocked; stale-handle if eligible.
			if stale {
				d.applyStale(ctx, db.Dir, g.ID, p, &result)
			}
		}
	}
	return result, nil
}

// applyStale records (and unless DryRun, performs) the stale action.
func (d CheckDeps) applyStale(ctx context.Context, dir, gateID string, p CheckParams, result *CheckResult) {
	action := p.StaleHandler
	if action == "" {
		action = "convert-to-human"
	}
	result.StaleActions = append(result.StaleActions, StaleAction{GateID: gateID, Action: action})
	if p.DryRun {
		return
	}
	switch action {
	case "close":
		_ = d.BD.ResolveGate(ctx, dir, gateID, "stale: closed by pb gate check")
	default: // convert-to-human: add "human" label → surfaces in `bd human list`
		_ = d.BD.AddLabel(ctx, dir, gateID, "human")
	}
}
