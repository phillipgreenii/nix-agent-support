package classify

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
)

// Summary classifies a refreshed summary against the one stored before it.
//
// A nil prev, a first sighting (next.First) and a Gone summary on either side
// have nothing to compare and yield the zero Result: the baseline rule.
//
// Mergeability is not recomputed. next.Summary.Mergeable is already the carried
// value (entity.Carry), and next.MergeabilityChanged says a known value moved to
// a different known one, so UNKNOWN is never a change; the previous known value
// is next.PrevMergeable. A rename is read from the references, not from
// next.Rename: the alias update that sets Rename happens before the
// transaction, and a retry after a failed transaction would no longer see it.
func Summary(prev *summary.Summary, next summary.Stored, bodyCap int) Result {
	if prev == nil || next.First || prev.Gone || next.Summary.Gone {
		return Result{}
	}
	p, n := *prev, next.Summary
	var b builder

	// State: the three kinds are moves between the states GitHub reports. A
	// side without a state is missing data, never a move.
	ps, ns := strings.ToUpper(strings.TrimSpace(p.State)), strings.ToUpper(strings.TrimSpace(n.State))
	if diff(&b, "state", p.State, n.State) && ps != "" && ns != "" && ps != ns {
		switch {
		case ns == "OPEN" && (ps == "MERGED" || ps == "CLOSED"):
			b.kind(KindReopened)
		case ns == "MERGED":
			b.kind(KindMerged)
		case ns == "CLOSED":
			b.kind(KindClosed)
		}
	}
	if diff(&b, "draft", p.IsDraft, n.IsDraft) {
		b.kind(KindDraftChanged)
	}
	if diff(&b, "head_sha", p.HeadRefOid, n.HeadRefOid) && p.HeadRefOid != "" && n.HeadRefOid != "" {
		b.kind(KindHeadChanged)
	}
	if diff(&b, "base", p.BaseRefName, n.BaseRefName) && p.BaseRefName != "" && n.BaseRefName != "" {
		b.kind(KindBaseChanged)
	}
	if diff(&b, "checks_state", p.ChecksState, n.ChecksState) && p.ChecksState != "" && n.ChecksState != "" {
		b.kind(KindCIChanged)
	}
	if next.MergeabilityChanged {
		b.kind(KindMergeabilityChanged)
		b.field("mergeable", jsonOf(next.PrevMergeable), jsonOf(n.Mergeable))
	}

	// Review and feedback signals: the decision and the counts.
	reviewMoved := diff(&b, "review_decision", p.ReviewDecision, n.ReviewDecision)
	if diff(&b, "review_count", p.ReviewCount, n.ReviewCount) {
		reviewMoved = true
	}
	if reviewMoved {
		b.kind(KindReviewChanged)
	}
	if diff(&b, "comment_count", p.CommentCount, n.CommentCount) {
		b.kind(KindFeedbackChanged)
	}

	// Rename: both references known and different. Both are JSON strings.
	if pr, nr := p.Ref(), n.Ref(); pr != "" && nr != "" && pr != nr {
		b.kind(KindRenamed)
		b.field("ref", jsonOf(pr), jsonOf(nr))
	}

	// Content that carries no kind.
	diff(&b, "title", p.Title, n.Title)
	diffBody(&b, "body", p.Body, n.Body, bodyCap)
	diff(&b, "author", p.Author, n.Author)
	diff(&b, "label_count", p.LabelCount, n.LabelCount)
	diffSet(&b, "labels", p.Labels, n.Labels)
	diff(&b, "review_thread_count", p.ReviewThreadCount, n.ReviewThreadCount)
	diff(&b, "head_ref", p.HeadRefName, n.HeadRefName)

	return b.result()
}
