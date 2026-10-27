//go:build contract

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/dtest"
	"github.com/phillipgreenii/pg-router-handlers/internal/eventlog"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// Real-bd regression guards for the claim releases of the split-triage and
// external-close paths (bead pg2-ybkav, the pg2-qyc3h class).
//
// WHY THIS FILE EXISTS (global rule V-3): bd 1.3.1 refuses an unguarded
// `bd update <id> --status=open --assignee=` against an in_progress bead held
// by a DIFFERENT actor, which is the production shape on these paths: the
// split-triager runs as its own actor while the parent's claim belongs to the
// drain worker's, and the session an external close ended held its claim under
// the worker's actor, not the handler's. Each test claims the bead as one actor
// and acts as another against a REAL `bd` in a throwaway store
// (dtest.NewRealBD), then reads the bead back. The control that proves the
// refusal is TestPark_RealBD_UnguardedCrossActorReleaseIsRefused
// (death_release_realbd_contract_test.go).
//
// The names carry the `TestPark_RealBD_` prefix so the
// `pg-router-ccpool-handler-bd-contract-test` hook's -run filter picks them up
// (the beads package is not in that hook's package list, which is why the split
// tests live here). Run directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run 'TestPark_RealBD_(Split|Unsplittable|ExternalClose)' ./internal/executor/

const splitTriagerActor = "pgii-pool__split-triager"

// splitParent returns a runner acting as the split-triager and a parent bead
// the drain worker (another actor) holds in_progress with needs-split-review.
func splitParent(t *testing.T) (context.Context, *beads.CLIRunner, string) {
	t.Helper()
	ctx, worker := dtest.NewRealBD(t, deadWorkerActor)
	id := claimed(ctx, t, worker, beads.LabelNeedsSplitReview)
	iss, err := beads.ShowObj(ctx, worker, id)
	if err != nil || iss.Status != "in_progress" || iss.Assignee != deadWorkerActor {
		t.Fatalf("precondition: parent must be in_progress under %s; got status=%q assignee=%q err=%v", deadWorkerActor, iss.Status, iss.Assignee, err)
	}
	return ctx, otherActorRunner(worker, splitTriagerActor), id
}

func TestPark_RealBD_SplitReleasesAParentClaimedByAnotherActor(t *testing.T) {
	ctx, triager, id := splitParent(t)
	plan := beads.SplitPlan{Rationale: "two separable parts", Children: []beads.SplitChild{
		{Title: "part a", Description: "do a", Acceptance: "- [ ] a done"},
		{Title: "part b", Description: "do b", Acceptance: "- [ ] b done"},
	}}
	ids, err := beads.ApplySplit(ctx, triager, id, plan)
	if err != nil {
		t.Fatalf("a split of a bead claimed by another actor must succeed: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("want two children, got %v", ids)
	}
	iss, err := beads.ShowObj(ctx, triager, id)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Status != "open" || iss.Assignee != "" {
		t.Errorf("the split parent must be released: status=%q assignee=%q", iss.Status, iss.Assignee)
	}
	if !iss.HasLabel(beads.LabelWasSplit) {
		t.Errorf("the parent must carry %s; labels=%v", beads.LabelWasSplit, iss.Labels)
	}
	for _, l := range []string{"human", beads.LabelNeedsSplitReview} {
		if iss.HasLabel(l) {
			t.Errorf("the parent must not carry %q; labels=%v", l, iss.Labels)
		}
	}
}

func TestPark_RealBD_UnsplittableReleasesAParentClaimedByAnotherActor(t *testing.T) {
	ctx, triager, id := splitParent(t)
	if err := beads.MarkUnsplittable(ctx, triager, id, "single indivisible refactor"); err != nil {
		t.Fatalf("a not-splittable verdict on a bead claimed by another actor must succeed: %v", err)
	}
	iss, err := beads.ShowObj(ctx, triager, id)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Status != "open" || iss.Assignee != "" {
		t.Errorf("the parent must be released: status=%q assignee=%q", iss.Status, iss.Assignee)
	}
	for _, l := range []string{"human", "attention"} {
		if !iss.HasLabel(l) {
			t.Errorf("the parent must be handed to the operator (%q); labels=%v", l, iss.Labels)
		}
	}
	if iss.HasLabel(beads.LabelNeedsSplitReview) {
		t.Errorf("needs-split-review must be removed; labels=%v", iss.Labels)
	}
}

// The external-close branch of waitDone (ccpool closed the session, reason
// idle_ttl) releases a claim held by another actor, and writes no failure record.
func TestPark_RealBD_ExternalCloseReleasesClaimHeldByAnotherActor(t *testing.T) {
	ctx, handler, id := crossActorBead(t)
	cfg := fastCfg()
	role := reviewRole(cfg)
	role.CCPool.Actor = handlerActor
	name := "pg-router-review-" + id
	row := ccpool.Session{ExternalID: name, Live: false, State: ccpool.StateWorking, CloseReason: "idle_ttl"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{row}, {row}, {row}}}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	e := &ccpoolRun{deps: Deps{CC: cc, BD: handler, Cfg: cfg, Log: lw, Now: clk.Now, Tick: clk.TickAdvancing()}}

	werr := e.waitDone(ctx, nil, DispatchContext{Role: role, Item: item.Item{ID: id}}, name)
	var ec *externallyClosed
	if !errors.As(werr, &ec) {
		t.Fatalf("want an external close, got %v", werr)
	}
	if ec.releaseErr != nil {
		t.Fatalf("the release must hold against bd's claim guard: %v", ec.releaseErr)
	}
	iss, err := beads.ShowObj(ctx, handler, id)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Status != "open" || iss.Assignee != "" {
		t.Errorf("the bead must be released: status=%q assignee=%q", iss.Status, iss.Assignee)
	}
	if raw, _ := os.ReadFile(logPath); strings.Contains(string(raw), externalReleaseKind) {
		t.Errorf("a successful release must not emit %s; log:\n%s", externalReleaseKind, raw)
	}
}
