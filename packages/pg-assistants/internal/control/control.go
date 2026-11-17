// Package control is the daemon's client for pg-claude-control: it reads a
// session's usage report and sends submit, interrupt and stop requests to the
// control mod of the Claude at a pane pid.
//
// Every call targets the Claude as pid:<n>, so a name or session id can never
// resolve to another session, and runs through a cmdrun.Runner with a timeout
// (CallTimeout, or a graceful stop's escalation plus StopMargin), the same
// value the supervisor puts on the call's context. A call inherits the
// daemon's environment, which carries the operator's CLAUDE_CONFIG_DIR and
// PG_CLAUDE_CONTROL_STATE_DIR, so the tool finds the same registry and inboxes
// the sessions use.
//
// A non-zero exit the tool gives a meaning comes back as an error wrapping
// one of the sentinels below, with the tool's standard error in the message;
// a call ended by its context (a cancel or the timeout) comes back wrapping
// the context's error.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/exitcode"
)

const (
	// CallTimeout bounds usage, submit, interrupt and stop --signal. It covers
	// the tool's own waits: up to 3s for a missing inbox plus the reply wait
	// (5s for usage and interrupt --reply).
	CallTimeout = 15 * time.Second
	// StopMargin is added to a graceful stop's escalation for its bound.
	StopMargin = 15 * time.Second
	// usageReplyWait is usage's --timeout: how long the tool waits for the mod.
	usageReplyWait = 5 * time.Second
)

// The block kinds the daemon tracks; a reading of any other kind is dropped.
const (
	KindFiveHour = "five_hour"
	KindSevenDay = "seven_day"
)

// Client runs pg-claude-control.
type Client struct {
	Bin string        // absolute path to pg-claude-control
	Run cmdrun.Runner // nil means cmdrun.Exec
}

// Usage is one usage report.
type Usage struct {
	SessionID     string // the report's session, from the tool's registry
	HaveContext   bool   // the report carried a context token count
	ContextTokens int64
	ContextPct    float64 // 0 when absent
	Window        int64   // 0 when absent
	Limits        []Limit
}

// Limit is one account block reading.
type Limit struct {
	Kind       string    `json:"kind"`               // KindFiveHour or KindSevenDay
	Fraction   float64   `json:"fraction"`           // percentUsed / 100, within 0..1
	ResetsAt   time.Time `json:"resets_at,omitzero"` // zero when absent or unparseable
	ObservedAt time.Time `json:"observed_at"`        // when the daemon read it
}

// Valid reports whether l is a reading the daemon tracks: a known kind and a
// fraction within 0..1.
func (l Limit) Valid() bool {
	return (l.Kind == KindFiveHour || l.Kind == KindSevenDay) && l.Fraction >= 0 && l.Fraction <= 1
}

// The tool's exit codes, as sentinels.
var (
	ErrNoControl = errors.New("no control mod")    // exit 3
	ErrNoProcess = errors.New("no such process")   // exit 4
	ErrNoReply   = errors.New("no reply")          // exit 6
	ErrBusy      = errors.New("mod not accepting") // exit 9
	ErrRefused   = errors.New("refused by mod")    // exit 21
	ErrAmbiguous = errors.New("ambiguous target")  // exit 22
)

// The tool's app-specific exit codes.
const (
	exitRefused   = 21
	exitAmbiguous = 22
)

var sentinels = map[int]error{
	exitcode.Unavailable: ErrNoControl,
	exitcode.NotFound:    ErrNoProcess,
	exitcode.Timeout:     ErrNoReply,
	exitcode.Busy:        ErrBusy,
	exitRefused:          ErrRefused,
	exitAmbiguous:        ErrAmbiguous,
}

// noTurn is the mod's refusal reason for an interrupt with no turn running.
const noTurn = "no turn"

func target(pid int) string { return "pid:" + strconv.Itoa(pid) }

// Usage runs `usage pid:<n> --json --timeout 5s` and parses the report. now
// becomes each reading's ObservedAt.
func (c Client) Usage(ctx context.Context, pid int, now time.Time) (Usage, error) {
	res, err := c.run(ctx, CallTimeout, nil, "usage", target(pid), "--json", "--timeout", usageReplyWait.String())
	if err != nil {
		return Usage{}, err
	}
	return parseUsage(res.Stdout, now)
}

// Submit runs `submit pid:<n>` with text on standard input.
func (c Client) Submit(ctx context.Context, pid int, text string) error {
	_, err := c.run(ctx, CallTimeout, []byte(text), "submit", target(pid))
	return err
}

// Interrupt runs `interrupt pid:<n> --reply`, which returns once the turn is
// aborted. A "no turn" refusal is success: there was nothing to abort.
func (c Client) Interrupt(ctx context.Context, pid int) error {
	// --timeout keeps the tool's reply wait below CallTimeout, so a missing
	// reply is ErrNoReply and not a masking DeadlineExceeded.
	res, err := c.run(ctx, CallTimeout, nil, "interrupt", target(pid), "--reply", "--timeout", usageReplyWait.String())
	if errors.Is(err, ErrRefused) && isNoTurn(res.Stderr) {
		return nil
	}
	return err
}

// isNoTurn reports whether the tool's standard error ("error: refused_by_mod"
// then the mod's reason) gives the reason "no turn".
func isNoTurn(stderr []byte) bool {
	for _, line := range strings.Split(string(stderr), "\n") {
		if strings.TrimSpace(line) == noTurn {
			return true
		}
	}
	return false
}

// Stop runs `stop pid:<n> --escalate <escalate>`: abort the turn, run /exit,
// and SIGTERM the Claude if it is still alive after escalate. It is bounded by
// escalate plus StopMargin. A non-positive escalate is refused without a call,
// since the tool would then wait for /exit without ever escalating.
func (c Client) Stop(ctx context.Context, pid int, escalate time.Duration) error {
	if escalate <= 0 {
		return fmt.Errorf("pg-claude-control stop: escalate %v is not positive", escalate)
	}
	_, err := c.run(ctx, escalate+StopMargin, nil, "stop", target(pid), "--escalate", escalate.String())
	return err
}

// Signal runs `stop pid:<n> --signal`: SIGTERM at once, without the mod.
func (c Client) Signal(ctx context.Context, pid int) error {
	_, err := c.run(ctx, CallTimeout, nil, "stop", target(pid), "--signal")
	return err
}

// run executes one subcommand. The Result is returned with the error so a
// caller can read the refusal text.
func (c Client) run(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (cmdrun.Result, error) {
	run := c.Run
	if run == nil {
		run = cmdrun.Exec
	}
	res := run(ctx, cmdrun.Call{Name: c.Bin, Args: args, Stdin: stdin, Timeout: timeout})
	verb := args[0]
	switch {
	case res.CtxErr != nil:
		return res, fmt.Errorf("pg-claude-control %s: %w", verb, res.CtxErr)
	case res.StartErr != nil:
		return res, fmt.Errorf("pg-claude-control %s: %w", verb, res.StartErr)
	case res.OK():
		return res, nil
	}
	msg := strings.TrimSpace(string(res.Stderr))
	if s, ok := sentinels[res.Code]; ok && res.Signal == "" {
		return res, fmt.Errorf("pg-claude-control %s: %w: %s", verb, s, msg)
	}
	return res, fmt.Errorf("pg-claude-control %s: exit %d (signal %q): %s", verb, res.Code, res.Signal, msg)
}

// report is the tool's `usage --json` output; the result is the mod's answer,
// whose fields are absent when the engine has none.
type report struct {
	Session string `json:"session"`
	Result  struct {
		Context *struct {
			Tokens  *float64 `json:"tokens"`
			Percent *float64 `json:"percent"`
			Window  *float64 `json:"window"`
		} `json:"context"`
		RateLimits []struct {
			Kind        string   `json:"kind"`
			PercentUsed *float64 `json:"percentUsed"`
			ResetsAt    string   `json:"resetsAt"`
		} `json:"rateLimits"`
	} `json:"result"`
}

func parseUsage(stdout []byte, now time.Time) (Usage, error) {
	var r report
	if err := json.Unmarshal(stdout, &r); err != nil {
		return Usage{}, fmt.Errorf("pg-claude-control usage: cannot parse the report: %w", err)
	}
	u := Usage{SessionID: r.Session}
	if cx := r.Result.Context; cx != nil {
		if cx.Tokens != nil && *cx.Tokens >= 0 {
			u.HaveContext, u.ContextTokens = true, int64(*cx.Tokens)
		}
		if cx.Percent != nil {
			u.ContextPct = *cx.Percent
		}
		if cx.Window != nil && *cx.Window > 0 {
			u.Window = int64(*cx.Window)
		}
	}
	for _, rl := range r.Result.RateLimits {
		if rl.PercentUsed == nil {
			continue
		}
		// Valid drops a reading outside 0..100 percent (pgd has seen
		// epoch-sized bogus values) and an untracked kind.
		l := Limit{Kind: rl.Kind, Fraction: *rl.PercentUsed / 100, ObservedAt: now}
		// To the second: sessions report one block's reset time with
		// different fractions of a second, and the block tracker lets a later
		// reset time win, so a stale lower reading could otherwise beat a
		// fresh higher one of the same block.
		if t, err := time.Parse(time.RFC3339Nano, rl.ResetsAt); err == nil {
			l.ResetsAt = t.Truncate(time.Second)
		}
		if l.Valid() {
			u.Limits = append(u.Limits, l)
		}
	}
	return u, nil
}
