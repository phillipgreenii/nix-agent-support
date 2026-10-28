package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Delegate-as-real-OS-process test harness -----------------------------
//
// ADR 0071 §4 Phase B, B2's Contract requires stubbing a delegate as "a
// real, separate OS process ... using Go's os/exec + stdlib primitives — no
// live Claude Code needed". Every delegate in this file is therefore still a
// real child process, started by dispatch.go's own `sh -c <Delegate.Command>`
// exactly as in production: the stub is a small POSIX shell command line
// (printf, sed, sleep, a backgrounded grandchild) built by helperCommand
// below.
//
// These stubs used to re-exec THIS test binary as the delegate. Under -race
// the test binary is race-built, and the race runtime sleeps a flat 1.0 s at
// every process exit (atexit_sleep_ms), so each delegate call cost about one
// second (43 children = 41 s of the package's 44 s). sh, printf and sleep are
// not race-built and exit at once. The race defaults are NOT changed.
//
// Why a Runner seam (the pg-decider prchange shape) does not fit here: the
// behaviour under test is dispatch.go's own process handling (sh -c, the
// context kill, cmd.WaitDelay against a leaked grandchild), and B2 requires
// real processes, so a seam that answered in process would bypass the code
// under test. Output-only stubs (abstain, decide, ...) are shell printf lines
// instead, so no test depends on a seam fake while the process semantics stay
// covered by the same real exec path.

// helperCommand builds the Delegate.Command shell string for the stub named by
// spec ("verb" or "verb:param"); see the switch for the vocabulary the tests
// in this file use.
func helperCommand(t *testing.T, spec string) string {
	t.Helper()
	verb, param, _ := strings.Cut(spec, ":")

	switch verb {
	case "abstain":
		return printOut("{}")
	case "decide":
		return printOut(fmt.Sprintf(`{"permissionDecision":%s}`, jsonString(param)))
	case "decide-nested":
		// Emits the real Claude Code hook contract's nested envelope --
		// what a delegate that is ALSO an independently-registered real
		// hook (e.g. claude-extended-tool-approver) unconditionally
		// produces -- rather than this router's own flat convention
		// (tc-6sfia).
		return printOut(fmt.Sprintf(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":%s}}`, jsonString(param)))
	case "annotate":
		return printOut(fmt.Sprintf(`{"additionalContext":%s}`, jsonString(param)))
	case "rewrite-input":
		return printOut(fmt.Sprintf(`{"updatedInput":{"marker":%s}}`, jsonString(param)))
	case "rewrite-output":
		return printOut(fmt.Sprintf(`{"updatedToolOutput":{"marker":%s}}`, jsonString(param)))
	case "rewrite-decision-input":
		return printOut(fmt.Sprintf(`{"decision":{"updatedInput":{"marker":%s}}}`, jsonString(param)))
	case "reflect-tool-input":
		return reflectField("tool_input")
	case "reflect-tool-response":
		return reflectField("tool_response")
	case "nonjson":
		return printOut("not json {{{")
	case "wrong-shape":
		return printOut(`{"permissionDecision":123}`)
	case "extra-fields":
		return printOut(`{"permissionDecision":"allow","totallyUnknownField":{"x":1}}`)
	case "sleep-then-abstain":
		// "exec sleep" replaces the shell, so the context kill reaches the
		// sleeping process itself and closes the stdout pipe at once; the
		// delegate never reaches its "{}" because it is killed first.
		ms, _ := strconv.Atoi(param)
		return "exec sleep " + sleepSeconds(ms)
	case "partial-then-hang":
		// Writes an unterminated JSON fragment, then blocks (until killed)
		// without ever closing stdout on its own -- simulates a delegate
		// killed mid-write.
		return printOut(`{"permission`) + "; exec sleep 30"
	case "leaky-grandchild":
		// Spawns a detached grandchild that inherits this process's own
		// stdout (the router's pipe) and sleeps well past both
		// delegateWaitDelay and this test's context budget, then exits
		// itself immediately -- reproducing "a delegate killed via context
		// cancellation [or a normal quick exit] that leaves a grandchild
		// process holding the pipe open" (dispatch.go's delegateWaitDelay
		// doc comment).
		ms, _ := strconv.Atoi(param)
		return "sleep " + sleepSeconds(ms) + " &"
	}
	t.Fatalf("unknown delegate stub spec %q", spec)
	return ""
}

// printOut is a shell command that writes s to stdout with no trailing
// newline.
func printOut(s string) string {
	return "printf '%s' " + shQuote(s)
}

func sleepSeconds(ms int) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// reflectField is a shell command that reads the incoming hook payload from
// stdin and echoes the raw JSON of one top-level field back as
// additionalContext, so a downstream test delegate can prove what it actually
// received (e.g. a prior delegate's rewrite, not the original payload). The
// payload dispatch.go sends is json.Marshal of a map, so it is compact; these
// tests send a single-field payload, which sed unwraps and JSON-escapes.
func reflectField(field string) string {
	return `body=$(cat | sed -e 's/^{"` + field + `":\(.*\)}$/\1/' -e 's/\\/\\\\/g' -e 's/"/\\"/g'); ` +
		`printf '{"additionalContext":"%s"}' "$body"`
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

// generousBudget is far above any plausible loaded-machine delegate latency;
// merge-outcome tests use it so the production 6s TotalBudget (which each
// delegate process can eat a large share of under load) cannot turn a
// later delegate into a Skipped one and flip the merge result (pg2-s2n49).
const generousBudget = 10 * time.Minute

func dispatchGenerous(ctx context.Context, event string, delegates []Delegate, payload map[string]json.RawMessage, projectDir, dataDir string) Result {
	return dispatch(ctx, generousBudget, delegateWaitDelay, event, delegates, payload, projectDir, dataDir)
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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, payload, "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PostToolUse", delegates, payload, "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PermissionRequest", delegates, payload, "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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
			result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

// --- Nested hookSpecificOutput envelope (tc-6sfia) --------------------------

// TestDispatchNestedHookSpecificOutputEnvelopeIsHonoredNotAbstained covers
// tc-6sfia's empirical reproduction: a delegate that is ALSO an
// independently-registered real Claude Code hook (e.g.
// claude-extended-tool-approver) unconditionally nests its response under
// "hookSpecificOutput" per the real Claude Code hook contract, rather than
// this router's own flat convention. Before the fix, that decision was
// silently dropped by json.Unmarshal and the delegate was treated as
// Abstain end to end.
func TestDispatchNestedHookSpecificOutputEnvelopeIsHonoredNotAbstained(t *testing.T) {
	delegates := []Delegate{helperDelegate(t, "nested-delegate", "decide", 1, "decide-nested:allow")}

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

	if result.Entries[0].Verdict != VerdictApplied {
		t.Errorf("verdict = %q, want %q (the nested envelope must be unwrapped, not treated as abstain)", result.Entries[0].Verdict, VerdictApplied)
	}
	if result.Output.PermissionDecision != "allow" {
		t.Errorf("PermissionDecision = %q, want %q", result.Output.PermissionDecision, "allow")
	}
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
// WaitDelay elapses. The wait delay is injected (a short one, via the
// explicit dispatch seam) rather than really waiting out the production
// delegateWaitDelay of 2s (ADR 0111, pg2-b495dq); the grandchild still sleeps
// well past it, so the mechanism is exercised exactly as before.
func TestDispatchHangingDelegateBoundedByWaitDelayMechanism(t *testing.T) {
	const waitDelay = 300 * time.Millisecond
	delegates := []Delegate{helperDelegate(t, "leaky", "decide", 1, "leaky-grandchild:4000")}

	start := time.Now()
	result := dispatch(context.Background(), TotalBudget, waitDelay, "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())
	elapsed := time.Since(start)

	if elapsed < waitDelay-50*time.Millisecond {
		t.Errorf("Dispatch returned after only %s -- want it to actually observe the leaky pipe for the %s wait delay (suspiciously fast, WaitDelay may not be exercised)", elapsed, waitDelay)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("Dispatch took %s, want it bounded by the %s wait delay, not the grandchild's 4s sleep or the 6s TotalBudget", elapsed, waitDelay)
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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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

	result := dispatchGenerous(context.Background(), "PreToolUse", delegates, rawPayload(nil), "", t.TempDir())

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
