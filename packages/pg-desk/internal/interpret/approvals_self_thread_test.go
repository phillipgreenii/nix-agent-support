package interpret

import "testing"

// tc builds one inline review-thread comment of thread tid.
func tc(id, author, tid string, mod func(*prComment)) prComment {
	c := prComment{ID: id, Author: author, ThreadID: id, ReviewThreadID: tid}
	if mod != nil {
		mod(&c)
	}
	return c
}

// selfThreadPR is a team PR (author "author", head h2) whose operator "me"
// review is stale (made against h1), with the given inline comments.
func selfThreadPR(comments ...prComment) prShow {
	return prShow{
		State: "open", Author: "author", HeadSHA: "h2", MergeStateStatus: "CLEAN",
		Reviews:  []prReview{rev("me", "COMMENTED", oidp("h1"))},
		Comments: comments,
	}
}

func TestSelfHasUnansweredThread(t *testing.T) {
	resolved := func(c *prComment) { c.Resolved = true }
	outdated := func(c *prComment) { c.ThreadOutdated = true }
	tests := []struct {
		name string
		pr   prShow
		self string
		want bool
	}{
		{"operator comment with no author reply", selfThreadPR(tc("c1", "me", "t1", nil)), "me", true},
		{"author replied in the thread", selfThreadPR(tc("c1", "me", "t1", nil), tc("c2", "author", "t1", nil)), "me", false},
		{"resolved thread", selfThreadPR(tc("c1", "me", "t1", resolved)), "me", false},
		{"outdated thread", selfThreadPR(tc("c1", "me", "t1", outdated)), "me", false},
		{"another reviewer's thread only", selfThreadPR(tc("c1", "mate", "t1", nil)), "me", false},
		{"operator only replied in a teammate's thread", selfThreadPR(tc("c1", "mate", "t1", nil), tc("c2", "me", "t1", nil)), "me", true},
		{"author answered a different thread", selfThreadPR(tc("c1", "me", "t1", nil), tc("c2", "author", "t2", nil)), "me", true},
		{"top-level comment is never a thread", selfThreadPR(prComment{ID: "c1", Author: "me"}), "me", false},
		{"legacy payload, each inline comment stands alone", selfThreadPR(
			tc("c1", "me", "", nil), tc("c2", "author", "", nil),
		), "me", true},
		{"legacy payload, author comment is its own thread", selfThreadPR(tc("c2", "author", "", nil)), "me", false},
		{"unknown self", selfThreadPR(tc("c1", "me", "t1", nil)), "", false},
		{"unknown pr author", func() prShow {
			p := selfThreadPR(tc("c1", "me", "t1", nil))
			p.Author = ""
			return p
		}(), "me", false},
		{"pending review comments are excluded", func() prShow {
			p := selfThreadPR()
			p.Reviews = append(p.Reviews, prReview{
				ID: "p", Author: "me", State: "pending",
				Comments: []prComment{tc("c1", "me", "t1", nil)},
			})
			return p
		}(), "me", false},
		{"submitted review comments count", func() prShow {
			p := selfThreadPR()
			p.Reviews = []prReview{{
				ID: "r", Author: "me", State: "COMMENTED", CommitOID: oidp("h1"),
				Comments: []prComment{tc("c1", "me", "t1", nil)},
			}}
			return p
		}(), "me", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selfHasUnansweredThread(tt.pr, tt.self); got != tt.want {
				t.Errorf("selfHasUnansweredThread = %v, want %v", got, tt.want)
			}
			if got := computeApprovals(tt.pr, tt.self, nil, nil).SelfUnansweredThread; got != tt.want {
				t.Errorf("Approvals.SelfUnansweredThread = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideTeam_UnansweredOperatorThread(t *testing.T) {
	green := ciRollupResult{State: "success"}
	open := tc("c1", "me", "t1", nil)
	tests := []struct {
		name       string
		mod        func(*prShow)
		wantWait   WaitingOn
		wantReason string
		wantPanel  string
	}{
		{
			"stale review with an open operator thread waits on the author", nil,
			WaitingOnAuthor, ReasonUnansweredThread, PanelTeamAwaitingOwner,
		},
		{"a teammate approval of the current head does not change it", func(p *prShow) {
			p.Reviews = append(p.Reviews, rev("mate", "APPROVED", oidp("h2")))
		}, WaitingOnAuthor, ReasonUnansweredThread, PanelTeamAwaitingOwner},
		{"a live review request does not change it", func(p *prShow) {
			p.ReviewRequests = []string{"me"}
		}, WaitingOnAuthor, ReasonUnansweredThread, PanelTeamAwaitingOwner},
		{"an acting-reviewer COMMENTED review with no stale state", func(p *prShow) {
			p.Reviews = []prReview{rev("me", "COMMENTED", oidp("h2"))}
		}, WaitingOnAuthor, ReasonUnansweredThread, PanelTeamAwaitingOwner},
		{"the author replied: back to stale-review", func(p *prShow) {
			p.Comments = append(p.Comments, tc("c2", "author", "t1", nil))
		}, WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe},
		{"the thread is resolved: back to stale-review", func(p *prShow) {
			p.Comments = []prComment{tc("c1", "me", "t1", func(c *prComment) { c.Resolved = true })}
		}, WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe},
		{"the thread is outdated: back to stale-review", func(p *prShow) {
			p.Comments = []prComment{tc("c1", "me", "t1", func(c *prComment) { c.ThreadOutdated = true })}
		}, WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe},
		{"another reviewer's unanswered thread stays stale-review", func(p *prShow) {
			p.Comments = []prComment{tc("c1", "mate", "t1", nil)}
		}, WaitingOnMe, ReasonStaleReview, PanelTeamAwaitingMe},
		{"operator only replied in a teammate's thread", func(p *prShow) {
			p.Comments = []prComment{tc("c1", "mate", "t1", nil), tc("c2", "me", "t1", nil)}
		}, WaitingOnAuthor, ReasonUnansweredThread, PanelTeamAwaitingOwner},
		{"pending draft thread plus a live request stays review-requested", func(p *prShow) {
			p.ReviewRequests = []string{"me"}
			p.Reviews = []prReview{{
				ID: "p", Author: "me", State: "PENDING",
				Comments: []prComment{tc("c1", "me", "t1", nil)},
			}}
			p.Comments = nil
		}, WaitingOnMe, ReasonReviewRequested, PanelTeamAwaitingMe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := selfThreadPR(open)
			if tt.mod != nil {
				tt.mod(&p)
			}
			w, r, panel := decideFromReviews(OwnershipTeam, p, green)
			if w != tt.wantWait || r != tt.wantReason || panel != tt.wantPanel {
				t.Errorf("got (%q, %q, panel %q), want (%q, %q, panel %q)", w, r, panel, tt.wantWait, tt.wantReason, tt.wantPanel)
			}
		})
	}
}
