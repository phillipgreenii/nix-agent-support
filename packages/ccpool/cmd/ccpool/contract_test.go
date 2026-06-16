//go:build contract

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/eventlog"
)

// SCOPE POLICY (pg2-tnmb): every test in this file MUST make at least one
// observation that depends on REAL Claude Code behaving a specific way — that is
// what "contract" means: pin the Claude TUI/runtime contract so a Claude Code
// upgrade localizes drift here. Tests that would pass against a dummy/stub claude
// (pure CLI arg handling, store-state filtering with injected states, generic
// eviction/purge math) are NOT contract tests; they live as plain unit/integration
// tests and were removed from this suite. Specifically relocated, with their
// coverage preserved elsewhere:
//   - Cancel of a nonexistent/not-live session -> session.TestCancel_notLiveErrors
//     (+ cmd TestCancelExitCode for the exit-code mapping).
//   - Cancel of an idle (ready) session is a no-op success ->
//     session.TestCancel_idleNormalizesToReady.
//   - attend candidate filtering (needs_input-only, dead-pane filtered,
//     --include-done) -> cmd TestAttendCandidates + TestPickCandidate_* /
//     TestPickNumbered_Parse.
//   - reap oldest-by-last_activity SELECTION -> session.TestReap_overCapClosesOldestFirst
//     and cmd TestReap_closesOverCap (fake-claude + real tmux).
//   - close --purge removes the store row -> session.TestClose_purgeDeletesRow +
//     cmd TestClose_endsTheSession.
// What stays here is only what real claude alone can verify.

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

// TestContract_NeedsInput_AskUserQuestionViaHook pins the deterministic, hook-driven
// needs_input detection (pg2-7a5b, pg2-r0zz). The PreToolUse/AskUserQuestion hook
// fires the instant the model invokes the tool — even in -p — flipping the row to
// needs_input and recording the question text, NON-BLOCKING so the picker still
// renders. We drive a REAL turn that calls AskUserQuestion, then poll `state --json`
// until the reconciled state is waiting-for-human and assert the question field
// carries the probe text (parsed from JSON — never a raw-pane substring, which the
// old transcript-fallback scenario false-positived on echoed prompt text).
func TestContract_NeedsInput_AskUserQuestionViaHook(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("a")
	const probe = "CCPROBE which path"
	const askPrompt = "Use the AskUserQuestion tool right now as your first action: ask '" + probe + "?' with options 'Alpha' and 'Bravo'. Do nothing else first."
	sb.ccp("reply", "a", askPrompt, "--no-wait")

	// Poll the reconciled state JSON until the hook has flipped the row to
	// waiting-for-human. A generous deadline; a turn that never starts is a
	// real-claude/env issue, not a ccpool verdict -> scaffoldFail.
	deadline := time.Now().Add(90 * time.Second)
	var sj stateJSON
	waiting := false
	for time.Now().Before(deadline) {
		out, _ := sb.ccp("state", "a", "--json")
		var got stateJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err == nil && got.State == "waiting-for-human" {
			sj = got
			waiting = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !waiting {
		scaffoldFail(t, "state never reached waiting-for-human via the AskUserQuestion hook (model may not have called the tool, or the turn never started)")
	}
	liveAssert(t, "reconciled waiting-for-human (hook-driven)", true, true)
	// The question field is the hook-recorded AskUserQuestion text; assert it carries
	// the probe (parsed from JSON, not a raw-pane substring).
	liveAssert(t, "state JSON question field carries the probe question text", strings.Contains(sj.Question, probe), true)
}

// TestContract_Canary_GoldenMarkerRendersInPane is the SINGLE intentional pane
// assertion in this suite. Every other scenario keeps its real-claude pin on the
// state/event-log layer (pane-assertion-free by design — see the contract-harness
// design doc), because pane text is the most upgrade-fragile surface Claude Code
// has. This one canary deliberately depends on it: it drives a KNOWN golden marker
// all the way out to the visible pane and asserts it renders, so that if a Claude
// Code upgrade ever breaks the pane-capture path itself (capture-pane returns
// nothing, the TUI stops echoing model output, etc.) it localizes HERE as a
// low-cost early warning, rather than silently degrading the gates the other
// scenarios lean on.
//
// Marker strategy / why it dodges the prompt-echo false-positive trap that the
// AskUserQuestion `seen` check hit (see pg2-7a5b note above): the prompt never
// contains the literal string we assert. We hand the model the prefix "CCGOLDEN"
// and a unique numeric nonce as SEPARATE tokens and instruct it to concatenate
// them with NO separator. The asserted marker `CCGOLDEN<nonce>` therefore appears
// in the pane ONLY because the model actually produced that output and the pane
// rendered it — a verbatim copy of the prompt text (TUI prompt-echo) cannot
// satisfy the check, since the joined form is absent from the prompt. The nonce
// (pid + UnixNano) also makes the marker unique per run, so it can't collide with
// TUI chrome or anything left over in scrollback.
func TestContract_Canary_GoldenMarkerRendersInPane(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("g")
	nonce := fmt.Sprintf("%d%d", os.Getpid(), time.Now().UnixNano())
	marker := "CCGOLDEN" + nonce // the literal we assert; absent from the prompt below
	canaryPrompt := fmt.Sprintf(
		"Concatenate the text CCGOLDEN and the number %s with no space or punctuation "+
			"between them, and reply with exactly that single token and nothing else.", nonce)
	sb.ccp("reply", "g", canaryPrompt, "--no-wait")
	// Poll the pane until the joined marker renders (mirrors the AskUserQuestion
	// poll idiom: 90s deadline, 500ms sleeps).
	deadline := time.Now().Add(90 * time.Second)
	markerSeen := false
	for time.Now().Before(deadline) {
		if strings.Contains(sb.cap("g"), marker) {
			markerSeen = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !markerSeen {
		// Driving/capture broken (pane never showed the model's output), not a
		// verdict on ccpool — classify as scaffold, not a live failure.
		scaffoldFail(t, "golden marker %q never rendered in the pane (pane-capture path may be broken)", marker)
	}
	liveAssert(t, "golden marker rendered in pane", markerSeen, true)
}

// TestContract_Reap_EvictsLiveClaudeSafely pins the REAL-claude concern reap exists
// for: tearing down a LIVE running claude under the pool cap is clean. The
// oldest-by-last_activity SELECTION is generic and covered elsewhere (see SCOPE
// POLICY) — including TestReap_closesOverCap, which already does this against
// fake-claude. What only real claude verifies is that reap actually terminates a
// heavyweight claude process (tmux session gone, no orphan) while a fresher
// session survives; a stub that exits on /exit cannot stand in for that.
func TestContract_Reap_EvictsLiveClaudeSafely(t *testing.T) {
	sb := newSandbox(t)
	sb.setMaxSessions(1) // cap=1: a second live session is over-cap → the LRU is reaped
	sb.mustNew("victim")
	// A distinct last_activity_at SECOND makes "victim" unambiguously the LRU
	// (last_activity_at is epoch seconds; sort is not stable on ties). Both sessions
	// stay far inside idle_ttl (30m), so this exercises CAP eviction, not TTL.
	time.Sleep(1100 * time.Millisecond)
	sb.mustNew("survivor")
	_, code, _ := sb.ccpTimed(60*time.Second, "reap")
	liveAssert(t, "reap exits 0", code, 0)
	// doctor liveness derives from tmux has-session, so live=false ⟺ the real claude
	// was actually torn down. (victim may remain as a non-live row OR be absent —
	// either way it is no longer live.)
	out, _ := sb.ccp("doctor")
	liveAssert(t, "reaped victim's live claude is gone", sessionLineHas(out, "victim", "live=true"), false)
	liveAssert(t, "fresher survivor stays live", sessionLineHas(out, "survivor", "live=true"), true)
}

// TestContract_EventLog_ReflectsOrderedTransitions closes a contract-coverage gap
// (pg2-mxpj): state DETECTION (`ccpool state` reporting state=working/idle) was
// already live-asserted, but the JSONL event-log OUTPUT — the append-only,
// ordered (from→to) transition log shipped by pg2-qech — was NOT verified against
// real Claude. The only prior coverage of the log was a non-contract smoke test
// driven by injected events, so a Claude Code upgrade gave us no high-confidence
// signal that the ordered-transition log still tracks reality (e.g. that a turn
// still emits ready→working→done in that order, with the prompt's input actions
// recorded). This scenario drives a REAL turn to completion and asserts the
// PARSED, ORDERED transitions match the observed lifecycle, so a Claude Code
// upgrade that breaks the hook/transition wiring localizes HERE.
func TestContract_EventLog_ReflectsOrderedTransitions(t *testing.T) {
	sb := newSandbox(t)
	sb.mustNew("e")
	// Drive a real turn that COMPLETES, so the stop hook logs a `done` transition.
	// A SHORT prompt finishes quickly: the event log still records the ready→working
	// transition and the prompt's input actions regardless of timing, while keeping
	// the turn inside the completion poll budget (a long thinkingPrompt could never
	// reach `done` in time). --no-wait returns immediately; we gate on completion below.
	sb.ccp("reply", "e", "Reply with exactly: DONE", "--no-wait")

	// Gate on completion: poll reconciled state until the turn is done (state=idle),
	// mirroring the existing poll loops (generous deadline, 1s sleeps). A turn that
	// never completes is a real-claude/env issue, not a ccpool verdict -> scaffoldFail.
	deadline := time.Now().Add(120 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		out, _ := sb.ccp("state", "e")
		if strings.Contains(out, "state=idle") {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		scaffoldFail(t, "turn never reached state=idle within budget (real-claude/env issue, not a ccpool verdict)")
	}

	// Read the JSONL event log from its canonical location: in DEFAULT pool mode
	// config.StateDirPath() is <XDG_STATE_HOME>/ccpool, and the log is events.jsonl
	// beside hook.log. A broken path/parse is a scaffold failure, not a live verdict.
	path := filepath.Join(sb.envGet("XDG_STATE_HOME"), "ccpool", "events.jsonl")
	events, err := eventlog.Read(path)
	if err != nil {
		scaffoldFail(t, "reading event log %q failed (log path/mechanics broken): %v", path, err)
	}
	if len(events) == 0 {
		scaffoldFail(t, "event log %q is empty after a completed turn (log path/mechanics broken)", path)
	}

	// Assert on the PARSED, structured Events (NOT a raw-file substring): find the
	// first `working` transition for "e" and the first `done` transition that comes
	// strictly AFTER it in slice (append) order. We deliberately pin only this stable
	// working→done ordering — mirroring how the reconciled-state asserts pin only
	// state=working and never the timing-sensitive thinking-vs-streaming sub-state —
	// so this does not over-fit to sub-states the event log may reorder by timing.
	workingIdx, idleIdx := -1, -1
	for i, e := range events {
		if e.Name != "e" || e.Kind != "transition" {
			continue
		}
		if workingIdx < 0 && e.To == "working" {
			workingIdx = i
		}
		// ADR 0015: the Stop hook now records `idle` (was `done`) — ccpool reports
		// the turn ended (returned to idle), not a work-completion judgment.
		if workingIdx >= 0 && idleIdx < 0 && e.To == "idle" {
			idleIdx = i
		}
	}
	liveAssert(t, "event log records a working transition before an idle transition", workingIdx >= 0 && idleIdx > workingIdx, true)

	// The prompt's input actions are recorded as structured input events for "e",
	// at or after the working transition (the prompt is delivered as part of the
	// send that drives ready->working). Pin the two stable, always-present actions
	// of a delivered prompt: paste then enter (clear-input precedes them but is the
	// less interesting pre-clear, so we don't over-pin it).
	pasteAfterWorking, enterAfterWorking := false, false
	for i, e := range events {
		if e.Name != "e" || e.Kind != "input" || i < workingIdx {
			continue
		}
		switch e.Action {
		case "paste":
			pasteAfterWorking = true
		case "enter":
			enterAfterWorking = true
		}
	}
	liveAssert(t, "event log records a paste input action at/after the working transition", pasteAfterWorking, true)
	liveAssert(t, "event log records an enter input action at/after the working transition", enterAfterWorking, true)
}

// listRow returns the `list --json` row for externalID (ok=false if absent). It
// is the contract suite's window onto fields `state --json` does not expose —
// the claude_session_id and the display name set via --name. A parse failure is
// a harness/output-mechanics problem, not a live verdict -> scaffoldFail.
func (sb *sandbox) listRow(externalID string) (listJSON, bool) {
	out, _ := sb.ccp("list", "--json")
	var rows []listJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		scaffoldFail(sb.t, "list --json did not parse (output mechanics broken): %v\n%s", err, out)
		return listJSON{}, false
	}
	for _, r := range rows {
		if r.ExternalID == externalID {
			return r, true
		}
	}
	return listJSON{}, false
}

// TestContract_Resume_NewResumesExistingClaudeSession pins the ADR-0015 resume
// path against REAL claude: `ccpool new <id>` on a row whose tmux is gone but
// whose Claude session still exists on disk must RESUME that conversation
// (`claude --resume <claude_session_id>`) and PRESERVE the same claude_session_id
// — not mint a fresh one. This is the suite's only coverage that the real
// `claude --resume <session-id>` flag works AND that the hook-recorded
// transcript_path resume probe matches a transcript real claude actually persists
// (ADR 0015); a stub claude that exits on /exit cannot stand in for either.
func TestContract_Resume_NewResumesExistingClaudeSession(t *testing.T) {
	// PENDING (blocker found live, 2026-06-16): claude does not persist a transcript
	// file for ccpool-launched sessions — it reports a transcript_path via the hook
	// but no file lands there, while normal interactive sessions persist fine. With
	// no on-disk transcript there is nothing to resume, so this scenario cannot be
	// verified yet. (This is also the deeper root of the original pr-pool wedge: the
	// phantom session had no transcript.) pr-pool itself never resumes (per-attempt
	// ids + purge), so this gap does not affect pr-pool. Drop this pending() once
	// ccpool-launched sessions persist transcripts — the body below is the
	// ready-to-run verification.
	pending(t,
		"new on a tmux-gone session resumes via `claude --resume <claude_session_id>`, preserving the session id",
		"claude to persist transcripts for ccpool-launched sessions (see the transcript-persistence investigation bead)")
	sb := newSandbox(t)
	sb.mustNew("r")
	// Drive a SUBSTANTIAL turn so Claude actually persists the session transcript:
	// a trivial echo ("Reply with exactly X") gives Claude nothing worth writing,
	// so it reports a transcript_path but no file lands and there is nothing to
	// resume. Ask for a few sentences of genuine output instead.
	sb.ccp("reply", "r", "In two or three sentences, explain what a fast-forward git merge is.", "--no-wait")
	turnDeadline := time.Now().Add(120 * time.Second)
	completed := false
	for time.Now().Before(turnDeadline) {
		if out, _ := sb.ccp("state", "r"); strings.Contains(out, "state=idle") {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		scaffoldFail(t, "first turn never reached state=idle (real-claude/env issue); cannot set up the resume precondition")
	}
	first, ok := sb.listRow("r")
	if !ok || first.ClaudeSessionID == "" {
		scaffoldFail(t, "new did not record a claude_session_id for %q (launch mechanics broken)", "r")
	}
	if first.TranscriptPath == "" {
		scaffoldFail(t, "claude reported no transcript_path for %q; resume cannot be set up", "r")
	}
	// Resumability is driven by the HOOK-RECORDED transcript path (ADR 0015). Claude
	// flushes the transcript asynchronously, so poll for the file to land after the
	// turn before relying on it. If it never appears, that is a claude persistence
	// issue (not a ccpool resume regression) -> scaffoldFail with the precise path.
	transcriptDeadline := time.Now().Add(30 * time.Second)
	persisted := false
	for time.Now().Before(transcriptDeadline) {
		if _, err := os.Stat(first.TranscriptPath); err == nil {
			persisted = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !persisted {
		scaffoldFail(t, "claude never persisted a transcript at %q after a substantial turn; resume cannot be verified", first.TranscriptPath)
	}

	// Non-purge close ends the tmux session but KEEPS the row and leaves the Claude
	// conversation on disk — exactly the resume precondition (tmux gone, session
	// exists).
	_, code, _ := sb.ccpTimed(20*time.Second, "close", "r")
	liveAssert(t, "close exits 0", code, 0)
	if doctorOut, _ := sb.ccp("doctor"); sessionLineHas(doctorOut, "r", "live=true") {
		scaffoldFail(t, "session %q still live after close (teardown mechanics broken)", "r")
	}

	// new with the SAME external_id must resume the existing conversation.
	_, code, _ = sb.ccpTimed(90*time.Second, "new", "r")
	liveAssert(t, "resume-new exits 0", code, 0)

	// A turnless resume can take a moment to re-attach; poll for it to come back
	// live. A resume that never goes live is a real-claude/env issue (or a real
	// resume regression) — either way not a clean ccpool verdict here -> scaffoldFail.
	deadline := time.Now().Add(90 * time.Second)
	relive := false
	for time.Now().Before(deadline) {
		if doctorOut, _ := sb.ccp("doctor"); sessionLineHas(doctorOut, "r", "live=true") {
			relive = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !relive {
		scaffoldFail(t, "resumed session %q never came back live (real `claude --resume` may be failing)", "r")
	}

	second, ok := sb.listRow("r")
	liveAssert(t, "resumed row still present", ok, true)
	// THE contract: a real resume preserves the original claude_session_id. A fresh
	// launch (resume failed, or prune-and-recreate because the on-disk probe missed)
	// would mint a different one.
	liveAssert(t, "resume preserved the original claude_session_id (did not start fresh)", second.ClaudeSessionID, first.ClaudeSessionID)
}

// TestContract_New_NameFlagSetsDisplayName pins that REAL claude accepts the
// ADR-0015 `--name` launch flag — the session still reaches ready (new exits 0)
// — and that ccpool records the supplied display name distinct from the
// external_id key. (The `--session-id` flag is exercised by every `new`: every
// session launches with a generated --session-id and reaches ready, so real claude
// accepting --session-id is covered by any new-reaches-ready scenario.)
func TestContract_New_NameFlagSetsDisplayName(t *testing.T) {
	sb := newSandbox(t)
	const display = "contract-display-name"
	_, code, _ := sb.ccpTimed(90*time.Second, "new", "n2", "--name", display)
	liveAssert(t, "new --name exits 0 (real claude accepts --name and reaches ready)", code, 0)
	row, ok := sb.listRow("n2")
	liveAssert(t, "named session present in list", ok, true)
	liveAssert(t, "list reports the display name set via --name", row.Name, display)
}
