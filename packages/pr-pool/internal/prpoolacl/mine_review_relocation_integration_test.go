//go:build integration

package prpoolacl

// Phase A end-to-end proof for pg2-ynhr.5 (the mine self-review-sink
// relocation): with the legacy pg-pr beadsbridge/reviewhook/reviewsink
// machinery gone (Phase B strips it), something else must get a MINE PR's
// review findings into the SAME feedback->worker beads pipeline the
// ingest-sourced path still uses unchanged. That something is pr-pool's own
// review role (roles.reviewPromptBody) + ACL (this package's Reconcile).
//
// This cannot execute an actual LLM — no test in this module does; role
// PROMPTS are text handed to an agent, never interpreted here (see
// internal/dtest.FakeCC, which only records scheduler calls). What IS
// mechanically provable, and what this test proves, end to end against a
// REAL bd (embedded Dolt, mirroring internal/beads/integration_test.go's
// bdRepo helper — unexported there, so replicated here per that file's own
// precedent):
//
//  1. the ACL stamps the review-pr bead with ownership=mine metadata — the
//     exact input TestReviewPrompt_MineOwnershipFilesProcessFeedbackNotGitHub
//     (internal/roles) proves the rendered prompt branches on;
//  2. bd operations shaped EXACTLY like that rendered prompt instructs — one
//     process-feedback: bead carrying the "mine" label — are discovered by
//     the REAL feedback-source BeadsReady query;
//  3. bd operations shaped like the (unchanged) feedback role's own prompt
//     — a work bead labeled worker-ready — are discovered by the REAL
//     worker-source BeadsReady query.
//
// Two independently-defined query filters agreeing on what a bead shaped this
// way looks like, against a real store, is the strongest "flows end to end"
// proof available without an LLM in the loop.
import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// mineRelocationBdRepo spins up an isolated embedded-Dolt beads store in a
// temp dir. Skips (never fails) when bd is unavailable or under -short,
// mirroring internal/beads/integration_test.go's bdRepo.
func mineRelocationBdRepo(t *testing.T) (context.Context, *beads.CLIRunner) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping bd integration test in -short mode")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not on PATH; skipping bd integration test")
	}
	t.Setenv("BD_NON_INTERACTIVE", "1")
	dir := t.TempDir()
	r := beads.NewCLIRunnerForRepo(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if out, err := r.Run(ctx, "init", "--prefix", "tst"); err != nil {
		t.Skipf("bd init failed (embedded dolt unavailable in this env): %v\n%s", err, out)
	}
	// Register the merge-request custom type (mirrors pg-pr's own
	// pkg/beads/mergerequest_test.go newBDWorkspace): the ACL's Reconcile finds
	// the pg-pr-owned merge-request bead by this type, so the fixture must
	// declare it or "bd create --type=merge-request" fails validation.
	if out, err := r.Run(ctx, "config", "set", "types.custom", "merge-request"); err != nil {
		t.Fatalf("bd config set types.custom: %v\n%s", err, out)
	}
	return ctx, r
}

func TestIntegration_MineReviewRelocation_FeedbackToWorkerFlowsEndToEnd(t *testing.T) {
	ctx, r := mineRelocationBdRepo(t)

	// Step 1: the pg-pr-owned merge-request bead the ACL finds-or-reuses (NH2:
	// it never creates one itself).
	mrOut, err := r.Run(ctx, "create", "--type=merge-request", "--title", "o/r#7",
		"-d", "o/r#7", "--metadata", `{"repo":"o/r","pr_number":7}`, "--silent")
	if err != nil {
		t.Fatalf("create merge-request bead: %v\n%s", err, mrOut)
	}

	// Step 2: the ACL projects the review-pr bead for a MINE PR.
	prs := []PR{{
		Repo: "o/r", Number: 7, HeadSHA: "abc123", Branch: "feat/x",
		State: "open", Ownership: "mine",
		LastSyncedAt: time.Now().UTC().Format(time.RFC3339), Stale: false,
	}}
	reviewIDs, errs := Reconcile(ctx, r, prs)
	if len(errs) != 0 {
		t.Fatalf("Reconcile: %v", errs)
	}
	if len(reviewIDs) != 1 {
		t.Fatalf("expected exactly one review-pr id, got %v", reviewIDs)
	}
	reviewID := reviewIDs[0]

	iss, err := beads.ShowObj(ctx, r, reviewID)
	if err != nil {
		t.Fatalf("show review-pr bead: %v", err)
	}
	if got, _ := iss.Metadata["ownership"].(string); got != "mine" {
		t.Fatalf("review-pr bead ownership metadata = %q, want %q (the input the review prompt branches on)", got, "mine")
	}

	// Step 3: SIMULATE the review role's new mine-path bd action — exactly what
	// the rendered prompt in TestReviewPrompt_MineOwnershipFilesProcessFeedbackNotGitHub
	// instructs: file one process-feedback bead labeled mine, then close the
	// review-pr bead. No GitHub write anywhere in this path.
	pfOut, err := r.Run(ctx, "create", "--type=task",
		"--title", "process-feedback: o/r#7",
		"-d", "found: unhandled error in foo.go:42", "-l", "mine", "--silent")
	if err != nil {
		t.Fatalf("create process-feedback bead: %v\n%s", err, pfOut)
	}
	pfID := strings.TrimSpace(pfOut)
	if out, err := r.Run(ctx, "close", reviewID); err != nil {
		t.Fatalf("close review-pr bead: %v\n%s", err, out)
	}

	// Step 4: the REAL feedback-source query (roles.BuiltinQuerySet) must
	// discover it — proving "feedback flows" for the new path.
	qs := roles.BuiltinQuerySet(roles.BuiltinParams{})
	feedbackQuery, ok := findBeadsReadyQuery(qs, "feedback-source")
	if !ok {
		t.Fatalf("no feedback-source query in the built-in set")
	}
	events, err := feedbackQuery.Run(ctx, query.Env{BD: r})
	if err != nil {
		t.Fatalf("feedback-source query: %v", err)
	}
	if !eventsContainItem(events, pfID) {
		t.Fatalf("feedback-source query did not discover process-feedback bead %s; events=%+v", pfID, events)
	}

	// Step 5: SIMULATE the (UNCHANGED) feedback role's own prompt action: file
	// a work bead labeled worker-ready as a child of the PR, then close the
	// process-feedback bead. This is existing behavior this bead does not
	// modify — included so the chain is exercised end to end.
	workOut, err := r.Run(ctx, "create", "--type=task", "--title", "fix: unhandled error in foo.go",
		"-d", "fix the unhandled error", "--silent")
	if err != nil {
		t.Fatalf("create work bead: %v\n%s", err, workOut)
	}
	workID := strings.TrimSpace(workOut)
	if out, err := r.Run(ctx, "update", workID, "--add-label", "worker-ready"); err != nil {
		t.Fatalf("label worker-ready: %v\n%s", err, out)
	}
	if out, err := r.Run(ctx, "close", pfID); err != nil {
		t.Fatalf("close process-feedback bead: %v\n%s", err, out)
	}

	// Step 6: the REAL worker-source query must discover it — proving
	// "worker flows" and completing the end-to-end chain for the new path.
	workerQuery, ok := findBeadsReadyQuery(qs, "worker-source")
	if !ok {
		t.Fatalf("no worker-source query in the built-in set")
	}
	workEvents, err := workerQuery.Run(ctx, query.Env{BD: r})
	if err != nil {
		t.Fatalf("worker-source query: %v", err)
	}
	if !eventsContainItem(workEvents, workID) {
		t.Fatalf("worker-source query did not discover work bead %s; events=%+v", workID, workEvents)
	}
}

func findBeadsReadyQuery(qs query.SourceSet, name string) (query.BeadsReady, bool) {
	for _, s := range qs {
		if s.Name != name {
			continue
		}
		br, ok := s.Query.(query.BeadsReady)
		return br, ok
	}
	return query.BeadsReady{}, false
}

func eventsContainItem(events []event.Event, id string) bool {
	for _, e := range events {
		if e.Item.ID == id {
			return true
		}
	}
	return false
}
