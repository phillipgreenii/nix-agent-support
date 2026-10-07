package interpret

import "strings"

// computeReviewStaleness is the per-commit staleness axis of approvals (bead
// pg2-w7zai.2): it answers whether the operator's own review has gone stale
// behind a push, and whether a teammate's approval still stands for the
// current head. The connector reports, for each review, the commit it was
// submitted against (schema.PRReview.CommitOID), which is what makes the
// answer possible.
//
// selfStale is true iff the operator has submitted at least one review and
// none of them stands for the PR's current head. A review stands when it is
// not DISMISSED and was submitted against pr.HeadSHA; PENDING reviews are not
// submitted, so they are ignored. It is order independent on purpose: the
// show payload lists reviews newest first, and any submitted review made
// against the head means the operator has looked at this head. An empty
// commit_oid is GitHub's null commit (the reviewed commit was since deleted,
// for example by a force push), which cannot match a real head, so it is
// stale. An ABSENT commit_oid, an unknown head, or no configured self login
// is unknown, and unknown is never stale (INV-ATTNEVAL-6): facts stored before
// the connector reported the commit therefore raise nothing until the entity
// is hydrated again.
//
// teammateApprovalStanding is true iff some human other than the operator has
// an APPROVED review against the current head and no CHANGES_REQUESTED review
// of their own against it. Bots (the approver allowlist, "[bot]" accounts and
// knownBotLogins) never count, matching HumanApprovers (operator ruling
// 2026-10-02, pg2-k8lri). An approval whose commit is not reported does not
// count as standing.
func computeReviewStaleness(pr prShow, self string, approverAllowlist []string) (selfStale, teammateApprovalStanding bool) {
	head := pr.HeadSHA
	if head == "" {
		return false, false
	}
	allow := toSet(approverAllowlist)

	var reviewed, standing, unknown bool
	approvedAtHead := map[string]struct{}{}
	changesRequestedAtHead := map[string]struct{}{}
	for _, r := range pr.Reviews {
		atHead := r.CommitOID != nil && *r.CommitOID == head
		if self != "" && r.Author == self {
			switch r.State {
			case "APPROVED", "CHANGES_REQUESTED", "COMMENTED":
				reviewed = true
				switch {
				case r.CommitOID == nil:
					unknown = true
				case atHead:
					standing = true
				}
			case "DISMISSED":
				reviewed = true
			}
			continue
		}
		if !atHead {
			continue
		}
		_, isBot := allow[r.Author]
		if isBot || isBotLogin(r.Author) {
			continue
		}
		switch strings.ToUpper(r.State) {
		case "APPROVED":
			approvedAtHead[r.Author] = struct{}{}
		case "CHANGES_REQUESTED":
			changesRequestedAtHead[r.Author] = struct{}{}
		}
	}

	for author := range approvedAtHead {
		if _, withdrawn := changesRequestedAtHead[author]; !withdrawn {
			teammateApprovalStanding = true
			break
		}
	}
	selfStale = reviewed && !standing && !unknown
	return selfStale, teammateApprovalStanding
}
