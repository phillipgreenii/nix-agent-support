// Package killprobe approves the `kill -0 <pid>` liveness-probe idiom: a
// runtime-composed shape (not literally prescribed by any plugin text) that
// agents use to poll whether a backgrounded process is still running, most
// often as the condition of a Monitor until-loop —
// `kill -0 <pid> && echo RUNNING && echo DONE` or
// `until ! kill -0 "$PID" 2>/dev/null; do sleep 2; done`. Measured 2026-09-15..21
// (pg2-23z9w's `claude-extended-tool-approver report --since 2026-09-15
// --misses-only --group-by command`): 81 misses, the largest still-open class
// after pg2-23z9w resolved the other six. See
// `phillipgreenii-nix-agent-support` ADR 0074 for the design decision this
// rule implements (which forms are safe, and why).
//
// `kill` is deliberately ABSENT from internal/rules/dangerouscmds' blanket
// denylist (see that package's doc comment) and unclaimed by every other
// rule, so today EVERY invocation — including this safe one — falls through
// to chain exhaustion (abstain). This rule does not change that for any
// OTHER kill invocation: it claims (Approve) only the narrow null-signal
// probe shape and defers (NotApplicable) everything else, unchanged.
package killprobe

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "kill-probe" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("kill-probe: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "kill" {
			continue
		}
		if isSignalZeroProbe(pc.Args) {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "kill-probe: null-signal (0) liveness probe — queries process existence/permission only, sends no signal to any target",
				Module:   r.Name(),
			}, nil
		}
		// Any other `kill` invocation (the default SIGTERM, an explicit
		// non-zero/-named signal, or an unrecognized flag such as -l/-L/-a/-q)
		// is left unmatched here — same under-matching bias as pnworkspace's
		// approvedSubcommands doc: a missed match costs a prompt/abstain (the
		// status quo before this rule existed), never a wrong Approve.
		continue
	}
	return hookio.NotApplicable()
}

// isSignalZeroProbe reports whether args requests POSIX signal 0 (the "null
// signal") and nothing else disqualifying. Signal 0 is defined by kill(2) to
// perform its existence/permission check WITHOUT sending any actual signal —
// so unlike every other signal, the TARGET pid(s) are irrelevant to safety:
// even a broadcast target (`-1`, `0`, or a negative process-group id) has no
// ACTUAL side effect when the signal is null. Only the SIGNAL selection needs
// scrutiny; a positional pid operand is accepted unconditionally.
//
// Recognized signal-0 spellings (ADR 0074's decision): the glued numeric form
// `-0`, and the separate/`=`-joined named-flag forms `-s 0`, `--signal 0`,
// `--signal=0`. `-s0` (glued short-flag-plus-value) is deliberately NOT
// recognized — neither BSD/macOS nor GNU/util-linux `kill(1)` documents a
// glued form for `-s`, so there is no observed idiom to cover and adding it
// would be speculative surface.
//
// Any OTHER flag — a non-zero numeric signal (`-9`), a named signal (`-TERM`,
// `-KILL`), signal listing (`-l`, `-L`), or anything this function does not
// recognize (`-a`, `-q`/`--queue`, `-p`) — disqualifies the WHOLE invocation
// immediately (returns false without scanning further), even if a `-0` also
// appears earlier: a command that names two different signals is not the
// single-purpose liveness-probe idiom this rule exists for, and the safe
// default is to defer, not to guess which one wins at runtime.
//
// KNOWN, DELIBERATE LIMITATION: a negative pid (`-1`, or any negative
// process-group id) is lexically indistinguishable here from a glued numeric
// signal flag (both are "-" followed by digits), so it is misread as a
// second, disqualifying signal spec and the whole invocation defers instead
// of approving. This is the safe direction — a miss costs a prompt/abstain,
// never a wrong Approve (killprobe_test.go's "documented limitation" case
// pins it) — and is not worth a getopt-style flags-before-operands split for
// an idiom that in practice always targets one positive pid.
func isSignalZeroProbe(args []string) bool {
	sawZeroSignal := false
argLoop:
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-s" || a == "--signal":
			i++
			if i >= len(args) || args[i] != "0" {
				return false
			}
			sawZeroSignal = true
		case strings.HasPrefix(a, "--signal="):
			if strings.TrimPrefix(a, "--signal=") != "0" {
				return false
			}
			sawZeroSignal = true
		case a == "--":
			// End-of-options marker: every remaining token is a pid operand
			// (even one that is lexically "-shaped"), so stop flag-parsing
			// rather than misread e.g. a negative-process-group pid as a flag.
			break argLoop
		case len(a) > 1 && a[0] == '-':
			// A glued short flag: `-0`, `-9`, `-TERM`, `-l`, … Only a purely
			// numeric body is recognized as a signal-number spelling; anything
			// else (a named signal, or a listing/other flag) disqualifies.
			body := a[1:]
			if n, err := strconv.Atoi(body); err == nil {
				if n != 0 {
					return false
				}
				sawZeroSignal = true
				continue
			}
			return false
		default:
			// A positional pid operand (or, before any flag, a plain
			// non-flag-shaped token) — accepted unconditionally per this
			// function's doc: signal 0 has no effect regardless of target.
		}
	}
	return sawZeroSignal
}
