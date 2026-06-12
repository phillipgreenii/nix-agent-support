//go:build contract

package main

import (
	"strings"
	"testing"
	"time"
)

// sessionLineHas reports whether the doctor/list line naming `session` also
// contains `marker`. Matching on the SAME line avoids a false positive where
// one row names the session and an unrelated row carries the marker.
func sessionLineHas(out, session, marker string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, session) && strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

func TestContract_Lifecycle_NewReachesReadyAndLive(t *testing.T) {
	sb := newSandbox(t)
	out, code, _ := sb.ccpTimed(90*time.Second, "new", "alpha")
	liveAssert(t, "new exit code", code, 0)
	liveAssert(t, "new reports ready", strings.Contains(out, "ready"), true)
	out, _ = sb.ccp("doctor")
	liveAssert(t, "doctor shows alpha live", sessionLineHas(out, "alpha", "live=true"), true)
	pending(t, "state is RECONCILED ready (doctor state= is cached)", "reconciled state query")
}

func TestContract_Lifecycle_CloseEndsSession(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("alpha")
	_, code, _ := sb.ccpTimed(20*time.Second, "close", "alpha")
	liveAssert(t, "close exit code", code, 0)
	// Objective: the tmux session is gone.
	out, _ := sb.ccp("doctor")
	liveAssert(t, "alpha not live after close", sessionLineHas(out, "alpha", "live=true"), false)
}

func TestContract_Lifecycle_ClosePurgeRemovesRow(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("alpha")
	if _, code := sb.ccp("close", "alpha", "--purge"); code != 0 {
		t.Fatalf("close --purge failed")
	}
	out, _ := sb.ccp("list", "--all")
	liveAssert(t, "alpha purged from list", strings.Contains(out, "alpha"), false)
}

func TestContract_Cancel_StreamingInterrupts(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("s")
	sb.ccp("reply", "s", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("s", 90*time.Second) // scaffoldFails if streaming never starts
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "s")
	liveAssert(t, "cancel during streaming exits 0", code, 0)
	pending(t, "session reaches reconciled idle after cancel", "reconciled state query")
}

func TestContract_Cancel_ThinkingIsUnconfirmed(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("k")
	sb.ccp("reply", "k", thinkingPrompt, "--no-wait")
	sb.waitForThinking("k", 30*time.Second)
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "k")
	// BASELINE: today thinking-cancel cannot be confirmed -> exit 6. Pinning the
	// observed value means a future fix (exit 0) trips this and forces re-triage.
	baseline(t, "pg2-33gl", "cancel during thinking exit code", code, 6)
	pending(t, "thinking cancel should reach idle + exit 0", "reconciled state query / cancel fix")
}

func TestContract_Cancel_IdleNormalizes(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("i")
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "i") // ready/idle session
	liveAssert(t, "cancel on idle exits 0", code, 0)
}

func TestContract_Cancel_NonexistentErrors(t *testing.T) {
	sb := newSandbox(t)
	_, code := sb.ccp("cancel", "ghost")
	liveAssert(t, "cancel nonexistent is non-zero", code != 0, true)
}

func TestContract_Cancel_StaleMarkerFalsePositive(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("m")
	// Produce a real streaming interrupt so the pane retains "Interrupted".
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("m", 90*time.Second)
	sb.ccp("cancel", "m") // pane now shows "Interrupted"
	// Start a fresh thinking turn; force the row to working so cancel bursts.
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForThinking("m", 30*time.Second)
	// Precondition: the false-positive case requires the stale "Interrupted"
	// marker to STILL be visible in the pane (tmux capture-pane has no
	// scrollback). If a fresh thinking turn scrolled it out, interruptLanded
	// would return false -> exit 6, falsely signalling the pg2-33gl bug got
	// fixed. That is a setup failure, not a contract change.
	if !reInterrupted.MatchString(sb.cap("m")) {
		scaffoldFail(t, "stale Interrupted marker scrolled out of the visible pane; cannot set up the false-positive case")
	}
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "m")
	// BASELINE: the stale "Interrupted" line false-positives interruptLanded ->
	// thinking-cancel wrongly exits 0. Expected to FLIP to 6 (or idle) once fixed.
	baseline(t, "pg2-33gl", "stale-marker thinking cancel (false positive) exit code", code, 0)
}

func TestContract_Send_BusyRefused(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("b")
	sb.ccp("reply", "b", thinkingPrompt, "--no-wait")
	sb.waitForThinking("b", 30*time.Second)
	_, code := sb.ccp("reply", "b", "second message") // no flags -> ModeRefuseIfBusy
	baseline(t, "n/a", "reply on busy session exit code", code, 5)
}

func TestContract_Send_NoWaitReturnsImmediately(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("n")
	_, code, elapsed := sb.ccpTimed(20*time.Second, "reply", "n", thinkingPrompt, "--no-wait")
	liveAssert(t, "--no-wait exit 0", code, 0)
	liveAssert(t, "--no-wait returns under 15s (does not block on the turn)", elapsed < 15*time.Second, true)
	pending(t, "row is 'working' after --no-wait", "reconciled state query")
}

func TestContract_Interrupt_ThinkingAborts(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("x")
	sb.ccp("reply", "x", thinkingPrompt, "--no-wait")
	sb.waitForThinking("x", 30*time.Second)
	out, code, _ := sb.ccpTimed(20*time.Second, "reply", "x", "PROBE_MUST_NOT_DELIVER", "--interrupt")
	// BASELINE: interrupt during thinking cannot confirm the cancel -> aborts -> exit 1.
	baseline(t, "pg2-33gl", "reply --interrupt during thinking exit code", code, 1)
	liveAssert(t, "interrupt abort does not paste the probe", strings.Contains(sb.cap("x"), "PROBE_MUST_NOT_DELIVER"), false)
	_ = out
	pending(t, "interrupt should carry a distinct exit code, not generic 1", "distinct interrupt exit code (exit-code-1-is-general-error)")
}

// attendFixture stands up N live sessions and sets their store states.
func attendFixture(t *testing.T, sb *sandbox, states map[string]string) {
	t.Helper()
	for name := range states {
		sb.mustNew(name)
	}
	for name, st := range states {
		sb.setState(name, st) // both a row AND a live pane (filter drops paneless rows)
	}
}

func TestContract_Attend_NoTTYListsCandidates(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"q1": "needs_input", "q2": "needs_input", "q3": "done"})
	out, code := sb.ccp("attend") // stdin is not a TTY under go test
	liveAssert(t, "attend no-TTY exit 0", code, 0)
	liveAssert(t, "lists q1", strings.Contains(out, "q1"), true)
	liveAssert(t, "lists q2", strings.Contains(out, "q2"), true)
	liveAssert(t, "excludes done q3", strings.Contains(out, "q3"), false)
}

func TestContract_Attend_IncludeDone(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"q1": "needs_input", "q3": "done"})
	out, code := sb.ccp("attend", "--include-done")
	liveAssert(t, "attend --include-done exit 0", code, 0)
	liveAssert(t, "includes done q3", strings.Contains(out, "q3"), true)
}

func TestContract_Attend_ZeroCandidates(t *testing.T) {
	sb := newSandbox(t)
	attendFixture(t, sb, map[string]string{"r1": "ready"})
	out, code := sb.ccp("attend")
	liveAssert(t, "attend zero exit 0", code, 0)
	liveAssert(t, "says none waiting", strings.Contains(out, "no sessions waiting"), true)
}

func TestContract_Attend_NumberedAndFzfBranchSelection(t *testing.T) {
	pending(t, "attend branch selection (no-TTY/fzf/numbered) + numbered-index parse now covered by plain unit tests in attend_test.go (TestPickCandidate_*, TestPickNumbered_Parse)", "nothing further here; only the live fzf subprocess exec stays out of contract scope (real process)")
}

func TestContract_NeedsInput_AskUserQuestionViaTranscriptFallback(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("a")
	const askPrompt = "Use the AskUserQuestion tool right now as your first action: ask 'CCPROBE which path?' with options 'Alpha' and 'Bravo'. Do nothing else first."
	sb.ccp("reply", "a", askPrompt, "--no-wait")
	// The AskUserQuestion gap: no Notification hook fires; ccpool detects it via the
	// transcript only on a blocking wait. Here we just confirm the picker renders.
	deadline := time.Now().Add(90 * time.Second)
	seen := false
	for time.Now().Before(deadline) {
		if strings.Contains(sb.cap("a"), "Alpha") || strings.Contains(sb.cap("a"), "CCPROBE") {
			seen = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !seen {
		scaffoldFail(t, "AskUserQuestion picker never rendered (model may not have called the tool)")
	}
	liveAssert(t, "AskUserQuestion picker rendered", seen, true)
	pending(t, "row reaches needs_input + the pending question text is queryable", "reconciled state + associated info (AskUserQuestion gap)")
}

func TestContract_Reap_EvictsOldestOverCap(t *testing.T) {
	// new does NOT enforce max_sessions; only reap evicts oldest-by-last_activity.
	pending(t, "reap evicts oldest-by-last_activity down to cap", "deterministic reap assertion (needs activity-time control / state query)")
}
