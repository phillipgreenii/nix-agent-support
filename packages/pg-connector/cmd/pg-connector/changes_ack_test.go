package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests manual acknowledgement through the real cobra commands, in
// process, against recording fake backends (helpers: changes_forward_test.go,
// changes_queryless_test.go): the --no-ack flag of changes (INTF-WIRE-ACK-1)
// and the changes-ack verb (INTF-WIRE-ACK-2).

const (
	ackCapsOwning = `{"protocolVersion":1,"schemaVersions":{"issue":12},"ops":["capabilities","changes","changes_ack"],"vocabulary":{"owns_changes":true,"cache_opt_out":true}}`
	ackCapsPlain  = `{"protocolVersion":1,"schemaVersions":{"issue":12},"ops":["capabilities","list"]}`
	// ackAnswer is a backend's own changes_ack answer; acked_seq is later than
	// the position asked for on purpose.
	ackAnswer    = `{"protocolVersion":1,"schemaVersion":12,"result":{"consumer":"c1","query":"mine","acked_seq":77,"store_identity":"0123456789abcdef0123456789abcdef"}}`
	ackPlainList = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#7","repo":"o/r","number":7,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#7"],"cursor":null,"truncated":false}}`
)

func ackOwningBackend(t *testing.T, name string, byOp map[string]string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+".log")
	if _, ok := byOp["capabilities"]; !ok {
		byOp["capabilities"] = ackCapsOwning
	}
	writeRecordingBackend(t, name, logPath, byOp)
	return logPath
}

// runAck runs the root command with a plain buffer as the caller's output.
func runAck(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out bytes.Buffer
	stderr, code, _ = executeChangesLogged(t, &out, args)
	return out.String(), stderr, code
}

func TestAckChanges_NoAck_WritesTheSameOutput_SendsNoChangesAck_WithoutTheFlagExactlyOne(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-owning")
	answer := fwdAnswer([]string{fwdRow("T-1", 3, "changed", `["title"]`, `[]`, 2)}, 9, `[]`)
	logPath := ackOwningBackend(t, "backend-ack-owning", map[string]string{"changes": answer, "changes_ack": ackAnswer})
	base := []string{"issue", "changes", "--consumer", "c1", "--query", "mine", "--backend", "backend-ack-owning"}

	plain, _, code := runAck(t, base...)
	if code != 0 {
		t.Fatalf("exit code without the flag = %d; stdout=%s", code, plain)
	}
	if n := len(requestsFor(readRequestLog(t, logPath), "changes_ack")); n != 1 {
		t.Fatalf("changes_ack requests without --no-ack = %d, want exactly 1", n)
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	manual, stderr, code := runAck(t, append(base, "--no-ack")...)
	if code != 0 {
		t.Fatalf("exit code with --no-ack = %d; stdout=%s stderr=%s", code, manual, stderr)
	}
	if manual != plain {
		t.Fatalf("--no-ack changed the output:\nwith:    %s\nwithout: %s", manual, plain)
	}
	reqs := readRequestLog(t, logPath)
	if n := len(requestsFor(reqs, "changes_ack")); n != 0 {
		t.Fatalf("changes_ack requests with --no-ack = %d, want 0 (%v)", n, opsOf(reqs))
	}
	if n := len(requestsFor(reqs, "changes")); n != 1 {
		t.Fatalf("changes requests with --no-ack = %d, want 1", n)
	}
}

func TestAckChanges_NoAck_QuietPollReportsNextSeq_AndChangesAckOfItIsAccepted(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-quiet")
	logPath := ackOwningBackend(t, "backend-ack-quiet", map[string]string{"changes": fwdAnswer(nil, 41, `[]`), "changes_ack": ackAnswer})

	stdout, _, code := runAck(t, "issue", "changes", "--consumer", "c1", "--query", "mine", "--no-ack")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	w := decodeFwdWire(t, stdout)
	if len(w.Changes) != 0 || len(w.Sources) != 1 || w.Sources[0].NextSeq == nil || *w.Sources[0].NextSeq != 41 {
		t.Fatalf("a quiet poll must still report next_seq 41: %s", stdout)
	}
	if n := len(requestsFor(readRequestLog(t, logPath), "changes_ack")); n != 0 {
		t.Fatalf("changes_ack requests after a --no-ack poll = %d, want 0", n)
	}

	ackOut, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "41")
	if code != 0 {
		t.Fatalf("changes-ack exit code = %d; stdout=%s", code, ackOut)
	}
	acks := requestsFor(readRequestLog(t, logPath), "changes_ack")
	if len(acks) != 1 || acks[0].Args["next_seq"] != float64(41) || acks[0].Args["consumer"] != "c1" || acks[0].Args["query"] != "mine" {
		t.Fatalf("recorded changes_ack = %v", acks)
	}
}

// The non-forwarded (ledger) source of a --no-ack call is disabled and its
// ledger file is byte-identical before and after.
func TestAckChanges_NoAck_LedgerSource_IsDisabled_AndItsLedgerIsUntouched(t *testing.T) {
	setupTypedRegistry(t, "pr", "backend-ack-mix-owning", "backend-ack-mix-plain")
	logOwning := ackOwningBackend(t, "backend-ack-mix-owning", map[string]string{
		"changes":     fwdAnswer([]string{fwdRow("o/r#1", 1, "added", `["entered_query"]`, `[]`, 1)}, 4, `[]`),
		"changes_ack": ackAnswer,
	})
	logPlain := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-ack-mix-plain", logPlain, map[string]string{"capabilities": ackCapsPlain, "list": ackPlainList})

	// A first ordinary poll of the ledger-path backend alone creates its ledger.
	if _, _, code := runAck(t, "pr", "changes", "--consumer", "c1", "--query", "mine", "--backend", "backend-ack-mix-plain"); code != 0 {
		t.Fatalf("ordinary ledger poll exit code = %d", code)
	}
	key := LedgerKey{Type: "pr", Backend: "backend-ack-mix-plain", Query: "mine"}
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the ordinary poll left no ledger: %v", err)
	}
	listsBefore := len(requestsFor(readRequestLog(t, logPlain), "list"))

	stdout, _, code := runAck(t, "pr", "changes", "--consumer", "c1", "--query", "mine", "--no-ack")
	if code != 0 {
		t.Fatalf("exit code = %d (a disabled row is not degraded); stdout=%s", code, stdout)
	}
	w := decodeFwdWire(t, stdout)
	var sawOwning, sawPlain bool
	for _, s := range w.Sources {
		switch s.Backend {
		case "backend-ack-mix-owning":
			sawOwning = s.Status == "succeeded" && s.NextSeq != nil && *s.NextSeq == 4
		case "backend-ack-mix-plain":
			sawPlain = s.Status == "disabled" && s.NextSeq == nil &&
				strings.Contains(s.Reason, `"backend-ack-mix-plain"`) && strings.Contains(s.Reason, "manual acknowledgement") && strings.Contains(s.Reason, "owns its changes")
		}
	}
	if !sawOwning || !sawPlain {
		t.Fatalf("sources = %+v", w.Sources)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the ledger of a ledger-path source changed under --no-ack:\nbefore: %s\nafter:  %s", before, after)
	}
	plainReqs := readRequestLog(t, logPlain)
	if n := len(requestsFor(plainReqs, "list")); n != listsBefore || len(requestsFor(plainReqs, "changes")) != 0 || len(requestsFor(plainReqs, "changes_ack")) != 0 {
		t.Fatalf("the ledger-path backend was sent more than its capabilities request: %v", opsOf(plainReqs))
	}
	if n := len(requestsFor(readRequestLog(t, logOwning), "changes_ack")); n != 0 {
		t.Fatalf("changes_ack requests = %d under --no-ack, want 0", n)
	}
}

func TestAckChanges_NoAck_EveryBackendALedgerSource_FailsInvalidArgumentListingEachReason(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-all-a", "backend-ack-all-b")
	logA := filepath.Join(t.TempDir(), "a.log")
	logB := filepath.Join(t.TempDir(), "b.log")
	writeRecordingBackend(t, "backend-ack-all-a", logA, map[string]string{"capabilities": ackCapsPlain})
	writeRecordingBackend(t, "backend-ack-all-b", logB, map[string]string{"capabilities": ackCapsPlain})

	stdout, _, code := runAck(t, "issue", "changes", "--consumer", "c1", "--query", "mine", "--no-ack")
	if code == 0 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, `backend-ack-all-a`) || !strings.Contains(stdout, `backend-ack-all-b`) || !strings.Contains(stdout, "manual acknowledgement") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	for _, p := range []string{logA, logB} {
		if ops := opsOf(readRequestLog(t, p)); len(ops) != 1 || ops[0] != "capabilities" {
			t.Fatalf("a backend received %v, want only its capabilities request", ops)
		}
	}
}

func TestAckChangesAck_PrintsTheBackendsAnswerUnchanged_AndOmitsAbsentQuery(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-verb")
	logPath := ackOwningBackend(t, "backend-ack-verb", map[string]string{"changes_ack": ackAnswer})

	stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "5", "--backend", "backend-ack-verb")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if err := json.Unmarshal([]byte(ackAnswer), &want); err != nil {
		t.Fatal(err)
	}
	if string(got["result"]) != string(want["result"]) || !strings.Contains(string(got["result"]), `"acked_seq":77`) || !strings.Contains(string(got["result"]), `"store_identity":"0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("the backend's answer must be printed unchanged: got %s", got["result"])
	}

	if _, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--next-seq", "0"); code != 0 {
		t.Fatalf("query-less changes-ack exit code = %d", code)
	}
	acks := requestsFor(readRequestLog(t, logPath), "changes_ack")
	if len(acks) != 2 {
		t.Fatalf("changes_ack requests = %v", acks)
	}
	if acks[0].Args["query"] != "mine" || acks[0].Args["next_seq"] != float64(5) {
		t.Fatalf("first request = %v", acks[0].Args)
	}
	if _, has := acks[1].Args["query"]; has || acks[1].Args["next_seq"] != float64(0) {
		t.Fatalf("with --query omitted the request must carry no query key: %v", acks[1].Args)
	}
}

func TestAckChangesAck_TwoBackendsAndNoBackendFlag_FailsInvalidArgumentWithNoWireCall(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-two-a", "backend-ack-two-b")
	logA := ackOwningBackend(t, "backend-ack-two-a", map[string]string{"changes_ack": ackAnswer})
	logB := ackOwningBackend(t, "backend-ack-two-b", map[string]string{"changes_ack": ackAnswer})

	stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "5")
	if code != 1 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, "--backend") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	for _, p := range []string{logA, logB} {
		if reqs := readRequestLog(t, p); len(reqs) != 0 {
			t.Fatalf("a backend received %v, want an empty request log", opsOf(reqs))
		}
	}
	// Pinning one of the two works.
	if _, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "5", "--backend", "backend-ack-two-b"); code != 0 {
		t.Fatalf("pinned changes-ack exit code = %d", code)
	}
	if n := len(requestsFor(readRequestLog(t, logB), "changes_ack")); n != 1 {
		t.Fatalf("pinned backend changes_ack requests = %d, want 1", n)
	}
	if reqs := readRequestLog(t, logA); len(reqs) != 0 {
		t.Fatalf("the unpinned backend received %v", opsOf(reqs))
	}
}

func TestAckChangesAck_NonOwningBackend_FailsInvalidArgumentNamingTheInstance_NoLedgerWrite(t *testing.T) {
	stateDir := setupTypedRegistry(t, "issue", "backend-ack-plain")
	logPath := filepath.Join(t.TempDir(), "plain.log")
	writeRecordingBackend(t, "backend-ack-plain", logPath, map[string]string{"capabilities": ackCapsPlain})

	stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "5")
	if code != 1 || !strings.Contains(stdout, "invalid_argument") || !strings.Contains(stdout, `backend-ack-plain`) || !strings.Contains(stdout, "does not own changes") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	if ops := opsOf(readRequestLog(t, logPath)); len(ops) != 1 || ops[0] != "capabilities" {
		t.Fatalf("the backend received %v, want only its capabilities request", ops)
	}
	if files := ledgerFiles(t, stateDir); len(files) != 0 {
		t.Fatalf("changes-ack wrote a ledger: %v", files)
	}
}

func TestAckChangesAck_BackendFailure_IsPrintedExitsNonZero_AndIsNotRetried(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-fail")
	logPath := ackOwningBackend(t, "backend-ack-fail", map[string]string{"changes_ack": fwdErrorEnvelope("unavailable", "daemon went away")})

	stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", "5")
	if code == 0 || !strings.Contains(stdout, "unavailable") || !strings.Contains(stdout, "daemon went away") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	if n := len(requestsFor(readRequestLog(t, logPath), "changes_ack")); n != 1 {
		t.Fatalf("changes_ack requests = %d, want exactly 1 (no retry)", n)
	}
}

func TestAckChangesAck_NextSeqNotANonNegativeInteger_FailsInvalidArgumentWithNoWireCall(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-badseq")
	logPath := ackOwningBackend(t, "backend-ack-badseq", map[string]string{"changes_ack": ackAnswer})
	for _, bad := range []string{"-1", "abc", "1.5", ""} {
		stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--query", "mine", "--next-seq", bad)
		if code != 1 || !strings.Contains(stdout, "invalid_argument") {
			t.Fatalf("--next-seq %q: code=%d stdout=%s", bad, code, stdout)
		}
	}
	if reqs := readRequestLog(t, logPath); len(reqs) != 0 {
		t.Fatalf("a backend received %v, want an empty request log", opsOf(reqs))
	}
}

func TestAckChangesAck_NotFoundAnswer_ExitsFour(t *testing.T) {
	setupTypedRegistry(t, "issue", "backend-ack-nf")
	ackOwningBackend(t, "backend-ack-nf", map[string]string{"changes_ack": fwdErrorEnvelope("not_found", "no such consumer")})
	stdout, _, code := runAck(t, "issue", "changes-ack", "--consumer", "c1", "--next-seq", "5")
	if code != 4 || !strings.Contains(stdout, "not_found") {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
}
