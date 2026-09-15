package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// metadataStrings reads a []any-typed JSON array field back out of a
// decoded rawItem's Metadata map as []string, for assertions.
func metadataStrings(t *testing.T, item rawItem, key string) []string {
	t.Helper()
	raw, ok := item.Metadata[key]
	if !ok {
		t.Fatalf("metadata[%q] missing (metadata=%v)", key, item.Metadata)
	}
	vals, ok := raw.([]any)
	if !ok {
		t.Fatalf("metadata[%q] = %T, want []any", key, raw)
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("metadata[%q] element = %T, want string", key, v)
		}
		out = append(out, s)
	}
	return out
}

func TestChanges_DegradedOutcome_ExitsZeroWithDegradedSourcesNamed(t *testing.T) {
	withFactory(t, "changes_degraded")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (items=%+v)", len(items), items)
	}

	// Item 0: non-empty title kept as-is; degraded_sources names only
	// the degraded backend (b2), never the succeeded (b1) or disabled
	// (b3) ones.
	got := items[0]
	if got.ID != "e1" || got.Type != "issue" || got.Title != "Entity One" {
		t.Fatalf("items[0] = %+v", got)
	}
	if got.Metadata["change"] != "added" || got.Metadata["source"] != "b1" {
		t.Fatalf("items[0].metadata = %v", got.Metadata)
	}
	if ds := metadataStrings(t, got, "degraded_sources"); len(ds) != 1 || ds[0] != "b2" {
		t.Fatalf("items[0].metadata.degraded_sources = %v, want [b2]", ds)
	}

	// Item 1: empty entity title falls back to the entity's own id;
	// SAME degraded_sources list as item 0 (call-level, not per-entity).
	got = items[1]
	if got.ID != "e2" || got.Title != "e2" {
		t.Fatalf("items[1] = %+v, want title fallback to id", got)
	}
	if ds := metadataStrings(t, got, "degraded_sources"); len(ds) != 1 || ds[0] != "b2" {
		t.Fatalf("items[1].metadata.degraded_sources = %v, want [b2]", ds)
	}
}

func TestChanges_TotalFailure_ExitsNonZeroPrintingNothingOnStdout(t *testing.T) {
	withFactory(t, "total_failure")
	stdout, stderr, code := runCLI(t, "changes", "issue", "feedback-ready", "--consumer", "c1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty (this adapter MUST NOT propagate pg-connector's own exit 3 stdout body)", stdout)
	}
	if !strings.Contains(stderr, "total failure: no backend succeeded") {
		t.Fatalf("stderr = %q, want pg-connector's own stderr copied through", stderr)
	}
	// pg-connector's own exit code (3) must never surface as this
	// adapter's own exit code.
	if code == 3 {
		t.Fatalf("exit code = 3, this adapter MUST NOT propagate pg-connector's own exit code as its own")
	}
}

func TestChanges_InvalidArgument_ExitsNonZeroPrintingNothingOnStdout(t *testing.T) {
	withFactory(t, "invalid_argument")
	stdout, stderr, code := runCLI(t, "changes", "issue", "bogus", "--consumer", "c1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") {
		t.Fatalf("stderr = %q, want pg-connector's own stderr copied through", stderr)
	}
}

func TestChanges_BeadsDirSetsPgConnectorIssueBeadsDirInChildEnv(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "recorded-env")
	withFactory(
		t, "changes_ok_empty",
		"GO_HELPER_ENV_RECORD_FILE="+recordFile,
		"GO_HELPER_ENV_RECORD_VAR=PG_CONNECTOR_ISSUE_BEADS_DIR",
	)

	_, stderr, code := runCLI(t, "changes", "issue", "q", "--consumer", "c1", "--beads-dir", "/some/beads/path")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	got := readFile(t, recordFile)
	if got != "/some/beads/path" {
		t.Fatalf("child's own PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want /some/beads/path", got)
	}
}

func TestChanges_NoBeadsDirFlag_LeavesPgConnectorIssueBeadsDirUnset(t *testing.T) {
	recordFile := filepath.Join(t.TempDir(), "recorded-env")
	withFactory(
		t, "changes_ok_empty",
		"GO_HELPER_ENV_RECORD_FILE="+recordFile,
		"GO_HELPER_ENV_RECORD_VAR=PG_CONNECTOR_ISSUE_BEADS_DIR",
	)

	_, stderr, code := runCLI(t, "changes", "issue", "q", "--consumer", "c1")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	got := readFile(t, recordFile)
	if got != "<unset>" {
		t.Fatalf("child's own PG_CONNECTOR_ISSUE_BEADS_DIR = %q, want left unset entirely", got)
	}
}
