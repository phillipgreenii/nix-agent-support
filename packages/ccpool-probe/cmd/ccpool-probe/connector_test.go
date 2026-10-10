package main

import (
	"bytes"
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
	issues, err := listEscalated(context.Background(), defaultDedupQuery, noopWarn)
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
// pg-router-probe's own connector.go for the same reason: list is a
// fan-out op.
func TestListEscalatedDegradedStillOK(t *testing.T) {
	withFactory(t, "list_degraded_empty")
	issues, err := listEscalated(context.Background(), defaultDedupQuery, noopWarn)
	if err != nil {
		t.Fatalf("listEscalated: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d issues", len(issues))
	}
}

func TestListEscalatedTotalFailure(t *testing.T) {
	withFactory(t, "list_total_failure")
	_, err := listEscalated(context.Background(), defaultDedupQuery, noopWarn)
	if err == nil {
		t.Fatalf("expected an error for exit 3")
	}
}

// TestListEscalatedHonorsExplicitTimeout proves this probe's own
// pg-connector subprocess calls respect an explicit context deadline
// rather than hanging on a wedged pg-connector process -- the
// "pg-connector subprocess" half of "every external call in run MUST
// carry an explicit timeout" (ccpoolexec_test.go's
// TestListCcpoolSessionsHonorsExplicitTimeout is the ccpool half). run.go
// itself is what derives the short-lived ctx in production (via its own
// --pg-connector-timeout flag); this test builds one directly against
// connector.go's own listEscalated to keep the test fast and scoped to
// this file's own layer.
func TestListEscalatedHonorsExplicitTimeout(t *testing.T) {
	withFactory(t, "slow")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := listEscalated(ctx, defaultDedupQuery, noopWarn)
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
		_, _ = listEscalated(context.Background(), defaultDedupQuery, noopWarn)
	})
	if !strings.Contains(got, "--backend") || !strings.Contains(got, pgConnectorBackend) {
		t.Fatalf("issue list: expected --backend %s in argv, got %s", pgConnectorBackend, got)
	}
	if !strings.Contains(got, defaultDedupQuery) {
		t.Fatalf("issue list: expected the %s dedup query in argv, got %s", defaultDedupQuery, got)
	}
	// pg2-p48mo: the ready-only triager dispatch query hides human-labeled,
	// claimed, and deferred beads; dedup MUST NOT use it.
	if strings.Contains(got, "escalated-work") {
		t.Fatalf("issue list: dedup must not use the ready-only escalated-work query, got %s", got)
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

// TestConnectorBackendDefaultsToUnsuffixedBeadsBackend pins the default so a
// deployment that omits --connector-backend keeps pg-connector-issue-beads
// (pg2-otfq2).
func TestConnectorBackendDefaultsToUnsuffixedBeadsBackend(t *testing.T) {
	if defaultPgConnectorBackend != "pg-connector-issue-beads" {
		t.Fatalf("defaultPgConnectorBackend = %q", defaultPgConnectorBackend)
	}
	if pgConnectorBackend != defaultPgConnectorBackend {
		t.Fatalf("pgConnectorBackend = %q, want the default %q", pgConnectorBackend, defaultPgConnectorBackend)
	}
	f := newRunCmd().Flags().Lookup("connector-backend")
	if f == nil {
		t.Fatalf("--connector-backend flag missing")
	}
	if f.DefValue != defaultPgConnectorBackend {
		t.Fatalf("--connector-backend default = %q, want %q", f.DefValue, defaultPgConnectorBackend)
	}
}

// TestEveryPgConnectorCallUsesTheOverriddenBackend proves each call kind
// passes the overridden instance name and not the default (pg2-otfq2).
func TestEveryPgConnectorCallUsesTheOverriddenBackend(t *testing.T) {
	const override = "pg-connector-issue-beads-zr"
	old := pgConnectorBackend
	pgConnectorBackend = override
	t.Cleanup(func() { pgConnectorBackend = old })

	check := func(name, got string) {
		t.Helper()
		if !strings.Contains(got, "--backend "+override) {
			t.Fatalf("%s: expected --backend %s in argv, got %s", name, override, got)
		}
		if strings.Contains(got, "--backend pg-connector-issue-beads ") || strings.HasSuffix(got, "--backend pg-connector-issue-beads") {
			t.Fatalf("%s: default backend leaked into argv: %s", name, got)
		}
	}

	withFactory(t, "list_ok_with_match")
	check("list", recordedArgs(t, func() { _, _ = listEscalated(context.Background(), defaultDedupQuery, noopWarn) }))
	withFactory(t, "create_ok")
	check("create", recordedArgs(t, func() {
		_, _ = createIssue(context.Background(), "t", []string{"escalated"}, map[string]string{"k": "v"}, "body", noopWarn)
	}))
	withFactory(t, "update_ok")
	check("update", recordedArgs(t, func() {
		_ = updateIssueMetadata(context.Background(), "zr-1", map[string]string{"k": "v"}, noopWarn)
	}))
	withFactory(t, "comment_ok")
	check("comment", recordedArgs(t, func() { _ = commentIssue(context.Background(), "zr-1", "note", noopWarn) }))
}

// TestRunCmdConnectorBackendFlagSetsBackend drives the flag end to end
// through cobra: the value reaches pgConnectorBackend (pg2-otfq2).
func TestRunCmdConnectorBackendFlagSetsBackend(t *testing.T) {
	old := pgConnectorBackend
	t.Cleanup(func() { pgConnectorBackend = old })
	t.Setenv("PATH", "")
	var out, errOut bytes.Buffer
	_ = run([]string{"run", "--connector-backend", "pg-connector-issue-beads-pg2", "--snapshot-path", t.TempDir() + "/snapshot.json"}, &out, &errOut)
	if pgConnectorBackend != "pg-connector-issue-beads-pg2" {
		t.Fatalf("pgConnectorBackend = %q after --connector-backend, stderr=%q", pgConnectorBackend, errOut.String())
	}
}

func TestRunCmdConnectorBackendEmptyIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"run", "--connector-backend", ""}, &out, &errOut); code != 2 {
		t.Fatalf("expected exit 2, got %d (stderr=%q)", code, errOut.String())
	}
}
