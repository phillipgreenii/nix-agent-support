package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ForwardMargin is taken off the call's budget to give the deadline the daemon
// is told, so the daemon answers before the client's own budget ends and the
// client still has time to write what it got.
const ForwardMargin = time.Second

// Options configure Run.
type Options struct {
	// Config is the loaded config (REQUIRED): the socket path, the supervisor
	// label, the config path, the instance name, the write-notice timeout and
	// the queries capabilities lists.
	Config *config.Config
	// Build is this binary's version, sent as client_build and reported by
	// capabilities.
	Build string
	// Budget, when positive, replaces the per-op budget of a forwarded read
	// (tests; the real binary leaves it zero).
	Budget time.Duration
	// Margin overrides ForwardMargin; zero uses it.
	Margin time.Duration
	// Protocol overrides the client's socket protocol (tests); zero uses
	// socket.ProtocolVersion.
	Protocol int
	Logger   connectord.Logger
	// ClientLog, when non-nil, receives a kind=request row with outcome
	// daemon_unreachable whenever Run answers unavailable because the daemon
	// cannot be reached.
	ClientLog *logs.ClientLog
	// Backend performs the five writes directly against the origin tool. Nil
	// means the pjira-backed backend under the caller's own environment (the
	// real binary leaves it nil; tests pass a fake).
	Backend issue.Provider
}

// BudgetFor is the budget the client gives a forwarded call to op: the backend
// deadline scriptout.ServeLoop would apply to it (pkg/scriptout/limits.go).
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
	raw, req, err := readRequest(stdin)
	if err != nil {
		return writeResponse(stdout, scriptout.ErrorResponse(scriptout.WrapError(scriptout.ErrUnavailable, err.Error())))
	}
	switch {
	case req.Op == scriptout.OpCapabilities:
		if err := writeJSON(stdout, Capabilities(o.Config, o.Build)); err != nil {
			return 1
		}
		return 0
	case IsWrite(req.Op):
		return runWrite(ctx, o, raw, *req, stdout)
	}
	return forward(ctx, o, *req, stdout)
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
// schema version of the op's capability.
func writeOpError(stdout io.Writer, op string, sentinel error, message string) int {
	resp := scriptout.ErrorResponse(scriptout.WrapError(sentinel, message))
	resp.SchemaVersion = SchemaVersionFor(op)
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

// readRequest reads exactly one JSON request object from r (without waiting
// for end of input) and returns its raw bytes with the decoded request, with
// the same checks (and the same error classification, unavailable) as
// scriptout's own loop.
func readRequest(r io.Reader) (json.RawMessage, *scriptout.Request, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("client: empty stdin (expected one JSON request)")
		}
		return nil, nil, fmt.Errorf("client: decode request: %w", err)
	}
	var req scriptout.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, nil, fmt.Errorf("client: decode request: %w", err)
	}
	if req.Op == "" {
		return nil, nil, errors.New(`client: request is missing required field "op"`)
	}
	return raw, &req, nil
}
