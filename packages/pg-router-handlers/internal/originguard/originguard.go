// Package originguard is the handler's client of pg-origin-guard's Check: the
// cheap, read-only "is git authentication to this repository's realm available
// now" answer (INV-CCH-10). It never uses the network and never starts a login
// itself; the handler keeps its own active reachability probe (internal/
// originprobe) for the per-origin part and asks this package only for the
// certificate-and-presence part.
//
// The production [Check] runs the pg-origin-guard CLI (`check --repo <path>`)
// rather than linking its Go module, so the handler needs no module coupling to
// it. Exit 0 is available, 3 is unavailable (a refusal, not a failure of the
// handler's work), anything else is a tool fault.
package originguard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/exitcode"
)

// Exit codes of `pg-origin-guard check`, mirroring its contract: the shared
// workspace codes of x/exitcode that pg-origin-guard's CLI returns (its
// ExitOK and ExitUnavailable). Every other code (1 internal, 2 usage, 7 config)
// is a tool fault.
const (
	exitAvailable   = exitcode.OK
	exitUnavailable = exitcode.Unavailable
)

// DefaultBin is the command looked up on PATH when no binary is configured.
const DefaultBin = "pg-origin-guard"

// DefaultTimeout bounds one CLI call; Check is a local read, so it is short.
const DefaultTimeout = 10 * time.Second

// Verdict is the answer of a Check. Reason is the guard's reason token when it
// reported one (cert-expired, no-human, ...), else empty.
type Verdict struct {
	Available bool
	Reason    string
}

// Check answers whether git auth to the realm of repoPath is available. A
// non-nil error is a tool fault only, never "unavailable": callers MUST fail
// open on it.
type Check func(ctx context.Context, repoPath string) (Verdict, error)

// ExecCheck returns the production Check, running bin (DefaultBin when empty).
func ExecCheck(bin string) Check { return ExecCheckWith(bin, cmdrun.Exec) }

// ExecCheckWith is ExecCheck over an injectable cmdrun.Runner; tests pass a
// cmdruntest.Fake.
func ExecCheckWith(bin string, run cmdrun.Runner) Check {
	if bin == "" {
		bin = DefaultBin
	}
	return func(ctx context.Context, repoPath string) (Verdict, error) {
		res := run(ctx, cmdrun.Call{Name: bin, Args: []string{"check", "--repo", repoPath}, Timeout: DefaultTimeout})
		out := res.Stdout
		switch {
		case res.OK():
			return Verdict{Available: true, Reason: reasonOf(out)}, nil
		case res.StartErr != nil:
			return Verdict{}, fmt.Errorf("%s check --repo: %w", bin, res.StartErr)
		case res.CtxErr != nil:
			return Verdict{}, fmt.Errorf("%s check --repo: %w", bin, res.CtxErr)
		case res.Signal == "" && res.Code == exitUnavailable:
			return Verdict{Available: false, Reason: reasonOf(out)}, nil
		}
		return Verdict{}, fmt.Errorf("%s check --repo: exit status %d: %s", bin, res.Code, strings.TrimSpace(string(res.Stderr)))
	}
}

// reasonOf extracts the reason=<token> field of the first output line
// ("unavailable realm=zr reason=cert-expired").
func reasonOf(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, "reason="); ok {
			return v
		}
	}
	return ""
}
