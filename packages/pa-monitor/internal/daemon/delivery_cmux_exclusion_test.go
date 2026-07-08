package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// cmuxPsRun returns a CmuxSignaler.RunCmd that reports a single cmux server
// (serverPID) which is the parent of targetPID, so CmuxSignaler.Detect(targetPID)
// walks ps-ancestry and resolves true. Kept local because the signal package's
// fake-ps fixture is unexported; it mirrors the two ps invocations
// CmuxSignaler makes: `ps -A -o pid,comm` and `ps -o ppid= -p <pid>`.
func cmuxPsRun(serverPID, targetPID int) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ps" {
			return nil, fmt.Errorf("unexpected command %q", name)
		}
		if len(args) >= 3 && args[0] == "-A" && args[1] == "-o" && args[2] == "pid,comm" {
			return []byte(fmt.Sprintf("  PID COMMAND\n%5d cmux\n", serverPID)), nil
		}
		if len(args) == 4 && args[0] == "-o" && args[1] == "ppid=" && args[2] == "-p" {
			var pid int
			if _, err := fmt.Sscanf(args[3], "%d", &pid); err != nil {
				return nil, fmt.Errorf("bad pid arg %q: %w", args[3], err)
			}
			if pid == targetPID {
				return []byte(fmt.Sprintf("%d\n", serverPID)), nil
			}
			return []byte(""), nil
		}
		return nil, fmt.Errorf("unexpected ps args %v", args)
	}
}

// TestDeliveryExcludesCmuxKeepAwakeRetainsCmux proves the two paths that both
// read opts.NudgerSignalers are structurally separated per ADR 0022:
//
//   - DELIVERY (lifecycle.go builds SignalerAdapter over WithoutCmux(...)):
//     a cmux-hosted target must resolve to NO signaler, so the in-daemon path
//     can never select cmux and the daemon never execs cmux.
//   - D5 KEEP-AWAKE (lifecycle.go passes the full, unfiltered slice to
//     hasUnattemptedNudgeableDisrupt): the SAME cmux-hosted disrupt MUST still
//     resolve a signaler and hold the Mac awake.
//
// A single real *signal.CmuxSignaler that detects the target pid drives both
// halves, so the assertions are about which SLICE each path uses, not about
// differing signaler behavior.
func TestDeliveryExcludesCmuxKeepAwakeRetainsCmux(t *testing.T) {
	const (
		sid       = "sid-cmux"
		targetPID = 6002
		serverPID = 6000
	)

	cmux := &signal.CmuxSignaler{RunCmd: cmuxPsRun(serverPID, targetPID)}
	// full mirrors opts.NudgerSignalers: the slice the daemon feeds BOTH paths.
	full := []signal.Signaler{cmux}

	// Precondition: on the unfiltered slice, cmux resolves the target — this is
	// exactly the signaler whose Send used to shell out to `cmux`.
	if signal.ResolveSignaler(full, targetPID) == nil {
		t.Fatal("fixture broken: cmux should Detect the target pid via ps-ancestry")
	}

	// --- DELIVERY path: MUST NOT be able to resolve cmux ---
	delivery := signal.WithoutCmux(full)
	if got := signal.ResolveSignaler(delivery, targetPID); got != nil {
		t.Errorf("delivery slice resolved %q for a cmux target; cmux must be structurally absent (ADR 0022)", got.Name())
	}
	// The SignalerAdapter built over the delivery slice therefore errors
	// (no signaler) rather than executing cmux.
	if err := (&SignalerAdapter{Signalers: delivery}).Send(targetPID, "resume"); err == nil {
		t.Error("delivery SignalerAdapter.Send unexpectedly found a signaler for the cmux target")
	}

	// --- D5 KEEP-AWAKE path: MUST still consider the cmux-hosted disrupt ---
	wm := newWMForTest(t)
	le := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now().Add(-time.Minute)}
	tree := treeWithSessions(svWithError(sid, targetPID, le))
	if !hasUnattemptedNudgeableDisrupt(tree, wm, full) {
		t.Error("D5 keep-awake dropped the cmux-hosted disrupt (regression: cmux must remain in the keep-awake slice)")
	}
}
