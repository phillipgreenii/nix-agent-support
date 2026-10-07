package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// This file tests review_submit when head_sha is an EARLIER head of the PR
// (INV-REVHEAD-1..3). The request helpers in review_submit_test.go build
// requests at headA; here the live head is headB and headA is still a commit
// of the PR unless a test says otherwise.

const headC = "c0c1c2c3c4c5c6c7c8c9d0d1d2d3d4d5d6d7d8d9"

// mainPatch is main.go's diff at headA. Positions (RIGHT/LEFT line -> position):
// RIGHT 1->1, 5->6, 21->8, 22->10, 23->11; LEFT 3->3, 21->9.
var mainPatch = strings.Join([]string{
	"@@ -1,4 +1,5 @@",
	" package main",
	" ",
	"-func old() {}",
	"+func new() {}",
	"+func extra() {}",
	" var x = 1",
	"@@ -20,3 +21,3 @@",
	" a",
	"-b",
	"+c",
	" d",
}, "\n")

// movedFixture is a PR whose live head is headB while headA is one of its
// commits, with a few files in the difference at headA.
func movedFixture(t *testing.T) *submitFixture {
	t.Helper()
	f := newSubmitFixture(t)
	f.host.head = headB
	f.host.commits = []string{headA, headB}
	f.host.compared = []github.ComparedFile{
		{Path: "main.go", Patch: mainPatch},
		{Path: "big.go"}, // the host omitted the patch
		{Path: "new.go", Patch: "@@ -0,0 +1,3 @@\n+a\n+b\n+c"},
		{Path: "renamed.go", PreviousPath: "old.go", Patch: "@@ -1,2 +1,2 @@\n-x\n+y\n z"},
	}
	return f
}

func (f *submitFixture) written() []github.ReviewWriteItem {
	var out []github.ReviewWriteItem
	for _, w := range f.host.writes {
		out = append(out, w.items...)
	}
	return out
}

// TestSubmitLiveHeadKeepsTheThreadPath: a head_sha equal to the live head
// behaves exactly as before: no history or compare read, line-and-side anchors.
func TestSubmitLiveHeadKeepsTheThreadPath(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.compared = []github.ComparedFile{{Path: "main.go", Patch: mainPatch}}
	res := f.mustSubmit(req("overall", point("main.go", 22, "use c")))
	if res.HeadMoved || res.HeadSHA != headA || res.LiveHeadSHA != headA {
		t.Fatalf("result = %+v", res)
	}
	if f.host.historyReads != 0 || f.host.compareReads != 0 {
		t.Errorf("the live-head path must not read the history or a comparison: %d %d", f.host.historyReads, f.host.compareReads)
	}
	items := f.written()
	if len(items) != 1 || items[0].AtCommit() || items[0].Line != 22 || items[0].Side != "RIGHT" || items[0].Position != 0 {
		t.Fatalf("items = %+v", items)
	}
	if strings.Contains(f.host.creates[0], "Saved at") {
		t.Errorf("a live-head review carries no saved-at line: %q", f.host.creates[0])
	}
}

// TestSubmitEarlierHeadUsesThePositionPath is the main case: headA is an
// earlier commit of the PR, so the review is created and filled AT headA.
func TestSubmitEarlierHeadUsesThePositionPath(t *testing.T) {
	f := movedFixture(t)
	res := f.mustSubmit(req("overall", point("main.go", 22, "use c"), point("new.go", 2, "why b?")))

	if res.Status != pr.StatusPosted || res.State != "pending" || res.Added != 2 || res.Body != pr.BodyWritten {
		t.Fatalf("result = %+v", res)
	}
	if res.HeadSHA != headA || !res.HeadMoved || res.LiveHeadSHA != headB {
		t.Fatalf("head_sha/head_moved/live_head_sha = %q/%v/%q", res.HeadSHA, res.HeadMoved, res.LiveHeadSHA)
	}
	rev := f.host.pending[0]
	if rev.commit != headA {
		t.Errorf("the review is created at the saved-at head, got %q", rev.commit)
	}
	wantBody := sectionOf(headA, "overall\n\nSaved at a1b2c3d; PR head was b9b8b7b when saved.")
	if len(f.host.creates) != 1 || f.host.creates[0] != wantBody {
		t.Errorf("created body = %q, want %q", f.host.creates, wantBody)
	}
	items := f.written()
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	for i, want := range []struct {
		path string
		pos  int
	}{{"main.go", 10}, {"new.go", 2}} {
		it := items[i]
		if !it.AtCommit() || it.CommitOID != headA || it.Path != want.path || it.Position != want.pos || it.Line != 0 {
			t.Errorf("item %d = %+v, want position %d in %s at %s", i, it, want.pos, want.path, headA)
		}
		if !strings.Contains(it.Body, "*Posted by pg-connector at a1b2c3d.*") {
			t.Errorf("item %d body = %q must be attributed to the saved-at head", i, it.Body)
		}
	}
	for _, c := range rev.comments {
		if c.commit != headA {
			t.Errorf("a comment is recorded at %q, want %q", c.commit, headA)
		}
	}
	if len(f.host.compareCalls) != 1 {
		t.Fatalf("compare reads = %d", len(f.host.compareCalls))
	}
	cc := f.host.compareCalls[0]
	if cc.base != baseTip || cc.head != headA || strings.Join(cc.wanted, ",") != "main.go,new.go" {
		t.Errorf("compare call = %+v", cc)
	}
	st := f.sidecar()
	if st.LastAppend == nil || st.LastAppend.Head != headA || !bodyHeadRecorded(st, headA) || bodyHeadRecorded(st, headB) {
		t.Errorf("sidecar = %+v", st)
	}
	// The saved review is stale for the live head, so the live head is still
	// seen as needing its own review (INV-REVHEAD-3).
	pending, err := f.b.PendingReview(context.Background(), pr.PendingReviewRequest{ID: "foo/bar#42"})
	if err != nil {
		t.Fatal(err)
	}
	if rv := pending.Review; !pending.Pending || pending.HeadSHA != headB || rv.CommitSHA != headA || rv.CommentsAtHead != 0 || rv.ReviewedHead || !rv.Stale {
		t.Errorf("pending = %+v review = %+v", pending, rv)
	}
}

// TestSubmitEarlierHeadSidesAndRename: LEFT resolves against old-file
// numbering, a rename is reachable by its old path on LEFT only, and the path
// sent is the one the diff is listed under.
func TestSubmitEarlierHeadSidesAndRename(t *testing.T) {
	f := movedFixture(t)
	f.mustSubmit(req(
		"",
		leftPoint("main.go", 21, "why remove b?"),
		leftPoint("old.go", 1, "x was fine"),
		point("renamed.go", 1, "y is new"),
	))
	items := f.written()
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	for i, want := range []struct {
		path string
		pos  int
	}{{"main.go", 9}, {"renamed.go", 1}, {"renamed.go", 2}} {
		if items[i].Path != want.path || items[i].Position != want.pos {
			t.Errorf("item %d = (%s, %d), want (%s, %d)", i, items[i].Path, items[i].Position, want.path, want.pos)
		}
	}
}

// TestSubmitEarlierHeadAcceptsAFullShaInAnyCase: the match is
// case-insensitive and the review is recorded under the host's own spelling.
func TestSubmitEarlierHeadAcceptsAFullShaInAnyCase(t *testing.T) {
	f := movedFixture(t)
	r := req("", point("main.go", 22, "use c"))
	r.HeadSHA = strings.ToUpper(headA)
	res := f.mustSubmit(r)
	if res.HeadSHA != headA || !res.HeadMoved || f.host.pending[0].commit != headA {
		t.Fatalf("result = %+v review commit = %q", res, f.host.pending[0].commit)
	}
}

// TestSubmitEarlierHeadNotInTheCommitListIsInvalid: nothing is written, and
// the message names both shas.
func TestSubmitEarlierHeadNotInTheCommitListIsInvalid(t *testing.T) {
	f := movedFixture(t)
	f.host.commits = []string{headB}
	_, err := f.submit(req("overall", point("main.go", 22, "use c")))
	isCode(t, err, scriptout.ErrInvalidArgument)
	for _, want := range []string{"head moved", headA, headB, "not a commit of the pull request", "nothing was written"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
	f.untouched()
	if f.host.compareReads != 0 {
		t.Errorf("no comparison is read for a sha that is not a commit: %d", f.host.compareReads)
	}
	if _, statErr := os.Stat(f.store.Dir); statErr == nil {
		t.Errorf("the sidecar must not be created by a refused run")
	}
}

// TestSubmitAbbreviatedHeadIsInvalidBeforeAnyRead: an abbreviated sha is never
// expanded, even when it names a commit of the PR and even at the live head.
func TestSubmitAbbreviatedHeadIsInvalidBeforeAnyRead(t *testing.T) {
	for name, head := range map[string]string{
		"older commit": headA[:12],
		"live head":    headB[:7],
		"39 chars":     headA[:39],
	} {
		t.Run(name, func(t *testing.T) {
			f := movedFixture(t)
			r := req("", point("main.go", 22, "x"))
			r.HeadSHA = head
			_, err := f.submit(r)
			isCode(t, err, scriptout.ErrInvalidArgument)
			if !strings.Contains(err.Error(), "full 40-character") || !strings.Contains(err.Error(), "abbreviated") {
				t.Errorf("error %q must say that the full sha is required", err)
			}
			if f.host.reads != 0 || f.host.historyReads != 0 {
				t.Errorf("validation must run before any read: %d %d", f.host.reads, f.host.historyReads)
			}
			f.untouched()
		})
	}
}

// TestSubmitEarlierHeadWithATruncatedCommitList fails closed: not found in a
// list that was cut is unavailable, never invalid_argument; found is fine.
func TestSubmitEarlierHeadWithATruncatedCommitList(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		f := movedFixture(t)
		f.host.commits = []string{headB}
		f.host.commitsTruncated = true
		_, err := f.submit(req("", point("main.go", 22, "x")))
		isCode(t, err, scriptout.ErrUnavailable)
		if errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "nothing was written") {
			t.Errorf("err = %v", err)
		}
		f.untouched()
	})
	t.Run("found", func(t *testing.T) {
		f := movedFixture(t)
		f.host.commitsTruncated = true
		res := f.mustSubmit(req("", point("main.go", 22, "x")))
		if res.Added != 1 || !res.HeadMoved {
			t.Fatalf("result = %+v", res)
		}
	})
}

// TestSubmitEarlierHeadReadFailuresWriteNothing: the history and the
// comparison are read before anything is written.
func TestSubmitEarlierHeadReadFailuresWriteNothing(t *testing.T) {
	cases := map[string]func(*fakeHost){
		"history":            func(h *fakeHost) { h.historyErr = errors.New("gh: Bad Gateway (HTTP 502)") },
		"comparison":         func(h *fakeHost) { h.compareErr = errors.New("gh: Bad Gateway (HTTP 502)") },
		"comparison cut off": func(h *fakeHost) { h.compareErr = fmt.Errorf("compare: %w", github.ErrCompareTruncated) },
		"no base tip":        func(h *fakeHost) { h.baseOID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := movedFixture(t)
			mutate(f.host)
			_, err := f.submit(req("overall", point("main.go", 22, "x")))
			isCode(t, err, scriptout.ErrUnavailable)
			f.untouched()
			if _, statErr := os.Stat(f.store.Dir); statErr == nil {
				t.Errorf("the sidecar must not be created by a refused run")
			}
		})
	}
}

// TestSubmitEarlierHeadRepliesUseTheReplyMutation: a reply joins its thread
// and needs no comparison.
func TestSubmitEarlierHeadRepliesUseTheReplyMutation(t *testing.T) {
	f := movedFixture(t)
	f.host.addSubmitted(headA, hostComment{id: "C_0", threadID: "PRRT_1", path: "main.go", line: 22, body: "old", commit: headA})
	res := f.mustSubmit(req("", pr.ReviewComment{ThreadID: "PRRT_1", Body: "agreed"}))
	if res.Added != 1 || !res.HeadMoved || res.Status != pr.StatusPosted {
		t.Fatalf("result = %+v", res)
	}
	items := f.written()
	if len(items) != 1 || !items[0].IsReply() || items[0].ReplyToThreadID != "PRRT_1" || items[0].AtCommit() {
		t.Fatalf("items = %+v", items)
	}
	if f.host.compareReads != 0 {
		t.Errorf("a reply needs no comparison: %d", f.host.compareReads)
	}
}

// TestSubmitEarlierHeadAnchorsThatDoNotResolveFailPerItem: a line not in the
// diff, an omitted patch and a file not in the difference each fail alone as
// anchor_rejected; the message shape is the ordinary one and the rest lands.
func TestSubmitEarlierHeadAnchorsThatDoNotResolveFailPerItem(t *testing.T) {
	f := movedFixture(t)
	ok := point("main.go", 22, "use c")
	cs := []pr.ReviewComment{
		ok,
		point("main.go", 99, "outside the diff"),
		point("big.go", 1, "patch omitted"),
		point("absent.go", 1, "not in the difference"),
		leftPoint("main.go", 22, "use c"), // old line 22 is a context line: resolves
	}
	_, err := f.submit(req("", cs...))
	isCode(t, err, scriptout.ErrInvalidArgument)
	msg := err.Error()
	for _, want := range []string{
		"2 of 5 comments landed; failed: ",
		"anchor_rejected:" + pointFP("main.go", 99, "outside the diff") + ":main.go:99",
		"anchor_rejected:" + pointFP("big.go", 1, "patch omitted") + ":big.go:1",
		"anchor_rejected:" + pointFP("absent.go", 1, "not in the difference") + ":absent.go:1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
	items := f.written()
	if len(items) != 2 || items[0].Position != 10 || items[1].Position != 11 {
		t.Fatalf("only the resolvable points are sent: %+v", items)
	}
	st := f.sidecar()
	if !st.Has(pointFP("main.go", 22, "use c")) || st.Has(pointFP("main.go", 99, "outside the diff")) {
		t.Errorf("only confirmed fingerprints are recorded: %+v", st)
	}
	if st.LastAppend == nil || st.LastAppend.Added != 2 {
		t.Errorf("last_append = %+v", st.LastAppend)
	}
}

// TestSubmitEarlierHeadAllAnchorsRejectedStillReportsPerItem: with nothing to
// send the run does not abort early: it reports each item.
func TestSubmitEarlierHeadAllAnchorsRejectedStillReportsPerItem(t *testing.T) {
	f := movedFixture(t)
	_, err := f.submit(req("overall", point("main.go", 99, "a"), point("big.go", 1, "b")))
	isCode(t, err, scriptout.ErrInvalidArgument)
	if !strings.Contains(err.Error(), "0 of 2 comments landed; failed: anchor_rejected:") {
		t.Fatalf("err = %q", err)
	}
	if len(f.host.writes) != 0 {
		t.Errorf("nothing resolvable means no write call: %+v", f.host.writes)
	}
	if len(f.host.creates) != 1 {
		t.Errorf("the review and its body are still saved: %v", f.host.creates)
	}
}

// TestSubmitEarlierHeadReplayIsIdempotent: the fingerprint comes from the
// request's own line, so replaying the identical request adds nothing.
func TestSubmitEarlierHeadReplayIsIdempotent(t *testing.T) {
	f := movedFixture(t)
	r := req("overall", point("main.go", 22, "use c"), point("new.go", 2, "why b?"))
	first := f.mustSubmit(r)
	second := f.mustSubmit(r)
	if first.Status != pr.StatusPosted || second.Status != pr.StatusNoChange || second.AlreadyPresent != 2 || second.Added != 0 {
		t.Fatalf("first = %+v second = %+v", first, second)
	}
	if second.Body != pr.BodyKept || !second.HeadMoved || second.HeadSHA != headA || second.LiveHeadSHA != headB {
		t.Errorf("second = %+v", second)
	}
	if len(f.written()) != 2 || len(f.host.pending[0].comments) != 2 {
		t.Errorf("no duplicates: writes = %d comments = %d", len(f.written()), len(f.host.pending[0].comments))
	}
}

// TestSubmitEarlierHeadAppendsToAReviewStartedAtAnotherCommit: the comments
// still anchor at the saved-at head, and the body gets the section for it.
func TestSubmitEarlierHeadAppendsToAReviewStartedAtAnotherCommit(t *testing.T) {
	f := movedFixture(t)
	f.host.commits = []string{headA, headB, headC}
	hand := f.host.addPending(headC, "typed by the operator")
	res := f.mustSubmit(req("overall", point("main.go", 22, "use c")))
	if res.Status != pr.StatusAppend || res.ReviewID != hand.nodeID() || res.HeadSHA != headA || !res.HeadMoved {
		t.Fatalf("result = %+v", res)
	}
	if hand.commit != headC {
		t.Errorf("the review-level commit is not rewritten: %q", hand.commit)
	}
	if len(hand.comments) != 1 || hand.comments[0].commit != headA {
		t.Errorf("the comment must be recorded at the saved-at head: %+v", hand.comments)
	}
	if len(f.host.updates) != 1 || !strings.HasPrefix(f.host.updates[0], "typed by the operator\n\n") ||
		!strings.Contains(f.host.updates[0], sectionOf(headA, "overall\n\nSaved at a1b2c3d; PR head was b9b8b7b when saved.")) {
		t.Errorf("body updates = %q", f.host.updates)
	}
}

// TestSubmitEarlierHeadWithTwoPendingReviews: comments go to the lowest-numbered
// review at the saved-at head, the body is left alone, extras are counted.
func TestSubmitEarlierHeadWithTwoPendingReviews(t *testing.T) {
	f := movedFixture(t)
	low := f.host.addPending(headA, "")
	f.host.addPending(headB, "other")
	res := f.mustSubmit(req("summary", point("main.go", 22, "use c")))
	if res.ExtraPendingReviews != 1 || res.Body != pr.BodySkippedExtraPending || res.Status != pr.StatusAppend || res.ReviewID != low.nodeID() {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.updates) != 0 || len(low.comments) != 1 || low.comments[0].commit != headA || len(f.host.pending[1].comments) != 0 {
		t.Fatalf("pending = %+v updates = %v", f.host.pending, f.host.updates)
	}
	if len(f.host.pending) != 2 {
		t.Errorf("nothing may be deleted")
	}
}

// TestSubmitEarlierHeadWithoutABodyStillSaysWhereItWasSaved: a created review
// has a body (GitHub refuses to edit an empty one) and it carries the line.
func TestSubmitEarlierHeadWithoutABodyStillSaysWhereItWasSaved(t *testing.T) {
	f := movedFixture(t)
	f.mustSubmit(req("", point("main.go", 22, "use c")))
	body := f.host.creates[0]
	if !strings.Contains(body, "*Posted by pg-connector at a1b2c3d.*") || !strings.Contains(body, "Saved at a1b2c3d; PR head was b9b8b7b when saved.") {
		t.Errorf("created body = %q", body)
	}
	if strings.Contains(body, "pg-section") {
		t.Errorf("no section is written without a body text: %q", body)
	}
}

// TestSubmitEarlierHeadNothingToWriteIsNoChange: with every comment already
// present no review is created and no comparison is read, but the result still
// says the head moved.
func TestSubmitEarlierHeadNothingToWriteIsNoChange(t *testing.T) {
	f := movedFixture(t)
	f.host.addSubmitted(headA, hostComment{
		id: "C_0", threadID: "T_0", path: "main.go", line: 22, commit: headA,
		body: "use c\n\n*Posted by pg-connector at a1b2c3d.*\n" + pgposted.Marker(pointFP("main.go", 22, "use c")),
	})
	res := f.mustSubmit(req("", point("main.go", 22, "use c")))
	if res.Status != pr.StatusNoChange || res.State != pr.StateNone || res.AlreadyPresent != 1 || !res.HeadMoved || res.LiveHeadSHA != headB {
		t.Fatalf("result = %+v", res)
	}
	f.untouched()
	if f.host.compareReads != 0 {
		t.Errorf("nothing to write needs no comparison: %d", f.host.compareReads)
	}
}

// TestSubmitEarlierHeadCommentCap: the cap is 20 at an earlier head (the path
// reads two more things inside the same 30s run), and 40 stays for the live head.
func TestSubmitEarlierHeadCommentCap(t *testing.T) {
	many := func(n int) []pr.ReviewComment {
		cs := make([]pr.ReviewComment, n)
		for i := range cs {
			cs[i] = point("new.go", 1+i%3, fmt.Sprintf("c%d", i))
		}
		return cs
	}
	t.Run("over the cap", func(t *testing.T) {
		f := movedFixture(t)
		_, err := f.submit(req("", many(maxOlderHeadComments+1)...))
		isCode(t, err, scriptout.ErrInvalidArgument)
		if !strings.Contains(err.Error(), fmt.Sprint(maxOlderHeadComments)) || !strings.Contains(err.Error(), "nothing was written") {
			t.Errorf("err = %v", err)
		}
		f.untouched()
		if f.host.historyReads != 0 || f.host.compareReads != 0 {
			t.Errorf("the cap is checked before the history and the comparison are read")
		}
	})
	t.Run("at the cap", func(t *testing.T) {
		f := movedFixture(t)
		res := f.mustSubmit(req("", many(maxOlderHeadComments)...))
		if res.Added != maxOlderHeadComments {
			t.Fatalf("added = %d", res.Added)
		}
	})
	t.Run("the live head keeps its own cap", func(t *testing.T) {
		f := newSubmitFixture(t)
		res := f.mustSubmit(req("", many(maxRequestComments)...))
		if res.Added != maxRequestComments {
			t.Fatalf("added = %d", res.Added)
		}
	})
}

// TestSubmitEarlierHeadCapIsSizedForTheExecTimeout is the time budget of the
// earlier-head path: two write documents at about 4s each, the live-head path's
// two reads (8s) and the two extra reads (8s) must leave the same 20% margin
// under scriptout.DefaultBackendTimeout that the live-head cap keeps.
func TestSubmitEarlierHeadCapIsSizedForTheExecTimeout(t *testing.T) {
	const (
		aliasesPerDocument = 10 // github.maxWriteAliasesPerDocument
		perDocument        = 4 * time.Second
		reads              = 8 * time.Second // lookup + re-read, generous
		extraReads         = 8 * time.Second // commit list + compare, generous
	)
	documents := (maxOlderHeadComments + aliasesPerDocument - 1) / aliasesPerDocument
	worst := time.Duration(documents)*perDocument + reads + extraReads
	if worst*5 > scriptout.DefaultBackendTimeout*4 {
		t.Fatalf("cap %d needs ~%v worst case; exec timeout is %v (lower the cap or revisit the budget)",
			maxOlderHeadComments, worst, scriptout.DefaultBackendTimeout)
	}
	if maxOlderHeadComments > maxRequestComments {
		t.Errorf("the earlier-head cap %d must not exceed the live-head cap %d", maxOlderHeadComments, maxRequestComments)
	}
}

// TestSubmitEarlierHeadSurvivesAForcePushBetweenLookupAndWrite: the head moves
// again after the lookup and headA drops out of the PR. The anchors carry the
// commit, so nothing is anchored at the new head; and when the host refuses a
// commit it no longer has, the run fails per item with nothing misanchored and
// nothing recorded.
func TestSubmitEarlierHeadSurvivesAForcePushBetweenLookupAndWrite(t *testing.T) {
	// The push lands after the lookup and the history read have vouched for
	// headA, while the comparison is being read, i.e. before the first write.
	forcePush := func(h *fakeHost) {
		h.onCompare = func(h *fakeHost) {
			h.head = headC
			h.commits = []string{headC}
		}
	}
	t.Run("the host still takes the old commit", func(t *testing.T) {
		f := movedFixture(t)
		forcePush(f.host)
		res := f.mustSubmit(req("", point("main.go", 22, "use c")))
		if res.Added != 1 || res.HeadSHA != headA {
			t.Fatalf("result = %+v", res)
		}
		for _, c := range f.host.pending[0].comments {
			if c.commit != headA {
				t.Errorf("a comment is anchored at %q, never at the new head", c.commit)
			}
		}
	})
	t.Run("the host refuses it", func(t *testing.T) {
		f := movedFixture(t)
		forcePush(f.host)
		f.host.rejectUnknownCommit = true
		_, err := f.submit(req("", point("main.go", 22, "use c"), point("new.go", 2, "why b?")))
		isCode(t, err, scriptout.ErrInvalidArgument)
		if !strings.Contains(err.Error(), "0 of 2 comments landed; failed: anchor_rejected:") {
			t.Errorf("err = %q", err)
		}
		for _, r := range f.host.pending {
			if len(r.comments) != 0 {
				t.Errorf("nothing may be anchored anywhere: %+v", r.comments)
			}
		}
		if st := f.sidecar(); st.Has(pointFP("main.go", 22, "use c")) || st.Has(pointFP("new.go", 2, "why b?")) {
			t.Errorf("a refused comment must not be recorded: %+v", st)
		}
	})
}

// TestSubmitEarlierHeadHoldsThePerPRLock: another holder of the per-PR lock
// stops the run before any read, and a concurrent submit waits behind the one
// that is inside the history and comparison reads.
func TestSubmitEarlierHeadHoldsThePerPRLock(t *testing.T) {
	t.Run("held elsewhere", func(t *testing.T) {
		f := movedFixture(t)
		held, err := f.lock.Acquire("foo", "bar", 42)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.submit(req("", point("main.go", 22, "x")))
		isCode(t, err, scriptout.ErrUnavailable)
		if f.host.reads != 0 || f.host.historyReads != 0 || f.host.compareReads != 0 {
			t.Errorf("nothing may be read without the lock: %d %d %d", f.host.reads, f.host.historyReads, f.host.compareReads)
		}
		_ = held.Release()
		f.mustSubmit(req("", point("main.go", 22, "x")))
	})
	t.Run("two concurrent submits", func(t *testing.T) {
		f := movedFixture(t)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		f.host.onCompare = func(*fakeHost) {
			once.Do(func() { close(entered) })
			<-release
		}
		type outcome struct {
			res pr.ReviewSubmitResult
			err error
		}
		first := make(chan outcome, 1)
		go func() {
			res, err := f.submit(req("", point("main.go", 22, "x")))
			first <- outcome{res, err}
		}()
		<-entered
		// The first run is inside the comparison read, holding the lock.
		_, err := f.submit(req("", point("main.go", 22, "x")))
		isCode(t, err, scriptout.ErrUnavailable)
		if f.host.compareReads != 1 {
			t.Errorf("the waiting run must not have read anything: compare reads = %d", f.host.compareReads)
		}
		close(release)
		got := <-first
		if got.err != nil || got.res.Added != 1 {
			t.Fatalf("first run = %+v, %v", got.res, got.err)
		}
		again := f.mustSubmit(req("", point("main.go", 22, "x")))
		if again.AlreadyPresent != 1 || len(f.host.pending[0].comments) != 1 {
			t.Errorf("a run after the first sees its comment: %+v", again)
		}
	})
}

// leftPoint is a new point on the old-file (LEFT) side.
func leftPoint(path string, line int, body string) pr.ReviewComment {
	return pr.ReviewComment{Path: path, Line: line, Side: "LEFT", Body: body}
}
