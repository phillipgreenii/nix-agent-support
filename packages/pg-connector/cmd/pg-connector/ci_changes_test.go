package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// This file tests the "ci changes" verb (ci_changes.go) through the real cobra
// command against recording fake backends (changes_forward_test.go's helpers):
// an owning ci backend is forwarded exactly like pr, a ci backend that does not
// own changes is answered by the umbrella itself and sent nothing but its
// capabilities request.

const (
	ciCapsOwning = `{"protocolVersion":1,"schemaVersions":{"ci":3},"ops":["capabilities","changes","changes_ack"],"vocabulary":{"owns_changes":true,"cache_opt_out":true}}`
	ciCapsPlain  = `{"protocolVersion":1,"schemaVersions":{"ci":3},"ops":["capabilities","list","list_runs"]}`
)

// ciRow is one changes[] row of a ci answer: the entity is a PR's CI state.
func ciRow(prID string, seq int64, change, kinds, fields string, version int64) string {
	return fmt.Sprintf(`{"change":%q,"source":"pg-connector-ci-github-actions","entity":{"pr_id":%q,"rollup":"failing","runs":[{"id":"r1","name":"build","status":"completed","conclusion":"failure"}],"version":%d,"head_sha":"abc123"},"seq":%d,"kinds":%s,"fields":%s}`,
		change, prID, version, seq, kinds, fields)
}

func ciAnswer(rows []string, nextSeq int64, freshness string) string {
	return fmt.Sprintf(fwdEnvelope, fmt.Sprintf(`{"sources":[{"backend":"pg-connector-ci-github-actions","status":"succeeded","version":7,"truncated":false,"next_seq":%d}],"changes":[%s],"sources_freshness":%s}`,
		nextSeq, strings.Join(rows, ","), freshness))
}

// setupCiRegistry registers backends as the ci backends and isolates all state.
func setupCiRegistry(t *testing.T, backends ...string) (stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	var sb strings.Builder
	sb.WriteString("connector:\n  ci:\n")
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

func runCiChanges(t *testing.T, logPath string, extra ...string) (stdout, stderr string, code int) {
	t.Helper()
	w := &markingWriter{logPath: logPath}
	args := append([]string{"ci", "changes", "--query", "mine", "--consumer", "c1"}, extra...)
	stderr, code, _ = executeChangesLogged(t, w, args)
	return w.buf.String(), stderr, code
}

// assertNoLedgerFiles fails if any ledger file exists under stateDir.
func assertNoLedgerFiles(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, ledgerSubdir, ledgerDirName))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("a ledger file was written: %s", e.Name())
		}
	}
}

func TestCiChanges_OwningBackend_ForwardsCIShapedRows_AcksAfterFlush(t *testing.T) {
	stateDir := setupCiRegistry(t, "backend-ci-owning")
	answer := ciAnswer([]string{
		ciRow("o/r#1", 11, "added", `["entered_query"]`, `[]`, 2),
		ciRow("o/r#1", 12, "changed", `["ci_changed"]`, `[{"field":"rollup","before":"passing","after":"failing"}]`, 3),
	}, 31, `[{"query":"mine","last_success_at":"2026-03-04T05:06:07Z"}]`)
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-owning", logPath, map[string]string{
		"capabilities": ciCapsOwning, "changes": answer, "changes_ack": fwdAckOK,
	})

	stdout, stderr, code := runCiChanges(t, logPath)
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
	reqs := readRequestLog(t, logPath)
	if n := len(requestsFor(reqs, "list")) + len(requestsFor(reqs, "list_runs")); n != 0 {
		t.Fatalf("a forwarded ci call sent a list op: %v", opsOf(reqs))
	}
	ch := requestsFor(reqs, "changes")
	if len(ch) != 1 {
		t.Fatalf("changes requests = %d: %v", len(ch), opsOf(reqs))
	}
	want := map[string]any{"consumer": "c1", "query": "mine", "cached": false, "reset": false}
	for k, v := range want {
		if ch[0].Args[k] != v {
			t.Fatalf("changes args[%q] = %v, want %v", k, ch[0].Args[k], v)
		}
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Changes) != 2 || w.Changes[0].Source != "backend-ci-owning" {
		t.Fatalf("wire = %s", stdout)
	}
	// The CI entity passes through unchanged.
	var entity map[string]json.RawMessage
	if err := json.Unmarshal(w.Changes[1].Entity, &entity); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pr_id", "rollup", "runs", "version", "head_sha"} {
		if _, ok := entity[k]; !ok {
			t.Fatalf("entity lost %q: %s", k, w.Changes[1].Entity)
		}
	}
	if string(entity["pr_id"]) != `"o/r#1"` || string(entity["version"]) != `3` || string(entity["head_sha"]) != `"abc123"` || !strings.Contains(string(entity["runs"]), `"conclusion":"failure"`) {
		t.Fatalf("entity changed: %s", w.Changes[1].Entity)
	}
	if w.Changes[0].Seq == nil || *w.Changes[0].Seq != 11 || w.Changes[1].Seq == nil || *w.Changes[1].Seq != 12 {
		t.Fatalf("seq not carried: %s", stdout)
	}
	if string(w.Changes[0].Kinds) != `["entered_query"]` || string(w.Changes[1].Kinds) != `["ci_changed"]` {
		t.Fatalf("kinds = %s, %s", w.Changes[0].Kinds, w.Changes[1].Kinds)
	}
	if !strings.Contains(string(w.Changes[1].Fields), `"after":"failing"`) {
		t.Fatalf("fields = %s", w.Changes[1].Fields)
	}
	if len(w.Sources) != 1 || w.Sources[0].NextSeq == nil || *w.Sources[0].NextSeq != 31 || w.Sources[0].Backend != "backend-ci-owning" {
		t.Fatalf("sources = %+v", w.Sources)
	}
	if w.SourcesFreshness == nil || len(*w.SourcesFreshness) != 1 || (*w.SourcesFreshness)[0].LastSuccessAt != "2026-03-04T05:06:07Z" {
		t.Fatalf("sources_freshness = %+v", w.SourcesFreshness)
	}
	acks := requestsFor(reqs, "changes_ack")
	if len(acks) != 1 || acks[0].Args["consumer"] != "c1" || acks[0].Args["query"] != "mine" || acks[0].Args["next_seq"] != float64(31) {
		t.Fatalf("acks = %+v", acks)
	}
	if out, ack := indexOfOp(reqs, fwdOutputMark), indexOfOp(reqs, "changes_ack"); out < 0 || ack < out {
		t.Fatalf("changes_ack sent before the output was written: %v", opsOf(reqs))
	}
	// The ci key (and only it) is stamped from sources_freshness.
	l := mustLoadLedger(t, LedgerKey{Type: "ci", Backend: "backend-ci-owning", Query: "mine"})
	if l.RefreshedAt == nil || l.RefreshedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-03-04T05:06:07Z" || l.Consumers["c1"].LastSeen.IsZero() {
		t.Fatalf("ci ledger key not stamped: %+v", l)
	}
	var files []string
	entries, _ := os.ReadDir(filepath.Join(stateDir, ledgerSubdir, ledgerDirName))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) != 1 {
		t.Fatalf("ledger files = %d, want only the ci key's: %v", len(files), files)
	}
}

func TestCiChanges_PassesCachedAndResetThrough(t *testing.T) {
	setupCiRegistry(t, "backend-ci-flags")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-flags", logPath, map[string]string{
		"capabilities": ciCapsOwning, "changes": ciAnswer(nil, 1, `[]`), "changes_ack": fwdAckOK,
	})
	if _, _, code := runCiChanges(t, logPath, "--cached", "--reset"); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	ch := requestsFor(readRequestLog(t, logPath), "changes")
	if len(ch) != 1 || ch[0].Args["cached"] != true || ch[0].Args["reset"] != true {
		t.Fatalf("changes args = %+v", ch)
	}
}

func TestCiChanges_NonOwningBackend_IsInvalidArgument_NamesInstance_SendsNothingButCapabilities(t *testing.T) {
	stateDir := setupCiRegistry(t, "backend-ci-plain")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-plain", logPath, map[string]string{"capabilities": ciCapsPlain})

	stdout, _, code := runCiChanges(t, logPath)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (invalid_argument); stdout=%s", code, stdout)
	}
	var env struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if env.Error.Code != "invalid_argument" || !strings.Contains(env.Error.Message, `"backend-ci-plain"`) || !strings.Contains(env.Error.Message, "does not own changes") {
		t.Fatalf("error = %+v", env.Error)
	}
	if ops := backendOps(readRequestLog(t, logPath)); len(ops) != 1 || ops[0] != "capabilities" {
		t.Fatalf("backend requests = %v, want only capabilities", ops)
	}
	assertNoLedgerFiles(t, stateDir)
}

func TestCiChanges_FailedCapabilities_CountsAsNotOwning_ReasonCarriesTheError(t *testing.T) {
	stateDir := setupCiRegistry(t, "backend-ci-nocaps")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-nocaps", logPath, map[string]string{
		"capabilities": fwdErrorEnvelope("unavailable", "no capabilities here"),
	})
	stdout, _, code := runCiChanges(t, logPath)
	if code != 1 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, "no capabilities here") || !strings.Contains(stdout, "does not own changes") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	if ops := backendOps(readRequestLog(t, logPath)); len(ops) != 1 || ops[0] != "capabilities" {
		t.Fatalf("backend requests = %v", ops)
	}
	assertNoLedgerFiles(t, stateDir)
}

func TestCiChanges_MixedRegistry_AnswersTheOwningOne_ReportsTheOther(t *testing.T) {
	stateDir := setupCiRegistry(t, "backend-ci-mix-owning", "backend-ci-mix-plain")
	logOwning := filepath.Join(t.TempDir(), "owning.log")
	logPlain := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-ci-mix-owning", logOwning, map[string]string{
		"capabilities": ciCapsOwning,
		"changes":      ciAnswer([]string{ciRow("o/r#2", 5, "changed", `["ci_changed"]`, `[]`, 9)}, 6, `[]`),
		"changes_ack":  fwdAckOK,
	})
	writeRecordingBackend(t, "backend-ci-mix-plain", logPlain, map[string]string{"capabilities": ciCapsPlain})

	w := &markingWriter{logPath: logOwning}
	_, code, _ := executeChangesLogged(t, w, []string{"ci", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (a disabled row is not degraded); stdout=%s", code, w.buf.String())
	}
	wire := decodeFwdWire(t, w.buf.String())
	if len(wire.Sources) != 2 || len(wire.Changes) != 1 || wire.Changes[0].Source != "backend-ci-mix-owning" {
		t.Fatalf("wire = %s", w.buf.String())
	}
	for _, s := range wire.Sources {
		switch s.Backend {
		case "backend-ci-mix-owning":
			if s.Status != "succeeded" || s.NextSeq == nil || *s.NextSeq != 6 {
				t.Fatalf("owning row = %+v", s)
			}
		case "backend-ci-mix-plain":
			if s.Status != "disabled" || s.NextSeq != nil || s.Version != 0 || !strings.Contains(s.Reason, `"backend-ci-mix-plain"`) || !strings.Contains(s.Reason, "does not own changes") {
				t.Fatalf("non-owning row = %+v", s)
			}
		default:
			t.Fatalf("unexpected source %+v", s)
		}
	}
	if ops := backendOps(readRequestLog(t, logPlain)); len(ops) != 1 || ops[0] != "capabilities" {
		t.Fatalf("non-owning backend requests = %v", ops)
	}
	own := readRequestLog(t, logOwning)
	if len(requestsFor(own, "changes")) != 1 || len(requestsFor(own, "changes_ack")) != 1 {
		t.Fatalf("owning backend requests = %v", opsOf(own))
	}
	entries, _ := os.ReadDir(filepath.Join(stateDir, ledgerSubdir, ledgerDirName))
	for _, e := range entries {
		if strings.Contains(e.Name(), "backend-ci-mix-plain") {
			t.Fatalf("a ledger file was written for the non-owning backend: %s", e.Name())
		}
	}
}

func TestCiChanges_BackendPin_OfANonOwningBackend_FailsTheWholeCall(t *testing.T) {
	setupCiRegistry(t, "backend-ci-pin-owning", "backend-ci-pin-plain")
	logOwning := filepath.Join(t.TempDir(), "owning.log")
	logPlain := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-ci-pin-owning", logOwning, map[string]string{"capabilities": ciCapsOwning, "changes": ciAnswer(nil, 1, `[]`), "changes_ack": fwdAckOK})
	writeRecordingBackend(t, "backend-ci-pin-plain", logPlain, map[string]string{"capabilities": ciCapsPlain})
	stdout, _, code := runCiChanges(t, logPlain, "--backend", "backend-ci-pin-plain")
	if code != 1 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, `\"backend-ci-pin-plain\"`) {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	if reqs := readRequestLog(t, logOwning); len(reqs) != 0 {
		t.Fatalf("the unpinned backend received %v", opsOf(reqs))
	}
}

// The daemon's own refusals reach the caller with their messages intact: the
// umbrella neither retries nor works around them, and acknowledges nothing.
func TestCiChanges_DaemonRefusals_ReachTheCallerIntact(t *testing.T) {
	cases := []struct{ name, msg string }{
		{"key-conflict", `consumer "c1" already holds the key (c1, pr, mine); one consumer cannot read both feeds of a query`},
		{"cursor-expired", "cursor_expired: position fell outside retention; re-run with --reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := "backend-ci-refuse-" + tc.name
			setupCiRegistry(t, backend)
			logPath := filepath.Join(t.TempDir(), "requests.log")
			writeRecordingBackend(t, backend, logPath, map[string]string{
				"capabilities": ciCapsOwning,
				"changes":      fwdErrorEnvelope("invalid_argument", tc.msg),
			})
			stdout, _, code := runCiChanges(t, logPath)
			if code == 0 {
				t.Fatalf("exit code = 0; stdout=%s", stdout)
			}
			w := decodeFwdWire(t, stdout)
			if len(w.Sources) != 1 || w.Sources[0].Status != "degraded" || !strings.Contains(w.Sources[0].Reason, tc.msg) {
				t.Fatalf("message must reach the caller intact: %+v", w.Sources)
			}
			reqs := readRequestLog(t, logPath)
			if len(requestsFor(reqs, "changes")) != 1 || len(requestsFor(reqs, "changes_ack")) != 0 {
				t.Fatalf("requests = %v", opsOf(reqs))
			}
		})
	}
}

func TestCiChanges_HumanMode_NamesTheEntityByPRID(t *testing.T) {
	setupCiRegistry(t, "backend-ci-human")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-human", logPath, map[string]string{
		"capabilities": ciCapsOwning,
		"changes":      ciAnswer([]string{ciRow("o/r#4", 1, "changed", `["ci_changed"]`, `[]`, 2)}, 2, `[]`),
		"changes_ack":  fwdAckOK,
	})
	w := &markingWriter{logPath: logPath}
	_, code, _ := executeChangesLogged(t, w, []string{"--output", "human", "ci", "changes", "--query", "mine", "--consumer", "c1"})
	if code != 0 || !strings.Contains(w.buf.String(), "changed o/r#4 (from backend-ci-human)") {
		t.Fatalf("code=%d human output = %q", code, w.buf.String())
	}
}

func TestCiChanges_HelpListsExactlyPrChangesFlags(t *testing.T) {
	flagsOf := func(args ...string) string {
		root := newRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(append(args, "--help"))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v --help: %v", args, err)
		}
		sub, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		sub.LocalFlags().VisitAll(func(f *pflag.Flag) {
			names = append(names, f.Name+":"+f.Value.Type()+":"+f.DefValue+fmt.Sprint(f.Hidden))
		})
		return strings.Join(names, ",") + "\n" + helpFlagLines(out.String())
	}
	pr, ci := flagsOf("pr", "changes"), flagsOf("ci", "changes")
	// The backend flag's own usage text names the entity type; the rest is identical.
	norm := func(s string) string {
		return strings.NewReplacer("every registered pr backend", "every registered X backend", "every registered ci backend", "every registered X backend").Replace(s)
	}
	if norm(pr) != norm(ci) {
		t.Fatalf("flags differ:\npr changes:\n%s\nci changes:\n%s", pr, ci)
	}
	for _, name := range []string{"--query", "--consumer", "--cached", "--reset", "--backend"} {
		if !strings.Contains(ci, name) {
			t.Fatalf("ci changes --help lacks %s:\n%s", name, ci)
		}
	}
	for _, hidden := range []string{"--since", "--before"} {
		if strings.Contains(ci, hidden) {
			t.Fatalf("ci changes --help lists the hidden flag %s:\n%s", hidden, ci)
		}
	}
}

// helpFlagLines keeps only the Flags section of a --help text.
func helpFlagLines(help string) string {
	i := strings.Index(help, "Flags:")
	if i < 0 {
		return help
	}
	return help[i:]
}

func TestCiChanges_SinceAndBeforeAreRefused(t *testing.T) {
	setupCiRegistry(t, "backend-ci-refuse-flags")
	logPath := filepath.Join(t.TempDir(), "requests.log")
	writeRecordingBackend(t, "backend-ci-refuse-flags", logPath, map[string]string{"capabilities": ciCapsOwning})
	for _, flag := range []string{"--since", "--before"} {
		stdout, _, code := runCiChanges(t, logPath, flag, "24h")
		if code != 1 || !strings.Contains(stdout, "invalid_argument") {
			t.Fatalf("%s: code=%d stdout=%s", flag, code, stdout)
		}
	}
	if reqs := backendOps(readRequestLog(t, logPath)); len(reqs) != 0 {
		t.Fatalf("a refused flag reached the backend: %v", reqs)
	}
}

// backendOps is the ops the backend received, without the output marker the
// caller's writer appends.
func backendOps(reqs []recordedRequest) []string {
	var ops []string
	for _, r := range reqs {
		if r.Op != fwdOutputMark {
			ops = append(ops, r.Op)
		}
	}
	return ops
}
