package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// DefaultBudget is the call budget of the status subcommand: a status check is
// a quick question, so a daemon that is down is retried for half of it (a
// supervisor restart window) and no longer.
const DefaultBudget = 4 * time.Second

// Collector asks the daemon for its report and falls back to the local one.
type Collector struct {
	Static Static
	// Instance is the instance asked; it makes no difference to the answer
	// ("pr" when empty).
	Instance string
	// Budget is the whole call budget; zero uses DefaultBudget.
	Budget time.Duration
	// Now is the clock; nil uses time.Now.
	Now    func() time.Time
	Logger connectord.Logger
}

// Collect returns the report and the verdict's exit code.
//
//   - The daemon answered with a report: that report (source "daemon"), with
//     the build-id difference between this binary and the daemon's filled in.
//   - The daemon answered with an error (it is migrating, or this binary's
//     protocol is outside its window): the local report, degraded, naming the
//     daemon's answer.
//   - No daemon: the local report, down.
func (c Collector) Collect(ctx context.Context) Report {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	budget := c.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	inst := c.Instance
	if inst == "" {
		inst = "pr"
	}
	sock := &socket.Client{
		SocketPath:  c.Static.SocketPath,
		Build:       c.Static.BuildID,
		RestartHint: proc.RestartHint(c.Static.SupervisorLabel, c.Static.ConfigPath),
		Logger:      c.Logger,
	}
	reply, err := sock.Call(ctx, inst, scriptout.Request{Op: OpName}, budget, max(budget-time.Second, 0))
	var (
		down     *socket.DaemonDownError
		mismatch *socket.VersionMismatchError
	)
	switch {
	case errors.As(err, &down):
		return Local(c.Static, now(), "the daemon is not reachable: "+down.Error())
	case errors.As(err, &mismatch):
		return Unhealthy(c.Static, now(), mismatch.Error())
	case err != nil:
		return Local(c.Static, now(), "the daemon is not reachable: "+err.Error())
	}

	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		return Unhealthy(c.Static, now(), "the daemon's answer is unreadable: "+err.Error())
	}
	if resp.Error != nil {
		return Unhealthy(c.Static, now(), fmt.Sprintf("the daemon answered %s: %s", resp.Error.Code, resp.Error.Message))
	}
	var r Report
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		return Unhealthy(c.Static, now(), "the daemon's report is unreadable: "+err.Error())
	}
	r.ClientBuildID = c.Static.BuildID
	r.BuildDiffers = r.BuildID != c.Static.BuildID
	return r
}

// Fallback is the client's local answer for the status op while the daemon is
// down: the local report as the op's result. It runs only for an unreachable
// daemon, so its health is down.
func Fallback(s Static, now func() time.Time) func(ctx context.Context, req scriptout.Request, cause error) (any, error) {
	if now == nil {
		now = time.Now
	}
	return func(_ context.Context, _ scriptout.Request, cause error) (any, error) {
		reason := "the daemon is not reachable"
		if cause != nil {
			reason += ": " + cause.Error()
		}
		return Local(s, now(), reason), nil
	}
}
