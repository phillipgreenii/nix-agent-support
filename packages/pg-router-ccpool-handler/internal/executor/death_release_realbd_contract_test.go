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

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// Real-bd regression guard for the death-path claim release (bead pg2-qyc3h).
//
// WHY THIS FILE EXISTS (global rule V-3): whether a handler may release a
// claim held by ANOTHER actor is bd's claim guard, a property of the REAL tool.
// bd 1.3.1 refuses an unguarded `bd update <id> --status=open --assignee=`
// against an in_progress bead held by a different actor ("cannot reassign
// ...: held by ..."), which is what left zr-xvbqa.2 in_progress for 23 h after
// its review session died (2026-10-12) while the handler discarded the error.
// These tests run waitDone's death branch against a REAL `bd` in a throwaway
// store, with the bead claimed by one actor and the handler's runner acting as
// another, and read the bead back.
//
// The test names carry the `TestPark_RealBD_` prefix so the
// `pg-router-ccpool-handler-bd-contract-test` hook's -run filter picks them up.
// Hermetic store: dtest.NewRealBD. Run directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run 'TestPark_RealBD_(Death|Unguarded)' ./internal/executor/

const (
	deadWorkerActor = "pgii-pool__review-dead-worker"
	handlerActor    = "pgii-pool__review-handler"
)

// otherActorRunner returns a runner on the same store as r, acting as actor.
func otherActorRunner(r *beads.CLIRunner, actor string) *beads.CLIRunner {
	return &beads.CLIRunner{Dir: r.Dir, Env: r.Env, Actor: actor}
}

// crossActorBead returns a runner for the handler, and a bead claimed by a
// different (now dead) worker actor.
func crossActorBead(t *testing.T) (ctx context.Context, handler *beads.CLIRunner, id string) {
	t.Helper()
	ctx, worker := dtest.NewRealBD(t, deadWorkerActor)
	id = claimed(ctx, t, worker)
	iss, err := beads.ShowObj(ctx, worker, id)
	if err != nil || iss.Status != "in_progress" || iss.Assignee != deadWorkerActor {
		t.Fatalf("precondition: bead must be in_progress under %s; got status=%q assignee=%q err=%v", deadWorkerActor, iss.Status, iss.Assignee, err)
	}
	return ctx, otherActorRunner(worker, handlerActor), id
}

// The control: the pre-fix release is refused by the real claim guard. If bd
// ever stops refusing it, this fails and the guard in beads.heldGuard can be
// reconsidered; until then it proves the tests below are not vacuous.
func TestPark_RealBD_UnguardedCrossActorReleaseIsRefused(t *testing.T) {
	ctx, handler, id := crossActorBead(t)
	err := beads.Unclaim(ctx, handler, id)
	if err == nil {
		t.Fatal("bd accepted an unguarded cross-actor release; the claim guard this fix works around is gone")
	}
	if !strings.Contains(err.Error(), "cannot reassign") {
		t.Errorf("expected bd's claim-guard refusal, got: %v", err)
	}
	iss, serr := beads.ShowObj(ctx, handler, id)
	if serr != nil || iss.Status != "in_progress" || iss.Assignee != deadWorkerActor {
		t.Errorf("a refused release must leave the claim alone: status=%q assignee=%q err=%v", iss.Status, iss.Assignee, serr)
	}
}

// deathWait drives waitDone's death branch for a review role whose session is
// gone, with the handler's runner (wrapped by bd) and an eventlog at logPath.
func deathWait(ctx context.Context, t *testing.T, bd beads.Runner, id string, onFailure roles.FailureAction, logPath string) error {
	t.Helper()
	cfg := fastCfg()
	role := reviewRole(cfg)
	role.CCPool.Actor = handlerActor
	role.CCPool.OnFailure = onFailure
	name := "pg-router-review-" + id
	dead := ccpool.Session{ExternalID: name, Live: false, State: ccpool.StateErrored, CloseReason: "handler"}
	cc := &dtest.FakeCC{ListSeq: [][]ccpool.Session{{dead}, {dead}, {dead}}}
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	e := &ccpoolRun{deps: Deps{CC: cc, BD: bd, Cfg: cfg, Log: lw, Now: clk.Now, Tick: clk.TickAdvancing()}}
	return e.waitDone(ctx, nil, DispatchContext{Role: role, Item: item.Item{ID: id}}, name)
}

// The bead a dead session held under another actor is released with human.
func TestPark_RealBD_DeathReleasesClaimHeldByAnotherActor(t *testing.T) {
	ctx, handler, id := crossActorBead(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	werr := deathWait(ctx, t, handler, id, roles.AddHuman, logPath)
	var sd *sessionDied
	if !errors.As(werr, &sd) {
		t.Fatalf("want a sessionDied, got %v", werr)
	}
	if sd.releaseErr != nil {
		t.Fatalf("the release must hold against bd's claim guard: %v", sd.releaseErr)
	}
	assertParked(ctx, t, handler, id)
	if raw, _ := os.ReadFile(logPath); strings.Contains(string(raw), deathReleaseKind) {
		t.Errorf("a successful release must not emit %s; log:\n%s", deathReleaseKind, raw)
	}
}

// The same under the unclaim role policy: released, and never labelled human.
func TestPark_RealBD_DeathUnclaimReleasesClaimHeldByAnotherActor(t *testing.T) {
	ctx, handler, id := crossActorBead(t)
	werr := deathWait(ctx, t, handler, id, roles.Unclaim, filepath.Join(t.TempDir(), "events.jsonl"))
	var sd *sessionDied
	if !errors.As(werr, &sd) || sd.releaseErr != nil {
		t.Fatalf("want a sessionDied with the release landed; err=%v", werr)
	}
	iss, err := beads.ShowObj(ctx, handler, id)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Status != "open" || iss.Assignee != "" || iss.HasLabel("human") {
		t.Errorf("want open, unassigned, no human; got status=%q assignee=%q labels=%v", iss.Status, iss.Assignee, iss.Labels)
	}
}

// raceBD lets the real bd answer every call, but right before the release's
// update it has ANOTHER actor take over the claim (the dead worker's lease
// lapsed and a live worker re-claimed the bead between the handler's read and
// its write).
type raceBD struct {
	inner *beads.CLIRunner
	id    string
	taker *beads.CLIRunner
	ctx   context.Context
	t     *testing.T
}

func (b *raceBD) Run(ctx context.Context, args ...string) (string, error) {
	if args[0] == "update" {
		if out, err := b.taker.Run(b.ctx, "update", b.id, "--assignee", "live-worker", "--force"); err != nil {
			b.t.Fatalf("simulating the takeover: %v\n%s", err, out)
		}
	}
	return b.inner.Run(ctx, args...)
}

// The guard also protects a peer: when the claim changed hands since the read,
// nothing is written (the bead is not robbed from its new holder, not labelled
// human), and the stale guard is surfaced as a failed release.
func TestPark_RealBD_DeathReleaseDoesNotRobAReclaimedBead(t *testing.T) {
	ctx, handler, id := crossActorBead(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	bd := &raceBD{inner: handler, id: id, taker: otherActorRunner(handler, "live-worker"), ctx: ctx, t: t}
	werr := deathWait(ctx, t, bd, id, roles.AddHuman, logPath)
	var sd *sessionDied
	if !errors.As(werr, &sd) {
		t.Fatalf("want a sessionDied, got %v", werr)
	}
	if sd.releaseErr == nil || !beads.IsGuardStale(sd.releaseErr) {
		t.Fatalf("a claim that changed hands must surface as a stale-guard failure; releaseErr=%v", sd.releaseErr)
	}
	iss, err := beads.ShowObj(ctx, handler, id)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Status != "in_progress" || iss.Assignee != "live-worker" || iss.HasLabel("human") {
		t.Errorf("the new holder's claim must be untouched: status=%q assignee=%q labels=%v", iss.Status, iss.Assignee, iss.Labels)
	}
	raw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(raw), `"kind":"`+deathReleaseKind+`"`) || !strings.Contains(string(raw), `"guard_stale":true`) {
		t.Errorf("the failed release must be counted as %s with guard_stale=true; log:\n%s", deathReleaseKind, raw)
	}
}
