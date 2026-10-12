// Package client is the client mode of pg-connector-github (the pr and ci
// modes): the thin scriptout backend the umbrella execs. It reads one
// scriptout request on stdin, writes one response on stdout and exits with the
// code scriptout.ExitCodeForCode gives the response's error code.
//
// It never opens the store and never calls GitHub. capabilities is answered
// from compiled-in values without the daemon: the umbrella's cacheEnabled
// fails OPEN when capabilities fails, so a forwarded call to a down daemon
// would silently re-enable the umbrella cache and hide the outage. EVERY
// other op, listed or not, is forwarded over the socket with the request's own
// deadline less a margin, so the daemon never assumes one; the daemon answers
// unknown_op for an op nobody registered, and with the daemon down any
// forwarded op answers unavailable (naming the socket path and the platform's
// restart hint) unless the op supplies a Fallback.
//
// The loop is this package's own rather than scriptout.ServeLoop because
// ServeLoop answers unknown_op for an op missing from its table, where this
// client must forward it.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ForwardMargin is taken off the call's budget to give the deadline the daemon
// is told, so the daemon answers before the client's own budget ends and the
// client still has time to write what it got.
const ForwardMargin = time.Second

// Options configure Run.
type Options struct {
	Instance Instance
	// SocketPath, SupervisorLabel and ConfigPath come from the loaded config.
	SocketPath      string
	SupervisorLabel string
	ConfigPath      string
	// Build is this binary's version, sent as client_build and reported by
	// capabilities.
	Build string
	// Budget, when positive, replaces the per-op budget (tests; the real
	// binary leaves it zero).
	Budget time.Duration
	// Margin overrides ForwardMargin; zero uses it.
	Margin time.Duration
	// Protocol overrides the client's socket protocol (tests); zero uses
	// socket.ProtocolVersion.
	Protocol int
	Logger   connectord.Logger
}

// BudgetFor is the budget the client gives a call to op: the backend deadline
// scriptout.ServeLoop would apply to it (pkg/scriptout/limits.go).
func BudgetFor(op string) time.Duration {
	switch op {
	case scriptout.OpList:
		return scriptout.ListBackendTimeout
	case scriptout.OpListActivity:
		return scriptout.ListActivityBackendTimeout
	}
	return scriptout.DefaultBackendTimeout
}

// Run serves one request from stdin and returns the process exit code.
func Run(ctx context.Context, o Options, stdin io.Reader, stdout io.Writer) int {
	req, err := readRequest(stdin)
	if err != nil {
		return writeResponse(stdout, scriptout.ErrorResponse(scriptout.WrapError(scriptout.ErrUnavailable, err.Error())))
	}

	if req.Op == scriptout.OpCapabilities {
		if err := writeJSON(stdout, o.Instance.Capabilities(o.Build)); err != nil {
			return 1
		}
		return 0
	}

	sock := &socket.Client{
		SocketPath:  o.SocketPath,
		Build:       o.Build,
		Protocol:    o.Protocol,
		RestartHint: proc.RestartHint(o.SupervisorLabel, o.ConfigPath),
		Logger:      o.Logger,
	}
	budget := o.Budget
	if budget <= 0 {
		budget = BudgetFor(req.Op)
	}
	margin := o.Margin
	if margin <= 0 {
		margin = ForwardMargin
	}
	deadline := max(budget-margin, 0)

	reply, err := sock.Call(ctx, o.Instance.Name, *req, budget, deadline)
	var (
		down     *socket.DaemonDownError
		mismatch *socket.VersionMismatchError
	)
	switch {
	case err == nil:
		return relay(stdout, reply.Response)
	case errors.As(err, &mismatch):
		return writeOpError(stdout, o.Instance, req.Op, scriptout.ErrVersionMismatch, mismatch.Error())
	case errors.As(err, &down):
		if op, ok := o.Instance.Ops[req.Op]; ok && op.Fallback != nil {
			return runFallback(ctx, stdout, op, *req, down)
		}
		return writeOpError(stdout, o.Instance, req.Op, scriptout.ErrUnavailable, down.Error())
	default:
		return writeOpError(stdout, o.Instance, req.Op, scriptout.ErrUnavailable, err.Error())
	}
}

// runFallback answers from the op's local fallback.
func runFallback(ctx context.Context, stdout io.Writer, op Op, req scriptout.Request, cause error) int {
	result, err := op.Fallback(ctx, req, cause)
	if err != nil {
		resp := scriptout.ErrorResponse(err)
		resp.SchemaVersion = op.SchemaVersion
		return writeResponse(stdout, resp)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp := scriptout.ErrorResponse(fmt.Errorf("client: marshal the fallback result for op %q: %w", req.Op, err))
		resp.SchemaVersion = op.SchemaVersion
		return writeResponse(stdout, resp)
	}
	return writeResponse(stdout, &scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, SchemaVersion: op.SchemaVersion, Result: raw})
}

// relay writes the daemon's response to stdout unmodified and returns the exit
// code its error code (if any) maps to.
func relay(stdout io.Writer, raw json.RawMessage) int {
	if _, err := stdout.Write(append(append([]byte(nil), raw...), '\n')); err != nil {
		return 1
	}
	var probe struct {
		Error *scriptout.Error `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Error == nil {
		return 0
	}
	if code := scriptout.ExitCodeForCode(probe.Error.Code); code != 0 {
		return code
	}
	// An error body with a code outside the taxonomy (a misbehaving daemon)
	// still exits non-zero, in the generic unavailable class.
	return scriptout.ExitCodeForCode("unavailable")
}

// writeOpError answers a failure the client composes itself, stamped with the
// op's schema version when the op is listed.
func writeOpError(stdout io.Writer, inst Instance, op string, sentinel error, message string) int {
	resp := scriptout.ErrorResponse(scriptout.WrapError(sentinel, message))
	if o, ok := inst.Ops[op]; ok {
		resp.SchemaVersion = o.SchemaVersion
	}
	return writeResponse(stdout, resp)
}

// writeResponse writes resp and returns the exit code for its error, if any.
func writeResponse(w io.Writer, resp *scriptout.Response) int {
	if err := writeJSON(w, resp); err != nil {
		return 1
	}
	if resp.Error == nil {
		return 0
	}
	return scriptout.ExitCodeForCode(resp.Error.Code)
}

// writeJSON encodes v as one JSON object, HTML escaping off like scriptout.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// readRequest reads exactly one JSON request object from r, with the same
// checks (and the same error classification, unavailable) as scriptout's own
// loop.
func readRequest(r io.Reader) (*scriptout.Request, error) {
	var req scriptout.Request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("client: empty stdin (expected one JSON request)")
		}
		return nil, fmt.Errorf("client: decode request: %w", err)
	}
	if req.Op == "" {
		return nil, errors.New(`client: request is missing required field "op"`)
	}
	return &req, nil
}
