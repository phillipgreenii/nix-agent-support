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
// implements the draft-review-closed capability the attention emitter needs.
type attnFinderBeads struct {
	refreshFakeBeads
	closed bool
	found  bool
}

func (f *attnFinderBeads) FindDraftReviewForPR(_ context.Context, _ string, _ int) (string, bool, bool, error) {
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

// A team PR whose PERSISTED facts make NeedsAttention true (draft-review bead
// closed + nobody approved + I haven't reviewed) emits pr.attention{need:true}.
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

	if err := e.emitAttention(ctx, e.bdClientFor(config.RepoConfig{Remote: "o/r"}), "o/r", 7, prID, ownership.Team); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}

	evs := attentionEvents(t, db)
	if len(evs) != 1 {
		t.Fatalf("want 1 pr.attention event, got %d: %+v", len(evs), evs)
	}
	if !evs[0].Need {
		t.Errorf("need should be true")
	}
	if evs[0].Reason != "draft-review-ready-unapproved" {
		t.Errorf("reason = %q", evs[0].Reason)
	}
	if evs[0].Repo != "o/r" || evs[0].Number != 7 {
		t.Errorf("payload identity = %+v", evs[0])
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

	bdc := &attnFinderBeads{closed: true, found: true} // draft review ready, but teammate approved wins
	e := newAttnEngine(t, bdc, db)

	if err := e.emitAttention(ctx, e.bdClientFor(config.RepoConfig{Remote: "o/r"}), "o/r", 8, prID, ownership.Team); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}
	evs := attentionEvents(t, db)
	if len(evs) != 1 || evs[0].Need {
		t.Fatalf("want 1 need:false event, got %+v", evs)
	}
}

// The draft-review-not-closed (not-ready) signal keeps need:false: proves the
// emitter consults FindDraftReviewForPR for the readiness input.
func TestEmitAttention_DraftReviewNotClosed(t *testing.T) {
	ctx := context.Background()
	db := store.OpenForTest(t)
	prID, _ := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 9, Ownership: "team", State: "open", HeadSHA: "h1"})
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	bdc := &attnFinderBeads{closed: false, found: true} // bead exists but still OPEN → not ready
	e := newAttnEngine(t, bdc, db)

	if err := e.emitAttention(ctx, bdc, "o/r", 9, prID, ownership.Team); err != nil {
		t.Fatalf("emitAttention: %v", err)
	}
	evs := attentionEvents(t, db)
	if len(evs) != 1 || evs[0].Need {
		t.Fatalf("want 1 need:false event (draft review not ready), got %+v", evs)
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
		drClosed    bool
	}
	fixtures := []fixture{
		{name: "ready+unapproved", revs: [][2]string{{"h1", ""}}, drClosed: true},
		{name: "teammate approved", revs: [][2]string{{"h1", ""}}, otherApprAt: "h1", drClosed: true},
		{name: "i reviewed head", revs: [][2]string{{"h1", "approved"}}, drClosed: true},
		{name: "re-review", revs: [][2]string{{"h1", "approved"}, {"h2", ""}}, drClosed: false},
		{name: "nothing", revs: [][2]string{{"h1", ""}}, drClosed: false},
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
			bdc := &attnFinderBeads{closed: fx.drClosed, found: true}
			e := newAttnEngine(t, bdc, db)

			// The write-model path: emit and read the need bit.
			if err := e.emitAttention(ctx, bdc, "o/r", number, prID, ownership.Team); err != nil {
				t.Fatalf("emitAttention: %v", err)
			}
			evs := attentionEvents(t, db)
			if len(evs) != 1 {
				t.Fatalf("want 1 event, got %d", len(evs))
			}
			beadNeed := evs[0].Need

			// The read-model path: feed the SAME store facts + the SAME readiness
			// signal to the SAME exported predicate the dashboard builder uses and
			// assert the two agree. (buildTeamRow calls snapshot.NeedsAttention;
			// emitAttention calls snapshot.NeedsAttention — one function, one truth.)
			revs, _ := db.ListRevisions(ctx, prID)
			dashboardNeed, _ := snapshot.NeedsAttention(revs, fx.drClosed)
			if dashboardNeed != beadNeed {
				t.Fatalf("dashboard NeedsAttention=%v but bead need=%v — predicate divergence!", dashboardNeed, beadNeed)
			}
		})
	}
}
