package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Delegate-as-real-OS-process test harness -----------------------------
//
// ADR 0071 §4 Phase B, B2's Contract requires stubbing a delegate as "a
// real, separate OS process ... using Go's os/exec + stdlib primitives — no
// live Claude Code needed". Rather than shipping standalone shell fixtures
// (that is packet B3's job, against the real built binary), these tests
// re-exec THIS test binary itself as the delegate subprocess, in the same
// idiom Go's own os/exec tests use (see os/exec/exec_test.go's
// TestHelperProcess pattern): TestMain intercepts before any test-flag
// parsing when GO_WANT_HELPER_PROCESS=1 is set, so the recursive invocation
// never runs this package's actual Test* functions.

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		runHelperProcess()
		return
	}
	os.Exit(m.Run())
}

// runHelperProcess is the delegate-stub entry point. spec (everything after
// "--") selects the stub behavior; see the switch below for the vocabulary
// the tests in this file use.
func runHelperProcess() {
	args := os.Args[1:]
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) == 0 {
		os.Exit(1)
	}
	spec := args[0]
	verb, param, _ := strings.Cut(spec, ":")

	switch verb {
	case "abstain":
		fmt.Print("{}")
	case "decide":
		fmt.Printf(`{"permissionDecision":%s}`, jsonString(param))
	case "annotate":
		fmt.Printf(`{"additionalContext":%s}`, jsonString(param))
	case "rewrite-input":
		fmt.Printf(`{"updatedInput":{"marker":%s}}`, jsonString(param))
	case "rewrite-output":
		fmt.Printf(`{"updatedToolOutput":{"marker":%s}}`, jsonString(param))
	case "rewrite-decision-input":
		fmt.Printf(`{"decision":{"updatedInput":{"marker":%s}}}`, jsonString(param))
	case "reflect-tool-input":
		reflectField("tool_input")
	case "reflect-tool-response":
		reflectField("tool_response")
	case "nonjson":
		fmt.Print("not json {{{")
	case "wrong-shape":
		fmt.Print(`{"permissionDecision":123}`)
	case "extra-fields":
		fmt.Print(`{"permissionDecision":"allow","totallyUnknownField":{"x":1}}`)
	case "sleep-then-abstain":
		ms, _ := strconv.Atoi(param)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		fmt.Print("{}")
	case "partial-then-hang":
		// Writes an unterminated JSON fragment, then blocks forever (until
		// killed) without ever closing stdout on its own — simulates a
		// delegate killed mid-write.
		fmt.Print(`{"permission`)
		select {}
	case "leaky-grandchild":
		// Spawns a detached grandchild that inherits this process's own
		// stdout (the router's pipe) and sleeps well past both
		// delegateWaitDelay and this test's context budget, then exits
		// itself immediately -- reproducing "a delegate killed via context
		// cancellation [or a normal quick exit] that leaves a grandchild
		// process holding the pipe open" (dispatch.go's delegateWaitDelay
		// doc comment).
		gc := exec.Command(os.Args[0], "--", "leaky-grandchild-sleep:"+param)
		gc.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		gc.Stdout = os.Stdout
		_ = gc.Start()
		os.Exit(0)
	case "leaky-grandchild-sleep":
		ms, _ := strconv.Atoi(param)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(0)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// reflectField reads the incoming hook payload from stdin and echoes the
// raw JSON of one top-level field back as additionalContext, so a
// downstream test delegate can prove what it actually received (e.g. a
// prior delegate's rewrite, not the original payload).
func reflectField(field string) {
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		fmt.Print("{}")
		return
	}
	out := map[string]string{}
	if val, ok := payload[field]; ok {
		out["additionalContext"] = string(val)
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(b)
}

// helperCommand builds a Delegate.Command shell string that re-execs this
// test binary as a delegate stub running spec, via runHelperProcess above.
// "exec" replaces the shell outright (no lingering "sh" fork), which keeps
// the process-tree assumptions in the WaitDelay/leaky-grandchild test exact.
func helperCommand(t *testing.T, spec string) string {
	t.Helper()
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	return fmt.Sprintf("GO_WANT_HELPER_PROCESS=1 exec %s -- %s", shQuote(bin), shQuote(spec))
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func helperDelegate(t *testing.T, name, contract string, priority int, spec string) Delegate {
	t.Helper()
	return Delegate{
		Name:     name,
		Event:    "PreToolUse",
		Matcher:  "*",
		Command:  helperCommand(t, spec),
		Contract: contract,
		Priority: priority,
	}
}

func rawPayload(fields map[string]string) map[string]json.RawMessage {
	m := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		m[k] = json.RawMessage(v)
	}
	return m
}

// --- Sequential dispatch order ---------------------------------------------

// TestDispatchSequentialOrderByPriority covers "Sequential dispatch in
// priority order" (ADR 0071 §4 Phase B, B1): delegates run in ascending
// Priority regardless of their order in the input slice, observable via the
// additionalContext join order (mergeState joins in apply-call/dispatch
// order).
func TestDispatchSequentialOrderByPriority(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "third", "annotate", 3, "annotate:third"),
		helperDelegate(t, "first", "annotate", 1, "annotate:first"),
		helperDelegate(t, "second", "annotate", 2, "annotate:second"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	want := "first\nsecond\nthird"
	if got := result.Output.AdditionalContext; got != want {
		t.Errorf("AdditionalContext = %q, want %q (priority order)", got, want)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("got %d attribution entries, want 3", len(result.Entries))
	}
	gotOrder := []string{result.Entries[0].DelegateName, result.Entries[1].DelegateName, result.Entries[2].DelegateName}
	wantOrder := []string{"first", "second", "third"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Errorf("Entries[%d].DelegateName = %q, want %q", i, gotOrder[i], wantOrder[i])
		}
	}
}

// --- Cumulative rewrite passthrough (all three rewrite-shaped fields) -----

// TestDispatchCumulativeRewritePassthroughUpdatedInput covers PreToolUse's
// updatedInput rewrite field.
func TestDispatchCumulativeRewritePassthroughUpdatedInput(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "rewriter", "rewrite", 1, "rewrite-input:hello"),
		helperDelegate(t, "observer", "annotate", 2, "reflect-tool-input"),
	}
	payload := rawPayload(map[string]string{"tool_input": `{"command":"echo original"}`})

	result := Dispatch(context.Background(), "PreToolUse", delegates, payload, "", t.TempDir())

	want := `{"marker":"hello"}`
	if got := result.Output.AdditionalContext; got != want {
		t.Errorf("second delegate saw tool_input = %q, want the first delegate's rewrite %q", got, want)
	}
}

// TestDispatchCumulativeRewritePassthroughUpdatedToolOutput covers
// PostToolUse's updatedToolOutput rewrite field.
func TestDispatchCumulativeRewritePassthroughUpdatedToolOutput(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "rewriter", "rewrite", 1, "rewrite-output:hello2"),
		helperDelegate(t, "observer", "annotate", 2, "reflect-tool-response"),
	}
	payload := rawPayload(map[string]string{"tool_response": `{"status":"ok"}`})

	result := Dispatch(context.Background(), "PostToolUse", delegates, payload, "", t.TempDir())

	want := `{"marker":"hello2"}`
	if got := result.Output.AdditionalContext; got != want {
		t.Errorf("second delegate saw tool_response = %q, want the first delegate's rewrite %q", got, want)
	}
}

// TestDispatchCumulativeRewritePassthroughDecisionUpdatedInput covers
// PermissionRequest's nested decision.updatedInput rewrite field, which
// advanceCumulativePayload folds into the same "tool_input" key the next
// delegate sees.
func TestDispatchCumulativeRewritePassthroughDecisionUpdatedInput(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "rewriter", "decide", 1, "rewrite-decision-input:hello3"),
		helperDelegate(t, "observer", "annotate", 2, "reflect-tool-input"),
	}
	payload := rawPayload(map[string]string{"tool_input": `{"command":"echo original"}`})

	result := Dispatch(context.Background(), "PermissionRequest", delegates, payload, "", t.TempDir())

	want := `{"marker":"hello3"}`
	if got := result.Output.AdditionalContext; got != want {
		t.Errorf("second delegate saw tool_input = %q, want the first delegate's decision.updatedInput rewrite %q", got, want)
	}
}

// --- Abstain / errored / malformed ------------------------------------------

// TestDispatchAbstainDoesNotShortCircuit covers "Abstain-continues (not
// short-circuit)".
func TestDispatchAbstainDoesNotShortCircuit(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "abstainer", "decide", 1, "abstain"),
		helperDelegate(t, "contributor", "annotate", 2, "annotate:contributed"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Output.PermissionDecision != "" {
		t.Errorf("PermissionDecision = %q, want empty (the abstainer contributed nothing)", result.Output.PermissionDecision)
	}
	if result.Output.AdditionalContext != "contributed" {
		t.Errorf("AdditionalContext = %q, want %q", result.Output.AdditionalContext, "contributed")
	}
}

// TestDispatchMalformedOutputTreatedAsAbstainChainContinues covers "Errored/
// malformed delegate output treated as Abstain, chain continues" for two
// malformed shapes: non-JSON stdout, and schema-valid-but-wrong-shape JSON
// (a permissionDecision typed as a number).
func TestDispatchMalformedOutputTreatedAsAbstainChainContinues(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "nonjson", "decide", 1, "nonjson"),
		helperDelegate(t, "wrongshape", "decide", 2, "wrong-shape"),
		helperDelegate(t, "contributor", "annotate", 3, "annotate:contributed"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Output.PermissionDecision != "" {
		t.Errorf("PermissionDecision = %q, want empty (both decide delegates were malformed)", result.Output.PermissionDecision)
	}
	if result.Output.AdditionalContext != "contributed" {
		t.Errorf("AdditionalContext = %q, want %q (the chain must continue past both malformed delegates)", result.Output.AdditionalContext, "contributed")
	}
	if len(result.Entries) != 3 {
		t.Fatalf("got %d attribution entries, want 3", len(result.Entries))
	}
	if result.Entries[0].Verdict != VerdictError {
		t.Errorf("nonjson delegate verdict = %q, want %q", result.Entries[0].Verdict, VerdictError)
	}
	if result.Entries[1].Verdict != VerdictError {
		t.Errorf("wrong-shape delegate verdict = %q, want %q", result.Entries[1].Verdict, VerdictError)
	}
	if result.Entries[2].Verdict != VerdictApplied {
		t.Errorf("contributor delegate verdict = %q, want %q", result.Entries[2].Verdict, VerdictApplied)
	}
}

// TestDispatchExtraFieldsIgnoredGracefully covers "valid JSON with
// unexpected extra fields (must be ignored gracefully)".
func TestDispatchExtraFieldsIgnoredGracefully(t *testing.T) {
	delegates := []Delegate{helperDelegate(t, "forward-compat", "decide", 1, "extra-fields")}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Entries[0].Verdict != VerdictApplied {
		t.Errorf("verdict = %q, want %q (an unknown extra field must not error the parse)", result.Entries[0].Verdict, VerdictApplied)
	}
	if result.Output.PermissionDecision != "allow" {
		t.Errorf("PermissionDecision = %q, want %q", result.Output.PermissionDecision, "allow")
	}
}

// TestDispatchCommandNotFoundIsAbstainNotFatal covers "a delegate command
// that doesn't exist/isn't executable".
func TestDispatchCommandNotFoundIsAbstainNotFatal(t *testing.T) {
	delegates := []Delegate{
		{Name: "missing", Event: "PreToolUse", Matcher: "*", Command: "/definitely/does/not/exist/claude-hook-router-test-xyz", Contract: "decide", Priority: 1},
		helperDelegate(t, "contributor", "annotate", 2, "annotate:contributed"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Entries[0].Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q", result.Entries[0].Verdict, VerdictError)
	}
	if result.Output.AdditionalContext != "contributed" {
		t.Errorf("AdditionalContext = %q, want %q (the chain must continue)", result.Output.AdditionalContext, "contributed")
	}
}

// --- Merge policy table through a real 2-delegate chain --------------------

// TestDispatchPermissionMergeTableThroughRealDispatch is the
// through-real-subprocesses counterpart of merge_test.go's
// TestMergePermissionDecisionTableAllOrderedPairs, which already exhaustively
// covers all 25 ordered pairs of {allow,ask,deny,defer,abstain} directly
// against mergeState (the exact same fold Dispatch uses internally). This
// test instead proves that wiring is exercised correctly end to end through
// two REAL delegate subprocesses and the actual Dispatch loop, per ADR 0071
// §4 Phase B, B1 "Merge policy table" — on a representative sample (an
// override in each direction, a same-rank tie, and both abstain
// combinations) rather than the full 25, since re-running the full
// cross-product through real subprocesses adds real wall-clock time with no
// additional coverage beyond what the exhaustive mergeState-level table
// already proves.
func TestDispatchPermissionMergeTableThroughRealDispatch(t *testing.T) {
	cases := [][2]string{
		{"allow", "allow"},     // same-rank tie
		{"allow", "deny"},      // override upward, weakest to strongest
		{"deny", "allow"},      // no override downward, strongest stays
		{"ask", "defer"},       // override upward, mid ranks
		{"defer", "ask"},       // no override downward, mid ranks
		{"abstain", "deny"},    // abstain contributes nothing, second wins
		{"deny", "abstain"},    // abstain contributes nothing, first stays
		{"abstain", "abstain"}, // both abstain: no opinion at all
	}
	for _, pair := range cases {
		first, second := pair[0], pair[1]
		t.Run(first+"_then_"+second, func(t *testing.T) {
			delegates := []Delegate{
				helperDelegate(t, "d1", "decide", 1, specFor(first)),
				helperDelegate(t, "d2", "decide", 2, specFor(second)),
			}
			result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

			want := expectedWinner([]string{first, second})
			if got := result.Output.PermissionDecision; got != want {
				t.Errorf("chain (%s, %s): PermissionDecision = %q, want %q", first, second, got, want)
			}
		})
	}
}

func specFor(permissionValue string) string {
	if permissionValue == "abstain" {
		return "abstain"
	}
	return "decide:" + permissionValue
}

// --- Total time budget -------------------------------------------------

// TestDispatchTotalBudgetExhaustionSkipsLaterDelegateAndReturnsPromptly
// covers "Total-budget enforcement across the whole chain (not
// per-delegate)" AND the acceptance criterion that a stub genuinely sleeping
// past budget still lets the router return promptly: delegate one is a
// real subprocess that sleeps well past a short total budget (so it is
// killed via context cancellation, not a config assertion), and delegate
// two -- which would individually have plenty of time if the budget were
// per-delegate -- is Skipped outright because the SHARED chain budget was
// already exhausted by the time its turn came.
func TestDispatchTotalBudgetExhaustionSkipsLaterDelegateAndReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	delegates := []Delegate{
		helperDelegate(t, "slow", "decide", 1, "sleep-then-abstain:5000"),
		helperDelegate(t, "never-invoked", "annotate", 2, "annotate:should-not-appear"),
	}

	start := time.Now()
	result := Dispatch(ctx, "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("Dispatch took %s, want well under its 6s TotalBudget default (proves the router does not hang on a slow delegate)", elapsed)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("got %d attribution entries, want 2", len(result.Entries))
	}
	if result.Entries[0].Verdict != VerdictError {
		t.Errorf("slow delegate verdict = %q, want %q (killed by budget exhaustion)", result.Entries[0].Verdict, VerdictError)
	}
	if result.Entries[1].Verdict != VerdictSkipped {
		t.Errorf("second delegate verdict = %q, want %q (shared chain budget already exhausted before its turn)", result.Entries[1].Verdict, VerdictSkipped)
	}
	if !result.Output.IsZero() {
		t.Errorf("Output = %#v, want the zero HookOutput (nothing contributed)", result.Output)
	}
}

// TestDispatchHangingDelegateBoundedByWaitDelayMechanism covers "a delegate
// that hangs past budget ..., asserting the router still returns within its
// overall budget, and asserting cmd.WaitDelay is actually set (the
// mechanism for the killed-child-leaves-fds-open hang)". The delegate here
// exits almost immediately but leaves a detached grandchild holding the
// stdout pipe open for several seconds -- exactly the scenario
// dispatch.go's delegateWaitDelay doc comment describes. Without
// cmd.WaitDelay actually wired up, this would hang for the grandchild's
// full sleep (or the full 6s TotalBudget); with it, Dispatch returns once
// WaitDelay (2s) elapses.
func TestDispatchHangingDelegateBoundedByWaitDelayMechanism(t *testing.T) {
	delegates := []Delegate{helperDelegate(t, "leaky", "decide", 1, "leaky-grandchild:4000")}

	start := time.Now()
	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())
	elapsed := time.Since(start)

	if elapsed < 1500*time.Millisecond {
		t.Errorf("Dispatch returned after only %s -- want it to actually observe the leaky pipe (suspiciously fast, WaitDelay may not be exercised)", elapsed)
	}
	if elapsed > 3500*time.Millisecond {
		t.Errorf("Dispatch took %s, want it bounded by delegateWaitDelay (2s), not the grandchild's 4s sleep or the 6s TotalBudget", elapsed)
	}
	if result.Entries[0].Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q (stdout was force-closed empty/incomplete)", result.Entries[0].Verdict, VerdictError)
	}
}

// TestDispatchKilledMidWritePartialFragmentIsAbstain covers "a delegate
// killed mid-write (SIGKILL) and a delegate that writes a partial JSON
// fragment then hangs" as the single combined scenario the packet describes
// -- both collapse to the same Abstain-mapped outcome, and this delegate
// itself (not a grandchild) is what hangs, so it is killed directly and
// Dispatch returns quickly, without waiting out delegateWaitDelay.
func TestDispatchKilledMidWritePartialFragmentIsAbstain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	delegates := []Delegate{helperDelegate(t, "partial", "decide", 1, "partial-then-hang")}

	start := time.Now()
	result := Dispatch(ctx, "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Errorf("Dispatch took %s, want it to return quickly (this delegate, not a grandchild, holds the pipe, so no WaitDelay wait is needed)", elapsed)
	}
	if result.Entries[0].Verdict != VerdictError {
		t.Errorf("verdict = %q, want %q (an unterminated JSON fragment cannot parse)", result.Entries[0].Verdict, VerdictError)
	}
	if !result.Output.IsZero() {
		t.Errorf("Output = %#v, want the zero HookOutput", result.Output)
	}
}

// --- Partial-chain failure semantics ----------------------------------------

// TestDispatchPartialChainFailurePreservesPriorContributions covers
// "Partial-chain failure semantics: mid-chain delegate (not first, not
// last) ... errors ⇒ router returns the merge of whatever prior delegates
// already contributed" -- and proves the chain still reaches the delegate
// after the failing one.
func TestDispatchPartialChainFailurePreservesPriorContributions(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "first", "decide", 1, "decide:ask"),
		{Name: "middle-fails", Event: "PreToolUse", Matcher: "*", Command: "/definitely/does/not/exist/claude-hook-router-test-xyz", Contract: "decide", Priority: 2},
		helperDelegate(t, "last", "annotate", 3, "annotate:tail"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Output.PermissionDecision != "ask" {
		t.Errorf("PermissionDecision = %q, want %q (the first delegate's contribution must survive the mid-chain failure)", result.Output.PermissionDecision, "ask")
	}
	if result.Output.AdditionalContext != "tail" {
		t.Errorf("AdditionalContext = %q, want %q (the chain must still reach the delegate after the failing one)", result.Output.AdditionalContext, "tail")
	}
}

// --- observe contract -------------------------------------------------------

// TestDispatchObserveContractResponseIsAlwaysDiscarded covers "observe ...
// invoked purely for its side effect": even a well-formed, opinionated
// response from an observe-contract delegate must never affect the merge.
func TestDispatchObserveContractResponseIsAlwaysDiscarded(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "observer", "observe", 1, "decide:deny"),
		helperDelegate(t, "decider", "decide", 2, "decide:allow"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Entries[0].Verdict != VerdictApplied {
		t.Errorf("observer verdict = %q, want %q (it did return valid output; it is DISCARDED, not erroring)", result.Entries[0].Verdict, VerdictApplied)
	}
	if result.Output.PermissionDecision != "allow" {
		t.Errorf("PermissionDecision = %q, want %q (the observer's \"deny\" must never reach the merge)", result.Output.PermissionDecision, "allow")
	}
}

// TestDispatchAllObserveShortCircuitReturnsEmptyResult covers "All-observe
// short-circuit: the merge computation never ran at all ... every response
// is discarded and the router returns {} unconditionally", while still
// dispatching every observer sequentially.
func TestDispatchAllObserveShortCircuitReturnsEmptyResult(t *testing.T) {
	delegates := []Delegate{
		helperDelegate(t, "observer-one", "observe", 1, "decide:deny"),
		helperDelegate(t, "observer-two", "observe", 2, "annotate:should-not-appear"),
	}

	result := Dispatch(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if !result.Output.IsZero() {
		t.Errorf("Output = %#v, want the zero HookOutput", result.Output)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("got %d attribution entries, want 2 (both observers still dispatched)", len(result.Entries))
	}
	for i, e := range result.Entries {
		if e.Verdict != VerdictApplied {
			t.Errorf("Entries[%d].Verdict = %q, want %q", i, e.Verdict, VerdictApplied)
		}
	}
}
