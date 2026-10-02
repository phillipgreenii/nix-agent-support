package main

import (
	"bytes"
	"context"
	"errors"
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
// live Grafana server, a real pg-connector binary, or real snapshot I/O.
type spyDeps struct {
	backlogResult int
	backlogErr    error
	fetchAlertsFn func(ctx context.Context, opts runOptions) ([]grafanaAlert, error)

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
		fetchAlerts: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			if s.fetchAlertsFn == nil {
				return nil, nil
			}
			return s.fetchAlertsFn(ctx, opts)
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
		fetchBacklog: func(ctx context.Context, opts runOptions) (int, error) {
			return s.backlogResult, s.backlogErr
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
		dedupQuery:         defaultDedupQuery,
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

func TestRunProbeBacklogAloneConfiguresDriftCheck(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.haveBacklog = true
	opts.backlog = 60 // only --backlog; --queue-depth deliberately unset
	if err := saveSnapshot(opts.snapshotPath, snapshot{Backlog: 1, QueueDepth: 5}); err != nil {
		t.Fatal(err)
	}
	spy := &spyDeps{}
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 1 || !strings.Contains(spy.created[0].title, "backlog") {
		t.Fatalf("expected exactly one backlog finding, got %+v", spy.created)
	}
	snap, _ := loadSnapshot(opts.snapshotPath)
	if snap.QueueDepth != 5 || snap.Backlog != 60 {
		t.Fatalf("queue-depth must be preserved and backlog updated, got %+v", snap)
	}
}

func TestRunProbeBacklogFromStatus(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.backlogFromStatus = true
	if err := saveSnapshot(opts.snapshotPath, snapshot{Backlog: 1}); err != nil {
		t.Fatal(err)
	}
	spy := &spyDeps{backlogResult: 80}
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("expected one backlog finding, got %+v", spy.created)
	}
	snap, _ := loadSnapshot(opts.snapshotPath)
	if snap.Backlog != 80 {
		t.Fatalf("expected snapshot backlog 80, got %d", snap.Backlog)
	}
}

func TestRunProbeBacklogFromStatusFailureIsDegraded(t *testing.T) {
	cmd, _ := testCmd()
	opts := baseOpts(t)
	opts.backlogFromStatus = true
	opts.binaryPath = os.Args[0] // a readable file so one sub-check succeeds
	spy := &spyDeps{backlogErr: errUnreachable}
	err := runProbe(cmd, opts, spy.toRunDeps(time.Now()))
	if exitCodeOf(t, err) != 4 {
		t.Fatalf("expected exit 4 (partial), got %v", err)
	}
}

func TestParseBacklog(t *testing.T) {
	got, err := parseBacklog([]byte(`{"queues":[{"type":"a","depth":3},{"type":"b","depth":4}]}`))
	if err != nil || got != 7 {
		t.Fatalf("got %d, %v; want 7", got, err)
	}
	if got, err := parseBacklog([]byte(`{"queues":[]}`)); err != nil || got != 0 {
		t.Fatalf("empty queues: got %d, %v", got, err)
	}
	if _, err := parseBacklog([]byte(`{}`)); err == nil {
		t.Fatalf("missing queues must error")
	}
	if _, err := parseBacklog([]byte(`not json`)); err == nil {
		t.Fatalf("bad json must error")
	}
}

// pg2-p93c0 / pg2-o6z19: the probe watches the rule that replaced the deleted
// aggregate backlog rule, so a stalled queue still reaches the escalation flow.
func TestRegisteredRuleUIDsTrackAlertRules(t *testing.T) {
	want := []string{
		"pg-router-liveness-down",
		"pg-router-queue-stalled",
		"pg-router-queue-depth-growing",
		"pg-router-failure-rate",
		"pg-router-budget-stops",
	}
	if strings.Join(registeredRuleUIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("registeredRuleUIDs = %v, want %v", registeredRuleUIDs, want)
	}
}

// TestRunProbeDedupSeesHumanLabeledBead reproduces pg2-dvkbh (2026-09-29:
// pg2-imr6o duplicated pg2-68005). The first bead for the fingerprint had
// gained the "human" label (and so was in neither the ready queue nor
// "escalated-work"); the next probe tick listed only ready beads, saw no
// match, and filed a second bead. The fake below models the two named
// queries faithfully: the ready-only triager query hides the parked bead,
// the dedup query shows it. No new bead MUST be created; the episode is
// APPENDED to the existing bead (here the alert's episode count moved, so
// the probe updates its tracked metadata and comments).
func TestRunProbeDedupSeesHumanLabeledBead(t *testing.T) {
	const fp = "pg-router-failure-rate|__alert_rule_uid__=pg-router-failure-rate,class=handler-error"
	parked := connectorIssue{
		ID:     "pg2-68005",
		Labels: []string{"escalated", "human"},
		Metadata: map[string]string{
			metaFingerprint:  fp,
			metaState:        "active",
			metaEpisodeCount: "0",
		},
	}
	fetch := func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
		return []grafanaAlert{{
			RuleUID:      "pg-router-failure-rate",
			Labels:       map[string]string{"__alert_rule_uid__": "pg-router-failure-rate", "class": "handler-error"},
			State:        "active",
			EpisodeCount: 1,
		}}, nil
	}
	newSpy := func() *spyDeps {
		return &spyDeps{
			fetchAlertsFn: fetch,
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
	buggyOpts.grafanaURL = "http://example.invalid"
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
	opts.grafanaURL = "http://example.invalid"
	spy := newSpy()
	if err := runProbe(cmd, opts, spy.toRunDeps(time.Now())); err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
	if len(spy.created) != 0 {
		t.Fatalf("a duplicate bead was created while a human-labeled bead held the fingerprint: %+v", spy.created)
	}
	if len(spy.updated) != 1 || spy.updated[0].id != "pg2-68005" {
		t.Fatalf("expected the episode to update pg2-68005, got %+v", spy.updated)
	}
	if len(spy.commented) != 1 || spy.commented[0].id != "pg2-68005" {
		t.Fatalf("expected the episode to be commented on pg2-68005, got %+v", spy.commented)
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
