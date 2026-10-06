package internal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/archive"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeArchiver records archive writes into the shared ops list (so the test
// can assert archive -> delete -> post ordering) and can be made to fail.
type fakeArchiver struct {
	gh   *fakeGH
	err  error
	recs []archive.Record
}

func (a *fakeArchiver) Write(rec archive.Record) (string, error) {
	a.gh.ops = append(a.gh.ops, "archive")
	if a.err != nil {
		return "", a.err
	}
	a.recs = append(a.recs, rec)
	return "/archive/foo/bar/pr-42/review-5001.json", nil
}

// supersedeReq is baseSubmitReq with supersede_pending set (head deadbeef).
func supersedeReq() pr.ReviewSubmitRequest {
	r := baseSubmitReq()
	r.SupersedePending = true
	return r
}

// lookupOf scripts a GetPendingReview answer around one pending review; the PR
// head is the request's, deadbeef.
func lookupOf(rev *github.PendingReviewNode) pendingReply {
	return pendingReply{data: &github.PendingReviewData{HeadSHA: "deadbeef", Reviews: reviewsOf(rev)}}
}

// supersedeBackend wires a fakeGH (live head matching the request) and a fake
// archiver into a Backend. seq scripts successive lookups.
func supersedeBackend(seq ...pendingReply) (*Backend, *fakeGH, *fakeArchiver) {
	gh := &fakeGH{pr: liveHeadPR(), pendingSeq: seq}
	arch := &fakeArchiver{gh: gh}
	return New(gh).WithArchiver(arch), gh, arch
}

func stalePending() *github.PendingReviewNode {
	return postedAsBackend("0ldc0mm1t", "old summary", "old finding one", "old finding two")
}

func assertUntouched(t *testing.T, gh *fakeGH, arch *fakeArchiver) {
	t.Helper()
	if len(gh.posts) != 0 || len(gh.deleted) != 0 {
		t.Errorf("nothing may be posted or deleted; posts=%+v deleted=%v", gh.posts, gh.deleted)
	}
	for _, op := range gh.ops {
		if op == "delete" || op == "post" {
			t.Errorf("unexpected op %q in %v", op, gh.ops)
		}
	}
	_ = arch
}

func TestSupersedeNoPendingPosts(t *testing.T) {
	b, gh, arch := supersedeBackend(lookupOf(nil))
	res, err := b.SubmitReview(context.Background(), supersedeReq())
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if res.Status != pr.StatusPosted || res.ReviewID != "PRR_node" || res.State != "pending" || res.Reason != "" {
		t.Fatalf("result = %+v, want posted", res)
	}
	if res.Supersede == nil || res.Supersede.Attempted || res.Supersede.Deleted {
		t.Errorf("supersede mirror = %+v, want zero", res.Supersede)
	}
	if !reflect.DeepEqual(gh.ops, []string{"lookup", "post"}) || len(arch.recs) != 0 {
		t.Errorf("ops = %v, archives = %d; want lookup,post and no archive", gh.ops, len(arch.recs))
	}
}

func TestSupersedeSameHeadSkips(t *testing.T) {
	// Even content that is not provably unedited is skipped at the same head:
	// the same-head check comes first and never touches the review.
	edited := postedAsBackend("DEADBEEF", "summary", "finding") // case-insensitive SHA
	edited.Body += "\nhuman note"
	for name, rev := range map[string]*github.PendingReviewNode{
		"untouched": postedAsBackend("deadbeef", "summary", "finding"),
		"edited":    edited,
	} {
		t.Run(name, func(t *testing.T) {
			b, gh, arch := supersedeBackend(lookupOf(rev))
			res, err := b.SubmitReview(context.Background(), supersedeReq())
			if err != nil {
				t.Fatalf("SubmitReview: %v", err)
			}
			if res.Status != pr.StatusSkipped || res.Reason != pr.ReasonSameHead {
				t.Fatalf("result = %+v, want skipped / %s", res, pr.ReasonSameHead)
			}
			if res.ReviewID != "PRR_node" || res.State != "pending" || res.PendingReview == nil ||
				res.PendingReview.DatabaseID != 5001 || res.PendingReview.URL == "" {
				t.Errorf("skipped must carry the existing review's id/url: %+v", res)
			}
			assertUntouched(t, gh, arch)
			if len(arch.recs) != 0 {
				t.Errorf("a skip archives nothing")
			}
		})
	}
}

func TestSupersedeReplacesStaleUnedited(t *testing.T) {
	// A null review commit (force-push removed it) is stale too.
	for name, rev := range map[string]*github.PendingReviewNode{
		"different commit": stalePending(),
		"null commit":      postedAsBackend("", "old summary", "old finding one", "old finding two"),
	} {
		t.Run(name, func(t *testing.T) {
			b, gh, arch := supersedeBackend(lookupOf(rev))
			res, err := b.SubmitReview(context.Background(), supersedeReq())
			if err != nil {
				t.Fatalf("SubmitReview: %v", err)
			}
			if res.Status != pr.StatusReplaced || res.ReviewID != "PRR_node" || res.State != "pending" {
				t.Fatalf("result = %+v, want replaced with the new review's id", res)
			}
			// Archive BEFORE delete, delete BEFORE post.
			if want := []string{"lookup", "archive", "delete", "post"}; !reflect.DeepEqual(gh.ops, want) {
				t.Fatalf("ops = %v, want %v", gh.ops, want)
			}
			if len(gh.deleted) != 1 || gh.deleted[0] != 5001 {
				t.Errorf("deleted = %v, want the old review's database id [5001]", gh.deleted)
			}
			if len(gh.posts) != 1 || gh.posts[0].commitID != "deadbeef" {
				t.Errorf("posts = %+v, want one anchored to the head", gh.posts)
			}
			sup := res.Superseded
			if sup == nil || sup.ReviewID != "PRR_node" || sup.DatabaseID != 5001 || sup.CommitSHA != rev.CommitOID ||
				sup.ArchivePath == "" || sup.URL == "" {
				t.Fatalf("superseded = %+v", sup)
			}
			if sup.Body != rev.Body || len(sup.Comments) != 2 || sup.Comments[0].Body != rev.Comments[0].Body {
				t.Errorf("superseded must carry the full old content: %+v", sup)
			}
			if len(arch.recs) != 1 || arch.recs[0].Body != rev.Body || arch.recs[0].DatabaseID != 5001 ||
				arch.recs[0].Repo != "foo/bar" || arch.recs[0].PR != 42 || len(arch.recs[0].Comments) != 2 {
				t.Errorf("archived record = %+v", arch.recs)
			}
			if res.Supersede == nil || !res.Supersede.Attempted || !res.Supersede.Deleted {
				t.Errorf("supersede mirror = %+v", res.Supersede)
			}
		})
	}
}

// TestSupersedeCRLFOnlyConversionIsStillReplaced: a web-UI save rewrites LF to
// CRLF (G2); that alone is NOT an edit.
func TestSupersedeCRLFOnlyConversionIsStillReplaced(t *testing.T) {
	rev := stalePending()
	rev.Body = strings.ReplaceAll(rev.Body, "\n", "\r\n")
	for i := range rev.Comments {
		rev.Comments[i].Body = strings.ReplaceAll(rev.Comments[i].Body, "\n", "\r\n")
	}
	b, _, _ := supersedeBackend(lookupOf(rev))
	res, err := b.SubmitReview(context.Background(), supersedeReq())
	if err != nil || res.Status != pr.StatusReplaced {
		t.Fatalf("CRLF-only change must still be replaced; res=%+v err=%v", res, err)
	}
}

// TestSupersedeBlockedHumanEdited: every way a stale review stops being
// provably unedited agent content blocks the supersede; the review is
// untouched, its URL is reported, and nothing is archived, deleted or posted.
func TestSupersedeBlockedHumanEdited(t *testing.T) {
	mut := func(f func(*github.PendingReviewNode)) *github.PendingReviewNode {
		r := stalePending()
		f(r)
		return r
	}
	cases := map[string]*github.PendingReviewNode{
		"marker-preserving comment text edit": mut(func(r *github.PendingReviewNode) {
			r.Comments[0].Body = strings.Replace(r.Comments[0].Body, "one", "ONE", 1)
		}),
		"marker removed from a comment": mut(func(r *github.PendingReviewNode) {
			r.Comments[0].Body = strings.Replace(r.Comments[0].Body, github.BotMarker, "", 1)
		}),
		"unmarked comment added": mut(func(r *github.PendingReviewNode) {
			r.Comments = append(r.Comments, github.PendingReviewComment{ID: "CX", Body: "human"})
		}),
		"comment removed": mut(func(r *github.PendingReviewNode) { r.Comments = r.Comments[:1] }),
		"body edited":     mut(func(r *github.PendingReviewNode) { r.Body = strings.Replace(r.Body, "old summary", "mine", 1) }),
		"body marker removed": mut(func(r *github.PendingReviewNode) {
			r.Body = "a human wrote this"
		}),
		"no digest (plain marker only)": {
			ID: "PRR_node", DatabaseID: 5001, URL: "https://example.invalid/r", CommitOID: "0ldc0mm1t",
			Body:     "summary\n" + github.BotMarker,
			Comments: []github.PendingReviewComment{{ID: "C1", Body: "x " + github.BotMarker}},
		},
		"left by pg-pr": {
			ID: "PRR_node", DatabaseID: 5001, URL: "https://example.invalid/r", CommitOID: "0ldc0mm1t",
			Body: "summary <!-- pg-pr -->",
		},
		"unreadable digest": mut(func(r *github.PendingReviewNode) {
			r.Body = strings.Replace(r.Body, github.DigestMarkerPrefix, github.DigestMarkerPrefix+"zz", 1)
		}),
	}
	for name, rev := range cases {
		t.Run(name, func(t *testing.T) {
			b, gh, arch := supersedeBackend(lookupOf(rev))
			res, err := b.SubmitReview(context.Background(), supersedeReq())
			if err != nil {
				t.Fatalf("a block is a status, not an error; got %v", err)
			}
			if res.Status != pr.StatusBlockedHumanPending || res.Reason != pr.ReasonHumanEdited {
				t.Fatalf("result = %+v, want blocked_human_pending / human_edited", res)
			}
			if res.ReviewID != "" || res.PendingReview == nil || res.PendingReview.DatabaseID != 5001 ||
				res.PendingReview.URL == "" || !strings.Contains(res.Message, res.PendingReview.URL) {
				t.Errorf("blocked must report the untouched review and its URL, and no new review id: %+v", res)
			}
			assertUntouched(t, gh, arch)
			if len(arch.recs) != 0 {
				t.Errorf("nothing may be archived for a blocked review")
			}
		})
	}
}

func TestSupersedeBlockedDetectionFailed(t *testing.T) {
	for name, errs := range map[string]error{
		"unavailable":     scriptout.WrapError(scriptout.ErrUnavailable, "review_pending: detection_failed: boom"),
		"unauthenticated": scriptout.WrapError(scriptout.ErrUnauthenticated, "review_pending: detection_failed: forbidden"),
	} {
		t.Run(name, func(t *testing.T) {
			b, gh, arch := supersedeBackend(pendingReply{err: errs})
			res, err := b.SubmitReview(context.Background(), supersedeReq())
			if err != nil {
				t.Fatalf("detection failure is a status; got error %v", err)
			}
			if res.Status != pr.StatusBlockedHumanPending || res.Reason != pr.ReasonDetectionFailed || res.ReviewID != "" {
				t.Fatalf("result = %+v", res)
			}
			if !strings.Contains(res.Message, "detection_failed") || res.PendingReview != nil {
				t.Errorf("message = %q ref = %+v", res.Message, res.PendingReview)
			}
			if want := []string{"lookup"}; !reflect.DeepEqual(gh.ops, want) {
				t.Errorf("ops = %v, want only the lookup (fail-closed)", gh.ops)
			}
			assertUntouched(t, gh, arch)
		})
	}
	// A lookup that "succeeds" but cannot name the head is an error from the
	// lookup (review_pending's fail-closed contract) and so blocks too.
	b, gh, arch := supersedeBackend(pendingReply{data: &github.PendingReviewData{}})
	res, err := b.SubmitReview(context.Background(), supersedeReq())
	if err != nil || res.Reason != pr.ReasonDetectionFailed {
		t.Fatalf("missing head: res=%+v err=%v", res, err)
	}
	assertUntouched(t, gh, arch)
}

func TestSupersedeBlockedArchiveFailedDeletesNothing(t *testing.T) {
	t.Run("archive write fails", func(t *testing.T) {
		b, gh, arch := supersedeBackend(lookupOf(stalePending()))
		arch.err = errors.New("disk full")
		res, err := b.SubmitReview(context.Background(), supersedeReq())
		if err != nil {
			t.Fatalf("SubmitReview: %v", err)
		}
		if res.Status != pr.StatusBlockedHumanPending || res.Reason != pr.ReasonArchiveFailed || res.ReviewID != "" {
			t.Fatalf("result = %+v", res)
		}
		if !strings.Contains(res.Message, "disk full") || res.PendingReview == nil || res.PendingReview.URL == "" {
			t.Errorf("message/ref = %q / %+v", res.Message, res.PendingReview)
		}
		if want := []string{"lookup", "archive"}; !reflect.DeepEqual(gh.ops, want) {
			t.Errorf("ops = %v, want %v (no delete after a failed archive)", gh.ops, want)
		}
		assertUntouched(t, gh, arch)
	})
	t.Run("no archive location configured", func(t *testing.T) {
		gh := &fakeGH{pr: liveHeadPR(), pendingSeq: []pendingReply{lookupOf(stalePending())}}
		res, err := New(gh).SubmitReview(context.Background(), supersedeReq()) // no WithArchiver
		if err != nil {
			t.Fatalf("SubmitReview: %v", err)
		}
		if res.Status != pr.StatusBlockedHumanPending || res.Reason != pr.ReasonArchiveFailed {
			t.Fatalf("result = %+v", res)
		}
		assertUntouched(t, gh, nil)
	})
}

// TestSupersedeBlockedDeleteRefused covers a delete the host refuses,
// including the submit-vs-delete race: the operator submitted the review
// between the lookup and the delete, so the delete answers HTTP 422
// non-pending (P2). The backend re-lists ONCE, reports delete_refused, and
// never retries the delete and never posts.
func TestSupersedeBlockedDeleteRefused(t *testing.T) {
	non422 := errors.New("github: delete review 5001: gh api failed: gh: Can not delete a non-pending pull request review (HTTP 422)")
	cases := map[string]struct {
		deleteErr   error
		relist      *pendingReply // nil: the delete error is not a 422, so no re-list happens
		wantRelists int
		wantMsg     string
	}{
		"race lost: submitted meanwhile, re-list shows none": {
			non422, &pendingReply{data: &github.PendingReviewData{HeadSHA: "deadbeef"}}, 1, "no longer pending",
		},
		"422 but re-list still shows it pending": {
			non422, &pendingReply{data: &github.PendingReviewData{HeadSHA: "deadbeef", Reviews: reviewsOf(stalePending())}}, 1, "still pending",
		},
		"422 and a different pending review now exists": {
			non422, &pendingReply{data: &github.PendingReviewData{HeadSHA: "deadbeef", Reviews: reviewsOf(func() *github.PendingReviewNode {
				r := stalePending()
				r.ID = "PRR_other"
				return r
			}())}}, 1, "different pending review",
		},
		"422 and the re-list itself fails": {
			non422, &pendingReply{err: errors.New("boom")}, 1, "re-listing it afterwards failed",
		},
		"GraphQL UNPROCESSABLE shape": {
			errors.New("github: delete review: UNPROCESSABLE: Can not delete a non-pending pull request review"),
			&pendingReply{data: &github.PendingReviewData{HeadSHA: "deadbeef"}}, 1, "no longer pending",
		},
		"403 token lost delete permission: no re-list": {
			errors.New("github: delete review 5001: gh: Forbidden (HTTP 403)"), nil, 0, "refused to delete",
		},
		"502 host failure: no re-list": {
			errors.New("github: delete review 5001: gh: Bad Gateway (HTTP 502)"), nil, 0, "refused to delete",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			seq := []pendingReply{lookupOf(stalePending())}
			if c.relist != nil {
				seq = append(seq, *c.relist)
			}
			b, gh, arch := supersedeBackend(seq...)
			gh.deleteErr = c.deleteErr
			res, err := b.SubmitReview(context.Background(), supersedeReq())
			if err != nil {
				t.Fatalf("SubmitReview: %v", err)
			}
			if res.Status != pr.StatusBlockedHumanPending || res.Reason != pr.ReasonDeleteRefused || res.ReviewID != "" {
				t.Fatalf("result = %+v, want blocked / delete_refused", res)
			}
			if !strings.Contains(res.Message, c.wantMsg) {
				t.Errorf("message %q must contain %q", res.Message, c.wantMsg)
			}
			if res.PendingReview == nil || res.PendingReview.URL == "" || !strings.Contains(res.Message, res.PendingReview.URL) {
				t.Errorf("the review URL must be reported: %+v", res)
			}
			if res.Supersede == nil || !res.Supersede.Attempted || res.Supersede.Deleted || res.Supersede.Error == "" {
				t.Errorf("supersede mirror = %+v", res.Supersede)
			}
			deletes, lookups := 0, 0
			for _, op := range gh.ops {
				switch op {
				case "delete":
					deletes++
				case "lookup":
					lookups++
				case "post":
					t.Errorf("nothing may be posted after a refused delete: %v", gh.ops)
				}
			}
			if deletes != 1 {
				t.Errorf("the delete must be attempted exactly once, never retried blindly: ops=%v", gh.ops)
			}
			if lookups != 1+c.wantRelists {
				t.Errorf("lookups = %d, want %d (one lookup + %d re-list): ops=%v", lookups, 1+c.wantRelists, c.wantRelists, gh.ops)
			}
			// The archive was written before the refused delete: recoverable.
			if len(arch.recs) != 1 {
				t.Errorf("archive happens before the delete attempt; records = %d", len(arch.recs))
			}
			_ = arch
		})
	}
}

// TestSupersedePostFailureAfterDeleteNamesArchive: if the new review cannot be
// posted after the old one was deleted, the error says so and where the old
// content is archived.
func TestSupersedePostFailureAfterDeleteNamesArchive(t *testing.T) {
	b, gh, _ := supersedeBackend(lookupOf(stalePending()))
	gh.postErr = errors.New("gh: Validation Failed (HTTP 422)")
	_, err := b.SubmitReview(context.Background(), supersedeReq())
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	for _, w := range []string{"previous pending review was deleted", "/archive/foo/bar/pr-42/review-5001.json"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q must mention %q", err, w)
		}
	}
	gh2 := &fakeGH{pr: liveHeadPR(), postErr: errors.New("gh: Bad Gateway (HTTP 502)"), pendingSeq: []pendingReply{lookupOf(nil)}}
	_, err = New(gh2).WithArchiver(&fakeArchiver{gh: gh2}).SubmitReview(context.Background(), supersedeReq())
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "no pending review was found") {
		t.Errorf("post failure with nothing to delete: err = %v", err)
	}
}

// TestSupersedeNeverSubmitsAndKeepsHeadPrecheck: the head pre-check still
// runs first, and the backend's host seam has no submit call, so no path can
// submit a review.
func TestSupersedeHeadPrecheckRunsFirst(t *testing.T) {
	b, gh, _ := supersedeBackend(lookupOf(stalePending()))
	gh.pr = liveHeadPR()
	req := supersedeReq()
	req.HeadSHA = "0therhead"
	_, err := b.SubmitReview(context.Background(), req)
	if !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "head moved") {
		t.Fatalf("err = %v, want head moved", err)
	}
	if len(gh.ops) != 0 {
		t.Errorf("the pre-check must fail before any lookup/archive/delete/post; ops=%v", gh.ops)
	}
}

// TestSupersedeWithRealArchiver runs the replace path against the production
// directory archiver, proving the archive file exists with the full content
// when the delete is issued.
func TestSupersedeWithRealArchiver(t *testing.T) {
	dir := t.TempDir()
	gh := &fakeGH{pr: liveHeadPR(), pendingSeq: []pendingReply{lookupOf(stalePending())}}
	b := New(gh).WithArchiver(archive.DirArchiver{Dir: dir})
	res, err := b.SubmitReview(context.Background(), supersedeReq())
	if err != nil || res.Status != pr.StatusReplaced {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	want := filepath.Join(dir, "foo", "bar", "pr-42", "review-5001.json")
	if res.Superseded.ArchivePath != want {
		t.Fatalf("archive_path = %q, want %q", res.Superseded.ArchivePath, want)
	}
	raw, err := os.ReadFile(want)
	if err != nil || !strings.Contains(string(raw), "old finding one") {
		t.Fatalf("archive file missing or lacks content: %v %s", err, raw)
	}

	// An archive directory that cannot be written blocks the supersede.
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gh2 := &fakeGH{pr: liveHeadPR(), pendingSeq: []pendingReply{lookupOf(stalePending())}}
	res, err = New(gh2).WithArchiver(archive.DirArchiver{Dir: file}).SubmitReview(context.Background(), supersedeReq())
	if err != nil || res.Reason != pr.ReasonArchiveFailed || len(gh2.deleted) != 0 {
		t.Fatalf("unwritable archive: res=%+v err=%v deleted=%v", res, err, gh2.deleted)
	}
}

// TestSubmitWithoutSupersedeNeverLooksUp: supersede_pending off keeps the old
// simple path (post only), so a pending review still surfaces as the distinct
// "pending review already exists" rejection.
func TestSubmitWithoutSupersedeNeverLooksUp(t *testing.T) {
	gh := &fakeGH{pr: liveHeadPR(), postErr: errors.New(pendingExists422)}
	_, err := New(gh).SubmitReview(context.Background(), baseSubmitReq())
	if !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "supersede_pending") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"post"}; !reflect.DeepEqual(gh.ops, want) {
		t.Errorf("ops = %v, want %v", gh.ops, want)
	}
}

// The tests below cover legacyPendingLookup, the supersede guard's own reading
// of the actor's pending review (marker and digest verdicts). They moved here
// from the review_pending tests when that record dropped those fields.

func TestLegacyLookupPartlyUnmarked(t *testing.T) {
	cases := map[string]*github.PendingReviewNode{
		"unmarked comment": {
			ID: "R", Body: markedBody, CommitOID: "head1",
			Comments: []github.PendingReviewComment{
				{ID: "c1", Body: "ok " + github.BotMarker},
				{ID: "c2", Body: "a human added this"},
			},
		},
		"unmarked body": {
			ID: "R", Body: "human wrote this", CommitOID: "head1",
			Comments: []github.PendingReviewComment{{ID: "c1", Body: "ok " + github.BotMarker}},
		},
	}
	for name, rev := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: reviewsOf(rev)}}
			res, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
			if err != nil {
				t.Fatalf("legacyPendingLookup: %v", err)
			}
			if res.Review.AllMarked {
				t.Fatalf("AllMarked must be false: %+v", res.Review)
			}
		})
	}
	// per-element flags are individually correct
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: reviewsOf(cases["unmarked comment"])}}
	res, _ := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
	if !res.Review.Comments[0].Marked || res.Review.Comments[1].Marked {
		t.Errorf("per-element marker flags wrong: %+v", res.Review)
	}
}

func TestLegacyLookupLegacyPGPRMarkerCounts(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{{
		ID: "R", CommitOID: "head1", Body: legacyPGPRMarker + "\nbody",
		Comments: []github.PendingReviewComment{{ID: "c1", Body: legacyPGPRMarker + "\nc"}},
	}}}}
	res, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("legacyPendingLookup: %v", err)
	}
	if !res.Review.AllMarked {
		t.Fatalf("a pg-pr marker must count as marked: %+v", res.Review)
	}
}

func TestLegacyLookupNoCommentsBodyMarkedIsAllMarked(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: reviewsOf(pendingReviewAt("head1"))}}
	res, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("legacyPendingLookup: %v", err)
	}
	if !res.Review.AllMarked || res.Review.Comments == nil || len(res.Review.Comments) != 0 {
		t.Fatalf("a marked body with no comments is all-marked with an empty (non-nil) list: %+v", res.Review)
	}
}

func TestLegacyLookupReportsURLAndDigestState(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head2", Reviews: reviewsOf(postedAsBackend("head1", "summary", "one", "two"))}}
	res, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
	if err != nil {
		t.Fatalf("legacyPendingLookup: %v", err)
	}
	r := res.Review
	if r.URL != "https://example.invalid/pr/42#review-5001" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.DigestState != "verified" || !r.AllMarked {
		t.Errorf("untouched backend content must be verified and fully marked: %+v", r)
	}
}

// TestLegacyLookupDigestStateTable: the digest, not the marker, is what makes
// content verified-unedited; a review with no digest is never verified.
func TestLegacyLookupDigestStateTable(t *testing.T) {
	mut := func(f func(*github.PendingReviewNode)) *github.PendingReviewNode {
		r := postedAsBackend("head1", "summary", "one", "two")
		f(r)
		return r
	}
	cases := map[string]struct {
		rev        *github.PendingReviewNode
		wantDigest string
		wantAll    bool
	}{
		"untouched": {postedAsBackend("head1", "summary", "one", "two"), "verified", true},
		"CRLF-converted body and comments, text otherwise unchanged": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.ReplaceAll(r.Body, "\n", "\r\n")
			for i := range r.Comments {
				r.Comments[i].Body = strings.ReplaceAll(r.Comments[i].Body, "\n", "\r\n")
			}
		}), "verified", true},
		"marker-preserving comment text edit": {mut(func(r *github.PendingReviewNode) {
			r.Comments[0].Body = strings.Replace(r.Comments[0].Body, "one", "ONE (edited)", 1)
		}), "mismatch", true},
		"marker removed from a comment": {mut(func(r *github.PendingReviewNode) {
			r.Comments[1].Body = strings.Replace(r.Comments[1].Body, github.BotMarker, "", 1)
		}), "mismatch", false},
		"unmarked comment added": {mut(func(r *github.PendingReviewNode) {
			r.Comments = append(r.Comments, github.PendingReviewComment{ID: "CX", Body: "a human added this"})
		}), "mismatch", false},
		"body text edited": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.Replace(r.Body, "summary", "summary (edited)", 1)
		}), "mismatch", true},
		"posted before digests existed": {&github.PendingReviewNode{
			ID: "R", DatabaseID: 1, CommitOID: "head1", Body: "x\n" + github.BotMarker,
			Comments: []github.PendingReviewComment{{ID: "C", Body: "y " + github.BotMarker}},
		}, "missing", true},
		"left by pg-pr": {&github.PendingReviewNode{
			ID: "R", DatabaseID: 1, CommitOID: "head1", Body: "x <!-- pg-pr -->",
		}, "missing", true},
		"damaged digest": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.Replace(r.Body, github.DigestMarkerPrefix, github.DigestMarkerPrefix+"zz", 1)
		}), "unreadable", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head2", Reviews: reviewsOf(c.rev)}}
			res, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
			if err != nil {
				t.Fatalf("legacyPendingLookup: %v", err)
			}
			if res.Review.DigestState != c.wantDigest || res.Review.AllMarked != c.wantAll {
				t.Errorf("digest_state=%q all_marked=%v, want %q / %v", res.Review.DigestState, res.Review.AllMarked, c.wantDigest, c.wantAll)
			}
		})
	}
}

// TestLegacyLookupManyPendingReviewsIsAFailedLookup: unlike review_pending, the
// supersede guard still refuses to act when it cannot tell which pending
// review it would delete.
func TestLegacyLookupManyPendingReviewsIsAFailedLookup(t *testing.T) {
	one, two := stalePending(), stalePending()
	two.DatabaseID = 5002
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "deadbeef", Reviews: []github.PendingReviewNode{*one, *two}}}
	_, err := New(gh).legacyPendingLookup(context.Background(), "foo/bar#42")
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "detection_failed") {
		t.Fatalf("err = %v, want unavailable detection_failed", err)
	}
}
