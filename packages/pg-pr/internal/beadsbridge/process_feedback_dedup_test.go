package beadsbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// cycleClient records every process-feedback write the projection makes, and
// serves a scripted ProcessingCycleState. It also records the (key, prBeadID)
// pair the projection resolves on, so a test can assert the lookup is keyed on
// (repo, pr_number) rather than on the merge-request bead id.
type cycleClient struct {
	noopBeadClient
	mr    *beads.MergeRequest
	state beads.ProcessingCycleState

	resolvedKeys   []string
	resolvedParent []string
	creates        []beads.CreateProcessingCycleInput
	appends        []appendCall
}

type appendCall struct {
	id      string
	note    string
	add     string
	removes []string
}

func (c *cycleClient) FindByRepoAndNumber(context.Context, string, int) (*beads.MergeRequest, error) {
	return c.mr, nil
}

func (c *cycleClient) ResolveProcessingCycle(_ context.Context, key, prBeadID string) (beads.ProcessingCycleState, error) {
	c.resolvedKeys = append(c.resolvedKeys, key)
	c.resolvedParent = append(c.resolvedParent, prBeadID)
	return c.state, nil
}

func (c *cycleClient) CreateProcessingCycle(_ context.Context, in beads.CreateProcessingCycleInput) (string, error) {
	c.creates = append(c.creates, in)
	return "cyc-new", nil
}

func (c *cycleClient) AppendProcessingCycleNote(_ context.Context, id, note, add string, removes []string) error {
	c.appends = append(c.appends, appendCall{id, note, add, removes})
	return nil
}

// openMR is the resolved merge-request bead every case below hangs off.
func openMR() *beads.MergeRequest { return &beads.MergeRequest{ID: "mr-1", Status: "open"} }

// summary builds a payload summary with the given counts.
func summary(n int, digest string, byKind map[string]int, reviewers ...string) *store.FeedbackSummary {
	return &store.FeedbackSummary{Unaddressed: n, ByKind: byKind, Reviewers: reviewers, Digest: digest}
}

// handleFeedback dispatches one feedback.created event.
func handleFeedback(t *testing.T, h *Handler, p FeedbackPayload) {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := h.Handle(context.Background(), store.Event{Type: store.EventFeedbackCreated, Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

// TestProcessFeedbackKeyedOnRepoAndPRNumber pins the identity change: the
// projection resolves the cycle by (repo, pr_number), passing the
// ProcessingCycleKey. Keying on the merge-request bead id alone was the
// duplication mechanism — a PR with two merge-request beads had its open cycle
// hanging off one of them, invisible to a lookup scoped to the other, so every
// re-sync created another cycle.
func TestProcessFeedbackKeyedOnRepoAndPRNumber(t *testing.T) {
	c := &cycleClient{mr: openMR()}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7, Summary: summary(1, "d1", map[string]int{"pr-comments": 1}, "alice"),
	})
	if len(c.resolvedKeys) != 1 {
		t.Fatalf("expected one resolve, got %d", len(c.resolvedKeys))
	}
	if c.resolvedKeys[0] != "o/r#7" {
		t.Fatalf("resolve key = %q, want %q (the (repo, pr_number) identity)", c.resolvedKeys[0], "o/r#7")
	}
	if c.resolvedParent[0] != "mr-1" {
		t.Errorf("parent fallback must still be passed, got %q", c.resolvedParent[0])
	}
}

// TestReSyncUpdatesOpenCycleInsteadOfCreatingASecond is the duplicate-on-re-sync
// reproduction: a live cycle already exists for this PR key, so the re-sync MUST
// update it (appending what is new) and MUST NOT create a second bead.
func TestReSyncUpdatesOpenCycleInsteadOfCreatingASecond(t *testing.T) {
	c := &cycleClient{
		mr:    openMR(),
		state: beads.ProcessingCycleState{Open: &beads.ProcessingCycle{ID: "cyc-1", Status: "open", Labels: []string{"mine", "fbsum:old0"}}},
	}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7, Mine: true,
		Summary: summary(2, "new1", map[string]int{"code-comment-thread": 2}, "alice"),
	})

	if len(c.creates) != 0 {
		t.Fatalf("re-sync created a SECOND process-feedback bead: %+v", c.creates)
	}
	if len(c.appends) != 1 {
		t.Fatalf("expected exactly one update of the existing bead, got %d", len(c.appends))
	}
	got := c.appends[0]
	if got.id != "cyc-1" {
		t.Errorf("updated bead = %q, want cyc-1", got.id)
	}
	if !strings.Contains(got.note, "2 unaddressed item(s)") || !strings.Contains(got.note, "code-comment-thread x2") {
		t.Errorf("appended note lacks substance: %q", got.note)
	}
	if got.add != "fbsum:new1" {
		t.Errorf("new set marker = %q, want fbsum:new1", got.add)
	}
	if len(got.removes) != 1 || got.removes[0] != "fbsum:old0" {
		t.Errorf("stale set marker must be dropped in the same update, got %v", got.removes)
	}
}

// TestReSyncWithUnchangedFeedbackWritesNothing is the churn guard on the update
// path: the daemon re-projects every tick, so an unchanged feedback set must
// produce NO bd write at all (every `bd update` is a Dolt commit).
func TestReSyncWithUnchangedFeedbackWritesNothing(t *testing.T) {
	c := &cycleClient{
		mr:    openMR(),
		state: beads.ProcessingCycleState{Open: &beads.ProcessingCycle{ID: "cyc-1", Status: "open", Labels: []string{"fbsum:same"}}},
	}
	h := New(c)
	for i := 0; i < 3; i++ {
		handleFeedback(t, h, FeedbackPayload{
			Repo: "o/r", Number: 7, Summary: summary(1, "same", map[string]int{"pr-comments": 1}, "alice"),
		})
	}
	if len(c.creates) != 0 || len(c.appends) != 0 {
		t.Fatalf("unchanged feedback set must write nothing; creates=%+v appends=%+v", c.creates, c.appends)
	}
}

// TestNoBeadWhenNothingUnaddressed is the empty-bead reproduction: a tick that
// surfaced no unaddressed reviewer feedback — the case where the only new PR
// activity was the author's own replies plus a push — must produce no bead at
// all. Pre-fix the projection created one unconditionally.
func TestNoBeadWhenNothingUnaddressed(t *testing.T) {
	c := &cycleClient{mr: openMR()}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7, Summary: &store.FeedbackSummary{Unaddressed: 0},
	})
	if len(c.creates) != 0 {
		t.Fatalf("created a bead with nothing to process: %+v", c.creates)
	}
	if len(c.resolvedKeys) != 0 {
		t.Errorf("must short-circuit before the cycle lookup, got %d resolves", len(c.resolvedKeys))
	}
}

// TestLegacyPayloadWithoutSummaryStillProjects pins the compatibility contract:
// a payload with NO summary (an outbox row enqueued before this change, or a
// hand-rolled one) is "unknown", not "zero", so it must still project rather
// than being silently dropped.
func TestLegacyPayloadWithoutSummaryStillProjects(t *testing.T) {
	c := &cycleClient{mr: openMR()}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{Repo: "o/r", Number: 7})
	if len(c.creates) != 1 {
		t.Fatalf("legacy payload must still create a cycle, got %d", len(c.creates))
	}
	if len(c.creates[0].Labels) != 0 {
		t.Errorf("no digest to record ⇒ no marker label, got %v", c.creates[0].Labels)
	}
}

// TestClosedPredecessorCoveringSameSetIsNotRecreated is the self-feeding-loop
// reproduction at the projection layer, and the exact shape of the observed
// defect: a cycle was processed and CLOSED, then the very next sync opened a
// fresh one for the same feedback. When the closed bead already covers this
// feedback set, nothing new arrived and no successor may be opened.
func TestClosedPredecessorCoveringSameSetIsNotRecreated(t *testing.T) {
	c := &cycleClient{
		mr: openMR(),
		state: beads.ProcessingCycleState{Closed: &beads.ProcessingCycle{
			ID: "cyc-closed", Status: "closed", Labels: []string{"fbsum:cover1"},
		}},
	}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7, Summary: summary(2, "cover1", map[string]int{"pr-comments": 2}, "alice"),
	})
	if len(c.creates) != 0 {
		t.Fatalf("re-created a cycle for feedback its closed predecessor already covered: %+v", c.creates)
	}
}

// TestClosedPredecessorWithNewFeedbackCreatesReferencingSuccessor is the other
// half of the closed-predecessor rule: genuinely new feedback DOES warrant a
// successor, but it must reference the predecessor rather than being a bare
// duplicate with an empty description.
func TestClosedPredecessorWithNewFeedbackCreatesReferencingSuccessor(t *testing.T) {
	c := &cycleClient{
		mr: openMR(),
		state: beads.ProcessingCycleState{Closed: &beads.ProcessingCycle{
			ID: "cyc-closed", Status: "closed", Labels: []string{"fbsum:cover1"},
		}},
	}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7, Mine: true,
		Summary: summary(1, "cover2", map[string]int{"ci-failure": 1}),
	})
	if len(c.creates) != 1 {
		t.Fatalf("expected one successor cycle, got %d", len(c.creates))
	}
	in := c.creates[0]
	if !strings.Contains(in.Description, "cyc-closed") {
		t.Errorf("successor description must reference the closed predecessor, got %q", in.Description)
	}
	if !in.Mine {
		t.Error("mine flag must be carried through")
	}
	if len(in.Labels) != 1 || in.Labels[0] != "fbsum:cover2" {
		t.Errorf("successor must record the set it covers, got %v", in.Labels)
	}
}

// TestCycleDescriptionCarriesSubstance pins criterion 5: the description must
// state the count and kind of unaddressed findings (and who raised them) so a
// drain session can triage without hitting the VCS API. Copying the title
// verbatim — the old behaviour — is exactly what made the duplicates
// indistinguishable.
func TestCycleDescriptionCarriesSubstance(t *testing.T) {
	c := &cycleClient{mr: openMR()}
	h := New(c)
	handleFeedback(t, h, FeedbackPayload{
		Repo: "o/r", Number: 7,
		Summary: summary(3, "d3", map[string]int{"code-comment-thread": 2, "ci-failure": 1}, "alice", "coderabbit[bot]"),
	})
	if len(c.creates) != 1 {
		t.Fatalf("expected one create, got %d", len(c.creates))
	}
	desc := c.creates[0].Description
	if desc == "" || desc == "process-feedback: o/r#7" || desc == "o/r#7" {
		t.Fatalf("description must not be the title verbatim, got %q", desc)
	}
	for _, want := range []string{
		"o/r#7",
		"3 unaddressed item(s)",
		"ci-failure x1",
		"code-comment-thread x2",
		"alice",
		"coderabbit[bot]",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q; got:\n%s", want, desc)
		}
	}
	// Kinds must render in a stable (sorted) order — an unstable rendering would
	// defeat the digest comparison that suppresses no-op writes.
	if strings.Index(desc, "ci-failure x1") > strings.Index(desc, "code-comment-thread x2") {
		t.Errorf("kind breakdown must be sorted, got:\n%s", desc)
	}
}
