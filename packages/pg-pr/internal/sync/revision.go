package sync

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/checkinterpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ciRollupFromSync maps []api.CIRun to a store.CIRollup. Classification and
// aggregation are delegated to internal/cirollup — the single source of
// truth for "is CI failed?" (pg2-qs46b). excl drops advisory checks (e.g.
// policy-bot) from the rollup entirely.
//
// now is an injectable clock; when nil it defaults to time.Now.
func ciRollupFromSync(runs []api.CIRun, now func() time.Time, excl *cirollup.Excluder) store.CIRollup {
	if now == nil {
		now = time.Now
	}
	capturedAt := now().UTC().Format(time.RFC3339)
	if len(runs) == 0 {
		return store.CIRollup{State: "none", CapturedAt: capturedAt}
	}
	r := cirollup.Compute(runs, excl)
	return store.CIRollup{
		State:      r.State,
		Passed:     r.Passed,
		Failed:     r.Failed,
		Pending:    r.Pending,
		CapturedAt: capturedAt,
	}
}

// excluderFromCheckInterpreters builds a cirollup.Excluder from the union of
// every configured check-interpreter's Patterns, regardless of Type — any
// check name a configured interpreter claims must also be excluded from the
// CI rollup exactly as excluded_ci_checks used to exclude it (pg2-4dz88.2.6's
// carried-forward invariant). cirollup and checkinterpret deliberately do not
// share an abstraction (docs/decisions/ci-gate.md's DEC-CIGATE-1), so this is
// the one place that reconciles "claimed by an interpreter" with "excluded
// from the rollup" by re-deriving an Excluder from the same pattern lists a
// Registry was built from.
func excluderFromCheckInterpreters(interpreters []config.CheckInterpreterConfig) *cirollup.Excluder {
	var patterns []string
	for _, ip := range interpreters {
		patterns = append(patterns, ip.Patterns...)
	}
	return cirollup.NewExcluder(patterns)
}

// checkInterpretersFrom converts a repo's configured
// []config.CheckInterpreterConfig into the plain []checkinterpret.Interpreter
// shape checkinterpret.New expects. The two types mirror each other
// field-for-field (see checkinterpret.Interpreter's doc comment); this is the
// conversion that package's doc comment names as this bead's (pg2-4dz88.2.6)
// job, so internal/checkinterpret never needs to import internal/config.
func checkInterpretersFrom(cfgs []config.CheckInterpreterConfig) []checkinterpret.Interpreter {
	out := make([]checkinterpret.Interpreter, len(cfgs))
	for i, c := range cfgs {
		out[i] = checkinterpret.Interpreter{Patterns: c.Patterns, Type: c.Type}
	}
	return out
}

// gateStateFromSync scans runs for the first one claimed as
// checkinterpret.ApprovalGateType by reg (in runs order — deterministic when
// more than one run is claimed, though that shouldn't normally happen) and
// classifies it into a store.GateState. now is an injectable clock, mirroring
// ciRollupFromSync; nil defaults to time.Now.
//
// ok is false when NO run is claimed as the approval-gate type — callers
// should skip the SetRevisionGateState write entirely in that case rather
// than force a value: a brand-new revision already defaults to "unknown"
// (store/revision.go's RecordRevision), so skipping is equivalent to writing
// Unknown for a revision seen for the first time, and it deliberately leaves
// an EXISTING revision's last-recorded gate state untouched if a later tick's
// CI runs stop including any gate-claimed check (rather than clobbering a
// real prior observation with a manufactured "unknown").
func gateStateFromSync(runs []api.CIRun, now func() time.Time, reg *checkinterpret.Registry) (store.GateState, bool) {
	if now == nil {
		now = time.Now
	}
	for _, r := range runs {
		typ, claimed := reg.Claim(r.Name)
		if !claimed || typ != checkinterpret.ApprovalGateType {
			continue
		}
		result := checkinterpret.ClassifyApprovalGate(r.Conclusion, r.Description)
		return store.GateState{
			State:      string(result.State),
			N:          result.N,
			M:          result.M,
			CapturedAt: now().UTC().Format(time.RFC3339),
		}, true
	}
	return store.GateState{}, false
}

// mergeClaimedRuns returns newRuns with any run from originalRuns that reg
// claims (via Registry.Claim, by run Name) appended — unless newRuns already
// carries a run with that same Name, in which case the newRuns entry wins
// (idempotence/no-duplication guard).
//
// This exists for reconcileTruncatedCI (sync.go, pg2-4dz88.2.7) and
// enrichOnePR (sync.go, pg2-g9fu0): both functions wholesale-replace (or, for
// enrichOnePR, always source) CIRuns from the dedicated CICD provider, but
// that provider is structurally Actions/CheckRun-only and can never carry a
// commit-status run (only GraphQL's statusCheckRollup can) — so a
// check-interpreter-claimed run such as an approval gate would otherwise be
// silently discarded on every truncated tick (reconcileTruncatedCI) or every
// per-PR refresh (enrichOnePR). Merging the claimed run(s) back in preserves
// the gate's rich, Description-bearing observation in both cases.
//
// A nil/empty reg or originalRuns is a no-op (returns newRuns unchanged),
// mirroring checkinterpret.Registry's own "nil-safe, claims nothing" and
// "empty is valid" conventions.
func mergeClaimedRuns(newRuns, originalRuns []api.CIRun, reg *checkinterpret.Registry) []api.CIRun {
	if reg == nil || len(originalRuns) == 0 {
		return newRuns
	}
	existing := make(map[string]struct{}, len(newRuns))
	for _, r := range newRuns {
		existing[r.Name] = struct{}{}
	}
	out := newRuns
	for _, r := range originalRuns {
		if _, claimed := reg.Claim(r.Name); !claimed {
			continue
		}
		if _, dup := existing[r.Name]; dup {
			continue
		}
		out = append(out, r)
		existing[r.Name] = struct{}{}
	}
	return out
}

// submittedReview is a filtered review targeted at a specific commit.
type submittedReview struct {
	// Approver is the GitHub login the review is attributed to — self for
	// mySubmittedReviews, the reviewing teammate for othersApprovedReviews.
	// Feeds store.SetApproval's per-approver row (pg2-4dz88.1.5).
	Approver    string
	CommitSHA   string
	State       string // store enum: approved/changes-requested/commented
	SubmittedAt string
	// Dismissed marks a review the code host reported as DISMISSED. State is
	// "approved" for such a review (the host does not report what it said
	// before the dismissal) and it lands in the per-approver table as a STALE
	// approval — never dropped (INV-APPROVAL-3, pg2-4dz88.1.7). recordApproval
	// routes a Dismissed review to SetDismissedApproval so it lands stale
	// rather than current.
	Dismissed bool
}

// mySubmittedReviews filters enriched.Reviews to reviews authored by self
// with a mappable state, returning the store-enum state + commit SHA + timestamp.
// GitHub review state (UPPERCASE) → store enum:
//
//	APPROVED → "approved"
//	CHANGES_REQUESTED → "changes-requested"
//	COMMENTED → "commented"
//	DISMISSED → "approved" + Dismissed (a STALE approval, INV-APPROVAL-3)
//	PENDING/other → skipped
func mySubmittedReviews(reviews []api.Review, self string) []submittedReview {
	if self == "" {
		return nil
	}
	var out []submittedReview
	for _, r := range reviews {
		if r.Author != self {
			continue
		}
		var storeState string
		var dismissed bool
		switch r.State {
		case "APPROVED":
			storeState = "approved"
		case "CHANGES_REQUESTED":
			storeState = "changes-requested"
		case "COMMENTED":
			storeState = "commented"
		case "DISMISSED":
			// A dismissed review is a STALE approval, not an absent one
			// (INV-APPROVAL-3). It used to fall through the default below and
			// vanish, so an approver who DID approve was indistinguishable
			// from one who never did (pg2-4dz88.1.7).
			storeState, dismissed = "approved", true
		default:
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       storeState,
			SubmittedAt: r.SubmittedAt,
			Dismissed:   dismissed,
		})
	}
	return out
}

// othersApprovedReviews returns the NON-SELF (teammate) reviews that are, or
// once were, approvals — the inverse-self counterpart of mySubmittedReviews.
// It underpins the store-derived "someone else approved" marker used by the
// attention predicate (pg2-4c5i.13). The viewer's OWN approval is deliberately
// EXCLUDED so it can never be mistaken for a teammate's approval (X3). A
// teammate's COMMENTED/CHANGES_REQUESTED review does not put the PR "off the
// hook" and is not returned. State is always "approved" for the entries
// returned; a DISMISSED teammate review is returned with Dismissed set — a
// STALE approval, never an absent one (INV-APPROVAL-3, pg2-4dz88.1.7) — and
// recordApproval routes it to SetDismissedApproval accordingly.
//
// See othersChangesRequestedReviews for the CHANGES_REQUESTED counterpart
// (pg2-4dz88.1.8), which feeds the SAME per-approver pr_approval table.
func othersApprovedReviews(reviews []api.Review, self string) []submittedReview {
	var out []submittedReview
	for _, r := range reviews {
		if self != "" && r.Author == self {
			continue // the viewer's own approval is NOT a teammate approval (X3)
		}
		var dismissed bool
		switch r.State {
		case "APPROVED":
			// A currently-standing teammate approval.
		case "DISMISSED":
			dismissed = true
		default:
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       "approved",
			SubmittedAt: r.SubmittedAt,
			Dismissed:   dismissed,
		})
	}
	return out
}

// othersChangesRequestedReviews returns the NON-SELF (teammate)
// CHANGES_REQUESTED reviews (pg2-4dz88.1.8) — the changes-requested
// counterpart of othersApprovedReviews, feeding the SAME per-approver
// pr_approval table so "a teammate explicitly asked for changes" becomes
// representable and distinct from both an absent record and that same
// approver's own APPROVED/STALE state. The viewer's OWN review is excluded
// for the same reason as othersApprovedReviews (X3): it is never a teammate
// review. A teammate's COMMENTED review is deliberately dropped here too —
// it MUST NOT be conflated with CHANGES_REQUESTED, so it is neither returned
// by this function nor by othersApprovedReviews.
//
// A teammate asking for changes does not put the PR "off the hook", so
// callers MUST NOT feed these entries into the others-approved ingest loop.
// State is always "changes-requested" for the entries returned.
func othersChangesRequestedReviews(reviews []api.Review, self string) []submittedReview {
	var out []submittedReview
	for _, r := range reviews {
		if self != "" && r.Author == self {
			continue // the viewer's own review is NOT a teammate review (X3)
		}
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       "changes-requested",
			SubmittedAt: r.SubmittedAt,
		})
	}
	return out
}

// recordApproval writes one observed review as a per-approver row
// (pg2-4dz88.1.5): a DISMISSED review lands as a STALE approval
// (pg2-4dz88.1.7, INV-APPROVAL-3), every other state as the state it was
// observed in.
func (e *Engine) recordApproval(ctx context.Context, prID int64, rv submittedReview) error {
	if rv.Dismissed {
		return e.deps.Store.SetDismissedApproval(ctx, prID, rv.Approver, rv.CommitSHA, rv.SubmittedAt)
	}
	return e.deps.Store.SetApproval(ctx, prID, rv.Approver, rv.CommitSHA, rv.State, rv.SubmittedAt)
}
