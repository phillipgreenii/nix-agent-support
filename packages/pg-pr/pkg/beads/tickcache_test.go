package beads

import (
	"context"
	"strings"
	"testing"
)

func TestLoadTickCache_EmptyWorkspace(t *testing.T) {
	c, _ := newBDWorkspace(t)
	cache := c.LoadTickCache(context.Background())
	if cache == nil {
		t.Fatal("LoadTickCache returned nil")
	}
	if len(cache.HumanLabeled) != 0 {
		t.Errorf("expected empty HumanLabeled, got %d", len(cache.HumanLabeled))
	}
	if len(cache.MergeRequestsByID) != 0 {
		t.Errorf("expected empty MergeRequestsByID, got %d", len(cache.MergeRequestsByID))
	}
	if len(cache.OpenProcessingByPR) != 0 {
		t.Errorf("expected empty OpenProcessingByPR, got %d", len(cache.OpenProcessingByPR))
	}
	if len(cache.FeedbackByCycle) != 0 {
		t.Errorf("expected empty FeedbackByCycle, got %d", len(cache.FeedbackByCycle))
	}
}

func TestLoadTickCache_PRWithOpenCycleAndFeedback(t *testing.T) {
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// Workspace setup: one PR bead, one open processing-cycle under it,
	// one feedback bead under the cycle.
	prID, _, err := c.EnsureMergeRequest(ctx, "test PR", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "x/y#1", false)
	if err != nil {
		t.Fatal(err)
	}
	// Create a feedback bead directly via bd CLI so the cache can find it
	// under TypeFeedback. We store fingerprint as metadata so FindFeedbackForPR works.
	out, err := runner.Run(ctx,
		"create", "--type=feedback", "--title", "test feedback",
		"--metadata", `{"kind":"comment-thread","fingerprint":"fp-abc"}`,
		"--silent")
	if err != nil {
		t.Fatal(err)
	}
	fbID := strings.TrimSpace(out)
	if fbID == "" {
		t.Fatal("bd create returned empty id")
	}
	if _, err := runner.Run(ctx, "dep", "add", fbID, cycleID, "--type=parent-child", "--no-cycle-check"); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	cache := c.LoadTickCache(ctx)
	if cache == nil {
		t.Fatal("LoadTickCache returned nil")
	}

	// MergeRequestsByID contains the PR.
	if _, ok := cache.MergeRequestsByID[prID]; !ok {
		t.Errorf("MergeRequestsByID missing %s; have %v", prID, cache.MergeRequestsByID)
	}

	// OpenProcessingByPR maps prID → cycleID.
	if got := cache.OpenProcessingByPR[prID]; got != cycleID {
		t.Errorf("OpenProcessingByPR[%s] = %q, want %q", prID, got, cycleID)
	}

	// FeedbackByCycle maps cycleID → [fb].
	fbs := cache.FeedbackByCycle[cycleID]
	if len(fbs) != 1 || fbs[0].ID != fbID {
		t.Errorf("FeedbackByCycle[%s] = %+v, want [%s]", cycleID, fbs, fbID)
	}

	// Lookup helpers.
	gotCycle, ok := cache.OpenCycleFor(prID)
	if !ok || gotCycle != cycleID {
		t.Errorf("OpenCycleFor(%s) = (%q, %v), want (%q, true)", prID, gotCycle, ok, cycleID)
	}
	gotMR, ok := cache.FindMergeRequest("x/y", 1)
	if !ok || gotMR.ID != prID {
		t.Errorf("FindMergeRequest(x/y, 1) = (%+v, %v), want id=%s", gotMR, ok, prID)
	}
	gotFB, ok := cache.FindFeedbackForPR(prID, "fp-abc")
	if !ok || gotFB.ID != fbID {
		t.Errorf("FindFeedbackForPR(%s, fp-abc) = (%+v, %v), want id=%s", prID, gotFB, ok, fbID)
	}
	if _, ok := cache.FindFeedbackForPR(prID, "fp-missing"); ok {
		t.Error("FindFeedbackForPR with bogus fingerprint should miss")
	}
}

func TestTickCache_NilSafe(t *testing.T) {
	var cache *TickCache
	if _, ok := cache.OpenCycleFor("anything"); ok {
		t.Error("OpenCycleFor on nil cache should return ok=false")
	}
	if got := cache.FeedbackUnder("anything"); got != nil {
		t.Errorf("FeedbackUnder on nil cache should return nil, got %+v", got)
	}
	if _, ok := cache.FindMergeRequest("r", 1); ok {
		t.Error("FindMergeRequest on nil cache should return ok=false")
	}
	if _, ok := cache.FindFeedbackForPR("anything", "fp"); ok {
		t.Error("FindFeedbackForPR on nil cache should return ok=false")
	}
}

func TestLoadTickCache_DepsUpByPR(t *testing.T) {
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// A merge-request with three descendants: a direct child task (open),
	// a direct child task (closed), and a grandchild task two hops up.
	prID, _, err := c.EnsureMergeRequest(ctx, "deps PR", MergeRequestFields{Repo: "x/y", PRNumber: 11})
	if err != nil {
		t.Fatal(err)
	}
	a := createChildBead(t, runner, prID, "direct-open")
	b := createChildBead(t, runner, prID, "direct-closed")
	closeBead(t, runner, b)
	g := createChildBead(t, runner, a, "grandchild")

	cache := c.LoadTickCache(ctx)
	deps, ok := cache.DepsUpFor(prID)
	if !ok {
		t.Fatalf("DepsUpFor(%s) ok=false; cache built for known PR should always return ok=true", prID)
	}
	byID := map[string]DepNode{}
	for _, d := range deps {
		byID[d.ID] = d
	}
	if _, has := byID[a]; !has {
		t.Errorf("DepsUpByPR missing direct child %s; got %+v", a, deps)
	}
	if _, has := byID[b]; !has {
		t.Errorf("DepsUpByPR missing closed direct child %s; got %+v", b, deps)
	}
	if byID[b].Status != "closed" {
		t.Errorf("DepsUpByPR child %s should carry status=closed; got %+v", b, byID[b])
	}
	if _, has := byID[g]; !has {
		t.Errorf("DepsUpByPR missing transitive grandchild %s; got %+v", g, deps)
	}
	// Root must not be included in its own dep list.
	if _, has := byID[prID]; has {
		t.Errorf("DepsUpByPR should not include the root %s", prID)
	}
}

func TestTickCache_DepsUpFor_NilCache(t *testing.T) {
	var cache *TickCache
	if _, ok := cache.DepsUpFor("anything"); ok {
		t.Error("DepsUpFor on nil cache should return ok=false")
	}
}

func TestTickCache_OpenProcessingByPR_IgnoresClosedCycles(t *testing.T) {
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	prID, _, err := c.EnsureMergeRequest(ctx, "PR", MergeRequestFields{Repo: "x/y", PRNumber: 7})
	if err != nil {
		t.Fatal(err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "x/y#7", false)
	if err != nil {
		t.Fatal(err)
	}
	// Close the cycle so LoadTickCache's open-task list skips it.
	if _, err := runner.Run(ctx, "close", cycleID, "--force"); err != nil {
		t.Fatalf("close cycle: %v", err)
	}

	cache := c.LoadTickCache(ctx)
	if got, ok := cache.OpenCycleFor(prID); ok {
		t.Errorf("OpenCycleFor returned closed cycle: (%q, %v)", got, ok)
	}
}
