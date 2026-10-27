package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This file tests the caller-facing capabilities verb (INTF-WIRE-CAPS-1,
// INTF-WIRE-CAPS-2) through the real cobra command, in process, against
// recording shell-script fake backends (helpers: changes_forward_test.go,
// changes_queryless_test.go).

const (
	capsAnswerWithQueries = `{"protocolVersion":1,"schemaVersions":{"issue":12},"ops":["capabilities","show","list","changes"],"vocabulary":{"owns_changes":true,"cache_opt_out":true,"queries":{"q":{"watched":true}}}}`
	capsAnswerNoQueries   = `{"protocolVersion":1,"schemaVersions":{"issue":3,"pr":4},"ops":["capabilities","list"],"vocabulary":{"owns_changes":false}}`
	capsAnswerNoVocab     = `{"protocolVersion":1,"schemaVersions":{"issue":1},"ops":["capabilities"]}`
)

// capsConfig writes the shared config registering backends under
// connector.<entityType> with the given backends: block (raw YAML, may be
// empty) and points PG_PR_CONFIG and XDG_STATE_HOME at temporary locations.
func capsConfig(t *testing.T, entityType, backendsYAML string, backends ...string) (stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	var sb strings.Builder
	sb.WriteString("connector:\n  " + entityType + ":\n")
	for _, b := range backends {
		sb.WriteString("    - " + b + "\n")
	}
	if backendsYAML != "" {
		sb.WriteString("backends:\n" + backendsYAML)
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", stateDir)
	return stateDir
}

func capsBackend(t *testing.T, name, answer string) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), name+".log")
	writeRecordingBackend(t, name, logPath, map[string]string{"capabilities": answer})
	return logPath
}

func runCapsArgs(t *testing.T, args []string) (stdout, stderr string, code int) {
	t.Helper()
	var out bytes.Buffer
	stderr, code, _ = executeChangesLogged(t, &out, args)
	return out.String(), stderr, code
}

type capsWire struct {
	Sources []struct {
		Source string `json:"source"`
		Status string `json:"status"`
		Count  int    `json:"count"`
		Reason string `json:"reason"`
	} `json:"sources"`
	Backends []struct {
		Backend         string                    `json:"backend"`
		ProtocolVersion int                       `json:"protocolVersion"`
		SchemaVersions  map[string]int            `json:"schemaVersions"`
		Ops             []string                  `json:"ops"`
		Vocabulary      map[string]any            `json:"vocabulary"`
		Queries         map[string]map[string]any `json:"queries"`
	} `json:"backends"`
}

func decodeCapsWire(t *testing.T, stdout string) capsWire {
	t.Helper()
	var w capsWire
	if err := json.Unmarshal([]byte(stdout), &w); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return w
}

func assertEmptyStateDirs(t *testing.T, stateDir string) {
	t.Helper()
	var found []string
	_ = filepath.Walk(stateDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Fatalf("the verb wrote files under the state dir: %v", found)
	}
}

func TestCapabilities_TwoBackends_EveryConfiguredQueryListed_WatchedFromVocabularyElseFalse(t *testing.T) {
	stateDir := capsConfig(t, "issue", "  backend-caps-a:\n    queries:\n      q: \"a\"\n      r: \"b\"\n  backend-caps-b:\n    queries:\n      s: \"c\"\n", "backend-caps-a", "backend-caps-b")
	logA := capsBackend(t, "backend-caps-a", capsAnswerWithQueries)
	logB := capsBackend(t, "backend-caps-b", capsAnswerNoQueries)

	stdout, stderr, code := runCapsArgs(t, []string{"issue", "capabilities"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	w := decodeCapsWire(t, stdout)
	if len(w.Sources) != 2 || len(w.Backends) != 2 {
		t.Fatalf("want 2 sources and 2 backends: %s", stdout)
	}
	for i, name := range []string{"backend-caps-a", "backend-caps-b"} {
		s := w.Sources[i]
		if s.Source != name || s.Status != "succeeded" || s.Count != 1 || s.Reason != "" {
			t.Fatalf("sources[%d] = %+v", i, s)
		}
		if w.Backends[i].Backend != name {
			t.Fatalf("backends[%d].backend = %q, want %q", i, w.Backends[i].Backend, name)
		}
	}

	a, b := w.Backends[0], w.Backends[1]
	if a.ProtocolVersion != 1 || !reflect.DeepEqual(a.SchemaVersions, map[string]int{"issue": 12}) ||
		!reflect.DeepEqual(a.Ops, []string{"capabilities", "show", "list", "changes"}) {
		t.Fatalf("backend a envelope = %+v", a)
	}
	var wantVocab struct {
		Vocabulary map[string]any `json:"vocabulary"`
	}
	if err := json.Unmarshal([]byte(capsAnswerWithQueries), &wantVocab); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Vocabulary, wantVocab.Vocabulary) {
		t.Fatalf("backend a vocabulary = %v, want the backend's own %v", a.Vocabulary, wantVocab.Vocabulary)
	}
	// q is declared watched by the backend; r is configured but not listed.
	if !reflect.DeepEqual(a.Queries, map[string]map[string]any{"q": {"watched": true}, "r": {"watched": false}}) {
		t.Fatalf("backend a queries = %v", a.Queries)
	}
	// b lists no queries member at all: every configured query reads false.
	if b.ProtocolVersion != 1 || !reflect.DeepEqual(b.SchemaVersions, map[string]int{"issue": 3, "pr": 4}) ||
		!reflect.DeepEqual(b.Ops, []string{"capabilities", "list"}) || !reflect.DeepEqual(b.Vocabulary, map[string]any{"owns_changes": false}) {
		t.Fatalf("backend b = %+v", b)
	}
	if !reflect.DeepEqual(b.Queries, map[string]map[string]any{"s": {"watched": false}}) {
		t.Fatalf("backend b queries = %v", b.Queries)
	}

	// Exactly one capabilities request per backend, nothing else, and no
	// ledger or cache entry.
	for name, logPath := range map[string]string{"a": logA, "b": logB} {
		if ops := opsOf(readRequestLog(t, logPath)); !reflect.DeepEqual(ops, []string{"capabilities"}) {
			t.Fatalf("backend %s requests = %v, want exactly one capabilities", name, ops)
		}
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestCapabilities_BackendWithoutConfiguredQueriesOrVocabulary_AnswersEmptyQueries(t *testing.T) {
	capsConfig(t, "issue", "", "backend-caps-bare")
	capsBackend(t, "backend-caps-bare", capsAnswerNoVocab)

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, `"queries":{}`) {
		t.Fatalf("a backend with no configured queries must answer queries {}: %s", stdout)
	}
	w := decodeCapsWire(t, stdout)
	if len(w.Backends) != 1 || len(w.Backends[0].Queries) != 0 || w.Backends[0].Vocabulary != nil {
		t.Fatalf("backends = %+v", w.Backends)
	}
}

func TestCapabilities_FakeDeclaringQueryWatched_IsReportedWatched_AndMissingConfiguredQueryFalse(t *testing.T) {
	capsConfig(t, "issue", "  backend-caps-w:\n    queries:\n      q: \"a\"\n      other: \"b\"\n", "backend-caps-w")
	capsBackend(t, "backend-caps-w", `{"protocolVersion":1,"schemaVersions":{"issue":1},"ops":["capabilities"],"vocabulary":{"queries":{"q":{"watched":true}}}}`)

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	q := decodeCapsWire(t, stdout).Backends[0].Queries
	if q["q"]["watched"] != true || q["other"]["watched"] != false || len(q) != 2 {
		t.Fatalf("queries = %v, want q watched true and other watched false", q)
	}
}

func TestCapabilities_FailedBackend_IsDegradedRowWithoutBackendsEntry_ExitCodeFollowsFanOutScheme(t *testing.T) {
	stateDir := capsConfig(t, "issue", "  backend-caps-ok:\n    queries:\n      q: \"a\"\n", "backend-caps-ok", "backend-caps-bad")
	capsBackend(t, "backend-caps-ok", capsAnswerWithQueries)
	capsBackend(t, "backend-caps-bad", "this is not a capabilities answer")

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities"})
	if code != 2 {
		t.Fatalf("partial failure exit code = %d, want 2; stdout=%s", code, stdout)
	}
	w := decodeCapsWire(t, stdout)
	if len(w.Sources) != 2 || w.Sources[0].Status != "succeeded" || w.Sources[1].Source != "backend-caps-bad" ||
		w.Sources[1].Status != "degraded" || w.Sources[1].Count != 0 || w.Sources[1].Reason == "" {
		t.Fatalf("sources = %+v", w.Sources)
	}
	if len(w.Backends) != 1 || w.Backends[0].Backend != "backend-caps-ok" {
		t.Fatalf("a failed backend must have no backends entry: %s", stdout)
	}

	// Pinned to the failing backend alone, every backend failed: exit 3.
	stdout, _, code = runCapsArgs(t, []string{"issue", "capabilities", "--backend", "backend-caps-bad"})
	if code != 3 {
		t.Fatalf("total failure exit code = %d, want 3; stdout=%s", code, stdout)
	}
	w = decodeCapsWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || len(w.Backends) != 0 {
		t.Fatalf("total failure answer = %s", stdout)
	}
	if !strings.Contains(stdout, `"backends":[]`) {
		t.Fatalf("backends must be an empty array, not null: %s", stdout)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestCapabilities_BackendFlag_QueriesOnlyThatBackend(t *testing.T) {
	capsConfig(t, "issue", "", "backend-caps-p1", "backend-caps-p2")
	log1 := capsBackend(t, "backend-caps-p1", capsAnswerWithQueries)
	log2 := capsBackend(t, "backend-caps-p2", capsAnswerNoQueries)

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities", "--backend", "backend-caps-p2"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeCapsWire(t, stdout)
	if len(w.Sources) != 1 || w.Sources[0].Source != "backend-caps-p2" || len(w.Backends) != 1 || w.Backends[0].Backend != "backend-caps-p2" {
		t.Fatalf("answer = %s", stdout)
	}
	if n := len(readRequestLog(t, log1)); n != 0 {
		t.Fatalf("the unpinned backend was asked %d times", n)
	}
	if ops := opsOf(readRequestLog(t, log2)); !reflect.DeepEqual(ops, []string{"capabilities"}) {
		t.Fatalf("pinned backend requests = %v", ops)
	}
}

func TestCapabilities_UnknownBackend_FailsInvalidArgument_NoBackendCall(t *testing.T) {
	stateDir := capsConfig(t, "issue", "", "backend-caps-u1")
	log1 := capsBackend(t, "backend-caps-u1", capsAnswerWithQueries)

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities", "--backend", "no-such-backend"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "invalid_argument") {
		t.Fatalf("want an invalid_argument error: %s", stdout)
	}
	if n := len(readRequestLog(t, log1)); n != 0 {
		t.Fatalf("a backend was called %d times for an unknown --backend", n)
	}
	assertEmptyStateDirs(t, stateDir)
}

func TestCapabilities_EmptyRegistry_FailsInvalidArgument(t *testing.T) {
	capsConfig(t, "pr", "", "backend-caps-other")

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "invalid_argument") {
		t.Fatalf("want an invalid_argument error: %s", stdout)
	}
}

// The verb is registered for every type that fans out, from one constructor.
func TestCapabilities_RegisteredForEveryFanOutType(t *testing.T) {
	for _, typ := range []string{"issue", "pr", "ci", "calendar"} {
		capsConfig(t, typ, "", "backend-caps-t-"+typ)
		capsBackend(t, "backend-caps-t-"+typ, capsAnswerNoVocab)
		stdout, _, code := runCapsArgs(t, []string{typ, "capabilities"})
		if code != 0 || len(decodeCapsWire(t, stdout).Backends) != 1 {
			t.Fatalf("%s capabilities: exit %d, stdout=%s", typ, code, stdout)
		}
	}
}

func TestCapabilities_HumanOutput_ListsEachQueryWatchedValue(t *testing.T) {
	capsConfig(t, "issue", "  backend-caps-h:\n    queries:\n      q: \"a\"\n      r: \"b\"\n", "backend-caps-h")
	capsBackend(t, "backend-caps-h", capsAnswerWithQueries)

	stdout, _, code := runCapsArgs(t, []string{"issue", "capabilities", "--output", "human"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	for _, want := range []string{"backend-caps-h: succeeded", "query q: watched=true", "query r: watched=false"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output lacks %q:\n%s", want, stdout)
		}
	}
}
