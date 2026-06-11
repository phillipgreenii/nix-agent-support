//go:build contract

package main

import (
	"strings"
	"testing"
	"time"
)

func TestContract_Lifecycle_NewReachesReadyAndLive(t *testing.T) {
	sb := newSandbox(t)
	out, code, _ := sb.ccpTimed(90*time.Second, "new", "alpha")
	liveAssert(t, "new exit code", code, 0)
	liveAssert(t, "new reports ready", strings.Contains(out, "ready"), true)
	out, _ = sb.ccp("doctor")
	liveAssert(t, "doctor shows alpha live", strings.Contains(out, "alpha") && strings.Contains(out, "live=true"), true)
	pending(t, "state is RECONCILED ready (doctor state= is cached)", "reconciled state query")
}

func TestContract_Lifecycle_CloseEndsSession(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "alpha"); code != 0 {
		t.Fatalf("setup new failed")
	}
	_, code, _ := sb.ccpTimed(20*time.Second, "close", "alpha")
	liveAssert(t, "close exit code", code, 0)
	// Objective: the tmux session is gone.
	out, _ := sb.ccp("doctor")
	liveAssert(t, "alpha not live after close", strings.Contains(out, "alpha") && strings.Contains(out, "live=true"), false)
}

func TestContract_Lifecycle_ClosePurgeRemovesRow(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "alpha"); code != 0 {
		t.Fatalf("setup new failed")
	}
	if _, code := sb.ccp("close", "alpha", "--purge"); code != 0 {
		t.Fatalf("close --purge failed")
	}
	out, _ := sb.ccp("list", "--all")
	liveAssert(t, "alpha purged from list", strings.Contains(out, "alpha"), false)
}

func TestContract_Cancel_StreamingInterrupts(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "s"); code != 0 {
		t.Fatalf("new failed")
	}
	sb.ccp("reply", "s", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("s", 90*time.Second) // scaffoldFails if streaming never starts
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "s")
	liveAssert(t, "cancel during streaming exits 0", code, 0)
	pending(t, "session reaches reconciled idle after cancel", "reconciled state query")
}

func TestContract_Cancel_ThinkingIsUnconfirmed(t *testing.T) {
	sb := newSandbox(t)
	if _, code := sb.ccp("new", "k"); code != 0 {
		t.Fatalf("new failed")
	}
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
	if _, code := sb.ccp("new", "i"); code != 0 {
		t.Fatalf("new failed")
	}
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
	if _, code := sb.ccp("new", "m"); code != 0 {
		t.Fatalf("new failed")
	}
	// Produce a real streaming interrupt so the pane retains "Interrupted".
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForStreaming("m", 90*time.Second)
	sb.ccp("cancel", "m") // pane now shows "Interrupted"
	// Start a fresh thinking turn; force the row to working so cancel bursts.
	sb.ccp("reply", "m", thinkingPrompt, "--no-wait")
	sb.waitForThinking("m", 30*time.Second)
	_, code, _ := sb.ccpTimed(15*time.Second, "cancel", "m")
	// BASELINE: the stale "Interrupted" line false-positives interruptLanded ->
	// thinking-cancel wrongly exits 0. Expected to FLIP to 6 (or idle) once fixed.
	baseline(t, "pg2-33gl", "stale-marker thinking cancel (false positive) exit code", code, 0)
}
