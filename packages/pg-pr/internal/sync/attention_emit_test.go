package sync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// attnFinderBeads is a sync.BeadClient (slim {ListMergeRequests}) that ALSO
// implements the legacy draft-review lookup, and COUNTS the calls. The attention
// projector MUST NOT call it any more: keying the first-review edge on that bead
// made the edge dead code at the shipped default (pg2-kh1ar), so the counter is
// asserted to stay at zero.
type attnFinderBeads struct {
	refreshFakeBeads
	closed bool
	found  bool
	calls  int
}

func (f *attnFinderBeads) FindDraftReviewForPR(_ context.Context, _ string, _ int) (string, bool, bool, error) {
	f.calls++
	return "dr-1", f.closed, f.found, nil
}

func newAttnEngine(t *testing.T, bdc BeadClient, db *store.DB) *Engine {
	t.Helper()
	vcs := newFakeVCS()
	e, err := New(Deps{
		Cfg: &config.Config{
			SelfLogin: "me",
			Repos:     []config.RepoConfig{{Remote: "o/r", VCS: "github", TeamMembers: []string{"teammate"}}},
		},
		VCS:      map[string]VCSProvider{"github": vcs},
		Beads:    bdc,
		StateDir: t.TempDir(),
		Store:    db,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

// attentionEvents decodes every pr.attention event from the outbox.
func attentionEvents(t *testing.T, db *store.DB) []store.AttentionPayload {
	t.Helper()
	var out []store.AttentionPayload
	for _, ev := range collectOutboxEvents(t, db) {
		if ev.Type != store.EventPRAttention {
			continue
		}
		var p store.AttentionPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("unmarshal AttentionPayload: %v", err)
		}
		out = append(out, p)
	}
	return out
}

// A team PR whose PERSISTED facts make NeedsAttention true (nobody approved + I
// haven't reviewed) emits pr.attention{need:true}.
func TestEmitAttention_NeedTrue(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 7, Ownership: "team", State: "open", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	bdc := &attnFinderBeads{closed: true, found: true}
	e := newAttnEngine(t, bdc, db)

	if err := e.emitAttention(ctx, "o/r", 7, prID, ownership.Team, false); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}

	evs := attentionEvents(t, db)
	if len(evs) != 1 {
		t.Fatalf("want 1 pr.attention event, got %d: %+v", len(evs), evs)
	}
	if !evs[0].Need {
		t.Errorf("need should be true")
	}
	if evs[0].Reason != "unreviewed-by-me" {
		t.Errorf("reason = %q", evs[0].Reason)
	}
	if evs[0].Repo != "o/r" || evs[0].Number != 7 {
		t.Errorf("payload identity = %+v", evs[0])
	}
}

// pg2-kh1ar: with the review kill switch at its BUILT-IN DEFAULT (review.enabled
// absent → ReviewEnabled()==false, so beadsbridge never produces a draft-review
// bead and none is ever found), a teammate PR with no prior review by me MUST
// still produce Need=true. This is the edge that used to be dead code.
//
// It also pins that the projector consults NO draft-review artifact at all: the
// fake's lookup counter MUST stay at zero even though the fake offers the
// capability.
func TestEmitAttention_FirstReviewFiresWithReviewDisabled(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 11, Ownership: "team", State: "open", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	// found=false models the shipped default exactly: no draft-review bead exists
	// because the kill-switched producer never created one.
	bdc := &attnFinderBeads{closed: false, found: false}
	e := newAttnEngine(t, bdc, db)
	if e.cfg().ReviewEnabled() {
		t.Fatal("precondition: the test config must leave review.enabled absent (disabled)")
	}

	if err := e.emitAttention(ctx, "o/r", 11, prID, ownership.Team, false); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}
	evs := attentionEvents(t, db)
	if len(evs) != 1 {
		t.Fatalf("want 1 pr.attention event, got %d: %+v", len(evs), evs)
	}
	if !evs[0].Need || evs[0].Reason != snapshot.AttentionReasonUnreviewed {
		t.Fatalf("attention event = %+v, want need:true reason:%q", evs[0], snapshot.AttentionReasonUnreviewed)
	}
	if bdc.calls != 0 {
		t.Errorf("FindDraftReviewForPR called %d times; the attention signal must not depend on the draft-review bead", bdc.calls)
	}
}

// When the facts flip (a teammate approved), the emitter emits need:false so the
// bridge closes the attention bead. Re-derived from facts every call (R1).
func TestEmitAttention_NeedFalseWhenApproved(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 8, Ownership: "team", State: "open", HeadSHA: "h1"})
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	if err := db.MarkRevisionOthersApproved(ctx, prID, "h1", "t"); err != nil {
		t.Fatalf("MarkRevisionOthersApproved: %v", err)
	}

	bdc := &attnFinderBeads{closed: true, found: true} // irrelevant now; teammate approval wins
	e := newAttnEngine(t, bdc, db)

	if err := e.emitAttention(ctx, "o/r", 8, prID, ownership.Team, false); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}
	evs := attentionEvents(t, db)
	if len(evs) != 1 || evs[0].Need {
		t.Fatalf("want 1 need:false event, got %+v", evs)
	}
}

// A CO-OWNED PR forces Need=false without consulting the revision timeline, so a
// team→co-owned transition idempotently CLOSES a previously opened attention bead.
func TestEmitAttention_CoOwnedForcesNeedFalse(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 9, Ownership: "co-owned", State: "open", HeadSHA: "h1"})
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	bdc := &attnFinderBeads{}
	e := newAttnEngine(t, bdc, db)

	if err := e.emitAttention(ctx, "o/r", 9, prID, ownership.CoOwned, false); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}
	evs := attentionEvents(t, db)
	if len(evs) != 1 || evs[0].Need {
		t.Fatalf("want 1 need:false event (co-owned is never my review target), got %+v", evs)
	}
}

// CONSISTENCY (D4 / R4): for the same store facts, the emitted pr.attention.need
// equals the dashboard TeamRow.NeedsAttention — because both call the SAME
// snapshot.NeedsAttention predicate over the SAME store-sourced inputs. Prove it
// end-to-end: derive the dashboard input the same way emitAttention does and
// assert they agree across a battery of fixtures.
func TestEmitAttention_ConsistentWithDashboard(t *testing.T) {
	ctx := context.Background()

	type fixture struct {
		name        string
		revs        [][2]string // (headSHA, myReviewState)
		otherApprAt string      // head SHA a teammate approved, "" for none
	}
	fixtures := []fixture{
		{name: "unreviewed", revs: [][2]string{{"h1", ""}}},
		{name: "teammate approved", revs: [][2]string{{"h1", ""}}, otherApprAt: "h1"},
		{name: "i reviewed head", revs: [][2]string{{"h1", "approved"}}},
		{name: "re-review", revs: [][2]string{{"h1", "approved"}, {"h2", ""}}},
		{name: "still unreviewed after a head advance", revs: [][2]string{{"h1", ""}, {"h2", ""}}},
	}

	for i, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			db := store.OpenForTest(t)
			number := 100 + i
			prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: number, Ownership: "team", State: "open"})
			for _, rv := range fx.revs {
				if _, _, err := db.RecordRevision(ctx, prID, rv[0], "b"); err != nil {
					t.Fatalf("RecordRevision: %v", err)
				}
				if rv[1] != "" {
					if err := db.MarkRevisionReviewed(ctx, prID, rv[0], rv[1], "t"); err != nil {
						t.Fatalf("MarkRevisionReviewed: %v", err)
					}
				}
			}
			if fx.otherApprAt != "" {
				if err := db.MarkRevisionOthersApproved(ctx, prID, fx.otherApprAt, "t"); err != nil {
					t.Fatalf("MarkRevisionOthersApproved: %v", err)
				}
			}
			bdc := &attnFinderBeads{}
			e := newAttnEngine(t, bdc, db)

			// The write-model path: emit and read the need bit.
			if err := e.emitAttention(ctx, "o/r", number, prID, ownership.Team, false); err != nil {
				t.Fatalf("emitAttention: %v", err)
			}
			evs := attentionEvents(t, db)
			if len(evs) != 1 {
				t.Fatalf("want 1 event, got %d", len(evs))
			}
			beadNeed := evs[0].Need

			// The read-model path: feed the SAME store facts to the SAME exported
			// predicate the dashboard builder uses and assert the two agree.
			// (buildTeamRow calls snapshot.NeedsAttention; emitAttention calls
			// snapshot.NeedsAttention — one function, one truth.)
			revs, _ := db.ListRevisions(ctx, prID)
			dashboardNeed, _ := snapshot.NeedsAttention(revs, false)
			if dashboardNeed != beadNeed {
				t.Fatalf("dashboard NeedsAttention=%v but bead need=%v — predicate divergence!", dashboardNeed, beadNeed)
			}
		})
	}
}
