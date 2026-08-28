package snapshot

// ActNow is the ONE pure predicate that decides team-PR panel membership,
// mirroring the NeedsAttention convention (attention.go): a single exported
// function, consumed by every reader that needs to know whether a team PR is
// "ready for review" so they can never diverge by re-deriving the question
// independently.
//
// Per the operator's 2026-08-24 ruling on pg2-4dz88.7.1 (carried on
// pg2-4dz88.7.3, verbatim): "the top group is for team PRs for which there is
// something for me to do. this means the PR is ready for review: not draft,
// CICD is green (except for policy-bot), either no bot review or bot review
// which doesn't say the PR shouldn't be merged ..., no merge conflicts."
//
//   - "not draft" is deliberately ABSENT from this predicate. Per the
//     2026-08-28 ruling superseding the earlier open fork on this bead: Build's
//     admission gate never lets a draft team PR become an individual TeamRow
//     at all (it stays folded into pg2-4dz88.7.6's opaque DroppedCount), so
//     there is no live code path that could produce a TeamRow{Draft: true}
//     for this predicate to exclude. Encoding a clause with no reachable
//     false case would be untestable and misleading.
//   - "CICD is green" is row.CIStatus == "success" — cirollup's own "no
//     countable run at all" (Rollup.State "none") and "still running"
//     ("pending") states are deliberately NOT treated as green: a build that
//     has not definitively passed is not yet "ready for review". Policy-bot
//     exclusion happens upstream, inside CIStatus itself (Build already
//     derives it via cirollup.Compute with the per-repo Excluder threaded
//     through BuilderInput.CheckInterpretersByRepo) — this predicate does not
//     need, and must not add, a second exclusion mechanism.
//   - "no bot review, or a bot review that doesn't say it can't be merged" is
//     !row.BotDisapproved — see that field's doc for the allowlist-based
//     read source and the staleness decision.
//   - "no merge conflicts" is !row.HasConflicts, already computed from
//     api.PR.HasConflict().
func ActNow(row TeamRow) bool {
	return row.CIStatus == "success" && !row.BotDisapproved && !row.HasConflicts
}

// PartitionTeamPanels splits rows into the ACT-NOW panel and its EXHAUSTIVE
// logical complement, the BLOCKED panel. Panel 2 is NEVER a second,
// independently hand-rolled predicate — per pg2-4dz88.7.3's load-bearing
// requirement, it is simply "!ActNow(row)", so every row lands in exactly one
// panel by construction and the two slices can never both miss a row (the
// exact O3 grooming-review hole a second enumeration would reproduce: the
// operator's own panel-2 description, "failed build or failed bot review or
// merge conflict", is illustrative, not exhaustive — it does not, for
// instance, cover a PR whose CI is merely still pending).
//
// Precondition: this function partitions whatever []TeamRow it is given. It
// does NOT itself filter Hidden (pg2-4dz88.4) or the silently-dropped set
// (pg2-4dz88.7.6, Snapshot.DroppedCount) — those are separate concerns applied
// upstream (Build's admission switch) or by a different consumer. A caller
// MUST apply that filtering itself before calling this function; passing the
// full tracked set (already Hidden-filtered per the caller's own policy) is
// the caller's responsibility, exactly as NeedsAttention's callers own drawing
// their own boundaries around its inputs.
//
// Both returned slices are non-nil (empty, never nil) even when a panel has
// no members, mirroring Build's own Mine/Team zero-value convention, so a
// JSON consumer sees `[]`, never `null`, for an empty panel.
func PartitionTeamPanels(rows []TeamRow) (actNow, blocked []TeamRow) {
	actNow = []TeamRow{}
	blocked = []TeamRow{}
	for _, r := range rows {
		if ActNow(r) {
			actNow = append(actNow, r)
		} else {
			blocked = append(blocked, r)
		}
	}
	return actNow, blocked
}
