// Package usagelimit answers one question for ccpool's admission paths: is an
// account usage window (the 5-hour block or the weekly limit) at its limit right
// now, and if so when does it reset?
//
// The reading is the co-resident monitor's own `status --json` "rate_limits"
// object (the status-line's server-side used_percentage and reset instant per
// window). This package never interprets transcripts or estimates cost: it asks
// the monitor and applies one rule — a window is HIT iff its used percentage is at
// or above the threshold AND its reset instant is still in the future.
//
// Every uncertainty fails OPEN. A monitor that is not installed, a daemon that is
// down, malformed output, a window with no percentage, or a hit window with no
// reset instant (so no way to say when it clears) all read as "not blocked" with
// an error the caller logs: ccpool must never stop working because it could not
// find out whether it should.
package usagelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Window names the usage window a Limit describes. The values are the JSON keys
// the monitor uses and appear verbatim in ccpool's capacity output.
type Window string

const (
	// FiveHour is the rolling 5-hour usage block.
	FiveHour Window = "five_hour"
	// SevenDay is the weekly (7-day) usage limit.
	SevenDay Window = "seven_day"
)

// Limit is a usage window that is at its limit. ResetsAt is when it clears, the
// instant after which work is accepted again.
type Limit struct {
	Window   Window    `json:"window"`
	UsedPct  float64   `json:"used_pct"`
	ResetsAt time.Time `json:"resets_at"`
}

// String renders the limit for an operator-facing refusal line.
func (l Limit) String() string {
	return fmt.Sprintf("%s usage limit hit (%.1f%% used), resets at %s",
		l.Window, l.UsedPct, l.ResetsAt.Local().Format(time.RFC3339))
}

// Checker reports the binding usage limit, or nil when none is hit. A non-nil
// error means the answer is unknown and is always accompanied by a nil Limit:
// callers treat it as "not blocked" and log it.
type Checker interface {
	Check(ctx context.Context) (*Limit, error)
}

// Off is the Checker for a disabled gate: it never blocks and never errors.
type Off struct{}

// Check implements Checker.
func (Off) Check(context.Context) (*Limit, error) { return nil, nil }

// statusDoc is the slice of `status --json` this package reads; unknown keys are
// ignored so the monitor can add fields freely.
type statusDoc struct {
	RateLimits *struct {
		FiveHour *windowDoc `json:"five_hour"`
		SevenDay *windowDoc `json:"seven_day"`
	} `json:"rate_limits"`
}

type windowDoc struct {
	UsedPct  *float64 `json:"used_pct"`
	ResetsAt *string  `json:"resets_at"`
}

// Evaluate applies the gate rule to one `status --json` document. It returns the
// BINDING limit when several windows are hit — the one that clears last, since
// work is not accepted again until every hit window has reset — or nil when none
// is. An error means the document could not be parsed.
func Evaluate(doc []byte, thresholdPct float64, now time.Time) (*Limit, error) {
	var s statusDoc
	if err := json.Unmarshal(doc, &s); err != nil {
		return nil, fmt.Errorf("decode status json: %w", err)
	}
	if s.RateLimits == nil {
		return nil, nil
	}
	var binding *Limit
	for _, w := range []struct {
		name Window
		doc  *windowDoc
	}{{FiveHour, s.RateLimits.FiveHour}, {SevenDay, s.RateLimits.SevenDay}} {
		l := hit(w.name, w.doc, thresholdPct, now)
		if l != nil && (binding == nil || l.ResetsAt.After(binding.ResetsAt)) {
			binding = l
		}
	}
	return binding, nil
}

// hit returns the Limit for one window when it is at/over the threshold with a
// known, future reset instant, else nil. A missing percentage is unknown (never
// 0%), and a hit window with no parseable reset cannot be said to clear, so it
// does not block.
func hit(name Window, w *windowDoc, thresholdPct float64, now time.Time) *Limit {
	if w == nil || w.UsedPct == nil || w.ResetsAt == nil || *w.UsedPct < thresholdPct {
		return nil
	}
	resets, err := time.Parse(time.RFC3339, *w.ResetsAt)
	if err != nil || !resets.After(now) {
		return nil
	}
	return &Limit{Window: name, UsedPct: *w.UsedPct, ResetsAt: resets}
}

// Runner executes the monitor command and returns its stdout. It is a seam so
// tests never spawn a process.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the production Runner: it runs the command and captures stdout,
// folding stderr into the error so a failure says why.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := bytes.TrimSpace(errb.Bytes()); len(msg) > 0 {
			return nil, fmt.Errorf("%s: %w (%s)", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out.Bytes(), nil
}

// Gate is the production Checker: it asks the monitor for `status --json` and
// applies Evaluate.
type Gate struct {
	Command      string
	ThresholdPct float64
	Timeout      time.Duration
	Run          Runner           // nil means ExecRunner
	Now          func() time.Time // nil means time.Now
}

// Check implements Checker. A zero Timeout means no extra bound beyond ctx.
func (g Gate) Check(ctx context.Context) (*Limit, error) {
	run := g.Run
	if run == nil {
		run = ExecRunner
	}
	now := time.Now
	if g.Now != nil {
		now = g.Now
	}
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	out, err := run(ctx, g.Command, "status", "--json")
	if err != nil {
		return nil, fmt.Errorf("usage gate: %w", err)
	}
	l, err := Evaluate(out, g.ThresholdPct, now())
	if err != nil {
		return nil, fmt.Errorf("usage gate: %w", err)
	}
	return l, nil
}
