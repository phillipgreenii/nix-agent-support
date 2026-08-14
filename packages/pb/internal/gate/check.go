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
	Resolved     []string `json:"resolved"`
	WouldResolve []string `json:"would_resolve,omitempty"`
	// Skipped holds gates that could NOT be determined — an unknown repo, a scan
	// failure, a dirty repo under --strict, an apply that cannot say what it built a
	// flake input from. `pb gate check` exits non-zero iff this is non-empty.
	Skipped []Skip `json:"skipped"`
	// Blocked holds gates that WERE determined and are correctly still closed: the
	// change is committed locally but is not in the applied system's flake lock. It
	// is deliberately NOT Skipped — a determinable "no" is not an undeterminable
	// gate, and routing it to Skipped would make `pb gate check` (the apply
	// post-hook) exit non-zero, and pn warn, after every apply with a normal pending
	// gate. That trains the operator to ignore the warning. It gets its own list so
	// the actionable reason has somewhere to be reported without touching the exit
	// code.
	Blocked      []Skip        `json:"blocked,omitempty"`
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
			set, err := d.PatchID.ScanPatchIDCommits(ctx, repo.Path, rng)
			if err != nil {
				result.Skipped = append(result.Skipped, Skip{GateID: g.ID, Repo: repoName, Reason: "scan failed: " + err.Error()})
				continue
			}
			// CONDITION 1 (unchanged): the gated patch-id appears in
			// applied_baseline..applied_ref, i.e. an apply actually ran over a
			// checkout containing this change. Key presence is exactly the old
			// `set[patchID]` test; the shas are the by-product condition 2 needs.
			if gatedCommits, found := set[patchID]; found {
				// CONDITION 2: that apply's lock contained the commit — applied only to
				// an input that apply actually RESOLVED THROUGH THE LOCK, not to one it
				// overrode with a local clone (bead pg2-14yqh).
				verdict, reason := d.applyBuiltGatedCommit(ctx, repo, gatedCommits)
				if verdict != lockSatisfied {
					entry := Skip{GateID: g.ID, Repo: repoName, Reason: reason}
					if verdict == lockUnknown {
						result.Skipped = append(result.Skipped, entry)
					} else {
						result.Blocked = append(result.Blocked, entry)
					}
					if stale {
						d.applyStale(ctx, db.Dir, g.ID, p, &result)
					}
					continue
				}
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

// pnLockedRevsSchema is the pn applied-state schema version that first recorded
// locked_revs, and therefore the version at or above which `terminal_input` /
// `locked_rev` in `pn workspace info` carry information (repo-base ADR 0025).
const pnLockedRevsSchema = 2

// pnOverrideRecordSchema is the pn applied-state schema version that first recorded
// WHICH inputs the apply overrode, and therefore the version at or above which
// `overridden` in `pn workspace info` carries information (repo-base ADR 0025's
// "what the apply overrode" amendment). Below it the field is absent, which is NOT
// the same claim as "false" — see applyBuiltGatedCommit's fourth branch.
const pnOverrideRecordSchema = 3

// shortRev truncates a rev for a human-facing skip reason.
func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// lockVerdict is applyBuiltGatedCommit's answer. The two non-satisfied values are
// deliberately distinct: one is an answer, the other is the absence of one, and they
// belong in different CheckResult lists (see CheckResult.Blocked).
type lockVerdict int

const (
	// lockSatisfied — condition 2 holds, or does not apply to this repo.
	lockSatisfied lockVerdict = iota
	// lockMissing — DETERMINED: the gated commit is not in the rev that apply built
	// the repo from. Correctly still gated; the remedy is push + relock + apply.
	lockMissing
	// lockUnknown — UNDETERMINABLE: the apply recorded no rev for an input it does
	// consume, or the gated commit could not be recovered. Fail closed.
	lockUnknown
)

// applyBuiltGatedCommit is CONDITION 2 of gate resolution: for a repo the apply
// resolved THROUGH THE TERMINAL'S FLAKE.LOCK, that apply must ALSO have built the
// repo from a rev that CONTAINS the gated commit. It returns lockSatisfied with an
// empty reason when the condition holds or does not apply, and otherwise a verdict
// plus a human-facing reason.
//
// Condition 1 alone is unsound for a repo the terminal pins as a remote flake input
// AND the build resolved that way. It proves an apply ran over a checkout holding
// the change; it does not prove the change was IN the build, because that build
// resolved the repo through the terminal's flake.lock. A commit landed on local main
// but never pushed and relocked therefore resolved its gate while being absent from
// the built system (bead pg2-ft60a), and the peer that then claimed the verification
// bead was most likely to conclude "the feature is broken" rather than "it was never
// deployed" — inviting a revert of correct work.
//
// The rev tested against is the one RECORDED WITH THAT APPLY, never the lock as it
// stands now. Testing "is the lock NOW past the commit" would re-admit the defect
// in a narrower window: an apply at T1 followed by a relock at T2 > T1 would pass
// both conditions while the system was built from the pre-relock rev.
//
// The gated COMMIT comes from the condition-1 scan (git patch-id reports the sha
// beside each patch-id), so nothing new is recorded at gate-CREATE time and
// `pb gate create` is untouched. More than one sha can carry the patch after a
// cherry-pick, and any one of them being in the lock means the change shipped.
//
// The branch table below is the whole applicability-and-compatibility story:
//
//   - schema < 2 — the record was written by a pn predating locked_revs, so there
//     is NO lock information. Skip: the alternative is that no gate can resolve
//     anywhere until the new pn is built, pushed, relocked and applied, which is a
//     bootstrap stall (and the fix itself ships through exactly that path).
//   - schema >= 2, no entry for the repo (TerminalInput false) — positive evidence
//     that the terminal does not consume it as a flake input. The TERMINAL's own
//     gates land here, which is why they keep resolving on condition 1 alone with no
//     special case: the apply builds the terminal from its local directory, so
//     condition 1 is the whole truth for it.
//   - schema >= 3, recorded as OVERRIDDEN — the apply passed
//     `--override-input <alias> git+file://<clone>` for it, so nix built it from the
//     LOCAL CLONE at eval-time HEAD and never consulted the lock. The premise of
//     condition 2 is simply false for such a repo, and its locked rev normally
//     TRAILS what was built, so enforcing it would block a gate on a change that IS
//     in the running system. Skip: condition 1 already covers it, exactly as it does
//     for the terminal. This is the operator ruling of 2026-08-14 on bead pg2-14yqh
//     (option (c)), which followed pg2-xg9wp DISPROVING the original premise that
//     these repos are built from their pinned revs.
//   - schema >= 2, entry present, NOT recorded as overridden — the condition
//     applies. An EMPTY rev means the apply could not say what it built that input
//     from, and that fails CLOSED: falling back to condition 1 there is exactly the
//     unsound case.
//
// A schema-2 record — locked_revs recorded, override set not — lands in the LAST
// row, i.e. it FAILS CLOSED, and that asymmetry against the schema < 2 row is
// deliberate. The two absences are not equivalent evidence:
//
//   - At schema < 2 there is no lock information, so condition 2 is UNEVALUABLE and
//     the only alternative to skipping is blocking every gate everywhere.
//   - At schema 2 condition 2 is fully evaluable, and its verdict is DETERMINATE
//     (lockMissing → Blocked, not Skipped, so `pb gate check` still exits 0 and the
//     apply post-hook does not warn). Leaning the other way would ASSERT an override
//     the record does not evidence, and a false "the change shipped" is the
//     expensive direction — it is the pg2-ft60a harm, whereas a false "still
//     blocked" costs one stale-handler escalation to a human.
//
// The window is also bounded: schema-2 records are rewritten at schema 3 by the next
// successful apply, so at most one apply cycle is affected — this is not the
// unbounded bootstrap stall the schema < 2 row exists to prevent.
func (d CheckDeps) applyBuiltGatedCommit(ctx context.Context, repo pn.Repo, gatedCommits []string) (lockVerdict, string) {
	if repo.AppliedStateSchema < pnLockedRevsSchema {
		return lockSatisfied, ""
	}
	if !repo.TerminalInput {
		return lockSatisfied, ""
	}
	if repo.AppliedStateSchema >= pnOverrideRecordSchema && repo.Overridden {
		return lockSatisfied, ""
	}
	if repo.LockedRev == "" {
		return lockUnknown, fmt.Sprintf("the terminal consumes %s as a flake input but the apply recorded "+
			"no locked rev for it, so it cannot be shown the built system contains the gated commit "+
			"(fail closed; check `pn workspace info` and the terminal's flake.lock)", repo.Name)
	}
	if len(gatedCommits) == 0 {
		return lockUnknown, fmt.Sprintf("could not recover the gated commit for %s from the patch-id scan, "+
			"so it cannot be checked against locked rev %s (fail closed)", repo.Name, shortRev(repo.LockedRev))
	}
	for _, sha := range gatedCommits {
		if d.PatchID.IsAncestor(ctx, repo.Path, sha, repo.LockedRev) {
			return lockSatisfied, ""
		}
	}
	reason := fmt.Sprintf("gated commit %s is not in %s, the flake.lock rev the apply built %s "+
		"from — the change is committed locally but not in the applied system; push it, relock the "+
		"terminal, then re-apply", shortRev(gatedCommits[0]), shortRev(repo.LockedRev), repo.Name)
	// Name the fail-closed ASSUMPTION when the record cannot speak to it, so this
	// verdict is diagnosable rather than looking like a plain "not shipped". Without
	// this the schema-2 window (see the branch table) reads as a wrong answer instead
	// of a conservative one.
	if repo.AppliedStateSchema < pnOverrideRecordSchema {
		reason += fmt.Sprintf(" — note: that apply's pn applied-state record is schema %d, which does not "+
			"record whether the apply OVERRODE this input; the lock condition is enforced in the absence "+
			"of that record (fail closed), so if the apply did override it, the next apply by a pn "+
			"writing schema %d will record the override and the gate will resolve",
			repo.AppliedStateSchema, pnOverrideRecordSchema)
	}
	return lockMissing, reason
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
