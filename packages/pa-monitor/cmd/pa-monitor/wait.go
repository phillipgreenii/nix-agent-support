package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// runWaitUntilAgentsFinished implements the `wait-until-agents-finished`
// subcommand. Streams WatchState and exits 0 when all agents have been
// idle for N consecutive ticks.
//
// "Idle" here is the ADR 0024 R3 busy notion: only `status == working` counts as
// busy (the loop below sums WorkingN, exactly as the daemon's IsAnyBusy does). A
// `blocked` session — notably `blocker = usage_limit`, i.e. a session that still
// has work but is waiting out the 5h/weekly usage window — does NOT hold this
// gate open and is indistinguishable from idle here.
//
// Callers MUST therefore read exit 0 as "idle reached", NOT as "work finished":
// it can be returned mid-window with blocked work pending that auto-resume will
// pick up at the window reset. A caller that must not proceed until pending work
// is genuinely done MUST additionally check for blocked sessions (e.g. the
// `sessions: N working, N blocked, N idle` line from `pa-monitor status`) rather
// than relying on this exit code alone. This is declared intent, not a defect:
// ADR 0024 R3 stands until a superseding decision, so do NOT "fix" it here.
//
// Exit codes:
//
//	0 = idle reached (no session `working` for N ticks; blocked counts as idle)
//	1 = timeout
//	2 = daemon unavailable past reconnect-grace; also bad flags (see below)
//	3 = invalid args. Reachable ONLY via the explicit validation below
//	    (currently just --consecutive-idle-checks <= 0, see
//	    validateConsecutiveIdleChecks for why); the
//	    `if err := fs.Parse(args); err != nil` branch immediately below is
//	    still dead code, because flag.ExitOnError makes fs.Parse call
//	    os.Exit(2) itself on a parse error (bad flag name, non-integer value)
//	    before ever returning one to check. Widening exit 3 into a general
//	    parse-error contract is bead pg2-3rlwm's job, not this one's — this
//	    comment records the split so the two do not collide.
func runWaitUntilAgentsFinished(args []string) {
	fs := flag.NewFlagSet("wait-until-agents-finished", flag.ExitOnError)
	// --maximum-wait <= 0 is intentionally ACCEPTED, not rejected: it makes
	// the deadline already-expired at start, so the wait exits 1 (timeout) on
	// its very first pass through the loop below — before ever dialing the
	// daemon — which is a defensible, if literal, reading of "maximum total
	// wait ... 0 seconds": do not wait at all. That is unlike
	// --consecutive-idle-checks <= 0 below, which cannot be satisfied on its
	// own terms regardless of reading. See
	// TestWaitUntilAgentsFinishedMaximumWaitZeroOrNegativeExitsImmediately.
	maxWaitS := fs.Int("maximum-wait", 7200, "Maximum total wait in seconds")
	consecutive := fs.Int("consecutive-idle-checks", 3, "Consecutive idle observations required (a gap in observation restarts the count); must be >= 1")
	// --reconnect-grace <= 0 is intentionally ACCEPTED, not rejected: it
	// skips the retry loop in waitForDaemon entirely, so a daemon that
	// disappears mid-wait fails at once (exit 2) with no retry — again a
	// literal, defensible reading of "seconds to wait for daemon return ...
	// 0 seconds". See
	// TestWaitUntilAgentsFinishedReconnectGraceZeroOrNegativeFailsWithoutRetry.
	graceS := fs.Int("reconnect-grace", 30, "Seconds to wait for daemon return mid-wait")
	if err := fs.Parse(args); err != nil {
		os.Exit(3)
	}

	if err := validateConsecutiveIdleChecks(*consecutive); err != nil {
		fmt.Fprintf(os.Stderr, "wait-until-agents-finished: %v\n", err)
		os.Exit(3)
	}

	os.Exit(waitUntilAgentsFinished(waitParams{
		maxWait:     time.Duration(*maxWaitS) * time.Second,
		consecutive: *consecutive,
		grace:       time.Duration(*graceS) * time.Second,
	}, os.Stderr))
}

// validateConsecutiveIdleChecks rejects --consecutive-idle-checks values that
// cannot mean what they say, rather than letting the loop below silently
// reinterpret them. It is split out of runWaitUntilAgentsFinished (which
// os.Exits) so a test can drive it directly without terminating the test
// process — the same reason waitUntilAgentsFinished itself is split out of
// that wrapper (see its doc comment below).
//
// consecutive <= 0 is rejected. The streak counter below is non-negative and
// only ever incremented, so a target below 1 can never be reached on its own
// terms — accepting it would mean secretly redefining it as 1 (the exact
// defect bead pg2-e05tm reports: --consecutive-idle-checks 0 silently
// behaving as 1) or inventing an undocumented "never satisfied" semantics
// nobody asked for. Neither is acceptable silently, so this is a hard error.
//
// consecutive == 1 is deliberately NOT rejected, even though the DECISION
// comment on waitUntilAgentsFinished (bead pg2-klyz7) notes that its
// gap-based streak-reset rule buys N == 1 nothing: the streak completes on
// the very first idle observation, so "consecutive in time" is vacuous at
// N <= 1. That is a materially different case from 0/negative: a caller
// passing 1 gets EXACTLY the value it asked for — act on the first idle
// reading, i.e. deliberately no debounce — which is a legitimate request,
// not a surprise the way 0 silently becoming 1 is. Widening this rejection to
// N <= 1 would turn that legitimate, if debounce-free, request into an error
// for no benefit to the caller, so it stays accepted. See
// TestWaitUntilAgentsFinishedConsecutiveIdleChecksOneExitsOnFirstIdleObservation
// for the runtime behavior this leaves in place.
func validateConsecutiveIdleChecks(consecutive int) error {
	if consecutive <= 0 {
		return fmt.Errorf("--consecutive-idle-checks must be >= 1 (a streak cannot reach a target below 1), got %d", consecutive)
	}
	return nil
}

// waitParams is the parsed flag set of `wait-until-agents-finished`.
type waitParams struct {
	maxWait     time.Duration // --maximum-wait
	consecutive int           // --consecutive-idle-checks
	grace       time.Duration // --reconnect-grace
}

// waitTimeout emits the documented timeout line and returns its exit code.
func waitTimeout(stderr io.Writer) int {
	fmt.Fprintln(stderr, "wait-until-agents-finished: timeout")
	return 1
}

// reconnectPause is the single pace shared by EVERY way this wait can lose its
// stream and go round again: a refused stream open, a stream that broke or went
// quiet, and a daemon that went away (waitForDaemon's poll). Naming it once is
// deliberate — a caller cannot tell those three apart, so they MUST NOT differ
// in how hard they retry, and three separate literals is how they drifted apart
// in the first place (bead pg2-2snsq). Every use is inside a select on the
// --maximum-wait context, never a bare Sleep, so pacing can never outlast the
// deadline (bead pg2-yzw29).
const reconnectPause = 500 * time.Millisecond

// pushIntervalMs is the PushIntervalMs requested from the daemon's WatchState
// stream below. pushBudget is DERIVED from this constant (not a separate
// literal) so the 2x relationship it documents cannot drift out of sync if
// this value ever changes — bumping pushIntervalMs alone moves pushBudget
// with it, in code, not merely in a comment someone has to remember to
// update to match.
const pushIntervalMs = 1000

// pushBudget is the longest gap between two state observations this wait treats
// as normal, and it answers two questions with one number:
//
//   - WATCHDOG, within one stream: no push from the daemon inside this window is
//     treated as a hung daemon, so break and reconnect. It is 2× pushIntervalMs
//     above, so a healthy stream has a full interval of slack.
//   - CONSECUTIVENESS, across a reconnect as well: two observations further apart
//     than this are not consecutive, so the later one starts a fresh idle streak
//     (see the DECISION in waitUntilAgentsFinished).
//
// One number for both is deliberate. The watchdog is already this loop's own
// declaration of the longest silence a healthy stream may have, so a gap that
// exceeds it is one the loop has itself judged abnormal — which leaves the two
// rules unable to disagree about whether the loop was observing.
const pushBudget = 2 * time.Duration(pushIntervalMs) * time.Millisecond

// waitUntilAgentsFinished is the wait loop proper, split out of the os.Exit
// wrapper above so tests can drive it against a fake daemon and observe both
// the exit code and the stderr contract (mirroring runBridgeChannel vs.
// runCmuxBridge). Returns the process exit code.
func waitUntilAgentsFinished(p waitParams, stderr io.Writer) int {
	// --maximum-wait is carried on the CONTEXT, not merely re-checked between
	// reconnects (bead pg2-yzw29). streamLoop below blocks in a select for as
	// long as the daemon keeps pushing, so a deadline consulted only in this
	// outer loop is never re-tested while the daemon is healthy AND some
	// session is `working` — the exact case a caller sets --maximum-wait for,
	// which made the documented exit 1 unreachable. A deadline-carrying parent
	// makes `<-ctx.Done()` fire inside streamLoop, landing on the timeout exit.
	waitCtx, waitCancel := context.WithDeadline(context.Background(), time.Now().Add(p.maxWait))
	defer waitCancel()

	// DECISION (bead pg2-klyz7): --consecutive-idle-checks N requires N idle
	// observations that are consecutive IN TIME. An observation therefore extends
	// the streak only if it lands within pushBudget of the previous one; further
	// out it STARTS a fresh streak of 1. lastObs carries that timestamp across
	// reconnects alongside the streak it qualifies.
	//
	// The flag exists to debounce a transient idle reading, and an unobserved gap
	// is precisely the window in which the thing being debounced is invisible:
	// observe idle twice, lose the stream, observe idle once more, and an
	// unqualified streak of 3 attests to a period a session may have spent
	// `working` throughout.
	//
	// DURATION is the axis, not break KIND. The three ways the stream can go
	// (refused open, missed push, Recv error) differ only in what the loop learns
	// about WHY it stopped observing, never in whether it stopped — so keying on
	// the kind would re-open exactly the drift the reconnect paths already suffered
	// (reconnectPause, bead pg2-2snsq). The gap also grades these correctly with no
	// per-path code: a missed push cannot cost less than its own budget, so that
	// path always restarts the streak, while a clean stream drop the loop redials
	// promptly costs nothing.
	//
	// NOT a reset per pass through this loop, which is the tempting one-liner: a
	// HEALTHY long wait also re-enters here on every reconnect (a graceful daemon
	// shutdown ends the stream with a clean EOF by design — internal/daemon
	// server.go's WatchState), so discarding the streak there would make N
	// unreachable under any daemon that drops its stream more often than every N
	// pushes, turning a satisfiable wait into a guaranteed exit 1. The real daemon
	// Sends immediately on stream open and the redial is paced at reconnectPause,
	// so a prompt drop resumes ~0.5s later — inside the budget by construction.
	//
	// Reachability is bounded rather than hoped for: N observations must now fall
	// within (N-1) × pushBudget of continuous observation. N ≤ 1 is unaffected —
	// the check only ever resets to 0 and the increment that follows still reaches
	// 1 — so no wait satisfiable by a single observation becomes unsatisfiable.
	streak := 0
	var lastObs time.Time

	for {
		if waitCtx.Err() != nil {
			return waitTimeout(stderr)
		}

		// The DIAL deliberately keeps its own fixed handshake budget
		// (rpcclient.DialPath bounds it at 2s) instead of inheriting waitCtx.
		// With a --maximum-wait shorter than that budget, a deadline-derived
		// dial ctx would make a MISSING daemon look like a timeout and lose the
		// specific `daemon unreachable` diagnosis the exit-2 contract promises —
		// which is what test-wait-for-agents-real-pa-monitor.bats reads to tell
		// "args accepted" from "args rejected". Only the STREAM below needs the
		// deadline; the conn's own lifetime is owned by Close, not by this ctx.
		client, err := rpcclient.Dial(context.Background())
		if err != nil {
			// Daemon unavailable. Apply reconnect grace if we already
			// observed at least one tick; otherwise fail immediately.
			if streak == 0 {
				fmt.Fprintln(stderr, "daemon unreachable")
				return 2
			}
			if err := waitForDaemon(waitCtx, p.grace); err != nil {
				// waitForDaemon honours waitCtx, so a grace window that would
				// outlast --maximum-wait ends AT the deadline: report that as
				// the timeout it is rather than as a daemon failure.
				if waitCtx.Err() != nil {
					return waitTimeout(stderr)
				}
				fmt.Fprintf(stderr, "wait-until-agents-finished: %v\n", err)
				return 2
			}
			// STRICTER than the pushBudget consecutiveness rule above, and
			// deliberately so (the difference the DECISION there requires be
			// justified): this is the one path where the state SOURCE may have been
			// replaced rather than merely gone unwatched. A restarted daemon builds
			// its first push from a freshly derived registry, so that push attests
			// to nothing about the interval before it — however short the gap
			// happened to be. lastObs needs no clearing: the rule above is gated on
			// streak > 0, so a zeroed streak already ignores it.
			streak = 0 // reset; daemon may have restarted
			continue
		}

		// This is where the wait actually blocks, so THIS is the ctx that must
		// carry the deadline (see the waitCtx comment above).
		ctx, cancel := context.WithCancel(waitCtx)
		stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: pushIntervalMs})
		if err != nil {
			cancel()
			_ = client.Close()
			// A REFUSED OPEN leaves the socket perfectly dialable, so this branch
			// is the one that loops straight back into a fresh dial. Unpaced that
			// was ~8k dial/WatchState/Close cycles a second — 15,925 measured over
			// a 2s window, ~57M over the 7200s default — for the whole remaining
			// wait (bead pg2-2snsq). The two sibling reconnects below and
			// waitForDaemon were already paced; only this one was not.
			//
			// It was also the only one that retried SILENTLY, so the spin showed up
			// as nothing but a late exit. Report it in the sibling paths' idiom
			// (cf. "push missed, reconnecting"), carrying the error, which is the
			// only thing here that says WHY the daemon refused.
			fmt.Fprintf(stderr, "wait: stream refused, reconnecting: %v\n", err)
			// DECISION (pg2-2snsq): a FLAT pause, not escalating backoff, and no
			// separate give-up-early diagnostic. RATE was the whole defect and one
			// flat pause fixes it; --maximum-wait already bounds the TOTAL, so a
			// growing delay has no unbounded cost left to contain — it would only
			// trade a bounded number of cheap retries for slower recovery from a
			// transient refusal. Escalating here ALONE would also re-open exactly
			// the gap this bead closed, with the three reconnect paths pacing
			// differently again. If a sustained refusal is ever measured in the
			// field, the escalation belongs on all three paths at once, driven by
			// that measurement — not on this one pre-emptively.
			select {
			case <-waitCtx.Done():
			case <-time.After(reconnectPause):
			}
			continue
		}

		// The watchdog budget below is pushBudget, which is also the
		// consecutiveness tolerance the streak is qualified against — see its
		// declaration for why the two are one number.
		type recvResult struct {
			msg *pb.DaemonState
			err error
		}
		recvCh := make(chan recvResult, 1)
		next := func() {
			go func() {
				m, e := stream.Recv()
				recvCh <- recvResult{m, e}
			}()
		}
		next()

	streamLoop:
		for {
			select {
			case <-ctx.Done():
				// The only cancel reaching here is waitCtx's --maximum-wait
				// deadline (the success path below returns without re-entering
				// this select), which the post-loop check turns into exit 1.
				break streamLoop
			case <-time.After(pushBudget):
				fmt.Fprintln(stderr, "wait: push missed, reconnecting")
				break streamLoop
			case r := <-recvCh:
				if r.err != nil {
					// A graceful daemon shutdown ends WatchState with a clean
					// EOF BY DESIGN (internal/daemon server.go's WatchState
					// handler returning nil) — a healthy long wait redials
					// through this every time the daemon cycles, so it MUST
					// NOT be reported as a failure. A canceled status is this
					// loop's OWN ctx ending (e.g. the --maximum-wait deadline
					// racing this goroutine's Recv), already reported via the
					// timeout path above — reporting it again here would be
					// the same false alarm under a different name. Anything
					// else is a genuine transport failure the caller could not
					// otherwise learn WHY happened, so say so, carrying the
					// error, in the sibling paths' idiom (cf. "stream
					// refused, reconnecting").
					if !isGracefulStreamEnd(r.err) {
						fmt.Fprintf(stderr, "wait: stream error, reconnecting: %v\n", r.err)
					}
					break streamLoop
				}
				next()
				st := r.msg
				if st == nil {
					continue
				}
				// Qualify this observation against the previous one BEFORE folding
				// it in (see the DECISION above). Doing it here, at the single point
				// where observations are counted, rather than on each break path, is
				// what makes the rule impossible for a future break path to forget:
				// the streak is only ever incremented and TESTED here, so qualifying
				// it lazily at the point of use is exactly equivalent to resetting it
				// eagerly at every break — and it can additionally tell a 0.5s
				// reconnect from a 30s one, which an eager reset cannot.
				now := time.Now()
				if gap := now.Sub(lastObs); streak > 0 && gap > pushBudget {
					// Say so. A streak discarded silently would present as nothing
					// but a late exit, the same way the unpaced reconnect presented
					// as nothing but a late exit (bead pg2-2snsq); this line is what
					// distinguishes "still waiting" from "making no progress".
					fmt.Fprintf(stderr, "wait: %s unobserved, idle streak restarted\n", gap.Round(time.Millisecond))
					streak = 0
				}
				lastObs = now
				// WorkingN only, per ADR 0024 R3 — a `blocked` session
				// (e.g. blocker = usage_limit) does not hold this gate
				// open. See the exit-code contract above.
				anyBusy := false
				for _, d := range st.GetDirs() {
					if d.GetWorkingN() > 0 {
						anyBusy = true
						break
					}
				}
				if anyBusy {
					streak = 0
				} else {
					streak++
					if streak >= p.consecutive {
						// Success exits at once; the deadline on waitCtx never
						// delays it.
						cancel()
						_ = client.Close()
						fmt.Fprintln(stderr, "all idle")
						return 0
					}
				}
			}
		}

		cancel()
		_ = client.Close()

		if waitCtx.Err() != nil {
			return waitTimeout(stderr)
		}
		// Brief pause before reconnect, itself bounded by the deadline.
		select {
		case <-waitCtx.Done():
		case <-time.After(reconnectPause):
		}
	}
}

// isGracefulStreamEnd reports whether a WatchState Recv error is an EXPECTED
// end of stream rather than a genuine transport failure: an io.EOF (the
// daemon's designed graceful-shutdown behaviour) or a codes.Canceled status
// (this loop's own ctx being canceled). status.Code unwraps a wrapped status
// error regardless of how deeply it is wrapped, so this is reliable even
// though grpc-go builds the Canceled status via status.FromContextError
// rather than returning context.Canceled verbatim.
func isGracefulStreamEnd(err error) bool {
	return errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled
}

// waitForDaemon polls Dial until it succeeds, grace expires, or ctx does. ctx
// carries the caller's --maximum-wait deadline: a reconnect-grace window MUST
// NOT outlast it, or a mid-wait daemon disappearance would restore the very
// unbounded wait bead pg2-yzw29 fixed. The caller distinguishes the two failure
// causes by re-reading ctx.Err().
func waitForDaemon(ctx context.Context, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		// The 500ms here is this poll's own dial BUDGET, not its pace, so it stays
		// a literal; the pace below is the shared reconnectPause.
		dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := rpcclient.Dial(dialCtx)
		cancel()
		if err == nil {
			_ = client.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectPause):
		}
	}
	return fmt.Errorf("daemon did not return within grace %s", grace)
}

// runConfigShow implements the `config show` subcommand. Prints the
// loaded config so users can verify nix-rendered values.
func runConfigShow(args []string) {
	// Lightweight — no RPC needed; just load the config file the daemon
	// would load and print key fields.
	cfg, err := configLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config show: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("plan_tier:                 %s\n", cfg.PlanTier)
	fmt.Printf("burn_window_short:         %s\n", cfg.BurnWindowShort)
	fmt.Printf("burn_window_long:          %s\n", cfg.BurnWindowLong)
	fmt.Printf("refresh_interval:          %s\n", cfg.RefreshInterval)
	fmt.Printf("caffeinate_grace:          %s\n", cfg.CaffeinateGrace)
	fmt.Printf("working_threshold:         %s\n", cfg.WorkingThreshold)
	fmt.Printf("idle_threshold:            %s\n", cfg.IdleThreshold)
	fmt.Printf("cmux_sidebar_enable:       %v\n", cfg.CmuxSidebarEnable)
	fmt.Printf("auto_restart_on_version_mismatch: %v\n", cfg.AutoRestartOnVersionMismatch)
	for _, d := range cfg.Decorators {
		fmt.Printf("decorator:                 %s -> %s (timeout %dms, env %d)\n", d.Name, d.Command, d.TimeoutMS, len(d.Env))
	}
}
