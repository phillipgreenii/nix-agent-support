package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dtest"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/failsig"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
)

const sigSession = "pg-router-worker-zr-w"

// oauthFailure is the 2026-09-23 incident text (git fetch -> step oauth).
const oauthFailure = "error: oauth command timed out\nfatal: Could not read from remote repository."

func jsonlRecord(t *testing.T, typ string, content any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": typ, "message": map[string]any{"role": typ, "content": content},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func toolResult(t *testing.T, text string) string {
	return jsonlRecord(t, "user", []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": text}})
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// failedDispatch runs the death path against a session whose transcript is at
// path, then finishWait, and returns the dispatch_result record (decoded) and
// the raw eventlog bytes.
func failedDispatch(t *testing.T, cc ccpoolRunnerFor, path string) (map[string]any, string) {
	t.Helper()
	cfg := fastCfg()
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	clk := &dtest.ManualClock{T: time.Unix(0, 0)}
	e := &ccpoolRun{deps: Deps{CC: cc.fake(), BD: bd, Cfg: cfg, Now: clk.Now, Tick: clk.TickAdvancing(), Log: w}}
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	werr := e.waitDone(context.Background(), nil, d, sigSession)
	if werr == nil {
		t.Fatal("expected failure")
	}
	_, _ = e.finishWait(context.Background(), d.Role.CCPool, d, sigSession, "", werr)
	_ = w.Close()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return findKind(t, string(raw), dispatchResultKind), string(raw)
}

func findKind(t *testing.T, raw, kind string) map[string]any {
	t.Helper()
	for _, l := range strings.Split(raw, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["kind"] == kind {
			return m
		}
	}
	t.Fatalf("no %q event in:\n%s", kind, raw)
	return nil
}

// ccpoolRunnerFor builds the FakeCC for a failed-session scenario.
type ccpoolRunnerFor struct {
	transcript string
	onClose    func()
}

type closeHookCC struct {
	*dtest.FakeCC
	hook func()
}

func (c *closeHookCC) Close(ctx context.Context, id string, purge bool) error {
	if c.hook != nil {
		c.hook()
	}
	return c.FakeCC.Close(ctx, id, purge)
}

func (c ccpoolRunnerFor) fake() ccpool.Runner {
	base := &dtest.FakeCC{ListSeq: [][]ccpool.Session{
		{{ExternalID: sigSession, Live: false, State: ccpool.StateErrored, TranscriptPath: c.transcript}},
	}}
	return &closeHookCC{FakeCC: base, hook: c.onClose}
}

func TestFailureSignature_oauthFixtureIsGitAuth(t *testing.T) {
	p := writeTranscript(t,
		jsonlRecord(t, "assistant", []map[string]any{{"type": "text", "text": "fetching"}}),
		toolResult(t, oauthFailure))
	rec, _ := failedDispatch(t, ccpoolRunnerFor{transcript: p}, p)
	if rec["failure_signature"] != "git-auth" {
		t.Fatalf("failure_signature = %v, want git-auth; rec=%v", rec["failure_signature"], rec)
	}
	for _, k := range []string{"role", "pool", "bead", "session", "signature_evidence"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("event missing %q: %v", k, rec)
		}
	}
	if rec["bead"] != "zr-w" || rec["session"] != sigSession {
		t.Errorf("bead/session = %v/%v", rec["bead"], rec["session"])
	}
	if ev, _ := rec["signature_evidence"].(string); len(ev) == 0 || len(ev) > failsig.MaxEvidenceLen {
		t.Errorf("evidence length %d out of (0,%d]", len(ev), failsig.MaxEvidenceLen)
	}
}

func TestFailureSignature_toolResultBlockArrayAndStringContent(t *testing.T) {
	arr := jsonlRecord(t, "user", []map[string]any{{"type": "tool_result", "content": []map[string]any{{"type": "text", "text": oauthFailure}}}})
	if got := classifyTranscript(writeTranscript(t, arr)).Signature; got != failsig.GitAuth {
		t.Errorf("array-form tool_result = %v", got)
	}
	str := jsonlRecord(t, "user", oauthFailure) // plain-string message content
	if got := classifyTranscript(writeTranscript(t, str)).Signature; got != failsig.GitAuth {
		t.Errorf("string-form content = %v", got)
	}
}

func TestFailureSignature_emptyOrUnreadablePathIsUnknown(t *testing.T) {
	for name, p := range map[string]string{"empty": "", "missing": filepath.Join(t.TempDir(), "nope.jsonl")} {
		rec, _ := failedDispatch(t, ccpoolRunnerFor{transcript: p}, p)
		if rec["failure_signature"] != "unknown" || rec["signature_evidence"] != "" {
			t.Errorf("%s: got %v / %q, want unknown / empty", name, rec["failure_signature"], rec["signature_evidence"])
		}
	}
}

func TestFailureSignature_successWithNonzeroExitIsUnknown(t *testing.T) {
	p := writeTranscript(t,
		toolResult(t, "Everything up-to-date\nbead zr-w closed; all checks passed"),
		jsonlRecord(t, "assistant", []map[string]any{{"type": "text", "text": "Done. Task completed successfully."}}))
	rec, _ := failedDispatch(t, ccpoolRunnerFor{transcript: p}, p)
	if rec["failure_signature"] != "unknown" {
		t.Fatalf("success transcript = %v, want unknown", rec["failure_signature"])
	}
}

func TestFailureSignature_neverLogsRawTranscriptOrCredentials(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	p := writeTranscript(t,
		toolResult(t, "UNRELATED-RAW-PAYLOAD-MARKER should never be logged"),
		toolResult(t, "fatal: Could not read from remote repository "+token+" https://bob:s3cretpw@example.com/org/repo.git"))
	rec, raw := failedDispatch(t, ccpoolRunnerFor{transcript: p}, p)
	if rec["failure_signature"] != "git-auth" {
		t.Fatalf("signature = %v", rec["failure_signature"])
	}
	for _, leak := range []string{token, "s3cretpw", "bob:", "UNRELATED-RAW-PAYLOAD-MARKER"} {
		if strings.Contains(raw, leak) {
			t.Errorf("eventlog leaked %q:\n%s", leak, raw)
		}
	}
}

func TestFailureSignature_truncatedFirstLineDropped(t *testing.T) {
	// A file longer than the 64 KB tail: the cut lands mid-line. The oauth
	// failure at the end must still classify; the same text only in the
	// dropped head must NOT (proves the tail bound).
	filler := toolResult(t, strings.Repeat("x", 1000))
	var lines []string
	lines = append(lines, toolResult(t, oauthFailure)) // head: beyond the tail
	for range 80 {
		lines = append(lines, filler)
	}
	if got := classifyTranscript(writeTranscript(t, lines...)).Signature; got != failsig.Unknown {
		t.Errorf("failure outside the 64 KB tail = %v, want unknown", got)
	}
	lines = append(lines, toolResult(t, oauthFailure))
	if got := classifyTranscript(writeTranscript(t, lines...)).Signature; got != failsig.GitAuth {
		t.Errorf("failure inside the tail with truncated first line = %v, want git-auth", got)
	}
}

func TestReadTail_dropsPartialFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	if err := os.WriteFile(p, []byte("AAAAAAAAAA\nBBB\nCCC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := readTail(p, 12)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "A") {
		t.Errorf("tail = %q must not contain the partial first line", b)
	}
}

func TestFailureSignature_capturedBeforeTeardown(t *testing.T) {
	p := writeTranscript(t, toolResult(t, oauthFailure))
	// The fake CC invalidates the transcript when the handler closes the row.
	rec, _ := failedDispatch(t, ccpoolRunnerFor{transcript: p, onClose: func() { _ = os.Remove(p) }}, p)
	if _, err := os.Stat(p); err == nil {
		t.Fatal("test bug: Close hook did not run, transcript still present")
	}
	if rec["failure_signature"] != "git-auth" {
		t.Fatalf("signature after teardown removed the transcript = %v, want git-auth (captured before Close)", rec["failure_signature"])
	}
}

func TestFailureSignature_budgetHardStop(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	e := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg)
	e.deps.Log = w
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	be := &watchdog.BudgetError{
		Role: "worker", Pool: "default", Bead: "zr-w", Session: sigSession,
		Limit: budget.LimitKind("tokens"), Used: 12, Cap: 10,
	}
	_, _ = e.finishWait(context.Background(), d.Role.CCPool, d, sigSession, "", be)
	_ = w.Close()
	raw, _ := os.ReadFile(logPath)
	rec := findKind(t, string(raw), dispatchResultKind)
	if rec["failure_signature"] != "budget" || rec["limit"] != "tokens" || rec["cap"] != float64(10) || rec["used"] != float64(12) {
		t.Errorf("budget record = %v", rec)
	}
}

func TestFailureSignature_successAndCancelRecordNothing(t *testing.T) {
	cfg := fastCfg()
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	w, _ := eventlog.New(logPath)
	e := newExec(&dtest.FakeCC{}, &dtest.ScriptBD{}, cfg)
	e.deps.Log = w
	d := DispatchContext{Role: workerRole(cfg), Item: item.Item{ID: "zr-w"}}
	_, _ = e.finishWait(context.Background(), d.Role.CCPool, d, sigSession, "", nil)
	_, _ = e.finishWait(context.Background(), d.Role.CCPool, d, sigSession, "", context.Canceled)
	_ = w.Close()
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), dispatchResultKind) {
		t.Errorf("unexpected dispatch_result for success/cancel: %s", raw)
	}
}

// TestRunbookRecipes runs the jq recipes documented in
// docs/runbooks/dispatched-session-failure-signatures.md against the fixture.
func TestRunbookRecipes(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH")
	}
	fixture := filepath.Join("testdata", "dispatch-results.jsonl")
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(jq, append(args, fixture)...).CombinedOutput()
		if err != nil {
			t.Fatalf("jq %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	first := run("-s", `map(select(.kind=="dispatch_result" and .failure_signature=="git-auth"))
       | min_by(.time) | {time, role, pool, bead}`)
	if !strings.Contains(first, "2026-09-23T14:01:00") || !strings.Contains(first, `"zr-1"`) {
		t.Errorf("first-occurrence recipe = %s", first)
	}
	perDay := run("-r", `select(.kind=="dispatch_result") | "\(.time[0:10]) \(.failure_signature)"`)
	if strings.Count(perDay, "2026-09-23 git-auth") != 2 || strings.Count(perDay, "\n") != 4 {
		t.Errorf("per-day recipe = %q (hard_stop/needs_input rows must be excluded)", perDay)
	}
	byRole := run("-r", `select(.kind=="dispatch_result") | "\(.failure_signature) \(.role) \(.pool)"`)
	if !strings.Contains(byRole, "git-auth review default") || !strings.Contains(byRole, "budget worker p2") {
		t.Errorf("per-role/pool recipe = %q", byRole)
	}
}
