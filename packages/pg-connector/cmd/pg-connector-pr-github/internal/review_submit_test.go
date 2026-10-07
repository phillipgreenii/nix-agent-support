package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	// headA and headB are realistic 40-hex heads; their section keys differ.
	headA = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	headB = "b9b8b7b6b5b4b3b2b1b0a9a8a7a6a5a4a3a2a1a0"
)

// hostComment is one comment held by the simulated host.
type hostComment struct {
	id       string
	threadID string
	path     string
	line     int
	body     string
	commit   string
	// position is the diff position of a comment appended at a named commit
	// (0 for a comment anchored by line at the live head).
	position int
}

// hostReview is one review (pending or submitted) held by the simulated host.
type hostReview struct {
	dbID     int64
	commit   string
	body     string
	comments []hostComment
}

func (r *hostReview) nodeID() string { return fmt.Sprintf("PRR_%d", r.dbID) }

// writeCall records one WriteReviewItems call.
type writeCall struct {
	reviewID string
	items    []github.ReviewWriteItem
}

// fakeHost is a stateful simulation of one PR's reviews on the host. It backs
// the lookup and the three write primitives of fakeGH, so a test drives whole
// create-or-append runs and the re-read sees what the writes did.
type fakeHost struct {
	head      string
	pending   []*hostReview
	submitted []*hostReview
	threads   map[string]bool
	nextID    int64

	// Knobs.
	readErr          func(call int) error
	afterRead        func(call int, h *fakeHost)
	create422        int // creates answered "one pending review" first, with a hand-started review appearing
	afterCreate      func(h *fakeHost)
	failItem         func(it github.ReviewWriteItem) github.WriteFailReason
	storeDespiteFail bool // a failed item is stored anyway (the answer was lost)
	dropWrites       bool // items are answered as landed but never stored
	updateTwoPending bool
	createErr        error

	// Earlier-head path: the PR's commit list and base tip (GetPRHistory) and
	// the compared files (GetComparedFiles). The commit list is empty by
	// default, so a head_sha other than the live head is "not a commit of the
	// PR" until a test says otherwise.
	commits          []string
	commitsTruncated bool
	baseOID          string
	compared         []github.ComparedFile
	historyErr       error
	compareErr       error
	// onCompare runs inside GetComparedFiles (a test blocks or inspects here).
	onCompare func(h *fakeHost)
	// rejectUnknownCommit makes the host refuse an append at a commit that is
	// not in the commit list, as GitHub refuses a commit it does not have.
	rejectUnknownCommit bool

	// Records.
	reads        int
	historyReads int
	compareReads int
	compareCalls []compareCall
	creates      []string // bodies of created reviews
	updates      []string // bodies sent to UpdateReviewBody
	writes       []writeCall
}

// compareCall records one GetComparedFiles call.
type compareCall struct {
	base, head string
	wanted     []string
}

// baseTip is the fake host's base branch tip.
const baseTip = "0123456789abcdef0123456789abcdef01234567"

func newFakeHost(head string) *fakeHost {
	return &fakeHost{head: head, threads: map[string]bool{}, nextID: 100, baseOID: baseTip}
}

// addPending adds a pending review (a hand-started one, say).
func (h *fakeHost) addPending(commit, body string) *hostReview {
	h.nextID++
	r := &hostReview{dbID: h.nextID, commit: commit, body: body}
	h.pending = append(h.pending, r)
	return r
}

// addSubmitted adds a submitted review of the viewer holding comments.
func (h *fakeHost) addSubmitted(commit string, cs ...hostComment) *hostReview {
	h.nextID++
	r := &hostReview{dbID: h.nextID, commit: commit, comments: cs}
	for _, c := range cs {
		h.threads[c.threadID] = true
	}
	h.submitted = append(h.submitted, r)
	return r
}

func (h *fakeHost) toNode(r *hostReview) github.PendingReviewNode {
	n := github.PendingReviewNode{
		ID: r.nodeID(), DatabaseID: r.dbID, URL: fmt.Sprintf("https://example.invalid/pull/1#pullrequestreview-%d", r.dbID),
		CommitOID: r.commit, Body: r.body,
	}
	for _, c := range r.comments {
		n.Comments = append(n.Comments, github.PendingReviewComment{
			ID: c.id, Path: c.path, Line: c.line, Body: c.body, OriginalCommitOID: c.commit, ReviewThreadID: c.threadID,
		})
	}
	return n
}

// snapshot is the lookup: the head, every pending review (lowest id first) and
// the viewer's submitted reviews.
func (h *fakeHost) snapshot() (*github.PendingReviewData, error) {
	h.reads++
	call := h.reads
	if h.readErr != nil {
		if err := h.readErr(call); err != nil {
			return nil, err
		}
	}
	data := &github.PendingReviewData{HeadSHA: h.head}
	for _, r := range h.pending {
		data.Reviews = append(data.Reviews, h.toNode(r))
	}
	for _, r := range h.submitted {
		n := h.toNode(r)
		data.Submitted = append(data.Submitted, github.SubmittedReviewNode{ID: n.ID, DatabaseID: n.DatabaseID, CommitOID: n.CommitOID, Comments: n.Comments})
	}
	if h.afterRead != nil {
		h.afterRead(call, h)
	}
	return data, nil
}

func (h *fakeHost) pendingByNode(id string) *hostReview {
	for _, r := range h.pending {
		if r.nodeID() == id {
			return r
		}
	}
	return nil
}

func (f *fakeGH) CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*github.CreatedReview, error) {
	f.ops = append(f.ops, "create")
	h := f.host
	if h.createErr != nil {
		return nil, h.createErr
	}
	if h.create422 > 0 {
		h.create422--
		h.addPending(h.head, "typed by the operator")
		return nil, fmt.Errorf("github: create pending review: %w: %w", github.ErrPendingReviewExists, errors.New("HTTP 422"))
	}
	if len(h.pending) > 0 {
		return nil, fmt.Errorf("github: create pending review: %w: %w", github.ErrPendingReviewExists, errors.New("HTTP 422"))
	}
	h.creates = append(h.creates, body)
	r := h.addPending(commitID, body)
	if h.afterCreate != nil {
		h.afterCreate(h)
	}
	return &github.CreatedReview{NodeID: r.nodeID(), ID: r.dbID, State: "pending"}, nil
}

func (f *fakeGH) WriteReviewItems(ctx context.Context, reviewID string, items []github.ReviewWriteItem) ([]github.ReviewWriteResult, error) {
	f.ops = append(f.ops, "write")
	h := f.host
	h.writes = append(h.writes, writeCall{reviewID: reviewID, items: items})
	rev := h.pendingByNode(reviewID)
	out := make([]github.ReviewWriteResult, len(items))
	for i, it := range items {
		reason := github.WriteFailReason("")
		if h.failItem != nil {
			reason = h.failItem(it)
		}
		if reason == "" && it.AtCommit() && h.rejectUnknownCommit && !slices.Contains(h.commits, it.CommitOID) {
			reason = github.ReasonAnchorRejected
		}
		if it.IsReply() && reason == "" && !h.threads[it.ReplyToThreadID] {
			reason = github.ReasonThreadNotFound
		}
		if reason != "" {
			out[i] = github.ReviewWriteResult{Reason: reason}
			if !h.storeDespiteFail {
				continue
			}
		} else {
			out[i] = github.ReviewWriteResult{Landed: true}
		}
		if h.dropWrites || rev == nil {
			continue
		}
		h.nextID++
		c := hostComment{id: fmt.Sprintf("C_%d", h.nextID), path: it.Path, line: it.Line, body: it.Body, commit: h.head, position: it.Position}
		if it.AtCommit() {
			c.commit = it.CommitOID
		}
		if it.IsReply() {
			c.threadID = it.ReplyToThreadID
		} else {
			c.threadID = fmt.Sprintf("T_%d", h.nextID)
			h.threads[c.threadID] = true
		}
		rev.comments = append(rev.comments, c)
	}
	return out, nil
}

func (f *fakeGH) GetPRHistory(ctx context.Context, repo string, number int) (*github.PRHistory, error) {
	f.ops = append(f.ops, "history")
	h := f.host
	h.historyReads++
	if h.historyErr != nil {
		return nil, h.historyErr
	}
	return &github.PRHistory{BaseOID: h.baseOID, CommitSHAs: slices.Clone(h.commits), Truncated: h.commitsTruncated}, nil
}

func (f *fakeGH) GetComparedFiles(ctx context.Context, repo, base, head string, wanted []string) ([]github.ComparedFile, error) {
	f.ops = append(f.ops, "compare")
	h := f.host
	h.compareReads++
	h.compareCalls = append(h.compareCalls, compareCall{base: base, head: head, wanted: slices.Clone(wanted)})
	if h.onCompare != nil {
		h.onCompare(h)
	}
	if h.compareErr != nil {
		return nil, h.compareErr
	}
	return slices.Clone(h.compared), nil
}

func (f *fakeGH) UpdateReviewBody(ctx context.Context, reviewID, body string) error {
	f.ops = append(f.ops, "update")
	h := f.host
	if h.updateTwoPending {
		return fmt.Errorf("github: update review body: %w: %w", github.ErrTwoPendingReviews, errors.New("only have one pending review"))
	}
	rev := h.pendingByNode(reviewID)
	if rev == nil {
		return errors.New("no such review")
	}
	if strings.TrimSpace(rev.body) == "" {
		// GitHub refuses to edit a review whose body is empty.
		return fmt.Errorf("github: update review body: %w: %w", github.ErrEmptyReviewBody, errors.New("Could not edit a review with a missing body"))
	}
	h.updates = append(h.updates, body)
	rev.body = body
	return nil
}

// submitFixture is a Backend over a fakeHost with a sidecar and lock in a temp
// directory.
type submitFixture struct {
	t     *testing.T
	host  *fakeHost
	gh    *fakeGH
	b     *Backend
	store pgposted.Store
	lock  pgposted.Locker
}

func newSubmitFixture(t *testing.T) *submitFixture {
	t.Helper()
	root := t.TempDir()
	f := &submitFixture{
		t:     t,
		host:  newFakeHost(headA),
		store: pgposted.Store{Dir: filepath.Join(root, "posted")},
		lock:  pgposted.Locker{Dir: filepath.Join(root, "locks"), Wait: 100 * time.Millisecond, Poll: 5 * time.Millisecond},
	}
	f.gh = &fakeGH{host: f.host}
	f.b = New(f.gh).WithPostedStore(f.store).WithLocker(f.lock)
	return f
}

func (f *submitFixture) submit(req pr.ReviewSubmitRequest) (pr.ReviewSubmitResult, error) {
	f.t.Helper()
	return f.b.SubmitReview(context.Background(), req)
}

func (f *submitFixture) mustSubmit(req pr.ReviewSubmitRequest) pr.ReviewSubmitResult {
	f.t.Helper()
	res, err := f.submit(req)
	if err != nil {
		f.t.Fatalf("SubmitReview: %v", err)
	}
	return res
}

func (f *submitFixture) sidecar() pgposted.State {
	f.t.Helper()
	st, err := f.store.Load("foo", "bar", 42)
	if err != nil {
		f.t.Fatalf("load sidecar: %v", err)
	}
	return st
}

// untouched asserts the host was neither written to nor created in.
func (f *submitFixture) untouched() {
	f.t.Helper()
	if len(f.host.creates) != 0 || len(f.host.updates) != 0 || len(f.host.writes) != 0 {
		f.t.Errorf("host must be untouched; creates=%v updates=%v writes=%d", f.host.creates, f.host.updates, len(f.host.writes))
	}
}

func point(path string, line int, body string) pr.ReviewComment {
	return pr.ReviewComment{Path: path, Line: line, Side: "RIGHT", Body: body}
}

func req(body string, cs ...pr.ReviewComment) pr.ReviewSubmitRequest {
	return pr.ReviewSubmitRequest{ID: "foo/bar#42", HeadSHA: headA, Body: body, Comments: cs}
}

func pointFP(path string, line int, body string) string {
	return pgposted.PointFingerprint(path, "RIGHT", line, body)
}

func isCode(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func sectionOf(head, text string) string {
	return pgposted.SectionOpen(head) + "\n" + text + "\n" + pgposted.SectionClose
}

// TestSubmitPostedThenNoChangeThenAppend walks the three statuses on one PR.
func TestSubmitPostedThenNoChangeThenAppend(t *testing.T) {
	f := newSubmitFixture(t)
	first := req("overall: fine", point("main.go", 12, "rename x"))

	res := f.mustSubmit(first)
	if res.Status != pr.StatusPosted || res.State != "pending" || res.Added != 1 || res.Body != pr.BodyWritten ||
		res.HeadSHA != headA || res.AsOf == "" || res.ReviewID == "" || res.URL == "" || res.ExtraPendingReviews != 0 {
		t.Fatalf("posted result = %+v", res)
	}
	if len(f.host.creates) != 1 || f.host.creates[0] != sectionOf(headA, "overall: fine") {
		t.Fatalf("created review must carry the head section as its body: %q", f.host.creates)
	}
	rev := f.host.pending[0]
	if rev.commit != headA || len(rev.comments) != 1 {
		t.Fatalf("review = %+v", rev)
	}
	body := rev.comments[0].body
	fp := pointFP("main.go", 12, "rename x")
	for _, want := range []string{"rename x", "*Posted by pg-connector at a1b2c3d.*", pgposted.Marker(fp)} {
		if !strings.Contains(body, want) {
			t.Errorf("comment body %q must contain %q", body, want)
		}
	}
	if !strings.HasSuffix(body, "*Posted by pg-connector at a1b2c3d.*\n"+pgposted.Marker(fp)) {
		t.Errorf("the marker must follow the attribution line: %q", body)
	}
	st := f.sidecar()
	if !st.Has(fp) || len(st.BodyHeads) != 1 || st.LastAppend == nil || st.LastAppend.Added != 1 || st.LastAppend.Head != headA {
		t.Fatalf("sidecar = %+v", st)
	}
	if res.LastAppend == nil || res.LastAppend.Added != 1 {
		t.Errorf("result last_append = %+v", res.LastAppend)
	}

	// The identical request again: nothing to write.
	f.host.creates, f.host.updates, f.host.writes = nil, nil, nil
	again := f.mustSubmit(first)
	if again.Status != pr.StatusNoChange || again.Added != 0 || again.AlreadyPresent != 1 || again.Body != pr.BodyKept ||
		again.ReviewID != rev.nodeID() || again.State != "pending" || again.URL == "" {
		t.Fatalf("no_change result = %+v", again)
	}
	f.untouched()
	if len(f.host.pending[0].comments) != 1 {
		t.Fatalf("no second comment may be written: %+v", f.host.pending[0].comments)
	}

	// A reply to a real thread plus a new point appends.
	thread := f.host.pending[0].comments[0].threadID
	third := f.mustSubmit(req("", pr.ReviewComment{ThreadID: thread, Body: "agreed"}, point("main.go", 40, "second finding")))
	if third.Status != pr.StatusAppend || third.Added != 2 || third.AlreadyPresent != 0 || third.Body != pr.BodyAbsent || third.ReviewID != rev.nodeID() {
		t.Fatalf("append result = %+v", third)
	}
	if len(f.host.pending) != 1 || len(f.host.pending[0].comments) != 3 {
		t.Fatalf("both comments must join the one review: %+v", f.host.pending)
	}
}

func TestSubmitNoChangeCreatesNoReview(t *testing.T) {
	f := newSubmitFixture(t)
	fp := pointFP("main.go", 12, "rename x")
	st := pgposted.State{}
	st.AddFingerprints(fp)
	if err := f.store.Save("foo", "bar", 42, st); err != nil {
		t.Fatal(err)
	}
	// The sidecar remembers the comment, the host holds nothing: the operator
	// deleted it.
	res := f.mustSubmit(req("", point("main.go", 12, "rename x")))
	if res.Status != pr.StatusNoChange || res.State != pr.StateNone || res.ReviewID != "" || res.URL != "" ||
		res.Dismissed != 1 || res.Added != 0 || res.Body != pr.BodyAbsent {
		t.Fatalf("result = %+v", res)
	}
	f.untouched()
	if len(f.host.pending) != 0 {
		t.Errorf("no review may be created: %+v", f.host.pending)
	}
	if after := f.sidecar(); after.LastAppend != nil {
		t.Errorf("a run that wrote nothing must not touch last_append: %+v", after)
	}
}

func TestSubmitAppendsToHandStartedReviewKeepingItsText(t *testing.T) {
	f := newSubmitFixture(t)
	hand := f.host.addPending(headA, "my own notes")

	res := f.mustSubmit(req("agent summary", point("main.go", 3, "x")))
	if res.Status != pr.StatusAppend || res.Body != pr.BodyWritten || res.Added != 1 || res.ReviewID != hand.nodeID() {
		t.Fatalf("result = %+v", res)
	}
	want := "my own notes\n\n" + sectionOf(headA, "agent summary")
	if hand.body != want {
		t.Fatalf("body = %q, want %q", hand.body, want)
	}
	if len(f.host.creates) != 0 {
		t.Errorf("an existing review must be used, not a new one created")
	}
}

// TestSubmitReplyOnlyFirstRequestThenBodyWritesTheSection is pg2-16jqj: a
// first request without a body must not leave an empty-bodied review that
// GitHub then refuses to edit ("Could not edit a review with a missing
// body"); a later request that carries a body writes its section.
func TestSubmitReplyOnlyFirstRequestThenBodyWritesTheSection(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addSubmitted(headA, hostComment{id: "C_old", path: "main.go", line: 1, body: "earlier", threadID: "T_old"})

	first := f.mustSubmit(req("", pr.ReviewComment{ThreadID: "T_old", Body: "agreed"}))
	if first.Status != pr.StatusPosted || first.Body != pr.BodyAbsent || first.Added != 1 {
		t.Fatalf("first = %+v", first)
	}
	if len(f.host.creates) != 1 || strings.TrimSpace(f.host.creates[0]) == "" {
		t.Fatalf("the review must be created with a non-empty body: %q", f.host.creates)
	}

	second := f.mustSubmit(req("overall: fine"))
	if second.Status != pr.StatusAppend || second.Body != pr.BodyWritten {
		t.Fatalf("second = %+v", second)
	}
	if body := f.host.pending[0].body; !strings.Contains(body, sectionOf(headA, "overall: fine")) {
		t.Fatalf("body = %q, want the section", body)
	}
	if st := f.sidecar(); len(st.BodyHeads) != 1 {
		t.Errorf("the written body head must be recorded: %+v", st)
	}
}

// TestSubmitCommentsOnlyFirstRequestThenBodyWritesTheSection: same, with new
// points instead of a reply.
func TestSubmitCommentsOnlyFirstRequestThenBodyWritesTheSection(t *testing.T) {
	f := newSubmitFixture(t)
	f.mustSubmit(req("", point("main.go", 3, "x")))
	second := f.mustSubmit(req("summary"))
	if second.Status != pr.StatusAppend || second.Body != pr.BodyWritten {
		t.Fatalf("second = %+v", second)
	}
}

// TestSubmitEmptyBodiedReviewDegradesToSkippedBody: a pending review that
// already has an empty body (hand-started) cannot be edited; the body is
// skipped and the comments still go out.
func TestSubmitEmptyBodiedReviewDegradesToSkippedBody(t *testing.T) {
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, "")
	res := f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if res.Body != pr.BodySkippedEmptyReview || res.Status != pr.StatusAppend || res.Added != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(rev.comments) != 1 || rev.body != "" {
		t.Fatalf("review = %+v", rev)
	}
	if st := f.sidecar(); len(st.BodyHeads) != 0 {
		t.Errorf("a skipped body must not be recorded as written: %+v", st)
	}

	// Body only, nothing else to write: no_change, body skipped.
	f2 := newSubmitFixture(t)
	f2.host.addPending(headA, "")
	res = f2.mustSubmit(req("summary"))
	if res.Body != pr.BodySkippedEmptyReview || res.Status != pr.StatusNoChange {
		t.Fatalf("body-only result = %+v", res)
	}
}

func TestSubmitNewHeadAddsASecondSection(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headA, sectionOf(headA, "old head text"))
	f.host.head = headB
	r := req("new head text", point("main.go", 3, "x"))
	r.HeadSHA = headB
	res := f.mustSubmit(r)
	if res.Status != pr.StatusAppend || res.Body != pr.BodyWritten {
		t.Fatalf("result = %+v", res)
	}
	want := sectionOf(headA, "old head text") + "\n\n" + sectionOf(headB, "new head text")
	if f.host.pending[0].body != want {
		t.Fatalf("body = %q, want %q", f.host.pending[0].body, want)
	}
}

func TestSubmitBodyDispositions(t *testing.T) {
	big := strings.Repeat("x", maxReviewBody-10)
	cases := map[string]struct {
		existing string
		sidecar  bool // sidecar records the head's section as written
		body     string
		want     string
		wantBody string // expected review body after the run
		updates  int
	}{
		"written":   {"typed", false, "text", pr.BodyWritten, "typed\n\n" + sectionOf(headA, "text"), 1},
		"kept":      {sectionOf(headA, "first text"), true, "different text", pr.BodyKept, sectionOf(headA, "first text"), 0},
		"absent":    {"typed", false, "", pr.BodyAbsent, "typed", 0},
		"dismissed": {"typed", true, "text", pr.BodyDismissed, "typed", 0},
		"too_large": {big, false, "text that does not fit", pr.BodyTooLarge, big, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSubmitFixture(t)
			rev := f.host.addPending(headA, c.existing)
			if c.sidecar {
				st := pgposted.State{}
				st.AddBodyHead(headA)
				if err := f.store.Save("foo", "bar", 42, st); err != nil {
					t.Fatal(err)
				}
			}
			// One comment so the run always has something to do besides the body.
			res := f.mustSubmit(req(c.body, point("main.go", 3, "x")))
			if res.Body != c.want {
				t.Fatalf("body = %q, want %q", res.Body, c.want)
			}
			if rev.body != c.wantBody || len(f.host.updates) != c.updates {
				t.Fatalf("review body = %q (updates %d), want %q (updates %d)", rev.body, len(f.host.updates), c.wantBody, c.updates)
			}
			if res.Status != pr.StatusAppend || res.Added != 1 {
				t.Errorf("result = %+v", res)
			}
		})
	}
}

// TestSubmitBodyOnlyNoChange: a kept, dismissed or absent body with nothing
// else to write is no_change and the review is untouched.
func TestSubmitBodyOnlyNoChange(t *testing.T) {
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, sectionOf(headA, "first"))
	res := f.mustSubmit(req("second text"))
	if res.Status != pr.StatusNoChange || res.Body != pr.BodyKept || res.ReviewID != rev.nodeID() {
		t.Fatalf("result = %+v", res)
	}
	f.untouched()
}

func TestSubmitBodySectionNeededCreatesReviewWithBodyOnly(t *testing.T) {
	f := newSubmitFixture(t)
	res := f.mustSubmit(req("just a summary"))
	if res.Status != pr.StatusPosted || res.Body != pr.BodyWritten || res.Added != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.writes) != 0 {
		t.Errorf("no comment write is expected: %+v", f.host.writes)
	}
}

// TestSubmitBodyMergesFromTheFreshRead: the body is rebuilt from a read made
// immediately before the write, so text typed after the run's first read
// survives.
func TestSubmitBodyMergesFromTheFreshRead(t *testing.T) {
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, "typed early")
	f.host.afterRead = func(call int, h *fakeHost) {
		if call == 1 {
			rev.body = "typed early\nand typed late"
		}
	}
	f.mustSubmit(req("summary"))
	want := "typed early\nand typed late\n\n" + sectionOf(headA, "summary")
	if rev.body != want {
		t.Fatalf("body = %q, want %q", rev.body, want)
	}
}

func TestSubmitSectionAddedMeanwhileIsKept(t *testing.T) {
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, "")
	f.host.afterRead = func(call int, h *fakeHost) {
		if call == 1 {
			rev.body = sectionOf(headA, "someone else's text")
		}
	}
	res := f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if res.Body != pr.BodyKept || len(f.host.updates) != 0 {
		t.Fatalf("result = %+v updates=%v", res, f.host.updates)
	}
}

func TestSubmitTwoPendingReviews(t *testing.T) {
	f := newSubmitFixture(t)
	low := f.host.addPending(headA, "")
	f.host.addPending(headA, "other")

	res := f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if res.ExtraPendingReviews != 1 || res.Body != pr.BodySkippedExtraPending || res.Status != pr.StatusAppend || res.ReviewID != low.nodeID() {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.updates) != 0 || len(low.comments) != 1 || len(f.host.pending[1].comments) != 0 {
		t.Fatalf("comments go to the lowest-numbered review and no body is written: %+v", f.host.pending)
	}
	if st := f.sidecar(); len(st.BodyHeads) != 0 {
		t.Errorf("a skipped body must not be recorded as written: %+v", st)
	}
	if len(f.host.pending) != 2 {
		t.Errorf("nothing may be deleted")
	}
}

// TestSubmitUpdateRefusedForTwoPendingReviews: the host refuses a body write
// because a second pending review appeared; the comments still go out.
func TestSubmitUpdateRefusedForTwoPendingReviews(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headA, "")
	f.host.updateTwoPending = true
	res := f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if res.Body != pr.BodySkippedExtraPending || res.Added != 1 {
		t.Fatalf("result = %+v", res)
	}
	if st := f.sidecar(); len(st.BodyHeads) != 0 {
		t.Errorf("sidecar = %+v", st)
	}
}

// TestSubmitCreate422LoopsToAppend: a create answered "one pending review"
// (a hand-started review appeared) starts the run over, which appends.
func TestSubmitCreate422LoopsToAppend(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.create422 = 1
	res := f.mustSubmit(req("", point("main.go", 3, "x")))
	if res.Status != pr.StatusAppend || res.Added != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.pending) != 1 || len(f.host.pending[0].comments) != 1 || len(f.host.creates) != 0 {
		t.Fatalf("the hand-started review must receive the comment: %+v", f.host.pending)
	}
}

// TestSubmitStartingOverIsBounded: when the host keeps changing under the run
// (a review is created by someone else and gone again on every read), the run
// gives up as unavailable instead of looping forever.
func TestSubmitStartingOverIsBounded(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.createErr = fmt.Errorf("github: create pending review: %w: %w", github.ErrPendingReviewExists, errors.New("HTTP 422"))
	_, err := f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	if f.host.reads != maxSubmitAttempts {
		t.Errorf("reads = %d, want %d", f.host.reads, maxSubmitAttempts)
	}
	f.untouched()
}

// TestSubmitCreateRaceIsDetectedNotRepaired: a second pending review appears
// around the create; the run reports it, appends to the lowest-numbered one,
// and deletes nothing.
func TestSubmitCreateRaceIsDetectedNotRepaired(t *testing.T) {
	f := newSubmitFixture(t)
	var low *hostReview
	f.host.afterCreate = func(h *fakeHost) {
		low = &hostReview{dbID: 1, commit: headA, body: "raced"}
		h.pending = append([]*hostReview{low}, h.pending...)
	}
	res := f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if res.Status != pr.StatusPosted || res.ExtraPendingReviews != 1 || res.Body != pr.BodySkippedExtraPending || res.ReviewID != low.nodeID() {
		t.Fatalf("result = %+v", res)
	}
	if len(low.comments) != 1 || len(f.host.pending) != 2 {
		t.Fatalf("pending = %+v", f.host.pending)
	}
	if st := f.sidecar(); len(st.BodyHeads) != 0 {
		t.Errorf("sidecar = %+v", st)
	}
}

func TestSubmitCreateErrorTaxonomy(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.createErr = errors.New("gh: Unprocessable Entity: commit_id is not part of the pull request (HTTP 422)")
	_, err := f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrInvalidArgument)

	f.host.createErr = errors.New("gh: API rate limit exceeded (HTTP 403)")
	_, err = f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
}

func TestSubmitReadErrorTaxonomy(t *testing.T) {
	cases := map[string]struct {
		err  error
		want error
	}{
		"not found":   {errors.New("gh: Not Found (HTTP 404)"), scriptout.ErrNotFound},
		"auth":        {errors.Join(github.ErrGHAuthInvalid, errors.New("gh: Forbidden (HTTP 403)")), scriptout.ErrUnauthenticated},
		"unavailable": {errors.New("gh: Bad Gateway (HTTP 502)"), scriptout.ErrUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSubmitFixture(t)
			f.host.readErr = func(int) error { return c.err }
			_, err := f.submit(req("", point("main.go", 3, "x")))
			isCode(t, err, c.want)
			f.untouched()
		})
	}
}

func TestSubmitRejectsBadInput(t *testing.T) {
	tooMany := make([]pr.ReviewComment, maxRequestComments+1)
	for i := range tooMany {
		tooMany[i] = point("a.go", i+1, "x")
	}
	mut := map[string]func(*pr.ReviewSubmitRequest){
		"bad id":   func(r *pr.ReviewSubmitRequest) { r.ID = "nope" },
		"no head":  func(r *pr.ReviewSubmitRequest) { r.HeadSHA = "" },
		"bad side": func(r *pr.ReviewSubmitRequest) { r.Comments[0].Side = "MIDDLE" },
		"no line":  func(r *pr.ReviewSubmitRequest) { r.Comments[0].Line = 0 },
		"no path":  func(r *pr.ReviewSubmitRequest) { r.Comments[0].Path = "" },
		"thread with path": func(r *pr.ReviewSubmitRequest) {
			r.Comments[0] = pr.ReviewComment{ThreadID: "PRRT_1", Path: "a.go", Body: "x"}
		},
		"thread with line": func(r *pr.ReviewSubmitRequest) {
			r.Comments[0] = pr.ReviewComment{ThreadID: "PRRT_1", Line: 3, Body: "x"}
		},
		"over the comment cap":     func(r *pr.ReviewSubmitRequest) { r.Comments = tooMany },
		"section delimiter":        func(r *pr.ReviewSubmitRequest) { r.Body = "a " + pgposted.SectionClose },
		"body with a short head":   func(r *pr.ReviewSubmitRequest) { r.Body = "text"; r.HeadSHA = "abc" },
		"section opener in a body": func(r *pr.ReviewSubmitRequest) { r.Body = "<!-- pg-section head=zzz -->" },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			f := newSubmitFixture(t)
			r := req("", point("main.go", 3, "x"))
			m(&r)
			_, err := f.submit(r)
			isCode(t, err, scriptout.ErrInvalidArgument)
			if f.host.reads != 0 {
				t.Errorf("validation must run before any read; reads = %d", f.host.reads)
			}
			f.untouched()
		})
	}
}

// TestSubmitCommentCapIsSizedForTheExecTimeout pins the ruling of bead
// pg2-m79ch: a request at the cap is written in at most
// ceil(cap/10) sequential GraphQL documents, and at the measured worst case of
// about 4s per document plus two reads it must finish well inside
// scriptout.DefaultExecTimeout, or the umbrella kills the first call mid-way.
func TestSubmitCommentCapIsSizedForTheExecTimeout(t *testing.T) {
	const (
		aliasesPerDocument = 10 // github.maxWriteAliasesPerDocument
		perDocument        = 4 * time.Second
		reads              = 8 * time.Second // lookup + re-read, generous
	)
	documents := (maxRequestComments + aliasesPerDocument - 1) / aliasesPerDocument
	worst := time.Duration(documents)*perDocument + reads
	if worst*5 > scriptout.DefaultExecTimeout*4 { // keep at least a 20% margin
		t.Fatalf("cap %d needs ~%v worst case; exec timeout is %v (lower the cap or revisit the ruling)",
			maxRequestComments, worst, scriptout.DefaultExecTimeout)
	}
}

func TestSubmitCommentsAtTheCapAreAccepted(t *testing.T) {
	f := newSubmitFixture(t)
	cs := make([]pr.ReviewComment, maxRequestComments)
	for i := range cs {
		cs[i] = point("a.go", i+1, "x")
	}
	res := f.mustSubmit(req("", cs...))
	if res.Added != maxRequestComments {
		t.Fatalf("added = %d", res.Added)
	}
}

func TestSubmitHeadMismatchUnderLockWritesNothing(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headB, "")
	f.host.head = headB              // the head moved
	f.host.commits = []string{headB} // headA is no commit of the PR (force-pushed away)
	_, err := f.submit(req("summary", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrInvalidArgument)
	for _, want := range []string{"head moved", headA, headB, "not a commit of the pull request", "nothing was written"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
	f.untouched()
	if _, statErr := os.Stat(filepath.Join(f.store.Dir)); statErr == nil {
		t.Errorf("the sidecar must not be created by a refused run")
	}
}

// TestSubmitHeadCheckUsesTheReadUnderTheLock: the head compared is the one the
// lookup under the lock returned, case-insensitively.
func TestSubmitHeadCheckUsesTheReadUnderTheLock(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.head = strings.ToUpper(headA)
	f.mustSubmit(req("", point("main.go", 3, "x")))
	if f.host.reads == 0 {
		t.Fatal("the run must read the head itself")
	}
}

func TestSubmitSupersedePendingIsAcceptedAndIgnored(t *testing.T) {
	f := newSubmitFixture(t)
	hand := f.host.addPending(headB, "keep me")
	r := req("", point("main.go", 3, "x"))
	r.SupersedePending = true
	res := f.mustSubmit(r)
	if res.Status != pr.StatusAppend || len(f.host.pending) != 1 || hand.body != "keep me" || len(hand.comments) != 1 {
		t.Fatalf("result = %+v pending = %+v", res, f.host.pending)
	}
}

func TestSubmitSideNormalizationAndIdenticalItemsMerge(t *testing.T) {
	f := newSubmitFixture(t)
	a := pr.ReviewComment{Path: "main.go", Line: 3, Body: "x"} // side "" is RIGHT
	b := pr.ReviewComment{Path: "main.go", Line: 3, Side: "right", Body: "x\r\n"}
	l := pr.ReviewComment{Path: "main.go", Line: 3, Side: "left", Body: "x"}
	res := f.mustSubmit(req("", a, b, l))
	if res.Added != 2 || len(f.host.writes) != 1 || len(f.host.writes[0].items) != 2 {
		t.Fatalf("identical items are one item: result = %+v writes = %+v", res, f.host.writes)
	}
	var sides []string
	for _, it := range f.host.writes[0].items {
		sides = append(sides, it.Side)
	}
	if strings.Join(sides, ",") != "RIGHT,LEFT" {
		t.Errorf("sides = %v", sides)
	}
}

// TestSubmitCRLFRequestMatchesStoredComment: a comment that was written with
// LF line endings is recognised when the request spells it with CRLF and
// trailing whitespace, so a web-UI round trip cannot cause a repost.
func TestSubmitCRLFRequestMatchesStoredComment(t *testing.T) {
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, "")
	fp := pointFP("main.go", 3, "line one\nline two")
	rev.comments = append(rev.comments, hostComment{id: "C_1", path: "main.go", line: 3, body: "line one\r\nline two\r\n\r\n" + pgposted.Marker(fp), commit: headA})
	res := f.mustSubmit(req("", point("main.go", 3, "line one\r\nline two  \r\n")))
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 1 {
		t.Fatalf("result = %+v", res)
	}
	f.untouched()
}

func TestSubmitSubmittedReviewCommentsCountAsPresent(t *testing.T) {
	f := newSubmitFixture(t)
	fp := pointFP("main.go", 3, "x")
	f.host.addSubmitted("old", hostComment{id: "C_s", threadID: "PRRT_s", path: "main.go", line: 3, body: "x\n" + pgposted.Marker(fp)})
	res := f.mustSubmit(req("", point("main.go", 3, "x")))
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 1 || res.State != pr.StateNone {
		t.Fatalf("result = %+v", res)
	}
}

// TestSubmitReplyRidesInThePendingReview: a reply to a thread of a SUBMITTED
// review, and one to a thread of the pending review, both pass the pending
// review's id.
func TestSubmitReplyRidesInThePendingReview(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addSubmitted("old", hostComment{id: "C_s", threadID: "PRRT_sub", path: "main.go", line: 3, body: "earlier"})
	pend := f.host.addPending(headA, "")
	pend.comments = append(pend.comments, hostComment{id: "C_p", threadID: "PRRT_pend", path: "a.go", line: 1, body: "draft"})
	f.host.threads["PRRT_pend"] = true

	res := f.mustSubmit(req("",
		pr.ReviewComment{ThreadID: "PRRT_sub", Body: "reply one"},
		pr.ReviewComment{ThreadID: "PRRT_pend", Body: "reply two"}))
	if res.Status != pr.StatusAppend || res.Added != 2 {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.writes) != 1 || f.host.writes[0].reviewID != pend.nodeID() {
		t.Fatalf("every reply must carry the pending review id: %+v", f.host.writes)
	}
	for _, it := range f.host.writes[0].items {
		if !it.IsReply() {
			t.Errorf("item = %+v", it)
		}
	}
	if len(pend.comments) != 3 {
		t.Errorf("replies must land in the pending review: %+v", pend.comments)
	}
}

func TestSubmitReplyToUnknownThreadIsPermanentFailure(t *testing.T) {
	f := newSubmitFixture(t)
	_, err := f.submit(req("", pr.ReviewComment{ThreadID: "PRRT_other_pr", Body: "x"}))
	isCode(t, err, scriptout.ErrInvalidArgument)
	fp := pgposted.ReplyFingerprint("PRRT_other_pr", "x")
	want := "0 of 1 comments landed; failed: thread_not_found:" + fp
	if !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("err = %q, want suffix %q", err, want)
	}
}

func TestSubmitPartialFailureMessageShapeAndCodes(t *testing.T) {
	fpBad := pointFP("bad.go", 5, "nope")
	fpOK := pointFP("ok.go", 1, "fine")
	mixed := []pr.ReviewComment{point("ok.go", 1, "fine"), point("bad.go", 5, "nope")}

	t.Run("permanent only is invalid_argument", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.failItem = func(it github.ReviewWriteItem) github.WriteFailReason {
			if it.Path == "bad.go" {
				return github.ReasonAnchorRejected
			}
			return ""
		}
		_, err := f.submit(req("", mixed...))
		isCode(t, err, scriptout.ErrInvalidArgument)
		want := "1 of 2 comments landed; failed: anchor_rejected:" + fpBad + ":bad.go:5"
		if !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("err = %q, want suffix %q", err, want)
		}
		st := f.sidecar()
		if !st.Has(fpOK) || st.Has(fpBad) || st.LastAppend == nil || st.LastAppend.Added != 1 {
			t.Fatalf("only the confirmed fingerprint is recorded: %+v", st)
		}
		// Replaying the identical request lands nothing new and fails the same
		// way: a permanent rejection needs a changed request.
		f.host.writes = nil
		_, err = f.submit(req("", mixed...))
		isCode(t, err, scriptout.ErrInvalidArgument)
		want = "0 of 1 comments landed; failed: anchor_rejected:" + fpBad + ":bad.go:5"
		if !strings.HasSuffix(err.Error(), want) || len(f.host.writes) != 1 || len(f.host.writes[0].items) != 1 {
			t.Fatalf("replay err = %q writes = %+v", err, f.host.writes)
		}
	})

	t.Run("a retryable failure is unavailable and the replay converges", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.failItem = func(it github.ReviewWriteItem) github.WriteFailReason {
			if it.Path == "bad.go" {
				return github.ReasonRateLimited
			}
			return ""
		}
		_, err := f.submit(req("", mixed...))
		isCode(t, err, scriptout.ErrUnavailable)
		if !strings.Contains(err.Error(), "1 of 2 comments landed; failed: rate_limited:"+fpBad+":bad.go:5") {
			t.Fatalf("err = %q", err)
		}
		f.host.failItem = nil
		res := f.mustSubmit(req("", mixed...))
		if res.Status != pr.StatusAppend || res.Added != 1 || res.AlreadyPresent != 1 {
			t.Fatalf("replay result = %+v", res)
		}
		if len(f.host.pending[0].comments) != 2 {
			t.Fatalf("comments = %+v", f.host.pending[0].comments)
		}
	})

	t.Run("a mix of permanent and retryable is unavailable", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.failItem = func(it github.ReviewWriteItem) github.WriteFailReason {
			if it.Path == "bad.go" {
				return github.ReasonAnchorRejected
			}
			return github.ReasonRateLimited
		}
		_, err := f.submit(req("", mixed...))
		isCode(t, err, scriptout.ErrUnavailable)
	})
}

func TestSubmitFailureListIsCappedAtTwenty(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.failItem = func(github.ReviewWriteItem) github.WriteFailReason { return github.ReasonAnchorRejected }
	cs := make([]pr.ReviewComment, 25)
	for i := range cs {
		cs[i] = point("a.go", i+1, "x")
	}
	_, err := f.submit(req("", cs...))
	isCode(t, err, scriptout.ErrInvalidArgument)
	msg := err.Error()
	if !strings.Contains(msg, "0 of 25 comments landed; failed: ") || strings.Count(msg, "anchor_rejected:") != 20 || !strings.HasSuffix(msg, "(+5 more)") {
		t.Fatalf("message = %q", msg)
	}
}

// TestSubmitTheReReadDecidesWhatLanded: a write answered as landed that the
// re-read does not show is unconfirmed and not recorded; one answered as
// failed that the re-read shows IS landed.
func TestSubmitTheReReadDecidesWhatLanded(t *testing.T) {
	t.Run("answered landed but absent", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.addPending(headA, "")
		f.host.dropWrites = true
		_, err := f.submit(req("", point("main.go", 3, "x")))
		isCode(t, err, scriptout.ErrUnavailable)
		if !strings.Contains(err.Error(), "0 of 1 comments landed; failed: unconfirmed:"+pointFP("main.go", 3, "x")+":main.go:3") {
			t.Fatalf("err = %q", err)
		}
		if st := f.sidecar(); len(st.Fingerprints) != 0 || st.LastAppend != nil {
			t.Fatalf("nothing may be recorded: %+v", st)
		}
	})
	t.Run("answered failed but present", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.addPending(headA, "")
		f.host.failItem = func(github.ReviewWriteItem) github.WriteFailReason { return github.ReasonUnconfirmed }
		f.host.storeDespiteFail = true
		res := f.mustSubmit(req("", point("main.go", 3, "x")))
		if res.Added != 1 || res.Status != pr.StatusAppend {
			t.Fatalf("result = %+v", res)
		}
		if st := f.sidecar(); !st.Has(pointFP("main.go", 3, "x")) {
			t.Fatalf("sidecar = %+v", st)
		}
	})
}

// TestSubmitFailedReReadRecordsNothing covers truncation and pagination of
// the re-read: the lookup is fail-closed, so a re-read that could not read
// every comment is an error, and then no sidecar entry is written.
func TestSubmitFailedReReadRecordsNothing(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headA, "")
	f.host.readErr = func(call int) error {
		if call >= 2 { // the first read passes; the re-read after the writes does not
			return errors.New("github: review comments truncated (100 of 150 read)")
		}
		return nil
	}
	_, err := f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	if !strings.Contains(err.Error(), "could not be confirmed") {
		t.Errorf("err = %q", err)
	}
	if _, statErr := os.Stat(f.store.Dir); statErr == nil {
		if st := f.sidecar(); len(st.Fingerprints) != 0 || len(st.BodyHeads) != 0 || st.LastAppend != nil {
			t.Fatalf("no entry may be written: %+v", st)
		}
	}
}

func TestSubmitMarkersOnLaterPagesAreSeen(t *testing.T) {
	// The lookup returns every comment of every pending review; a marker on the
	// 150th comment still classifies the request item as already present.
	f := newSubmitFixture(t)
	rev := f.host.addPending(headA, "")
	for i := 0; i < 149; i++ {
		rev.comments = append(rev.comments, hostComment{id: fmt.Sprintf("C_%d", i), path: "x.go", line: i + 1, body: "other"})
	}
	fp := pointFP("main.go", 3, "x")
	rev.comments = append(rev.comments, hostComment{id: "C_last", path: "main.go", line: 3, body: "x " + pgposted.Marker(fp)})
	res := f.mustSubmit(req("", point("main.go", 3, "x")))
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestSubmitDismissedCommentIsNotWrittenAgain(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headA, "")
	fp := pointFP("main.go", 3, "x")
	st := pgposted.State{}
	st.AddFingerprints(fp)
	if err := f.store.Save("foo", "bar", 42, st); err != nil {
		t.Fatal(err)
	}
	res := f.mustSubmit(req("", point("main.go", 3, "x"), point("main.go", 9, "y")))
	if res.Dismissed != 1 || res.Added != 1 || res.Status != pr.StatusAppend {
		t.Fatalf("result = %+v", res)
	}
	if len(f.host.writes[0].items) != 1 || f.host.writes[0].items[0].Line != 9 {
		t.Fatalf("writes = %+v", f.host.writes)
	}
}

func TestSubmitLockIsHeldForTheRun(t *testing.T) {
	f := newSubmitFixture(t)
	held, err := f.lock.Acquire("foo", "bar", 42)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	if f.host.reads != 0 {
		t.Errorf("nothing may be read without the lock; reads = %d", f.host.reads)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	f.mustSubmit(req("", point("main.go", 3, "x")))
	// The lock is released after the run: a second acquire succeeds at once.
	k, err := f.lock.Acquire("foo", "bar", 42)
	if err != nil {
		t.Fatalf("the lock must be released after the run: %v", err)
	}
	_ = k.Release()
}

func TestSubmitWithoutAStateDirectoryIsUnavailable(t *testing.T) {
	gh := &fakeGH{host: newFakeHost(headA)}
	_, err := New(gh).SubmitReview(context.Background(), req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
}

func TestSubmitCorruptSidecarWritesNothing(t *testing.T) {
	f := newSubmitFixture(t)
	path, err := f.store.Path("foo", "bar", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	f.untouched()
}

// TestSubmitSidecarSaveFailureIsAnError: the content reached the host but the
// sidecar could not be saved; the retry converges through the markers.
func TestSubmitSidecarSaveFailureIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}
	f := newSubmitFixture(t)
	if err := os.MkdirAll(f.store.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.store.Dir, 0o700) })
	_, err := f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	if !strings.Contains(err.Error(), "sidecar") {
		t.Errorf("err = %q", err)
	}
	if len(f.host.pending) != 1 || len(f.host.pending[0].comments) != 1 {
		t.Fatalf("the content did reach the host: %+v", f.host.pending)
	}
	// Once the sidecar is writable again the identical request converges.
	if err := os.Chmod(f.store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	res := f.mustSubmit(req("", point("main.go", 3, "x")))
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 1 || len(f.host.pending[0].comments) != 1 {
		t.Fatalf("replay result = %+v", res)
	}
}

func TestSubmitNeverDeletesOrSubmits(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.addPending(headB, "stale text")
	f.host.addPending(headB, "other stale")
	before := len(f.host.pending)
	f.mustSubmit(req("summary", point("main.go", 3, "x")))
	if len(f.host.pending) != before || len(f.host.submitted) != 0 {
		t.Fatalf("pending = %d submitted = %d", len(f.host.pending), len(f.host.submitted))
	}
	for _, op := range f.gh.ops {
		if op == "delete" || op == "submit" {
			t.Errorf("forbidden op %q", op)
		}
	}
}
