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
// live Grafana server, a real pg-connector binary, or real snapshot I/O.
type spyDeps struct {
	fetchAlertsFn func(ctx context.Context, opts runOptions) ([]grafanaAlert, error)

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
		fetchAlerts: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			if s.fetchAlertsFn == nil {
				return nil, nil
			}
			return s.fetchAlertsFn(ctx, opts)
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
		grafanaTimeout:     time.Second,
		pgConnectorTimeout: time.Second,
		ruleUIDs:           registeredRuleUIDs,
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
	// Nothing configured at all: grafana-url unset, queue-depth/backlog
	// not Changed, binary-path unset.
	spy := &spyDeps{}
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

func TestRunProbeCleanWhenGrafanaConfiguredButNothingFiring(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) { return nil, nil },
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("expected no bd issue created when nothing is firing")
	}
	if _, statErr := os.Stat(opts.snapshotPath); statErr != nil {
		t.Fatalf("expected a snapshot to be written on a successful run: %v", statErr)
	}
}

func TestRunProbeGrafanaFindingCreatesIssue(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{{RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active"}}, nil
		},
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
	if got.metadata[metaFingerprint] != "pg-router-liveness-down|instance=a" {
		t.Fatalf("got metadata %v", got.metadata)
	}
}

func TestRunProbeDedupSkipsWhenNothingNew(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{{RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active"}}, nil
		},
		listEscalatedResult: []connectorIssue{{
			ID: "zr-1",
			Metadata: map[string]string{
				metaFingerprint:  "pg-router-liveness-down|instance=a",
				metaState:        "active",
				metaEpisodeCount: "0",
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

func TestRunProbeDedupUpdatesOnChange(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{{RuleUID: "pg-router-liveness-down", Labels: map[string]string{"instance": "a"}, State: "active", EpisodeCount: 5}}, nil
		},
		listEscalatedResult: []connectorIssue{{
			ID: "zr-1",
			Metadata: map[string]string{
				metaFingerprint:  "pg-router-liveness-down|instance=a",
				metaState:        "active",
				metaEpisodeCount: "1",
			},
		}},
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.updated) != 1 || spy.updated[0].id != "zr-1" {
		t.Fatalf("expected an update on zr-1, got %+v", spy.updated)
	}
	if len(spy.commented) != 1 || spy.commented[0].id != "zr-1" {
		t.Fatalf("expected a comment on zr-1, got %+v", spy.commented)
	}
	if len(spy.created) != 0 {
		t.Fatalf("expected no create when a match already exists")
	}
}

func TestRunProbePartialWhenOneSubcheckFails(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.grafanaURL = "http://example.invalid"
	opts.haveQueueDepth = true
	opts.queueDepth = 60
	opts.haveBacklog = true
	opts.backlog = 60
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return nil, errUnreachable
		},
	}
	// Seed a prior snapshot so the queue-growth sub-check has a baseline
	// to diff against and actually produces a finding.
	if err := saveSnapshot(opts.snapshotPath, snapshot{QueueDepth: 1, Backlog: 1}); err != nil {
		t.Fatal(err)
	}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial), got err=%v", err)
	}
	// binary-hash was never configured either, but the run still
	// proceeded on the queue-growth sub-check that DID run, and filed a
	// finding for it.
	if len(spy.created) != 2 { // queue-depth AND backlog both grew past the none band
		t.Fatalf("expected 2 created issues (queue-depth, backlog), got %d: %+v", len(spy.created), spy.created)
	}
	for _, c := range spy.created {
		if len(c.body) == 0 {
			t.Fatalf("expected a non-empty body")
		}
	}
}

var errUnreachable = &testError{"grafana unreachable"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
