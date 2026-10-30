package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tests for "issue list --param" (bead pg2-eclw3j.3): the capability check, the
// wire field, the cache behavior.

const (
	qpCapsDeclared = `{"protocolVersion":1,"schemaVersions":{"issue":1},"ops":["capabilities","list"],"vocabulary":{"query_params":true}}`
	qpCapsOld      = `{"protocolVersion":1,"schemaVersions":{"issue":1},"ops":["capabilities","list"],"vocabulary":{"state":["open"]}}`
	qpEmptyList    = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`
)

func TestParseParamFlags(t *testing.T) {
	got, err := parseParamFlags([]string{"repo=a/b", "pr_number=7", "empty=", "eq=a=b"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]string{"repo": "a/b", "pr_number": "7", "empty": "", "eq": "a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, err := parseParamFlags(nil); got != nil || err != nil {
		t.Fatalf("no flags: %v, %v", got, err)
	}
	for _, bad := range [][]string{{"novalue"}, {"=v"}, {"a=1", "a=2"}} {
		if _, err := parseParamFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestIssueList_Param_DeclaredBackendReceivesParamsAfterCapabilities(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	writeRecordingBackend(t, "backend-qp-ok", logPath, map[string]string{"capabilities": qpCapsDeclared, "list": qpEmptyList})
	writeIssueConfigFor(t, "backend-qp-ok")

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "work-items-for-pr", "--param", "repo=ZR-Private/ziprecruiter", "--param", "pr_number=113446"})
	if code != 0 {
		t.Fatalf("exit %d; stdout=%s", code, stdout)
	}
	reqs := readRequestLog(t, logPath)
	if ops := opsOf(reqs); !reflect.DeepEqual(ops, []string{"capabilities", "list"}) {
		t.Fatalf("ops = %v, want capabilities before list", ops)
	}
	params, _ := requestsFor(reqs, "list")[0].Args["params"].(map[string]any)
	if !reflect.DeepEqual(params, map[string]any{"repo": "ZR-Private/ziprecruiter", "pr_number": "113446"}) {
		t.Fatalf("list args = %v", requestsFor(reqs, "list")[0].Args)
	}
	var outcome issueListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil || len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceSucceeded {
		t.Fatalf("outcome = %s (%v); want the usual sources shape with a succeeded source", stdout, err)
	}
}

// TestIssueList_Param_BackendWithoutTheCapabilityIsNeverSentParams: an old
// backend decodes list args leniently and would run the query WITHOUT the
// params, so the umbrella must refuse and send no list at all.
func TestIssueList_Param_BackendWithoutTheCapabilityIsNeverSentParams(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	writeRecordingBackend(t, "backend-qp-old", logPath, map[string]string{"capabilities": qpCapsOld, "list": qpEmptyList})
	writeIssueConfigFor(t, "backend-qp-old")

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "q", "--param", "repo=a/b"})
	if code == 0 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, "query_params") {
		t.Fatalf("exit %d, stdout=%s; want an invalid_argument refusal naming query_params", code, stdout)
	}
	if ops := opsOf(readRequestLog(t, logPath)); !reflect.DeepEqual(ops, []string{"capabilities"}) {
		t.Fatalf("ops = %v, want only capabilities (no list request)", ops)
	}
}

func TestIssueList_Param_CapabilitiesFailureFailsClosed(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	writeRecordingBackend(t, "backend-qp-nocaps", logPath, map[string]string{"list": qpEmptyList})
	writeIssueConfigFor(t, "backend-qp-nocaps")

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "q", "--param", "repo=a/b"})
	if code == 0 {
		t.Fatalf("exit 0; stdout=%s", stdout)
	}
	if len(requestsFor(readRequestLog(t, logPath), "list")) != 0 {
		t.Fatal("a list request was sent although capabilities could not be read")
	}
}

func TestIssueList_Param_MalformedFlagIsInvalidArgumentBeforeAnyCall(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	writeRecordingBackend(t, "backend-qp-bad", logPath, map[string]string{"capabilities": qpCapsDeclared, "list": qpEmptyList})
	writeIssueConfigFor(t, "backend-qp-bad")

	stdout, _, code := executePr(t, []string{"issue", "list", "--query", "q", "--param", "noequals"})
	if code == 0 || !strings.Contains(stdout, "invalid_argument") {
		t.Fatalf("exit %d, stdout=%s", code, stdout)
	}
	if reqs := readRequestLog(t, logPath); len(reqs) != 0 {
		t.Fatalf("backend was called: %v", opsOf(reqs))
	}
}

// TestIssueList_NoParam_NeverAsksCapabilitiesNorSendsParams: a call without
// --param is unchanged on the wire, so it works against any backend.
func TestIssueList_NoParam_NeverAsksCapabilitiesNorSendsParams(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	writeRecordingBackend(t, "backend-qp-none", logPath, map[string]string{"capabilities": qpCapsOld, "list": qpEmptyList})
	writeIssueConfigFor(t, "backend-qp-none")

	if stdout, _, code := executePr(t, []string{"issue", "list", "--query", "mine"}); code != 0 {
		t.Fatalf("exit %d; stdout=%s", code, stdout)
	}
	reqs := readRequestLog(t, logPath)
	if ops := opsOf(reqs); !reflect.DeepEqual(ops, []string{"list"}) {
		t.Fatalf("ops = %v, want only list", ops)
	}
	if _, has := reqs[0].Args["params"]; has {
		t.Fatalf("list args carry params: %v", reqs[0].Args)
	}
}

// TestIssueList_Param_WritesOnlyTheReturnedEntitiesToTheSharedCache states the
// cache cost of a per-PR call: the shared entity cache gets one entry per
// RETURNED entity (about 22 for a PR), not one per entity the tracker holds
// (the 3,740 of the full listing).
func TestIssueList_Param_WritesOnlyTheReturnedEntitiesToTheSharedCache(t *testing.T) {
	const backend = "backend-qp-cache"
	const n = 22
	asOf := time.Now().UTC().Format(time.RFC3339)
	var entities, ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("zr-%02d", i)
		ids = append(ids, fmt.Sprintf("%q", id))
		entities = append(entities, fmt.Sprintf(`{"id":%q,"title":"t","state":"open","as_of":%q,"stale":false}`, id, asOf))
	}
	list := fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[%s],"present_ids":[%s],"cursor":null,"truncated":false}}`,
		strings.Join(entities, ","), strings.Join(ids, ","))
	writeRecordingBackend(t, backend, filepath.Join(t.TempDir(), "log"), map[string]string{"capabilities": qpCapsDeclared, "list": list})
	writeIssueConfigFor(t, backend)

	before := cacheSaves.Load()
	if stdout, _, code := executePr(t, []string{"issue", "list", "--query", "work-items-for-pr", "--param", "repo=a/b", "--param", "pr_number=1"}); code != 0 {
		t.Fatalf("exit %d; stdout=%s", code, stdout)
	}
	if got := cacheSaves.Load() - before; got != 1 {
		t.Fatalf("cache saves = %d, want 1", got)
	}
	c, err := loadCache(CacheKey{Type: "issue", Backend: backend})
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if len(c.Entries) != n {
		t.Fatalf("cache holds %d entries, want exactly the %d returned entities", len(c.Entries), n)
	}
}

// TestIssueList_Param_UnavailableNeverServesTheCacheFallback: a parameterized
// query has no ledger membership, and cached entities of the backend answer a
// different question than the one asked, so an unavailable backend stays a
// degraded source with nothing served from the cache.
func TestIssueList_Param_UnavailableNeverServesTheCacheFallback(t *testing.T) {
	const backend = "backend-qp-down"
	unavailable := `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"bd down"}}`
	writeRecordingBackend(t, backend, filepath.Join(t.TempDir(), "log"), map[string]string{"capabilities": qpCapsDeclared, "list": unavailable})
	writeIssueConfigFor(t, backend)

	stdout, _, _ := executePr(t, []string{"issue", "list", "--query", "q", "--param", "repo=a/b"})
	var outcome issueListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("decode: %v (%s)", err, stdout)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].Status != SourceDegraded || len(outcome.Entities) != 0 {
		t.Fatalf("outcome = %+v, want one degraded source and no entities", outcome)
	}
}

// TestIssueList_Help_DocumentsParamAndCaller pins the help text the operator
// reads (bead pg2-eclw3j.4): --param, the capability gate, the same-apply
// coupling and the env-var-only caller.
func TestIssueList_Help_DocumentsParamAndCaller(t *testing.T) {
	stdout, _, code := executePr(t, []string{"issue", "list", "--help"})
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	for _, want := range []string{"--param", "vocabulary.query_params", "same apply", "PG_CONNECTOR_CALLER", "MUST NOT be watched"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("issue list --help lacks %q:\n%s", want, stdout)
		}
	}
}
