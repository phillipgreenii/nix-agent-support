package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func noopWarn(string) {}

func warnCollector() (func(string), *[]string) {
	var msgs []string
	return func(m string) { msgs = append(msgs, m) }, &msgs
}

func TestCreateIssueOK(t *testing.T) {
	withFactory(t, "create_ok")
	issue, err := createIssue(context.Background(), "title", []string{"escalated"}, map[string]string{"pg_router_escalation_fingerprint": "fp1"}, "body", noopWarn)
	if err != nil {
		t.Fatalf("createIssue: %v", err)
	}
	if issue.ID != "zr-123" {
		t.Fatalf("got id %q", issue.ID)
	}
	if issue.Metadata["pg_router_escalation_fingerprint"] != "fp1" {
		t.Fatalf("got metadata %v", issue.Metadata)
	}
}

func TestCreateIssueFail(t *testing.T) {
	withFactory(t, "create_fail")
	warn, msgs := warnCollector()
	_, err := createIssue(context.Background(), "title", nil, nil, "body", warn)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if len(*msgs) == 0 || !strings.Contains((*msgs)[0], "title required") {
		t.Fatalf("expected pg-connector's own stderr to be forwarded, got %v", *msgs)
	}
}

func TestUpdateIssueMetadataOK(t *testing.T) {
	withFactory(t, "update_ok")
	if err := updateIssueMetadata(context.Background(), "zr-123", map[string]string{"k": "v"}, noopWarn); err != nil {
		t.Fatalf("updateIssueMetadata: %v", err)
	}
}

func TestUpdateIssueMetadataFail(t *testing.T) {
	withFactory(t, "update_fail")
	if err := updateIssueMetadata(context.Background(), "zr-999", nil, noopWarn); err == nil {
		t.Fatalf("expected an error")
	}
}

func TestCommentIssueOK(t *testing.T) {
	withFactory(t, "comment_ok")
	if err := commentIssue(context.Background(), "zr-123", "note", noopWarn); err != nil {
		t.Fatalf("commentIssue: %v", err)
	}
}

func TestCommentIssueFail(t *testing.T) {
	withFactory(t, "comment_fail")
	if err := commentIssue(context.Background(), "zr-999", "note", noopWarn); err == nil {
		t.Fatalf("expected an error")
	}
}

func TestListEscalatedOK(t *testing.T) {
	withFactory(t, "list_ok_with_match")
	issues, err := listEscalated(context.Background(), noopWarn)
	if err != nil {
		t.Fatalf("listEscalated: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues", len(issues))
	}
	if issues[0].Metadata["pg_router_escalation_fingerprint"] != "fp1" {
		t.Fatalf("got %v", issues[0])
	}
}

// TestListEscalatedDegradedStillOK proves the fan-out exit code 2
// ("degraded but usable") is treated as success here, mirroring
// pg-router-source-pg-connector's own classifyExit(0,2) convention for
// the same "issue list" fan-out op.
func TestListEscalatedDegradedStillOK(t *testing.T) {
	withFactory(t, "list_degraded_empty")
	issues, err := listEscalated(context.Background(), noopWarn)
	if err != nil {
		t.Fatalf("listEscalated: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d issues", len(issues))
	}
}

func TestListEscalatedTotalFailure(t *testing.T) {
	withFactory(t, "list_total_failure")
	_, err := listEscalated(context.Background(), noopWarn)
	if err == nil {
		t.Fatalf("expected an error for exit 3")
	}
}

// TestListEscalatedHonorsExplicitTimeout proves this probe's own
// pg-connector subprocess calls respect an explicit context deadline
// rather than hanging on a wedged pg-connector process -- the
// "pg-connector subprocess" half of the "every external call in run MUST
// carry an explicit timeout" binding decision (grafana_test.go's
// TestFiringAlertsHonorsExplicitTimeout is the Grafana-HTTP half). run.go
// itself is what derives the short-lived ctx in production (via its own
// --pg-connector-timeout flag); this test builds one directly against
// connector.go's own listEscalated to keep the test fast and scoped to
// this file's own layer.
func TestListEscalatedHonorsExplicitTimeout(t *testing.T) {
	withFactory(t, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := listEscalated(ctx, noopWarn)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("listEscalated did not respect its context deadline: took %v", elapsed)
	}
}

// recordedArgs runs fn with GO_HELPER_ARGS_RECORD_FILE pointed at a fresh
// temp file and returns the exact argv the helper process observed
// (recordArgsIfRequested, testmain_test.go), so a test can assert on the
// REAL argv this probe built rather than trusting a code read of the
// hardcoded --backend constant.
func recordedArgs(t *testing.T, fn func()) string {
	t.Helper()
	path := t.TempDir() + "/args.txt"
	t.Setenv("GO_HELPER_ARGS_RECORD_FILE", path)
	fn()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	return string(data)
}

func TestEveryPgConnectorCallPinsTheBeadsBackend(t *testing.T) {
	withFactory(t, "list_ok_with_match")
	got := recordedArgs(t, func() {
		_, _ = listEscalated(context.Background(), noopWarn)
	})
	if !strings.Contains(got, "--backend") || !strings.Contains(got, pgConnectorBackend) {
		t.Fatalf("issue list: expected --backend %s in argv, got %s", pgConnectorBackend, got)
	}
	if !strings.Contains(got, "escalated-work") {
		t.Fatalf("issue list: expected the escalated-work named query in argv, got %s", got)
	}

	withFactory(t, "create_ok")
	got = recordedArgs(t, func() {
		_, _ = createIssue(context.Background(), "t", []string{"escalated"}, map[string]string{"k": "v"}, "body", noopWarn)
	})
	if !strings.Contains(got, "--backend") || !strings.Contains(got, pgConnectorBackend) {
		t.Fatalf("issue create: expected --backend %s in argv, got %s", pgConnectorBackend, got)
	}

	withFactory(t, "update_ok")
	got = recordedArgs(t, func() {
		_ = updateIssueMetadata(context.Background(), "zr-1", map[string]string{"k": "v"}, noopWarn)
	})
	if !strings.Contains(got, "--backend") || !strings.Contains(got, pgConnectorBackend) {
		t.Fatalf("issue update: expected --backend %s in argv, got %s", pgConnectorBackend, got)
	}

	withFactory(t, "comment_ok")
	got = recordedArgs(t, func() {
		_ = commentIssue(context.Background(), "zr-1", "note", noopWarn)
	})
	if !strings.Contains(got, "--backend") || !strings.Contains(got, pgConnectorBackend) {
		t.Fatalf("issue comment: expected --backend %s in argv, got %s", pgConnectorBackend, got)
	}
}

func TestMetadataArgsRepeatsFlag(t *testing.T) {
	args := metadataArgs(map[string]string{"a": "1"})
	if len(args) != 2 || args[0] != "--metadata" || args[1] != "a=1" {
		t.Fatalf("got %v", args)
	}
}
