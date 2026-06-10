package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fingerprintHash is a stable hash of the change-relevant fields.
//
//nolint:unused // consumed by the daemon fingerprint loop landed in a follow-up task.
func fingerprintHash(f vcs.PRFingerprint) string {
	s := fmt.Sprintf("%s|%s|%s|%s|%t|%d|%d|%d",
		f.UpdatedAt, f.HeadOID, f.StatusRollup, f.State, f.IsDraft,
		f.ReviewCount, f.CommentCount, f.ReviewThreadCount)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// diffResult is the detector's per-tick decision for one group.
//
//nolint:unused // consumed by the daemon fingerprint loop landed in a follow-up task.
type diffResult struct {
	enqueued map[prKey]bool
	reasons  map[prKey]string // added|changed|disappeared
	roster   map[prKey]string // new prev (hashes)
}

// diffRoster computes enqueues from the fresh roster vs the previous-tick
// hashes and the open-bead set. complete=false (query failed/truncated) skips
// the disappeared check (mass-close guard) but still records the roster.
// Callers pass the roster entries for one group; "added" = no open bead,
// "changed" = bead exists and fingerprint is new/differs.
//
//nolint:unused // consumed by the daemon fingerprint loop landed in a follow-up task.
func diffRoster(prev map[prKey]string, roster []vcs.PRFingerprint, openBeads map[prKey]bool, complete bool) diffResult {
	d := diffResult{
		enqueued: map[prKey]bool{},
		reasons:  map[prKey]string{},
		roster:   map[prKey]string{},
	}
	inRoster := map[prKey]bool{}
	for _, f := range roster {
		k := prKey{Repo: f.Repo, Number: f.Number}
		h := fingerprintHash(f)
		d.roster[k] = h
		inRoster[k] = true
		if old, seen := prev[k]; seen && old == h {
			continue // unchanged
		}
		d.enqueued[k] = true
		if openBeads[k] {
			d.reasons[k] = "changed"
		} else {
			d.reasons[k] = "added"
		}
	}
	if complete {
		for k := range openBeads {
			if !inRoster[k] {
				d.enqueued[k] = true
				d.reasons[k] = "disappeared"
			}
		}
	}
	return d
}

// buildMineQuery is the cross-repo "my open PRs" search (drafts included).
//
//nolint:unused // consumed by the daemon fingerprint loop landed in a follow-up task.
func buildMineQuery(cfg *config.Config) string {
	parts := []string{"is:pr", "is:open", "author:" + cfg.SelfLogin}
	for _, r := range cfg.Repos {
		parts = append(parts, "repo:"+r.Remote)
	}
	return strings.Join(parts, " ")
}

// buildTeamQuery is one repo's "team open PRs" search (drafts included; empty
// when the repo has no team members).
//
//nolint:unused // consumed by the daemon fingerprint loop landed in a follow-up task.
func buildTeamQuery(rcfg config.RepoConfig) string {
	if len(rcfg.TeamMembers) == 0 {
		return ""
	}
	parts := []string{"is:pr", "is:open", "repo:" + rcfg.Remote}
	for _, m := range rcfg.TeamMembers {
		parts = append(parts, "author:"+m)
	}
	return strings.Join(parts, " ")
}
