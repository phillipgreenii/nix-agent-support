package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

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
//	3 = invalid args — NOT reachable today: the flag.ExitOnError flag set
//	    below exits 2 itself, so fs.Parse never returns an error
func runWaitUntilAgentsFinished(args []string) {
	fs := flag.NewFlagSet("wait-until-agents-finished", flag.ExitOnError)
	maxWaitS := fs.Int("maximum-wait", 7200, "Maximum total wait in seconds")
	consecutive := fs.Int("consecutive-idle-checks", 3, "Consecutive idle observations required")
	graceS := fs.Int("reconnect-grace", 30, "Seconds to wait for daemon return mid-wait")
	if err := fs.Parse(args); err != nil {
		os.Exit(3)
	}

	os.Exit(waitUntilAgentsFinished(waitParams{
		maxWait:     time.Duration(*maxWaitS) * time.Second,
		consecutive: *consecutive,
		grace:       time.Duration(*graceS) * time.Second,
	}, os.Stderr))
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

	streak := 0

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
			streak = 0 // reset; daemon may have restarted
			continue
		}

		// This is where the wait actually blocks, so THIS is the ctx that must
		// carry the deadline (see the waitCtx comment above).
		ctx, cancel := context.WithCancel(waitCtx)
		stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: 1000})
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

		// Watchdog: no push from the daemon within 2× the requested push
		// interval is treated as a hung daemon; reconnect. Default
		// PushIntervalMs = 1000ms here → 2s budget.
		const pushBudget = 2 * time.Second
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
					break streamLoop
				}
				next()
				st := r.msg
				if st == nil {
					continue
				}
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
