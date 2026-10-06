package escalate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeTracker is an in-memory Tracker. A bead listed by ListOpen is every
// created bead that is not closed.
type fakeTracker struct {
	issues []*fakeIssue
	nextID int

	failList, failCreate, failComment, failSetMetadata, failClose error
	creates                                                       int
}

type fakeIssue struct {
	Issue
	NewIssue NewIssue
	Closed   string
	Comments []string
}

func (f *fakeTracker) ListOpen(context.Context) ([]Issue, error) {
	if f.failList != nil {
		return nil, f.failList
	}
	var out []Issue
	for _, is := range f.issues {
		if is.Closed == "" {
			cp := is.Issue
			cp.Metadata = map[string]string{}
			for k, v := range is.Metadata {
				cp.Metadata[k] = v
			}
			out = append(out, cp)
		}
	}
	return out, nil
}

func (f *fakeTracker) Create(_ context.Context, in NewIssue) (Issue, error) {
	if f.failCreate != nil {
		return Issue{}, f.failCreate
	}
	f.nextID++
	f.creates++
	md := map[string]string{}
	for k, v := range in.Metadata {
		md[k] = v
	}
	is := &fakeIssue{Issue: Issue{ID: fmt.Sprintf("bd-%d", f.nextID), Title: in.Title, Labels: in.Labels, Metadata: md}, NewIssue: in}
	f.issues = append(f.issues, is)
	return is.Issue, nil
}

func (f *fakeTracker) find(id string) *fakeIssue {
	for _, is := range f.issues {
		if is.ID == id {
			return is
		}
	}
	return nil
}

func (f *fakeTracker) Comment(_ context.Context, id, body string) error {
	if f.failComment != nil {
		return f.failComment
	}
	f.find(id).Comments = append(f.find(id).Comments, body)
	return nil
}

func (f *fakeTracker) SetMetadata(_ context.Context, id string, md map[string]string) error {
	if f.failSetMetadata != nil {
		return f.failSetMetadata
	}
	is := f.find(id)
	for k, v := range md {
		is.Metadata[k] = v
	}
	return nil
}

func (f *fakeTracker) Close(_ context.Context, id, reason string) error {
	if f.failClose != nil {
		return f.failClose
	}
	f.find(id).Closed = reason
	return nil
}

func (f *fakeTracker) open() []*fakeIssue {
	var out []*fakeIssue
	for _, is := range f.issues {
		if is.Closed == "" {
			out = append(out, is)
		}
	}
	return out
}

type fakeNotifier struct {
	sent []Notification
	fail error
}

func (n *fakeNotifier) Notify(_ context.Context, x Notification) error {
	if n.fail != nil {
		return n.fail
	}
	n.sent = append(n.sent, x)
	return nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)} }

func blocked(pr, reason string) Outcome {
	return Outcome{
		PR: pr, Status: StatusBlocked, Reason: reason,
		Message:   "the stale review was left in place",
		HeadSHA:   "9f3c1e2",
		ReviewURL: "https://example.test/" + pr + "#review-1",
	}
}

func resolved(pr, status string) Outcome {
	return Outcome{PR: pr, Status: status, HeadSHA: "aaaa111"}
}

type rig struct {
	t   *testing.T
	trk *fakeTracker
	not *fakeNotifier
	clk *fakeClock
	esc *Escalator
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	r := &rig{t: t, trk: &fakeTracker{}, not: &fakeNotifier{}, clk: newClock()}
	r.esc = New(r.trk, r.not, r.clk.now, cfg)
	return r
}

func (r *rig) handle(o Outcome) error {
	r.t.Helper()
	_, err := r.esc.Handle(context.Background(), o)
	return err
}

func (r *rig) mustHandle(o Outcome) {
	r.t.Helper()
	if err := r.handle(o); err != nil {
		r.t.Fatalf("Handle(%s %s): %v", o.PR, o.Status, err)
	}
}

func TestBlockedCreatesExactlyOneBeadAndNotifies(t *testing.T) {
	r := newRig(t, Config{Labels: []string{"repo-label"}, Priority: "P1"})
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))

	open := r.trk.open()
	if len(open) != 1 {
		t.Fatalf("open beads = %d, want 1", len(open))
	}
	b := open[0]
	for _, want := range []string{LabelHuman, LabelHumanFocusRequired, DefaultEscalationLabel, "repo-label"} {
		if !contains(b.Labels, want) {
			t.Errorf("labels %v lack %q", b.Labels, want)
		}
	}
	if b.NewIssue.Priority != "P1" {
		t.Errorf("priority = %q, want P1", b.NewIssue.Priority)
	}
	for _, want := range []string{"acme/api#1", "https://example.test/acme/api#1#review-1", ReasonHumanEdited, "9f3c1e2"} {
		if !strings.Contains(b.NewIssue.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, b.NewIssue.Description)
		}
	}
	if !strings.Contains(b.Title, "acme/api#1") {
		t.Errorf("title %q does not name the PR", b.Title)
	}
	md := b.Metadata
	if md[KeyKey] != "pr:acme/api#1" || md[KeyKind] != KindPR || md[KeyReason] != ReasonHumanEdited || md[KeyHead] != "9f3c1e2" {
		t.Errorf("metadata = %v", md)
	}
	if len(r.not.sent) != 1 {
		t.Fatalf("notifications = %d, want 1", len(r.not.sent))
	}
	n := r.not.sent[0]
	if !strings.Contains(n.Body, b.ID) || n.URL == "" || !strings.Contains(n.Title, "acme/api#1") {
		t.Errorf("notification = %+v", n)
	}
	if md[KeyLastNotified] != r.clk.now().Format(time.RFC3339) {
		t.Errorf("last notified = %q", md[KeyLastNotified])
	}
}

func TestDetectionFailedWithoutURLStillEscalates(t *testing.T) {
	r := newRig(t, Config{})
	o := blocked("acme/api#1", ReasonDetectionFailed)
	o.ReviewURL = ""
	r.mustHandle(o)
	if got := r.trk.open()[0].NewIssue.Description; !strings.Contains(got, "unknown") {
		t.Errorf("description should say the URL is unknown:\n%s", got)
	}
}

func TestDedupeAddsCommentNotSecondBeadAndHonoursRenotifyInterval(t *testing.T) {
	r := newRig(t, Config{RenotifyInterval: time.Hour})
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))

	// A repeat 10 minutes later: same bead, a comment, NO second notification.
	r.clk.advance(10 * time.Minute)
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	if len(r.trk.open()) != 1 || r.trk.creates != 1 {
		t.Fatalf("beads open=%d created=%d, want 1/1", len(r.trk.open()), r.trk.creates)
	}
	if len(r.not.sent) != 1 {
		t.Fatalf("notifications = %d within the interval, want 1", len(r.not.sent))
	}
	if len(r.trk.open()[0].Comments) != 1 {
		t.Fatalf("comments = %v, want the bump comment", r.trk.open()[0].Comments)
	}

	// Just under the interval since the FIRST send: still silent.
	r.clk.advance(49 * time.Minute)
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	if len(r.not.sent) != 1 {
		t.Fatalf("notifications = %d at 59m, want 1", len(r.not.sent))
	}

	// Exactly at the interval: notify again and restamp.
	r.clk.advance(time.Minute)
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	if len(r.not.sent) != 2 {
		t.Fatalf("notifications = %d at 60m, want 2", len(r.not.sent))
	}
	if got := r.trk.open()[0].Metadata[KeyLastNotified]; got != r.clk.now().Format(time.RFC3339) {
		t.Errorf("last notified = %q, want the restamp", got)
	}
	if r.trk.creates != 1 {
		t.Errorf("created %d beads, want 1", r.trk.creates)
	}
}

func TestBumpRefreshesReasonHeadAndURL(t *testing.T) {
	r := newRig(t, Config{})
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
	next := blocked("acme/api#1", ReasonHumanEdited)
	next.HeadSHA = "beef002"
	r.mustHandle(next)
	md := r.trk.open()[0].Metadata
	if md[KeyReason] != ReasonHumanEdited || md[KeyHead] != "beef002" {
		t.Errorf("metadata not refreshed: %v", md)
	}
}

func TestDifferentPRsGetSeparateBeads(t *testing.T) {
	r := newRig(t, Config{})
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	r.mustHandle(blocked("acme/api#2", ReasonHumanEdited))
	if len(r.trk.open()) != 2 || len(r.not.sent) != 2 {
		t.Fatalf("beads=%d notifications=%d, want 2/2", len(r.trk.open()), len(r.not.sent))
	}
}

func TestAutoCloseOnPostedSkippedReplaced(t *testing.T) {
	for _, status := range []string{StatusPosted, StatusSkipped, StatusReplaced, StatusAppend, StatusNoChange} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t, Config{})
			r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
			r.mustHandle(blocked("acme/api#2", ReasonHumanEdited))

			r.mustHandle(resolved("acme/api#1", status))

			open := r.trk.open()
			if len(open) != 1 || open[0].Metadata[KeyPR] != "acme/api#2" {
				t.Fatalf("only #2 should stay open, got %d beads", len(open))
			}
			closed := r.trk.issues[0].Closed
			if !strings.Contains(closed, status) || !strings.Contains(closed, "acme/api#1") {
				t.Errorf("close reason %q should name the PR and status", closed)
			}
		})
	}
}

func TestResolveWithoutEscalationIsANoOp(t *testing.T) {
	r := newRig(t, Config{})
	r.mustHandle(resolved("acme/api#1", StatusPosted))
	if len(r.trk.issues) != 0 || len(r.not.sent) != 0 {
		t.Fatalf("resolve with nothing open touched the tracker or notifier")
	}
}

func TestReEscalationAfterCloseCreatesAFreshBead(t *testing.T) {
	r := newRig(t, Config{})
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	r.mustHandle(resolved("acme/api#1", StatusReplaced))
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	if len(r.trk.open()) != 1 || r.trk.creates != 2 || len(r.not.sent) != 2 {
		t.Fatalf("open=%d created=%d notified=%d, want 1/2/2", len(r.trk.open()), r.trk.creates, len(r.not.sent))
	}
}

func TestLoudFailureOnEachDeliveryPath(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		setup       func(r *rig)
		prior       bool // a bead for the PR already exists before the failing call
		outcome     Outcome
		wantPrefix  string
		wantNotify  int  // notifications delivered by the failing call
		wantStamped bool // last-notified restamped on the bead
	}{
		{
			name: "create fails", setup: func(r *rig) { r.trk.failCreate = boom },
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "tracker: create", wantNotify: 1,
		},
		{
			name: "list fails on blocked", setup: func(r *rig) { r.trk.failList = boom },
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "tracker: list", wantNotify: 1,
		},
		{
			name: "notify fails on create", setup: func(r *rig) { r.not.fail = boom },
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "notify:", wantNotify: 0,
		},
		{
			name: "comment fails on bump", setup: func(r *rig) { r.trk.failComment = boom }, prior: true,
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "tracker: comment", wantNotify: 0,
		},
		{
			name: "metadata fails on bump", setup: func(r *rig) { r.trk.failSetMetadata = boom }, prior: true,
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "tracker: update", wantNotify: 0,
		},
		{
			name: "notify fails on a due bump", setup: func(r *rig) { r.not.fail = boom; r.clk.advance(48 * time.Hour) }, prior: true,
			outcome: blocked("acme/api#1", ReasonHumanEdited), wantPrefix: "notify:", wantNotify: 0,
		},
		{
			name: "close fails", setup: func(r *rig) { r.trk.failClose = boom }, prior: true,
			outcome: resolved("acme/api#1", StatusPosted), wantPrefix: "tracker: close", wantNotify: 0,
		},
		{
			name: "list fails on resolve", setup: func(r *rig) { r.trk.failList = boom }, prior: true,
			outcome: resolved("acme/api#1", StatusPosted), wantPrefix: "tracker: list", wantNotify: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, Config{})
			if tc.prior {
				r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
			}
			before := len(r.not.sent)
			tc.setup(r)
			err := r.handle(tc.outcome)
			if err == nil {
				t.Fatal("a delivery failure was swallowed: Handle returned nil")
			}
			if !strings.Contains(err.Error(), tc.wantPrefix) || !strings.Contains(err.Error(), "boom") {
				t.Errorf("error %q lacks prefix %q or the cause", err, tc.wantPrefix)
			}
			if got := len(r.not.sent) - before; got != tc.wantNotify {
				t.Errorf("notifications delivered by the failing call = %d, want %d", got, tc.wantNotify)
			}
		})
	}
}

func TestFailedNotificationIsRetriedOnTheNextBlock(t *testing.T) {
	r := newRig(t, Config{RenotifyInterval: 24 * time.Hour})
	r.not.fail = errors.New("channel down")
	if err := r.handle(blocked("acme/api#1", ReasonHumanEdited)); err == nil {
		t.Fatal("want the notify failure surfaced")
	}
	if got := r.trk.open()[0].Metadata[KeyLastNotified]; got != "" {
		t.Fatalf("last notified = %q after a FAILED send; it must stay unset", got)
	}

	// The channel recovers; the next block (well inside the interval) notifies,
	// because nothing was ever delivered.
	r.not.fail = nil
	r.clk.advance(time.Minute)
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	if len(r.not.sent) != 1 {
		t.Fatalf("notifications = %d, want the retry to deliver 1", len(r.not.sent))
	}
	if len(r.trk.open()) != 1 {
		t.Fatalf("beads = %d, want 1", len(r.trk.open()))
	}
}

func TestBeadFailureStillNotifiesAndNotifyFailureKeepsBead(t *testing.T) {
	r := newRig(t, Config{})
	r.trk.failCreate = errors.New("tracker down")
	if err := r.handle(blocked("acme/api#1", ReasonHumanEdited)); err == nil {
		t.Fatal("want error")
	}
	if len(r.not.sent) != 1 {
		t.Errorf("a tracker failure must not silence the operator notification")
	}

	r2 := newRig(t, Config{})
	r2.not.fail = errors.New("channel down")
	if err := r2.handle(blocked("acme/api#1", ReasonHumanEdited)); err == nil {
		t.Fatal("want error")
	}
	if len(r2.trk.open()) != 1 {
		t.Errorf("a notification failure must not lose the bead")
	}
}

func TestUnknownStatusIsAnError(t *testing.T) {
	r := newRig(t, Config{})
	if err := r.handle(Outcome{PR: "acme/api#1", Status: "weird"}); err == nil {
		t.Fatal("unknown status was accepted")
	}
}

// --- systemic roll-up ---

func TestRollupReplacesPerPRBeadsOnceTheThresholdIsExceeded(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 3, RenotifyInterval: time.Hour})
	for i := 1; i <= 3; i++ {
		r.mustHandle(blocked(fmt.Sprintf("acme/api#%d", i), ReasonDeleteRefused))
	}
	if len(r.trk.open()) != 3 {
		t.Fatalf("at the threshold there should still be 3 per-PR beads, got %d", len(r.trk.open()))
	}

	// The 4th PR exceeds the threshold: ONE roll-up bead, no 4th per-PR bead.
	r.mustHandle(blocked("acme/api#4", ReasonDeleteRefused))
	open := r.trk.open()
	if len(open) != 4 {
		t.Fatalf("beads = %d, want 3 per-PR + 1 roll-up", len(open))
	}
	var roll *fakeIssue
	for _, b := range open {
		if b.Metadata[KeyKind] == KindRollup {
			roll = b
		}
	}
	if roll == nil {
		t.Fatal("no roll-up bead")
	}
	if roll.Metadata[KeyKey] != "rollup:"+ReasonDeleteRefused {
		t.Errorf("roll-up key = %q", roll.Metadata[KeyKey])
	}
	prs := splitPRs(roll.Metadata[KeyPRs])
	if len(prs) != 4 {
		t.Errorf("roll-up PRs = %v, want all 4", prs)
	}
	for _, want := range []string{LabelHuman, LabelHumanFocusRequired} {
		if !contains(roll.Labels, want) {
			t.Errorf("roll-up labels %v lack %q", roll.Labels, want)
		}
	}
	if got := r.not.sent[len(r.not.sent)-1]; !strings.Contains(got.Title, "many PRs") {
		t.Errorf("last notification %+v is not the roll-up one", got)
	}

	// The 5th PR joins the roll-up: no new bead, and (inside the interval) no
	// new notification.
	sent := len(r.not.sent)
	r.mustHandle(blocked("acme/api#5", ReasonDeleteRefused))
	if len(r.trk.open()) != 4 {
		t.Fatalf("5th PR created a bead: %d open", len(r.trk.open()))
	}
	if len(splitPRs(roll.Metadata[KeyPRs])) != 5 {
		t.Errorf("roll-up PRs = %v, want 5", roll.Metadata[KeyPRs])
	}
	if len(r.not.sent) != sent {
		t.Errorf("a join inside the re-notify interval notified again")
	}

	// After the interval a join re-notifies (once).
	r.clk.advance(time.Hour)
	r.mustHandle(blocked("acme/api#6", ReasonDeleteRefused))
	if len(r.not.sent) != sent+1 {
		t.Errorf("roll-up re-notify after the interval: got %d new notifications, want 1", len(r.not.sent)-sent)
	}
}

func TestHumanEditedNeverRollsUp(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 2})
	for i := 1; i <= 6; i++ {
		r.mustHandle(blocked(fmt.Sprintf("acme/api#%d", i), ReasonHumanEdited))
	}
	if len(r.trk.open()) != 6 {
		t.Fatalf("human_edited is per-PR: beads = %d, want 6", len(r.trk.open()))
	}
}

func TestRollupDisabledByNegativeThreshold(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: -1})
	for i := 1; i <= 6; i++ {
		r.mustHandle(blocked(fmt.Sprintf("acme/api#%d", i), ReasonDetectionFailed))
	}
	if len(r.trk.open()) != 6 {
		t.Fatalf("roll-up disabled: beads = %d, want 6", len(r.trk.open()))
	}
}

func TestRollupThresholdIsPerReason(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 1})
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
	r.mustHandle(blocked("acme/api#2", ReasonDetectionFailed))
	for _, b := range r.trk.open() {
		if b.Metadata[KeyKind] == KindRollup {
			t.Fatalf("one PR per reason must not roll up")
		}
	}
}

func TestPRLeavesRollupOnResolveAndRollupClosesWhenEmpty(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 1})
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused)) // per-PR (1 <= 1)
	r.mustHandle(blocked("acme/api#2", ReasonDeleteRefused)) // 2 > 1: roll-up of #1 and #2
	r.mustHandle(blocked("acme/api#3", ReasonDeleteRefused)) // joins the roll-up
	var roll *fakeIssue
	for _, b := range r.trk.open() {
		if b.Metadata[KeyKind] == KindRollup {
			roll = b
		}
	}
	if roll == nil || len(splitPRs(roll.Metadata[KeyPRs])) != 3 {
		t.Fatalf("setup: roll-up = %+v", roll)
	}

	r.mustHandle(resolved("acme/api#3", StatusPosted))
	if got := splitPRs(roll.Metadata[KeyPRs]); len(got) != 2 || contains(got, "acme/api#3") {
		t.Fatalf("PRs after #3 resolved = %v", got)
	}
	if roll.Closed != "" {
		t.Fatalf("roll-up closed while PRs remain")
	}

	r.mustHandle(resolved("acme/api#2", StatusSkipped))
	r.mustHandle(resolved("acme/api#1", StatusReplaced))
	if roll.Closed == "" {
		t.Fatalf("roll-up should close when its last PR resolves")
	}
	if len(r.trk.open()) != 0 {
		t.Errorf("open beads left: %d", len(r.trk.open()))
	}
}

func TestPRMovesBetweenRollupsWhenItsReasonChanges(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 1})
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
	r.mustHandle(blocked("acme/api#2", ReasonDeleteRefused)) // roll-up(delete_refused) = #1, #2
	var roll *fakeIssue
	for _, b := range r.trk.open() {
		if b.Metadata[KeyKind] == KindRollup {
			roll = b
		}
	}
	if roll == nil {
		t.Fatal("setup: no roll-up")
	}
	r.mustHandle(blocked("acme/api#9", ReasonDeleteRefused)) // joins roll-up
	r.mustHandle(blocked("acme/api#9", ReasonHumanEdited))   // a human edited it meanwhile
	if contains(splitPRs(roll.Metadata[KeyPRs]), "acme/api#9") {
		t.Fatalf("#9 should have left the delete_refused roll-up: %v", roll.Metadata[KeyPRs])
	}
	found := false
	for _, b := range r.trk.open() {
		if b.Metadata[KeyPR] == "acme/api#9" && b.Metadata[KeyReason] == ReasonHumanEdited {
			found = true
		}
	}
	if !found {
		t.Fatal("#9 should have its own human_edited bead")
	}
}

func TestExistingPerPRBeadWinsOverRollup(t *testing.T) {
	r := newRig(t, Config{RollupThreshold: 1})
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
	r.mustHandle(blocked("acme/api#2", ReasonDeleteRefused)) // roll-up created
	before := len(r.trk.open())
	r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused)) // #1 already has its own bead
	if len(r.trk.open()) != before {
		t.Fatalf("a repeat for a PR with its own bead created something")
	}
}

func TestRollupLoudFailures(t *testing.T) {
	boom := errors.New("boom")
	setup := func() *rig {
		r := newRig(t, Config{RollupThreshold: 1})
		r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
		return r
	}
	t.Run("roll-up create fails but still notifies", func(t *testing.T) {
		r := setup()
		r.trk.failCreate = boom
		before := len(r.not.sent)
		err := r.handle(blocked("acme/api#2", ReasonDeleteRefused))
		if err == nil || !strings.Contains(err.Error(), "roll-up") {
			t.Fatalf("err = %v", err)
		}
		if len(r.not.sent) != before+1 {
			t.Errorf("operator not notified")
		}
	})
	t.Run("roll-up notify fails", func(t *testing.T) {
		r := setup()
		r.not.fail = boom
		if err := r.handle(blocked("acme/api#2", ReasonDeleteRefused)); err == nil || !strings.Contains(err.Error(), "notify:") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("leaving a roll-up fails to update", func(t *testing.T) {
		r := setup()
		r.mustHandle(blocked("acme/api#2", ReasonDeleteRefused))
		r.mustHandle(blocked("acme/api#3", ReasonDeleteRefused))
		r.trk.failSetMetadata = boom
		if err := r.handle(resolved("acme/api#3", StatusPosted)); err == nil {
			t.Fatal("a failed roll-up update was swallowed")
		}
	})
	t.Run("closing the emptied roll-up fails", func(t *testing.T) {
		r := newRig(t, Config{RollupThreshold: 1})
		r.mustHandle(blocked("acme/api#1", ReasonDeleteRefused))
		r.mustHandle(blocked("acme/api#2", ReasonDeleteRefused))
		r.mustHandle(resolved("acme/api#1", StatusPosted)) // closes #1's own bead
		r.trk.failClose = boom
		if err := r.handle(resolved("acme/api#2", StatusPosted)); err == nil {
			t.Fatal("a failed close was swallowed")
		}
	})
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.RenotifyInterval != 12*time.Hour || c.RollupThreshold != 3 || c.EscalationLabel != DefaultEscalationLabel {
		t.Errorf("defaults = %+v", c)
	}
	if !contains(c.SystemicReasons, ReasonDetectionFailed) || !contains(c.SystemicReasons, ReasonDeleteRefused) || contains(c.SystemicReasons, ReasonHumanEdited) {
		t.Errorf("systemic reasons = %v", c.SystemicReasons)
	}
}

func TestReasonExplanationsCoverEveryReason(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range []string{ReasonDetectionFailed, ReasonHumanEdited, ReasonArchiveFailed, ReasonDeleteRefused, "something_new"} {
		e := reasonExplanation(reason)
		if e == "" || seen[e] {
			t.Errorf("reason %q has an empty or duplicate explanation", reason)
		}
		seen[e] = true
	}
}

// --- outcome parsing ---

func TestParseOutcome(t *testing.T) {
	tests := []struct {
		name    string
		pr      string
		in      string
		want    Outcome
		wantErr string
	}{
		{
			name: "enveloped blocked", pr: "acme/api#1",
			in:   `{"result":{"status":"blocked_human_pending","reason":"human_edited","message":"m","head_sha":"abc","pending_review":{"review_id":"R","url":"https://x/r"}}}`,
			want: Outcome{PR: "acme/api#1", Status: StatusBlocked, Reason: "human_edited", Message: "m", HeadSHA: "abc", ReviewURL: "https://x/r"},
		},
		{
			name: "bare posted", pr: "acme/api#1",
			in:   `{"status":"posted","review_id":"R","head_sha":"abc"}`,
			want: Outcome{PR: "acme/api#1", Status: StatusPosted, HeadSHA: "abc"},
		},
		{
			name: "append is tolerated like posted", pr: "acme/api#1",
			in:   `{"result":{"status":"append","head_sha":"abc","review_id":"r1","state":"pending"}}`,
			want: Outcome{PR: "acme/api#1", Status: StatusAppend, HeadSHA: "abc"},
		},
		{
			name: "no_change is tolerated like posted", pr: "acme/api#1",
			in:   `{"result":{"status":"no_change","head_sha":"abc","review_id":"r1","state":"pending"}}`,
			want: Outcome{PR: "acme/api#1", Status: StatusNoChange, HeadSHA: "abc"},
		},
		{
			name: "detection_failed has no pending review", pr: "acme/api#1",
			in:   `{"result":{"status":"blocked_human_pending","reason":"detection_failed","head_sha":"abc"}}`,
			want: Outcome{PR: "acme/api#1", Status: StatusBlocked, Reason: "detection_failed", HeadSHA: "abc"},
		},
		{name: "error envelope", pr: "p", in: `{"error":{"code":"unavailable","message":"down"}}`, wantErr: "not a review submit outcome"},
		{name: "no status", pr: "p", in: `{"result":{"review_id":"R"}}`, wantErr: "no status"},
		{name: "unknown status", pr: "p", in: `{"result":{"status":"merged"}}`, wantErr: "unknown status"},
		{name: "blocked without reason", pr: "p", in: `{"result":{"status":"blocked_human_pending"}}`, wantErr: "no reason"},
		{name: "not json", pr: "p", in: `nope`, wantErr: "decode"},
		{name: "empty pr", pr: " ", in: `{}`, wantErr: "PR id is empty"},
		{name: "pr with separator", pr: "a;b", in: `{}`, wantErr: "must not contain"},
		{name: "result not an object", pr: "p", in: `{"result":5}`, wantErr: "decode submit result"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseOutcome(tc.pr, []byte(tc.in))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNotAnOutcomeIsDetectable(t *testing.T) {
	_, err := ParseOutcome("p", []byte(`{"error":{"code":"x","message":"y"}}`))
	if !errors.Is(err, ErrNotAnOutcome) {
		t.Fatalf("err = %v, want ErrNotAnOutcome", err)
	}
}

func TestResolveClosesEveryPerPRBeadForThePR(t *testing.T) {
	r := newRig(t, Config{})
	r.mustHandle(blocked("acme/api#1", ReasonHumanEdited))
	// A race left a second bead for the same PR.
	dup := *r.trk.issues[0]
	dup.ID = "bd-dup"
	dup.Metadata = map[string]string{}
	for k, v := range r.trk.issues[0].Metadata {
		dup.Metadata[k] = v
	}
	r.trk.issues = append(r.trk.issues, &dup)

	r.mustHandle(resolved("acme/api#1", StatusPosted))
	if len(r.trk.open()) != 0 {
		t.Fatalf("%d bead(s) left open after resolve", len(r.trk.open()))
	}
}
