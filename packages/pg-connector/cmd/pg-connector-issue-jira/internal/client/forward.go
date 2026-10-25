package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// socketClient is the socket client every call of this package dials with.
func socketClient(o Options) *socket.Client {
	return &socket.Client{
		SocketPath:  o.Config.SocketPath,
		DaemonName:  config.DaemonName,
		Build:       o.Build,
		Protocol:    o.Protocol,
		RestartHint: proc.RestartHint(o.Config.SupervisorLabel, o.Config.Path, config.DaemonName),
		Logger:      o.Logger,
	}
}

// forward sends req to the daemon with the request's own deadline less a
// margin, so the daemon never assumes one, and relays the answer. With the
// daemon down it answers unavailable (naming the socket path, the supervisor
// and its restart hint) and appends the daemon_unreachable client-log row; it
// never reads the store, never calls the origin and never answers from a
// cache. The one exception is the status op, which the op catalog says reports
// even when the daemon is down: it is answered locally with status.Fallback
// (health down, source local), exit 0, and no daemon_unreachable row, because
// a caller that got a report did not see an outage.
func forward(ctx context.Context, o Options, req scriptout.Request, stdout io.Writer) int {
	budget := o.Budget
	if budget <= 0 {
		budget = BudgetFor(req.Op)
	}
	margin := o.Margin
	if margin <= 0 {
		margin = ForwardMargin
	}
	deadline := max(budget-margin, 0)

	started := time.Now()
	reply, err := socketClient(o).Call(ctx, RouterInstance, req, budget, deadline)
	var (
		down     *socket.DaemonDownError
		mismatch *socket.VersionMismatchError
	)
	switch {
	case err == nil:
		return relay(stdout, reply.Response)
	case errors.As(err, &mismatch):
		return writeOpError(stdout, req.Op, scriptout.ErrVersionMismatch, mismatch.Error())
	case errors.As(err, &down):
		if req.Op == status.Op {
			return localStatus(ctx, o, req, down, stdout)
		}
		o.ClientLog.DaemonUnreachable(o.Config.Instance, req.Op, started, down.Error())
		return writeOpError(stdout, req.Op, scriptout.ErrUnavailable, down.Error())
	default:
		return writeOpError(stdout, req.Op, scriptout.ErrUnavailable, err.Error())
	}
}

// localStatus answers the status op while the daemon is down: the static
// report with the reason the daemon could not be reached, as the op's result.
func localStatus(ctx context.Context, o Options, req scriptout.Request, cause error, stdout io.Writer) int {
	result, err := status.Fallback(status.Static(o.Config, o.Build))(ctx, req, cause)
	if err != nil {
		return writeOpError(stdout, req.Op, scriptout.ErrUnavailable, err.Error())
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return writeOpError(stdout, req.Op, scriptout.ErrUnavailable, fmt.Sprintf("client: marshal the local status report: %v", err))
	}
	return writeResponse(stdout, &scriptout.Response{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersion:   SchemaVersionFor(req.Op),
		Result:          raw,
	})
}

// DefaultStatusBudget is the whole call budget of the status mode.
const DefaultStatusBudget = 10 * time.Second

// CollectStatus asks the daemon for its report and falls back to the static
// one. A daemon that answered with a report: that report (source "daemon").
// A daemon that answered with an error (it is migrating, or this binary's
// protocol is outside its window): the static report, degraded, naming the
// daemon's answer. No daemon: the static report, down (exit 3).
func CollectStatus(ctx context.Context, o Options) status.Report {
	static := status.Static(o.Config, o.Build)
	budget := o.Budget
	if budget <= 0 {
		budget = DefaultStatusBudget
	}
	reply, err := socketClient(o).Call(ctx, RouterInstance, scriptout.Request{Op: status.Op}, budget, max(budget-time.Second, 0))
	var (
		down     *socket.DaemonDownError
		mismatch *socket.VersionMismatchError
	)
	switch {
	case errors.As(err, &down):
		return downReport(ctx, static, down)
	case errors.As(err, &mismatch):
		return degraded(static, mismatch.Error())
	case err != nil:
		return downReport(ctx, static, err)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		return degraded(static, "the daemon's answer is unreadable: "+err.Error())
	}
	if resp.Error != nil {
		return degraded(static, fmt.Sprintf("the daemon answered %s: %s", resp.Error.Code, resp.Error.Message))
	}
	var r status.Report
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		return degraded(static, "the daemon's report is unreadable: "+err.Error())
	}
	return r
}

// downReport is the static report with the reason the daemon was unreachable,
// health down.
func downReport(ctx context.Context, static status.Report, cause error) status.Report {
	out, _ := status.Fallback(static)(ctx, scriptout.Request{Op: status.Op}, cause)
	if r, ok := out.(status.Report); ok {
		return r
	}
	static.Health = status.Down
	return static
}

// degraded is the static report for a daemon that answered but not with a
// report: health degraded, with the daemon's answer as the reason.
func degraded(static status.Report, reason string) status.Report {
	static.Health, static.Source = status.Degraded, status.SourceLocal
	static.Reasons = append([]string{reason}, static.Reasons...)
	return static
}
