// Package gate orchestrates pn:applied gate create and check.
package gate

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

type CreateDeps struct {
	PN       pn.Client
	BD       bd.Client
	PatchID  patchid.Client
	R        run.Runner                                               // for git rev-list on --commits ranges
	Discover func(paths []string, root string) ([]discover.DB, error) // nil → discover.DistinctDBs
}

type CreateParams struct {
	WorkspaceDir string
	BeadID       string
	Repo         string
	Commit       string // single commit-ish; defaults to HEAD
	Commits      string // optional rev range → one gate per commit
	Reason       string
}

type CreatedGate struct {
	GateID  string `json:"gate-id"`
	AwaitID string `json:"await_id"`
	Repo    string `json:"repo"`
	PatchID string `json:"patch-id"`
	// AppliedBaseline is the repo's applied_ref AT CREATE TIME (ADR 0018) — a scan
	// lower-bound snapshot, not a claim about which commit will eventually resolve
	// the gate. Because the gated commit is (almost always) not yet applied when
	// the gate is created, this is EXPECTED to be a strict ancestor of — never
	// required to equal — whatever commit later satisfies the gate at check time.
	// Investigated in pg2-q61in: a real gate's baseline was verified to be a strict
	// ancestor of its landing commit, several commits back, with the patch-id
	// itself independently confirmed to match the correct commit. That is the
	// designed behaviour, not a staleness bug.
	AppliedBaseline string `json:"applied_baseline"`
}

type CreateResult struct {
	Gates []CreatedGate `json:"gates"`
}

func Create(ctx context.Context, d CreateDeps, p CreateParams) (CreateResult, error) {
	info, err := d.PN.Info(ctx, p.WorkspaceDir)
	if err != nil {
		return CreateResult{}, err
	}
	repo, ok := info.RepoByName(p.Repo)
	if !ok {
		return CreateResult{}, fmt.Errorf("repo %q is not in workspace %q", p.Repo, info.Root)
	}

	// Resolve which commits to gate.
	var commitish []string
	if p.Commits != "" {
		out, err := d.R.Run(ctx, "git",
			[]string{"-C", repo.Path, "rev-list", "--no-merges", "--reverse", p.Commits}, run.Options{})
		if err != nil {
			return CreateResult{}, fmt.Errorf("git rev-list %s: %w", p.Commits, err)
		}
		commitish = strings.Fields(out.Stdout)
		if len(commitish) == 0 {
			return CreateResult{}, fmt.Errorf("rev range %q matched no commits", p.Commits)
		}
	} else {
		c := p.Commit
		if c == "" {
			c = "HEAD"
		}
		commitish = []string{c}
	}

	// Co-locate the gate in the BEAD's OWN DB (a cross-DB blocks edge silently
	// fails to hold). Discover the workspace's distinct DBs and find the one
	// containing BeadID.
	dbDir, err := resolveBeadDB(ctx, d, info, p.BeadID)
	if err != nil {
		return CreateResult{}, err
	}

	var result CreateResult
	for _, cish := range commitish {
		pid, err := d.PatchID.Compute(ctx, repo.Path, cish)
		if err != nil {
			return result, err
		}
		awaitID := fmt.Sprintf("%s:%s:%s", info.Wsid, p.Repo, pid)
		gid, err := d.BD.CreateGate(ctx, dbDir, p.BeadID, "pn:applied", awaitID, p.Reason)
		if err != nil {
			return result, err
		}
		// repo.AppliedRef is fetched once, above, at the top of Create — the repo's
		// applied_ref AS OF THIS INVOCATION, before the commit(s) being gated here
		// have gone through their own apply. See CreatedGate.AppliedBaseline's doc
		// comment for why it is normal (per ADR 0018, confirmed by pg2-q61in) for
		// this value to end up several commits behind whatever eventually resolves
		// the gate.
		if err := d.BD.SetMetadata(ctx, dbDir, gid, "applied_baseline", repo.AppliedRef); err != nil {
			return result, fmt.Errorf("gate %s created but baseline write failed: %w", gid, err)
		}
		result.Gates = append(result.Gates, CreatedGate{
			GateID: gid, AwaitID: awaitID, Repo: p.Repo, PatchID: pid, AppliedBaseline: repo.AppliedRef,
		})
	}
	return result, nil
}

// resolveBeadDB finds the distinct workspace DB that contains beadID, so the
// gate is co-located with the bead it blocks.
func resolveBeadDB(ctx context.Context, d CreateDeps, info pn.Info, beadID string) (string, error) {
	paths := make([]string, 0, len(info.Repos))
	for _, r := range info.Repos {
		paths = append(paths, r.Path)
	}
	disc := d.Discover
	if disc == nil {
		disc = discover.DistinctDBs
	}
	dbs, err := disc(paths, info.Root)
	if err != nil {
		return "", err
	}
	for _, db := range dbs {
		if d.BD.HasBead(ctx, db.Dir, beadID) {
			return db.Dir, nil
		}
	}
	return "", fmt.Errorf("bead %q not found in any beads DB under workspace %q", beadID, info.Root)
}
