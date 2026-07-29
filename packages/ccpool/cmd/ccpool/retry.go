package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
	ct "github.com/phillipgreenii/claude-transcript"
)

// errNoLivePane signals the session's tmux pane is not live, so a re-nudge
// cannot be delivered (a process drop). The actuator treats this as a hand-back
// (never-fail), matching the spec: ccpool cannot resume a dead pane in-session.
var errNoLivePane = errors.New("ccpool: session pane is not live")

// retryClassName maps a RetryClass to the config token used in [retry].classes.
// terminal has no token (it is never retryable); it returns "".
func retryClassName(c ct.RetryClass) string {
	switch c {
	case ct.ClassTransientServer:
		return "transient_server"
	case ct.ClassTransientNetwork:
		return "transient_network"
	case ct.ClassRateLimited:
		return "rate_limited"
	default:
		return "terminal"
	}
}

// retrySet turns the configured class tokens into a lookup set. Unknown tokens
// are ignored (defensive — a typo can never silently retry an extra class).
func retrySet(classes []string) map[string]bool {
	set := make(map[string]bool, len(classes))
	for _, name := range classes {
		set[name] = true
	}
	return set
}

// retryDecision is the PURE retry policy: given the failed turn's RetryClass and
// the current persisted budget, decide whether to retry in place and, if so,
// how long to back off. It performs no I/O so it is exhaustively unit-tested.
//
// A retry happens iff ALL hold:
//   - retry is enabled,
//   - the class token is in the configured retry set (ccpool never retries
//     terminal or rate_limited unless explicitly configured),
//   - attempts remain: retryCount < maxAttempts,
//   - the window has budget: either no window is open yet
//     (windowStartedAt == 0) or now is still within timeout of its start.
//
// The backoff is baseDelay * 2^retryCount (1s → 2s → 4s for the defaults).
func retryDecision(cfg config.Retry, class ct.RetryClass, retryCount, windowStartedAt, now int64) (retry bool, backoff time.Duration) {
	if !cfg.Enabled {
		return false, 0
	}
	if !retrySet(cfg.Classes)[retryClassName(class)] {
		return false, 0
	}
	if retryCount >= int64(cfg.MaxAttempts) {
		return false, 0
	}
	timeout := time.Duration(cfg.Timeout)
	// windowStartedAt == 0 means no window has opened yet — the first retry is
	// always within budget. Once open, the window elapses after timeout.
	if windowStartedAt != 0 && timeout > 0 {
		elapsed := time.Duration(now-windowStartedAt) * time.Second
		if elapsed >= timeout {
			return false, 0
		}
	}
	base := time.Duration(cfg.BaseDelay)
	backoff = base << uint(retryCount) // base * 2^retryCount
	return true, backoff
}

// Nudger delivers a minimal "continue" nudge to a live session's pane. The
// production implementation actuates tmux send-keys (the same path pa-monitor's
// nudger uses, which ccpool sessions opt out of via PA_MONITOR_NO_NUDGE); tests
// inject a fake.
type Nudger interface {
	Nudge(tmuxSession string) error
}

// continueNudge is the body re-delivered to resume a session after a transient
// error. A short explicit prompt (not a bare Enter) survives an empty input box
// and reads as an intentional resume in the transcript.
const continueNudge = "continue"

// tmuxNudger actuates the re-nudge over tmux send-keys, mirroring the session
// service's deliverPrompt (clear input → bracketed paste → Enter) but without
// the full session machinery — the hook is a short-lived process. This is the
// same tmux actuation pa-monitor's nudger uses, which ccpool sessions opt out
// of via PA_MONITOR_NO_NUDGE, so there is no double-nudge.
type tmuxNudger struct{ c *tmux.Client }

// newTmuxNudger binds a nudger to the active pool's tmux socket.
func newTmuxNudger(socket string) *tmuxNudger { return &tmuxNudger{c: tmux.NewClient(socket)} }

func (n *tmuxNudger) Nudge(tmuxSession string) error {
	if !n.c.HasSession(tmuxSession) {
		return errNoLivePane
	}
	if err := n.c.SendKeys(tmuxSession, "C-u"); err != nil {
		return err
	}
	body := continueNudge
	if strings.HasPrefix(strings.TrimLeft(body, " \t"), "/") {
		body = " " + body // guard a leading slash from slash-command interpretation
	}
	if err := n.c.Paste(tmuxSession, body); err != nil {
		return err
	}
	return n.c.SendKeys(tmuxSession, "Enter")
}

// retryActuator performs the bounded in-place retry from the StopFailure hook.
// All dependencies are injected so the hook path is testable without tmux/sleep.
type retryActuator struct {
	cfg    config.Retry
	store  *store.Store
	nudger Nudger
	now    func() time.Time
	sleep  func(time.Duration)
}

// maybeRetry is the StopFailure decision+actuation. It returns retried=true when
// it resumed the session in place (the caller then keeps the row OUT of
// `errored`), and false when the caller should fall through to today's `errored`
// transition. It NEVER returns retried=true unless the re-nudge actually landed.
//
// NEVER-FAIL: any internal failure (transcript read, store, tmux) returns
// (false, err) so the caller hands back as `errored`; it must not crash the hook.
func (a *retryActuator) maybeRetry(ctx context.Context, sess store.Session, transcriptPath string) (retried bool, err error) {
	rec, err := ct.LastAPIError(transcriptPath)
	if err != nil {
		return false, err
	}
	// No classifiable api-error in the transcript → not a retry candidate.
	if rec.Kind == "" {
		return false, nil
	}
	now := a.now().Unix()
	doRetry, backoff := retryDecision(a.cfg, rec.RetryClass(), sess.RetryCount, sess.RetryWindowStartedAt, now)
	if !doRetry {
		return false, nil
	}
	if sess.TmuxSession == "" {
		// No pane to actuate (a dead/process-drop session) → hand back.
		return false, nil
	}
	if backoff > 0 {
		a.sleep(backoff)
	}
	// Re-deliver the continue nudge to the SAME session BEFORE persisting the
	// bump/transition, so a delivery failure leaves the row exactly as it was
	// (the caller then hands it back as `errored` via the never-fail fallback).
	if nerr := a.nudger.Nudge(sess.TmuxSession); nerr != nil {
		return false, nerr
	}
	if berr := a.store.BumpRetry(ctx, sess.ExternalID); berr != nil {
		return false, berr
	}
	// Keep the session OUT of `errored`: return it to `working` (the nudge
	// started a fresh turn). A transition failure hands back as `errored`.
	if _, terr := a.store.Transition(ctx, sess.ExternalID, store.Working, "", transcriptPath); terr != nil {
		return false, terr
	}
	return true, nil
}

// tryRetryOnFail loads the row for externalID and attempts an in-place retry via
// the actuator. It returns true ONLY when the session was actually resumed in
// place (so the caller skips the `errored` transition). Any error — store load,
// transcript read, tmux delivery — is swallowed and returns false so the caller
// hands the failure back as `errored` (never-fail policy, see runHook).
func tryRetryOnFail(ctx context.Context, ra *retryActuator, st *store.Store, externalID, transcriptPath string) bool {
	sess, ok, err := st.GetByExternalID(ctx, externalID)
	if err != nil || !ok {
		return false
	}
	retried, _ := ra.maybeRetry(ctx, sess, transcriptPath)
	return retried
}
