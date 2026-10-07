package interpret

import (
	"encoding/json"
	"testing"
)

func oidp(s string) *string { return &s }

func rev(author, state string, oid *string) prReview {
	return prReview{ID: author + state, Author: author, State: state, CommitOID: oid}
}

// TestComputeReviewStaleness pins the per-commit staleness axis (bead
// pg2-w7zai.2): my review is stale when none of my submitted reviews was made
// against the PR's current head, a standing teammate approval is a human
// APPROVED review against the head, and an unreported commit is unknown, never
// stale.
func TestComputeReviewStaleness(t *testing.T) {
	const head = "h2"
	allow := []string{"review-bot"}
	cases := []struct {
		name         string
		head         string
		reviews      []prReview
		wantSelf     bool
		wantStanding bool
	}{
		{"no reviews", head, nil, false, false},
		{"my review at the head stands", head, []prReview{rev("me", "APPROVED", oidp("h2"))}, false, false},
		{"my review before a push is stale", head, []prReview{rev("me", "APPROVED", oidp("h1"))}, true, false},
		{"my commented review before a push is stale", head, []prReview{rev("me", "COMMENTED", oidp("h1"))}, true, false},
		{"my changes-requested review before a push is stale", head, []prReview{rev("me", "CHANGES_REQUESTED", oidp("h1"))}, true, false},
		{"a later review at the head clears an earlier stale one, in either order", head, []prReview{rev("me", "APPROVED", oidp("h1")), rev("me", "COMMENTED", oidp("h2"))}, false, false},
		{"the same, newest first", head, []prReview{rev("me", "COMMENTED", oidp("h2")), rev("me", "APPROVED", oidp("h1"))}, false, false},
		{"a force-pushed-away commit is stale", head, []prReview{rev("me", "APPROVED", oidp(""))}, true, false},
		{"a dismissed review at the head is stale", head, []prReview{rev("me", "DISMISSED", oidp("h2"))}, true, false},
		{"my pending review is not a submitted review", head, []prReview{rev("me", "PENDING", oidp("h1"))}, false, false},
		{"an unreported commit is unknown, not stale", head, []prReview{rev("me", "APPROVED", nil)}, false, false},
		{"an unknown head is unknown, not stale", "", []prReview{rev("me", "APPROVED", oidp("h1"))}, false, false},
		{"a teammate approval at the head stands", head, []prReview{rev("me", "APPROVED", oidp("h1")), rev("bob", "APPROVED", oidp("h2"))}, true, true},
		{"a teammate approval before the push does not stand", head, []prReview{rev("bob", "APPROVED", oidp("h1"))}, false, false},
		{"a teammate approval with an unreported commit does not count as standing", head, []prReview{rev("bob", "APPROVED", nil)}, false, false},
		{"a teammate who then requested changes at the head does not stand", head, []prReview{rev("bob", "APPROVED", oidp("h2")), rev("bob", "CHANGES_REQUESTED", oidp("h2"))}, false, false},
		{"a bot approval at the head does not stand for the team", head, []prReview{rev("review-bot", "APPROVED", oidp("h2")), rev("github-actions", "APPROVED", oidp("h2"))}, false, false},
		{"my own approval is not a teammate's", head, []prReview{rev("me", "APPROVED", oidp("h2"))}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := prShow{HeadSHA: tc.head, Reviews: tc.reviews}
			self, standing := computeReviewStaleness(pr, "me", allow)
			if self != tc.wantSelf || standing != tc.wantStanding {
				t.Fatalf("computeReviewStaleness = (self stale %v, teammate approval standing %v), want (%v, %v)", self, standing, tc.wantSelf, tc.wantStanding)
			}
		})
	}
}

func TestComputeReviewStaleness_NoSelfLogin(t *testing.T) {
	pr := prShow{HeadSHA: "h2", Reviews: []prReview{rev("me", "APPROVED", oidp("h1"))}}
	if self, _ := computeReviewStaleness(pr, "", nil); self {
		t.Fatal("with no configured self login no review is mine")
	}
}

// TestApprovals_CarryReviewStaleness checks the fields reach the served
// Approvals and decode from the connector's wire shape, where an absent
// commit_oid is unknown and an empty one is a commit that is gone.
func TestApprovals_CarryReviewStaleness(t *testing.T) {
	raw := `{"repo":"o/r","number":1,"state":"open","head_sha":"h2","reviews":[
		{"id":"1","author":"me","state":"APPROVED","commit_oid":"h1"},
		{"id":"2","author":"bob","state":"APPROVED","commit_oid":"h2"}]}`
	var pr prShow
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		t.Fatal(err)
	}
	a := computeApprovals(pr, "me", nil, nil)
	if !a.SelfReviewStale || !a.HumanApprovalStanding {
		t.Fatalf("approvals = %+v, want self_review_stale and human_approval_standing", a)
	}
	b, _ := json.Marshal(Approvals{})
	if string(b) != `{"human_approvers":0,"human_approved":false,"self_approved":false,"human_changes_requested":false,"bot_verdict":"","waiting_on_me":false}` {
		t.Fatalf("zero Approvals JSON = %s; the new fields must be omitted when false so stored rows and goldens do not churn", b)
	}

	var old prShow
	if err := json.Unmarshal([]byte(`{"head_sha":"h2","reviews":[{"id":"1","author":"me","state":"APPROVED"}]}`), &old); err != nil {
		t.Fatal(err)
	}
	if got := computeApprovals(old, "me", nil, nil); got.SelfReviewStale {
		t.Fatalf("facts stored before the connector reported commit_oid read as stale: %+v", got)
	}
	var gone prShow
	if err := json.Unmarshal([]byte(`{"head_sha":"h2","reviews":[{"id":"1","author":"me","state":"APPROVED","commit_oid":""}]}`), &gone); err != nil {
		t.Fatal(err)
	}
	if got := computeApprovals(gone, "me", nil, nil); !got.SelfReviewStale {
		t.Fatalf("an empty commit_oid (commit gone) must read as stale: %+v", got)
	}
}
