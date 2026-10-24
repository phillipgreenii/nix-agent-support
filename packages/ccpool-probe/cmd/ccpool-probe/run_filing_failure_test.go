package main

import (
	"strings"
	"testing"
	"time"
)

// A bd write that fails for a finding means the finding was not filed, so
// the run must exit 4 (the handler discards stderr on exit 0), and the
// edge-triggered zombie-drift finding must be derived again next run.

var (
	twoZombies = []ccpoolSessionRow{
		{ExternalID: "sess-1", State: "working", Live: false},
		{ExternalID: "sess-2", State: "working", Live: false},
	}
	oneZombie = []ccpoolSessionRow{
		{ExternalID: "sess-1", State: "working", Live: false},
	}
)

// existingNeedsInputBead is an escalated bead whose recorded state differs
// from the fresh finding's, so decideAction picks actionUpdate for it.
func existingNeedsInputBead() []connectorIssue {
	return []connectorIssue{{
		ID: "zr-1",
		Metadata: map[string]string{
			metaFingerprint: "needs-input:sess-1",
			metaState:       "stale-state",
		},
	}}
}

func TestRunProbeZombieDriftCreateFailureIsPartialAndRederivedNextRun(t *testing.T) {
	opts := baseOpts(t)

	cmd1, _ := testCmd()
	spy1 := &spyDeps{allPoolRows: oneZombie}
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("baseline run: expected exit 0, got %v", err)
	}

	cmd2, stderr2 := testCmd()
	spy2 := &spyDeps{allPoolRows: twoZombies, createErr: errConnectorFailed}
	err := runProbe(cmd2, opts, spy2.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("failed create must exit 4 (partial), got err=%v", err)
	}
	if !strings.Contains(err.Error(), "create bd issue for zombie-count:+100%") {
		t.Fatalf("exit error must name the failed create, got %v", err)
	}
	if !strings.Contains(stderr2.String(), "failed to create bd issue for zombie-count:+100%") {
		t.Fatalf("stderr must still carry the failure, got:\n%s", stderr2.String())
	}
	if len(spy2.created) != 1 {
		t.Fatalf("failed run: expected 1 create attempt, got %d", len(spy2.created))
	}

	// Same zombie count again: the baseline must NOT have advanced, so the
	// finding is derived again and createIssue is called again.
	cmd3, _ := testCmd()
	spy3 := &spyDeps{allPoolRows: twoZombies}
	if err := runProbe(cmd3, opts, spy3.toRunDeps(time.Now())); err != nil {
		t.Fatalf("retry run: expected exit 0, got %v", err)
	}
	if len(spy3.created) != 1 || spy3.created[0].metadata[metaFingerprint] != "zombie-count:+100%" {
		t.Fatalf("retry run must derive and file the zombie-drift finding again, got %+v", spy3.created)
	}

	// Once filed, the baseline advances as before: a fourth run at the same
	// count is quiet.
	cmd4, _ := testCmd()
	spy4 := &spyDeps{allPoolRows: twoZombies}
	if err := runProbe(cmd4, opts, spy4.toRunDeps(time.Now())); err != nil {
		t.Fatalf("run after success: expected exit 0, got %v", err)
	}
	if len(spy4.created) != 0 {
		t.Fatalf("baseline must advance after a successful filing, got %d creates", len(spy4.created))
	}
}

func TestRunProbeZombieDriftDedupQueryFailureKeepsBaseline(t *testing.T) {
	opts := baseOpts(t)

	cmd1, _ := testCmd()
	spy1 := &spyDeps{allPoolRows: oneZombie}
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("baseline run: expected exit 0, got %v", err)
	}

	cmd2, _ := testCmd()
	spy2 := &spyDeps{allPoolRows: twoZombies, listEscalatedErr: errConnectorFailed}
	if exitCodeOf(t, runProbe(cmd2, opts, spy2.toRunDeps(time.Now()))) != 4 {
		t.Fatalf("dedup query failure must exit 4")
	}

	cmd3, _ := testCmd()
	spy3 := &spyDeps{allPoolRows: twoZombies}
	if err := runProbe(cmd3, opts, spy3.toRunDeps(time.Now())); err != nil {
		t.Fatalf("retry run: expected exit 0, got %v", err)
	}
	if len(spy3.created) != 1 {
		t.Fatalf("retry run must derive the zombie-drift finding again, got %d creates", len(spy3.created))
	}
}

func TestRunProbeUpdateMetadataFailureIsPartial(t *testing.T) {
	cmd, stderr := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{
		needsInputRows:      []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}},
		listEscalatedResult: existingNeedsInputBead(),
		updateErr:           errConnectorFailed,
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("failed metadata update must exit 4 (partial), got err=%v", err)
	}
	if !strings.Contains(err.Error(), "update bd issue zr-1 for needs-input:sess-1") {
		t.Fatalf("exit error must name the failed update, got %v", err)
	}
	if !strings.Contains(stderr.String(), "failed to update bd issue zr-1") {
		t.Fatalf("stderr must still carry the failure, got:\n%s", stderr.String())
	}
	if len(spy.updated) != 1 {
		t.Fatalf("expected 1 update attempt, got %d", len(spy.updated))
	}
}

func TestRunProbeCommentFailureIsPartial(t *testing.T) {
	cmd, stderr := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{
		needsInputRows:      []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}},
		listEscalatedResult: existingNeedsInputBead(),
		commentErr:          errConnectorFailed,
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("failed comment must exit 4 (partial), got err=%v", err)
	}
	if !strings.Contains(err.Error(), "comment on bd issue zr-1 for needs-input:sess-1") {
		t.Fatalf("exit error must name the failed comment, got %v", err)
	}
	if !strings.Contains(stderr.String(), "failed to comment on bd issue zr-1") {
		t.Fatalf("stderr must still carry the failure, got:\n%s", stderr.String())
	}
	if len(spy.commented) != 1 {
		t.Fatalf("expected 1 comment attempt, got %d", len(spy.commented))
	}
}

func TestRunProbeSuccessfulWritesStillExitZeroAndAdvanceBaseline(t *testing.T) {
	opts := baseOpts(t)
	cmd1, _ := testCmd()
	if err := runProbe(cmd1, opts, (&spyDeps{allPoolRows: oneZombie}).toRunDeps(time.Now())); err != nil {
		t.Fatalf("baseline run: %v", err)
	}
	cmd2, _ := testCmd()
	spy := &spyDeps{allPoolRows: twoZombies}
	if err := runProbe(cmd2, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("successful filing must exit 0, got %v", err)
	}
	snap, ok := loadSnapshot(opts.snapshotPath)
	if !ok || snap.ZombieCount != 2 {
		t.Fatalf("successful filing must advance the baseline to 2, got %d ok=%v", snap.ZombieCount, ok)
	}
}
