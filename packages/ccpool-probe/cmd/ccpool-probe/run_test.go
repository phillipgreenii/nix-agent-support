package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// testCmd builds a bare *cobra.Command with a real context and a captured
// stderr buffer -- runProbe is called directly (not via root.Execute()),
// mirroring newRunCmd's own RunE body without going through cobra's flag
// parsing.
func testCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	return cmd, &stderr
}

// spyDeps records every call this probe makes to its own external
// dependencies, so a test can assert on WHAT was attempted without a
// real ccpool/pg-connector binary or real snapshot I/O.
type spyDeps struct {
	// pools is what discovery returns; nil = the single ambient pool.
	pools []poolRef
	// Per-pool overrides keyed by poolRef.Label; a pool with no entry falls
	// back to the flat needsInputRows/allPoolRows/*Err fields below.
	needsInputByPool map[string][]ccpoolSessionRow
	allByPool        map[string][]ccpoolSessionRow
	allErrByPool     map[string]error

	needsInputRows []ccpoolSessionRow
	needsInputErr  error

	allPoolRows []ccpoolSessionRow
	allPoolErr  error

	listEscalatedResult []connectorIssue
	listEscalatedErr    error
	// listEscalatedByQuery, when non-nil, overrides listEscalatedResult:
	// it returns the beads a given named dedup query would see, letting a
	// test model "ready-only query hides a human-labeled bead, the
	// all-non-closed query shows it". listedQueries records every query
	// name the probe listed through.
	listEscalatedByQuery map[string][]connectorIssue
	listedQueries        []string

	created []struct {
		title    string
		labels   []string
		metadata map[string]string
		body     string
	}
	createErr error

	updated []struct {
		id       string
		metadata map[string]string
	}
	updateErr error

	commented []struct {
		id   string
		body string
	}
	commentErr error
}

func (s *spyDeps) toRunDeps(clock time.Time) runDeps {
	return runDeps{
		now: func() time.Time { return clock },
		listPools: func(string, func(string)) []poolRef {
			if s.pools == nil {
				return []poolRef{{Label: ambientPoolLabel}}
			}
			return s.pools
		},
		listNeedsInput: func(ctx context.Context, pool poolRef, warn func(string)) ([]ccpoolSessionRow, error) {
			if rows, ok := s.needsInputByPool[pool.Label]; ok {
				return rows, nil
			}
			return s.needsInputRows, s.needsInputErr
		},
		listAllPoolSessions: func(ctx context.Context, pool poolRef, warn func(string)) ([]ccpoolSessionRow, error) {
			if err, ok := s.allErrByPool[pool.Label]; ok {
				return nil, err
			}
			if rows, ok := s.allByPool[pool.Label]; ok {
				return rows, nil
			}
			return s.allPoolRows, s.allPoolErr
		},
		listEscalated: func(ctx context.Context, query string, warn func(string)) ([]connectorIssue, error) {
			s.listedQueries = append(s.listedQueries, query)
			if s.listEscalatedByQuery != nil {
				return s.listEscalatedByQuery[query], s.listEscalatedErr
			}
			return s.listEscalatedResult, s.listEscalatedErr
		},
		createIssue: func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error) {
			s.created = append(s.created, struct {
				title    string
				labels   []string
				metadata map[string]string
				body     string
			}{title, labels, metadata, description})
			if s.createErr != nil {
				return connectorIssue{}, s.createErr
			}
			return connectorIssue{ID: "zr-new"}, nil
		},
		updateMetadata: func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error {
			s.updated = append(s.updated, struct {
				id       string
				metadata map[string]string
			}{id, metadata})
			return s.updateErr
		},
		comment: func(ctx context.Context, id, body string, warn func(string)) error {
			s.commented = append(s.commented, struct {
				id   string
				body string
			}{id, body})
			return s.commentErr
		},
	}
}

// baseOpts points the snapshot at a path whose PARENT DIRECTORIES DO NOT
// EXIST, like the real default ($HOME/.local/state/ccpool-probe/ on a
// fresh host). An existing t.TempDir() parent hid pg2-d845f: every
// two-run test passed while production never persisted a snapshot.
func baseOpts(t *testing.T) runOptions {
	return runOptions{
		ccpoolTimeout:      time.Second,
		pgConnectorTimeout: time.Second,
		dedupQuery:         defaultDedupQuery,
		snapshotPath:       filepath.Join(t.TempDir(), "nested", "ccpool-probe", "snapshot.json"),
	}
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ece *exitCodeError
	if errors.As(err, &ece) {
		return ece.code
	}
	t.Fatalf("expected an *exitCodeError, got %T: %v", err, err)
	return -1
}

func TestRunProbeTotalFailureWritesNoSnapshot(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{needsInputErr: errCcpoolFailed, allPoolErr: errCcpoolFailed}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 3 {
		t.Fatalf("expected exit 3 (total failure), got err=%v", err)
	}
	if _, statErr := os.Stat(opts.snapshotPath); statErr == nil {
		t.Fatalf("expected no snapshot to be written on total failure")
	}
	if len(spy.created) != 0 {
		t.Fatalf("expected no bd issue to be created on total failure")
	}
}

func TestRunProbeCleanWhenNothingFound(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("expected no bd issue created when nothing is found")
	}
	if _, statErr := os.Stat(opts.snapshotPath); statErr != nil {
		t.Fatalf("expected a snapshot to be written on a successful run: %v", statErr)
	}
}

func TestRunProbeNeedsInputFindingCreatesIssue(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{
		needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", Name: "worker", State: "needs_input", Live: true}},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected exactly 1 bd issue created, got %d", len(spy.created))
	}
	got := spy.created[0]
	if got.labels[0] != "escalated" {
		t.Fatalf("expected the escalated label, got %v", got.labels)
	}
	if got.metadata[metaFingerprint] != "needs-input:sess-1" {
		t.Fatalf("got metadata %v", got.metadata)
	}
}

func TestRunProbeDedupSkipsWhenNothingNew(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{
		needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}},
		listEscalatedResult: []connectorIssue{{
			ID: "zr-1",
			Metadata: map[string]string{
				metaFingerprint: "needs-input:sess-1",
				metaState:       "needs_input",
			},
		}},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 0 || len(spy.updated) != 0 || len(spy.commented) != 0 {
		t.Fatalf("expected no bd write when nothing changed: created=%d updated=%d commented=%d", len(spy.created), len(spy.updated), len(spy.commented))
	}
}

// TestRunProbeZombieDriftEstablishesBaselineThenAlertsOnGrowth drives
// runProbe TWICE against the same persisted snapshot path: the first run
// has no prior baseline (quiet by construction), the second observes
// 100% growth in working-and-dead sessions and creates a finding. The
// snapshot path's parent directories do not exist (baseOpts), so the first
// run must create them for the second to see a baseline (pg2-d845f).
func TestRunProbeZombieDriftEstablishesBaselineThenAlertsOnGrowth(t *testing.T) {
	opts := baseOpts(t)

	cmd1, _ := testCmd()
	spy1 := &spyDeps{
		allPoolRows: []ccpoolSessionRow{{ExternalID: "sess-1", State: "working", Live: false}},
	}
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("first run: expected exit 0, got %v", err)
	}
	if len(spy1.created) != 0 {
		t.Fatalf("first run: expected no finding with no prior baseline, got %d", len(spy1.created))
	}
	if _, ok := loadSnapshot(opts.snapshotPath); !ok {
		t.Fatalf("first run must persist a snapshot, creating its missing parent directories")
	}

	cmd2, _ := testCmd()
	spy2 := &spyDeps{
		allPoolRows: []ccpoolSessionRow{
			{ExternalID: "sess-1", State: "working", Live: false},
			{ExternalID: "sess-2", State: "working", Live: false},
		},
	}
	err := runProbe(cmd2, opts, spy2.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("second run: expected exit 0, got %v", err)
	}
	if len(spy2.created) != 1 {
		t.Fatalf("second run: expected 1 finding for 100%% zombie growth, got %d", len(spy2.created))
	}
	if spy2.created[0].metadata[metaFingerprint] != "zombie-count:+100%" {
		t.Fatalf("got metadata %v", spy2.created[0].metadata)
	}
}

func TestRunProbePartialWhenNeedsInputSubcheckDegrades(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	// Seed a prior snapshot so the zombie-drift sub-check has a baseline
	// to diff against and actually produces a finding.
	if err := saveSnapshot(opts.snapshotPath, snapshot{ZombieCount: 1}); err != nil {
		t.Fatal(err)
	}
	spy := &spyDeps{
		needsInputErr: errCcpoolFailed,
		allPoolRows:   []ccpoolSessionRow{{ExternalID: "s1", State: "working"}, {ExternalID: "s2", State: "working"}},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial), got err=%v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected the zombie-drift sub-check to still produce a finding, got %d", len(spy.created))
	}
	if len(spy.created[0].body) == 0 {
		t.Fatalf("expected a non-empty body")
	}
}

func TestRunProbeDedupQueryFailureIsPartial(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := &spyDeps{
		needsInputRows:   []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}},
		listEscalatedErr: errConnectorFailed,
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial), got err=%v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("expected no create when the dedup query itself fails")
	}
}

var (
	workerPool = poolRef{Label: "pg-router-ccpool-worker", Dir: "/pools/pg-router-ccpool-worker"}
	reviewPool = poolRef{Label: "pg-router-ccpool-review", Dir: "/pools/pg-router-ccpool-review"}
)

// TestRunProbeReportsStuckSessionInRolePoolNamingThePool is the pg2-bkzrc
// acceptance test: two role pools, one holding a stuck session, which must
// be reported with that pool named; the clean pool contributes nothing.
func TestRunProbeReportsStuckSessionInRolePoolNamingThePool(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	stuck := ccpoolSessionRow{ExternalID: "sess-r1", Name: "reviewer", State: "needs_input", Live: true, CWD: "/tmp/r1"}
	spy := &spyDeps{
		pools: []poolRef{{Label: ambientPoolLabel}, workerPool, reviewPool},
		needsInputByPool: map[string][]ccpoolSessionRow{
			ambientPoolLabel: nil,
			workerPool.Label: nil,
			reviewPool.Label: {stuck},
		},
	}
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected exactly 1 bd issue (the stuck review-pool session), got %d", len(spy.created))
	}
	got := spy.created[0]
	if got.metadata[metaFingerprint] != "needs-input:"+reviewPool.Label+":sess-r1" {
		t.Errorf("fingerprint %q does not scope the finding to the review pool", got.metadata[metaFingerprint])
	}
	if !strings.Contains(got.title, reviewPool.Label) || !strings.Contains(got.body, "pool="+reviewPool.Label) {
		t.Errorf("finding must name the pool; title=%q body=%q", got.title, got.body)
	}
	if strings.Contains(got.body, workerPool.Label) {
		t.Errorf("clean worker pool must not appear in the finding: %q", got.body)
	}
}

// TestRunProbeScansEveryPoolForNeedsInput proves each pool is queried (not
// only the first), by stuck sessions with the SAME external id in two
// pools producing two distinct, pool-scoped findings.
func TestRunProbeScansEveryPoolForNeedsInput(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	row := ccpoolSessionRow{ExternalID: "same-id", State: "needs_input", Live: true}
	spy := &spyDeps{
		pools: []poolRef{workerPool, reviewPool},
		needsInputByPool: map[string][]ccpoolSessionRow{
			workerPool.Label: {row},
			reviewPool.Label: {row},
		},
	}
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 2 {
		t.Fatalf("expected 2 findings (one per pool), got %d", len(spy.created))
	}
	if spy.created[0].metadata[metaFingerprint] == spy.created[1].metadata[metaFingerprint] {
		t.Errorf("same external id in two pools must not collapse to one fingerprint")
	}
}

func TestRunProbeNeverPromptedNeedsTwoConsecutiveRuns(t *testing.T) {
	opts := baseOpts(t)
	ready := ccpoolSessionRow{ExternalID: "sess-w1", Name: "w", State: "ready", Live: true}
	mk := func() *spyDeps {
		return &spyDeps{
			pools:     []poolRef{{Label: ambientPoolLabel}, workerPool},
			allByPool: map[string][]ccpoolSessionRow{workerPool.Label: {ready}},
		}
	}

	cmd1, _ := testCmd()
	spy1 := mk()
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(spy1.created) != 0 {
		t.Fatalf("first sighting of a ready session must not be a finding, got %d", len(spy1.created))
	}

	cmd2, _ := testCmd()
	spy2 := mk()
	if err := runProbe(cmd2, opts, spy2.toRunDeps(time.Now())); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(spy2.created) != 1 {
		t.Fatalf("second consecutive sighting must be a finding, got %d", len(spy2.created))
	}
	got := spy2.created[0]
	if got.metadata[metaFingerprint] != "never-prompted:"+workerPool.Label+":sess-w1" {
		t.Errorf("fingerprint = %q", got.metadata[metaFingerprint])
	}
	if !strings.Contains(got.body, "pool="+workerPool.Label) {
		t.Errorf("finding must name the pool: %q", got.body)
	}
}

// TestRunProbeFailedPoolKeepsReadyClockAndBaseline: one pool's listing
// failing makes the run partial (exit 4), must not reset that pool's
// never-prompted clock, and must not overwrite the zombie baseline with a
// partial sum.
func TestRunProbeFailedPoolKeepsReadyClockAndBaseline(t *testing.T) {
	opts := baseOpts(t)
	key := readyKey(workerPool, "sess-w1")
	if err := saveSnapshot(opts.snapshotPath, snapshot{ZombieCount: 5, ReadySeen: []string{key}}); err != nil {
		t.Fatal(err)
	}
	cmd, _ := testCmd()
	// The surviving pool alone holds 10 working-and-dead rows: summed as if
	// it were the whole fleet (5 -> 10, +100%) it would raise a drift
	// finding, so a finding or a clobbered baseline both mean a partial
	// sum leaked through. The failed pool must turn drift OFF for every
	// pool, not just itself.
	var ambientRows []ccpoolSessionRow
	for i := 0; i < 10; i++ {
		ambientRows = append(ambientRows, ccpoolSessionRow{ExternalID: fmt.Sprintf("z%d", i), State: "working", Live: false})
	}
	spy := &spyDeps{
		pools:        []poolRef{{Label: ambientPoolLabel}, workerPool},
		allByPool:    map[string][]ccpoolSessionRow{ambientPoolLabel: ambientRows},
		allErrByPool: map[string]error{workerPool.Label: errCcpoolFailed},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial), got %v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("drift must be skipped when any pool's list failed, got findings: %+v", spy.created)
	}
	snap, ok := loadSnapshot(opts.snapshotPath)
	if !ok {
		t.Fatal("snapshot not written")
	}
	if snap.ZombieCount != 5 {
		t.Errorf("zombie baseline clobbered by a partial sum: %d", snap.ZombieCount)
	}
	if len(snap.ReadySeen) != 1 || snap.ReadySeen[0] != key {
		t.Errorf("failed pool's ready keys must carry forward, got %v", snap.ReadySeen)
	}
}

// TestRunProbeDedupSeesHumanLabeledBead reproduces pg2-p48mo (the ccpool
// twin of pg2-dvkbh, where pg-router-probe filed pg2-imr6o as a duplicate
// of pg2-68005). The first bead for the fingerprint had gained the
// "human" label (and so was in neither the ready queue nor
// "escalated-work"); the next probe tick listed only ready beads, saw no
// match, and filed a second bead. The fake below models the two named
// queries faithfully: the ready-only triager query hides the parked bead,
// the dedup query shows it. No new bead MUST be created; the new state
// is APPENDED to the existing bead (here the session's state moved, so
// the probe updates its tracked metadata and comments).
func TestRunProbeDedupSeesHumanLabeledBead(t *testing.T) {
	parked := connectorIssue{
		ID:     "zr-parked",
		Labels: []string{"escalated", "human"},
		Metadata: map[string]string{
			metaFingerprint: "needs-input:sess-1",
			metaState:       "stale-state",
		},
	}
	newSpy := func() *spyDeps {
		return &spyDeps{
			needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input", Live: true}},
			listEscalatedByQuery: map[string][]connectorIssue{
				"escalated-work":  nil, // ready --label escalated --exclude-label human
				defaultDedupQuery: {parked},
			},
		}
	}

	// Sanity: with the pre-fix (ready-only) query the probe cannot see the
	// parked bead and files a duplicate -- this is the bug.
	buggyCmd, _ := testCmd()
	buggyOpts := baseOpts(t)
	buggyOpts.dedupQuery = "escalated-work"
	buggy := newSpy()
	if err := runProbe(buggyCmd, buggyOpts, buggy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(buggy.created) != 1 {
		t.Fatalf("test harness premise broken: ready-only query should reproduce the duplicate, created=%d", len(buggy.created))
	}

	// The fix: the default dedup query sees the human-labeled bead.
	cmd, _ := testCmd()
	opts := baseOpts(t)
	spy := newSpy()
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("a duplicate bead was created while a human-labeled bead held the fingerprint: %+v", spy.created)
	}
	if len(spy.updated) != 1 || spy.updated[0].id != "zr-parked" {
		t.Fatalf("expected the new state to update zr-parked, got %+v", spy.updated)
	}
	if len(spy.commented) != 1 || spy.commented[0].id != "zr-parked" {
		t.Fatalf("expected the new state to be commented on zr-parked, got %+v", spy.commented)
	}
	if len(spy.listedQueries) != 1 || spy.listedQueries[0] != defaultDedupQuery {
		t.Fatalf("expected dedup to list through %q, got %v", defaultDedupQuery, spy.listedQueries)
	}
}

// TestRunCmdDedupQueryDefault pins the CLI default so a deployment that
// omits --dedup-query still dedups against every non-closed bead.
func TestRunCmdDedupQueryDefault(t *testing.T) {
	f := newRunCmd().Flags().Lookup("dedup-query")
	if f == nil {
		t.Fatalf("--dedup-query flag missing")
	}
	if f.DefValue != defaultDedupQuery || defaultDedupQuery == "escalated-work" {
		t.Fatalf("dedup-query default = %q (want %q, never the ready-only escalated-work)", f.DefValue, defaultDedupQuery)
	}
}

// TestRunProbeDeadRowsAreNotZombiesAndNotStuck: errored rows and dead
// ready rows piling up must not trip zombie drift, and a dead needs_input
// row must not be filed (pg2-d845f).
func TestRunProbeDeadRowsAreNotZombiesAndNotStuck(t *testing.T) {
	opts := baseOpts(t)

	cmd1, _ := testCmd()
	spy1 := &spyDeps{allPoolRows: []ccpoolSessionRow{{ExternalID: "w", State: "working", Live: false}}}
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("first run: %v", err)
	}

	cmd2, _ := testCmd()
	spy2 := &spyDeps{
		allPoolRows: []ccpoolSessionRow{
			{ExternalID: "w", State: "working", Live: false},
			{ExternalID: "e1", State: "errored", Live: false},
			{ExternalID: "e2", State: "errored", Live: true},
			{ExternalID: "r1", State: "ready", Live: false},
			{ExternalID: "s1", State: "starting", Live: false},
			{ExternalID: "lw", State: "working", Live: true},
		},
		needsInputRows: []ccpoolSessionRow{{ExternalID: "dead-ni", State: "needs_input", Live: false}},
	}
	if err := runProbe(cmd2, opts, spy2.toRunDeps(time.Now())); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(spy2.created) != 0 {
		t.Fatalf("errored/dead-ready growth and a dead needs_input row must file nothing, got %+v", spy2.created)
	}
	snap, ok := loadSnapshot(opts.snapshotPath)
	if !ok || snap.ZombieCount != 1 {
		t.Fatalf("zombie_count = %d ok=%v, want 1 (only the working+dead row)", snap.ZombieCount, ok)
	}
}

// TestRunProbeSnapshotSaveFailureIsPartialButStillFiles forces the write
// error portably by making the snapshot's parent a regular file. The run
// must still file its findings (bodies carry the degraded note), print the
// persist failure on stderr, and exit 4 -- the handler discards stderr on
// exit 0, so a swallowed warning is invisible (pg2-d845f).
func TestRunProbeSnapshotSaveFailureIsPartialButStillFiles(t *testing.T) {
	cmd, stderr := testCmd()
	opts := baseOpts(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.snapshotPath = filepath.Join(blocker, "snapshot.json")
	spy := &spyDeps{
		needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", Name: "worker", State: "needs_input", Live: true}},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial) on snapshot save failure, got err=%v", err)
	}
	wantStderr := "ccpool-probe: failed to persist snapshot " + opts.snapshotPath + ": "
	if !strings.Contains(stderr.String(), wantStderr) {
		t.Fatalf("stderr missing %q:\n%s", wantStderr, stderr.String())
	}
	if err == nil || !strings.Contains(err.Error(), "snapshot: persist "+opts.snapshotPath+": ") {
		t.Fatalf("exit error must name the degraded snapshot persist, got %v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("findings must still be filed on a save failure, got %d", len(spy.created))
	}
	if !strings.Contains(spy.created[0].body, "snapshot: persist "+opts.snapshotPath) {
		t.Fatalf("filed body must carry the degraded note:\n%s", spy.created[0].body)
	}
}
