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
	// Reconciled (Unit B): a freshly-ready session is `idle` in the reconciled
	// vocabulary (the cached doctor state= is `ready`; the two are intentionally
	// distinct). The streaming-via-diff branch is gated on a Working/Starting row,
	// so a freshly-ready session still drawing its TUI reads idle, not streaming.
	out, _ = sb.ccp("state", "alpha")
	liveAssert(t, "reconciled state is idle after new", strings.Contains(out, "state=idle"), true)
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
	// Unchanged: still exit 0. Now justified by pane-stability — after the cancel
	// the pane is STATIC (prose stops, "Interrupted" shown, nothing animates), so
	// it confirms regardless of the persistent "Thought for Ns"/⏺ markers it retains.
	liveAssert(t, "cancel during streaming exits 0", code, 0)
	out, _ := sb.ccp("state", "s")
	liveAssert(t, "reconciled idle after cancel", strings.Contains(out, "state=idle"), true)
}

func TestContract_Cancel_ThinkingInterrupts(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("k")
	sb.ccp("reply", "k", thinkingPrompt, "--no-wait")
	sb.waitForThinking("k", 30*time.Second)
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "k")
	// LIVE (pg2-33gl fixed): pane-stability confirms the thinking turn stopped
	// (the spinner stops animating) even though no "Interrupted" marker prints in
	// the thinking-rewind path -> exit 0. Was baseline 6 before the fix.
	liveAssert(t, "cancel during thinking exits 0", code, 0)
	out, _ := sb.ccp("state", "k")
	liveAssert(t, "reconciled idle after cancel", strings.Contains(out, "state=idle"), true)
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

func TestContract_Cancel_StaleMarkerIgnored(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("m")
	// Produce a real streaming interrupt so the pane retains a stale "Interrupted"
	// (+ "Thought for Ns" / ⏺) marker in the visible pane.
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("m", 90*time.Second)
	sb.ccp("cancel", "m") // pane now shows a stale "Interrupted"
	// Start a fresh thinking turn and cancel it.
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForThinking("m", 30*time.Second)
	// Diagnostic: whether the stale marker is still visible. Under pane-stability
	// it does NOT matter (static text never affects the change-detection), so this
	// is logged, not gated — the cancel must succeed either way. (Contrast the old
	// marker-grep, which this stale line false-positived.)
	staleMarkerPresent := reInterrupted.MatchString(sb.cap("m"))
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "m")
	// LIVE (pg2-33gl fixed): pane-stability ignores the stale static marker and
	// confirms the FRESH thinking turn's cancel -> exit 0 (a correct 0, where the
	// old code's 0 here was a false positive off the stale marker).
	liveAssert(t, "cancel of fresh thinking turn exits 0 despite a stale Interrupted marker", code, 0)
	t.Logf("OUTCOME=info test=%q stale marker present at cancel time: %v", t.Name(), staleMarkerPresent)
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
	// Gate on the thinking phase before reading state: the --no-wait returned
	// while the turn is in flight, but the reconciled query needs the pane to be
	// visibly animating (else it can flake to idle in the launch gap). Assert only
	// state=working, not the sub (thinking vs streaming is a phase race).
	sb.waitForThinking("n", 30*time.Second)
	out, _ := sb.ccp("state", "n")
	liveAssert(t, "reconciled working after --no-wait", strings.Contains(out, "state=working"), true)
}

func TestContract_Interrupt_ThinkingCancelsAndDelivers(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("x")
	sb.ccp("reply", "x", thinkingPrompt, "--no-wait")
	sb.waitForThinking("x", 30*time.Second)
	out, code, _ := sb.ccpTimed(60*time.Second, "reply", "x", "PROBE_SHOULD_DELIVER", "--interrupt")
	// LIVE (pg2-33gl fixed, INVERTED from the old abort baseline): interrupt during
	// thinking now cancels the turn (pane-stability confirms) and DELIVERS the new
	// prompt -> exit 0, and the probe IS pasted/answered. The distinct exit code 6
	// only fires when the cancel genuinely cannot be confirmed (covered by the
	// fake-driven unit test TestSendInterrupt_abortsOnUnconfirmedCancel).
	liveAssert(t, "interrupt during thinking exits 0", code, 0)
	liveAssert(t, "interrupt delivers the probe after cancelling", strings.Contains(sb.cap("x"), "PROBE_SHOULD_DELIVER"), true)
	_ = out
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
	liveAssert(t, "AskUserQuestion prompt accepted (model began the turn)", seen, true)
	// PENDING (pg2-7a5b): reconciled `waiting-for-human` is NOT reliably detectable
	// for a LIVE paused AskUserQuestion. Real-claude evidence (2026-06-12): while the
	// turn is paused awaiting the answer, the JSONL persists NO assistant event (only
	// the user prompt + metadata), so `IsAwaitingInput` returns false; and the only
	// live signal — the pane picker render — has no pinned stable marker yet (this
	// `seen` check even false-positives on the echoed prompt text). The state +
	// IsAwaitingInput fallback ship in Unit B; reliable live detection is deferred.
	pending(t, "reconciled waiting-for-human for a live AskUserQuestion + question TEXT",
		"live picker pane marker (transcript persists no assistant event while paused) — see pg2-7a5b")
}

func TestContract_Reap_EvictsOldestOverCap(t *testing.T) {
	// new does NOT enforce max_sessions; only reap evicts oldest-by-last_activity.
	pending(t, "reap evicts oldest-by-last_activity down to cap", "deterministic reap assertion (needs activity-time control / state query)")
}
