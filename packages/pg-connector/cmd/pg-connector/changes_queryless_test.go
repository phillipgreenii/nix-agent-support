package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests the query-less call of the changes verb (HT-27, INV-HOIST-16):
// --query is optional, an owning backend is forwarded without a query argument
// and the umbrella passes its store identity through, and a backend that does not
// own its changes is answered as a disabled source (never by running its ledger
// path). The helpers are those of changes_forward_test.go.

const (
	qlIdentity = "0123456789abcdef0123456789abcdef"
	// qlCapsPlain is the capabilities answer of a backend that does not own its
	// changes, for any entity type.
	qlCapsPlain = `{"protocolVersion":1,"schemaVersions":{"pr":12},"ops":["capabilities","list"]}`
)

// setupTypedRegistry registers backends as the backends of entityType and
// isolates all state.
func setupTypedRegistry(t *testing.T, entityType string, backends ...string) (stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	var sb strings.Builder
	sb.WriteString("connector:\n  " + entityType + ":\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", stateDir)
	return stateDir
}

// qlAnswer is a forwarded answer that carries store_identity.
func qlAnswer(rows []string, nextSeq int64) string {
	return fmt.Sprintf(fwdEnvelope, fmt.Sprintf(`{"sources":[{"backend":"pg-connector-pr-github","status":"succeeded","version":5,"truncated":false,"next_seq":%d}],"changes":[%s],"sources_freshness":[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z"}],"store_identity":%q}`,
		nextSeq, strings.Join(rows, ","), qlIdentity))
}

func runTypedChanges(t *testing.T, logPath, entityType string, extra ...string) (stdout string, code int) {
	t.Helper()
	w := &markingWriter{logPath: logPath}
	args := append([]string{entityType, "changes", "--consumer", "c1"}, extra...)
	_, code, _ = executeChangesLogged(t, w, args)
	return w.buf.String(), code
}

type qlSource struct {
	Backend       string `json:"backend"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	NextSeq       *int64 `json:"next_seq"`
	StoreIdentity string `json:"store_identity"`
}

func decodeQlSources(t *testing.T, stdout string) []qlSource {
	t.Helper()
	var w struct {
		Sources []qlSource `json:"sources"`
	}
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return w.Sources
}

func ledgerFiles(t *testing.T, stateDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, ledgerSubdir, ledgerDirName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestQuerylessChanges_OwningBackend_ForwardsWithoutAQuery_PassesTheStoreIdentity_StampsNoLedger(t *testing.T) {
	stateDir := setupTypedRegistry(t, "pr", "backend-ql-owning")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ql-owning", logPath, map[string]string{
		"capabilities": fwdCapsOwning,
		"changes":      qlAnswer([]string{fwdRow("o/r#1", 41, "changed", `["title"]`, `[]`, 3)}, 57),
		"changes_ack":  fwdAckOK,
	})

	stdout, code := runTypedChanges(t, logPath, "pr")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	reqs := readRequestLog(t, logPath)
	changes := requestsFor(reqs, "changes")
	if len(changes) != 1 {
		t.Fatalf("changes requests = %d: %v", len(changes), opsOf(reqs))
	}
	if _, has := changes[0].Args["query"]; has {
		t.Fatalf("a query-less call forwarded a query argument: %v", changes[0].Args)
	}
	if changes[0].Args["consumer"] != "c1" || changes[0].Args["cached"] != false || changes[0].Args["reset"] != false {
		t.Fatalf("changes args = %v", changes[0].Args)
	}
	acks := requestsFor(reqs, "changes_ack")
	if len(acks) != 1 {
		t.Fatalf("changes_ack requests = %d: %v", len(acks), opsOf(reqs))
	}
	if _, has := acks[0].Args["query"]; has {
		t.Fatalf("a query-less ack carried a query argument: %v", acks[0].Args)
	}
	if acks[0].Args["consumer"] != "c1" || acks[0].Args["next_seq"] != float64(57) {
		t.Fatalf("ack args = %v", acks[0].Args)
	}
	if out, ack := indexOfOp(reqs, fwdOutputMark), indexOfOp(reqs, "changes_ack"); out < 0 || ack < out {
		t.Fatalf("changes_ack was sent before the output was written: %v", opsOf(reqs))
	}

	src := decodeQlSources(t, stdout)
	if len(src) != 1 || src[0].StoreIdentity != qlIdentity || src[0].NextSeq == nil || *src[0].NextSeq != 57 {
		t.Fatalf("sources = %+v, want the store identity passed through on the source row", src)
	}
	if got := ledgerFiles(t, stateDir); len(got) != 0 {
		t.Fatalf("a query-less call created or stamped a per-query ledger entry: %v", got)
	}
}

func TestQuerylessChanges_FailedForward_StampsNoLedger(t *testing.T) {
	stateDir := setupTypedRegistry(t, "pr", "backend-ql-down")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ql-down", logPath, map[string]string{
		"capabilities": fwdCapsOwning,
		"changes":      fwdErrorEnvelope("unavailable", "daemon socket refused"),
	})
	stdout, code := runTypedChanges(t, logPath, "pr")
	if code == 0 {
		t.Fatalf("exit code = 0 for an unavailable owning backend; stdout=%s", stdout)
	}
	src := decodeQlSources(t, stdout)
	if len(src) != 1 || src[0].Status != "degraded" || !strings.Contains(src[0].Reason, "daemon socket refused") {
		t.Fatalf("sources = %+v", src)
	}
	if got := ledgerFiles(t, stateDir); len(got) != 0 {
		t.Fatalf("the failure path stamped a ledger for a call with no query: %v", got)
	}
}

func TestChangesWithAQuery_StillSendsItAndPassesTheStoreIdentity(t *testing.T) {
	setupTypedRegistry(t, "pr", "backend-ql-withquery")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ql-withquery", logPath, map[string]string{
		"capabilities": fwdCapsOwning, "changes": qlAnswer(nil, 3), "changes_ack": fwdAckOK,
	})
	stdout, code := runTypedChanges(t, logPath, "pr", "--query", "mine")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	reqs := readRequestLog(t, logPath)
	if q := requestsFor(reqs, "changes"); len(q) != 1 || q[0].Args["query"] != "mine" {
		t.Fatalf("changes requests = %v", q)
	}
	if q := requestsFor(reqs, "changes_ack"); len(q) != 1 || q[0].Args["query"] != "mine" {
		t.Fatalf("changes_ack requests = %v", q)
	}
	if src := decodeQlSources(t, stdout); len(src) != 1 || src[0].StoreIdentity != qlIdentity {
		t.Fatalf("sources = %+v", src)
	}
}

func TestQuerylessChanges_NonOwningBackend_IsInvalidArgument_ForPrIssueAndCi(t *testing.T) {
	for _, entityType := range []string{"pr", "issue", "ci"} {
		t.Run(entityType, func(t *testing.T) {
			name := "backend-ql-plain-" + entityType
			stateDir := setupTypedRegistry(t, entityType, name)
			logPath := filepath.Join(t.TempDir(), "requests.log")
			writeRecordingBackend(t, name, logPath, map[string]string{"capabilities": qlCapsPlain})

			stdout, code := runTypedChanges(t, logPath, entityType)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (invalid_argument); stdout=%s", code, stdout)
			}
			var env struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal([]byte(stdout), &env); err != nil {
				t.Fatalf("decode %q: %v", stdout, err)
			}
			if env.Error.Code != "invalid_argument" || !strings.Contains(env.Error.Message, `"`+name+`"`) || !strings.Contains(env.Error.Message, "a query is required") {
				t.Fatalf("error = %+v, want invalid_argument naming the instance and saying a query is required", env.Error)
			}
			if ops := backendOps(readRequestLog(t, logPath)); len(ops) != 1 || ops[0] != "capabilities" {
				t.Fatalf("backend requests = %v, want only capabilities (the ledger path must not run)", ops)
			}
			if got := ledgerFiles(t, stateDir); len(got) != 0 {
				t.Fatalf("ledger files written: %v", got)
			}
		})
	}
}

func TestQuerylessChanges_MixedRegistry_AnswersTheOwningOne_AndReportsTheOtherAsDisabled(t *testing.T) {
	stateDir := setupTypedRegistry(t, "pr", "backend-ql-mix-owning", "backend-ql-mix-plain")
	logOwning := filepath.Join(t.TempDir(), "owning.log")
	logPlain := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-ql-mix-owning", logOwning, map[string]string{
		"capabilities": fwdCapsOwning, "changes": qlAnswer(nil, 6), "changes_ack": fwdAckOK,
	})
	writeRecordingBackend(t, "backend-ql-mix-plain", logPlain, map[string]string{"capabilities": qlCapsPlain})

	stdout, code := runTypedChanges(t, logOwning, "pr")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a disabled row is not degraded); stdout=%s", code, stdout)
	}
	for _, s := range decodeQlSources(t, stdout) {
		switch s.Backend {
		case "backend-ql-mix-owning":
			if s.Status != "succeeded" || s.StoreIdentity != qlIdentity {
				t.Fatalf("owning row = %+v", s)
			}
		case "backend-ql-mix-plain":
			if s.Status != "disabled" || s.StoreIdentity != "" || !strings.Contains(s.Reason, `"backend-ql-mix-plain"`) || !strings.Contains(s.Reason, "a query is required") {
				t.Fatalf("non-owning row = %+v", s)
			}
		default:
			t.Fatalf("unexpected source %+v", s)
		}
	}
	if ops := backendOps(readRequestLog(t, logPlain)); len(ops) != 1 || ops[0] != "capabilities" {
		t.Fatalf("non-owning backend requests = %v", ops)
	}
	if got := ledgerFiles(t, stateDir); len(got) != 0 {
		t.Fatalf("ledger files written: %v", got)
	}
}
