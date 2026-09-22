package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	needsInputRows []ccpoolSessionRow
	needsInputErr  error

	allPoolRows []ccpoolSessionRow
	allPoolErr  error

	listEscalatedResult []connectorIssue
	listEscalatedErr    error

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
		listNeedsInput: func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error) {
			return s.needsInputRows, s.needsInputErr
		},
		listAllPoolSessions: func(ctx context.Context, warn func(string)) ([]ccpoolSessionRow, error) {
			return s.allPoolRows, s.allPoolErr
		},
		listEscalated: func(ctx context.Context, warn func(string)) ([]connectorIssue, error) {
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

func baseOpts(t *testing.T) runOptions {
	return runOptions{
		ccpoolTimeout:      time.Second,
		pgConnectorTimeout: time.Second,
		snapshotPath:       filepath.Join(t.TempDir(), "snapshot.json"),
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
		needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", Name: "worker", State: "needs_input"}},
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
		needsInputRows: []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input"}},
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
// 100% growth and creates a finding.
func TestRunProbeZombieDriftEstablishesBaselineThenAlertsOnGrowth(t *testing.T) {
	opts := baseOpts(t)

	cmd1, _ := testCmd()
	spy1 := &spyDeps{
		allPoolRows: []ccpoolSessionRow{{ExternalID: "sess-1", State: "working"}},
	}
	if err := runProbe(cmd1, opts, spy1.toRunDeps(time.Now())); err != nil {
		t.Fatalf("first run: expected exit 0, got %v", err)
	}
	if len(spy1.created) != 0 {
		t.Fatalf("first run: expected no finding with no prior baseline, got %d", len(spy1.created))
	}

	cmd2, _ := testCmd()
	spy2 := &spyDeps{
		allPoolRows: []ccpoolSessionRow{
			{ExternalID: "sess-1", State: "working"},
			{ExternalID: "sess-2", State: "errored"},
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
		allPoolRows:   []ccpoolSessionRow{{ExternalID: "s1", State: "working"}, {ExternalID: "s2", State: "errored"}},
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
		needsInputRows:   []ccpoolSessionRow{{ExternalID: "sess-1", State: "needs_input"}},
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
