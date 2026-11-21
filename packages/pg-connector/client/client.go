package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

// Binary is the program name a Client runs by default.
const Binary = "pg-connector"

// DefaultTimeout bounds one call when Client.Timeout is zero.
const DefaultTimeout = 60 * time.Second

// Client runs pg-connector verbs through a cmdrun.Runner, one method per
// verb. The zero value runs the real binary (cmdrun.Exec); tests set Run to a
// Fake's runner.
type Client struct {
	// Run is the process seam; nil means cmdrun.Exec.
	Run cmdrun.Runner
	// Binary overrides the program name; empty means Binary.
	Binary string
	// Timeout bounds each call; zero means DefaultTimeout.
	Timeout time.Duration
	// Env is appended to each call's environment.
	Env []string
}

// ListRequest selects a "<type> list" call.
type ListRequest struct {
	Type         string
	Query        string
	Backend      string
	IDsOnly      bool
	Fingerprints bool
}

// GetRequest selects a "<type> show <id>" call.
type GetRequest struct {
	Type    string
	ID      string
	Backend string
	Fresh   bool
	Watch   bool
}

// ChangesRequest selects a "<type> changes" call.
type ChangesRequest struct {
	Type     string
	Query    string
	Consumer string
	Backend  string
	Cached   bool
	Reset    bool
	// NoAck forwards and returns the answer without acknowledging any source;
	// the caller then acknowledges each next_seq itself with ChangesAck.
	NoAck bool
}

// ChangesAckRequest selects a "<type> changes-ack" call.
type ChangesAckRequest struct {
	Type     string
	Query    string
	Consumer string
	Backend  string
	NextSeq  int64
}

// CIRequest selects a "ci list <pr-id>" call.
type CIRequest struct {
	PRID  string
	Fresh bool
}

// List runs "<type> list --query Q". A degraded fan-out (exit 5) is returned
// with a nil error; the outcome's ExitCode and Sources say which source
// failed.
func (c Client) List(ctx context.Context, r ListRequest) (ListOutcome[json.RawMessage], error) {
	args := []string{r.Type, "list", "--query", r.Query}
	args = appendFlag(args, "--backend", r.Backend)
	args = appendBool(args, "--ids-only", r.IDsOnly)
	args = appendBool(args, "--fingerprints", r.Fingerprints)
	args = append(args, "--output", "json")
	var out ListOutcome[json.RawMessage]
	err := c.fanOut(ctx, args, &out)
	return out, err
}

// Get runs "<type> show <id>" and returns the answer's result object. A
// not-found answer is an *Error with ExitNotFound (see IsNotFound).
func (c Client) Get(ctx context.Context, r GetRequest) (json.RawMessage, error) {
	args := []string{r.Type, "show", r.ID}
	args = appendBool(args, "--fresh", r.Fresh)
	args = appendFlag(args, "--backend", r.Backend)
	args = appendBool(args, "--watch", r.Watch)
	return c.targeted(ctx, args)
}

// Changes runs "<type> changes --consumer C". A degraded fan-out is returned
// with a nil error.
func (c Client) Changes(ctx context.Context, r ChangesRequest) (ChangesOutcome, error) {
	args := []string{r.Type, "changes"}
	args = appendFlag(args, "--query", r.Query)
	args = append(args, "--consumer", r.Consumer)
	args = appendFlag(args, "--backend", r.Backend)
	args = appendBool(args, "--cached", r.Cached)
	args = appendBool(args, "--reset", r.Reset)
	args = appendBool(args, "--no-ack", r.NoAck)
	args = append(args, "--output", "json")
	var out ChangesOutcome
	err := c.fanOut(ctx, args, &out)
	return out, err
}

// ChangesAck runs "<type> changes-ack" and returns the backend's answer.
func (c Client) ChangesAck(ctx context.Context, r ChangesAckRequest) (json.RawMessage, error) {
	args := []string{r.Type, "changes-ack"}
	args = appendFlag(args, "--query", r.Query)
	args = append(args, "--consumer", r.Consumer, "--next-seq", strconv.FormatInt(r.NextSeq, 10))
	args = appendFlag(args, "--backend", r.Backend)
	args = append(args, "--output", "json")
	return c.targeted(ctx, args)
}

// Ci runs "ci list <pr-id>".
func (c Client) Ci(ctx context.Context, r CIRequest) (CIListOutcome[json.RawMessage], error) {
	args := []string{"ci", "list", r.PRID}
	args = appendBool(args, "--fresh", r.Fresh)
	args = append(args, "--output", "json")
	var out CIListOutcome[json.RawMessage]
	err := c.fanOut(ctx, args, &out)
	return out, err
}

// Capabilities runs "<type> capabilities" (backend optional).
func (c Client) Capabilities(ctx context.Context, entityType, backend string) (json.RawMessage, error) {
	args := []string{entityType, "capabilities"}
	args = appendFlag(args, "--backend", backend)
	args = append(args, "--output", "json")
	return c.stdout(ctx, args, ExitOK)
}

// Health runs "<type> health --backend B".
func (c Client) Health(ctx context.Context, entityType, backend string) (json.RawMessage, error) {
	return c.stdout(ctx, []string{entityType, "health", "--backend", backend, "--output", "json"}, ExitOK)
}

// ConfigValidate runs "config validate"; a degraded answer is returned with a
// nil error.
func (c Client) ConfigValidate(ctx context.Context) (FanOutOutcome, error) {
	var out FanOutOutcome
	err := c.fanOut(ctx, []string{"config", "validate"}, &out)
	return out, err
}

// LedgerShow runs "ledger show" and returns its stdout.
func (c Client) LedgerShow(ctx context.Context) (json.RawMessage, error) {
	return c.stdout(ctx, []string{"ledger", "show", "--output", "json"}, ExitOK)
}

// do runs one call and maps every failure that is not a clean exit into an
// *Error; it returns the raw result for the caller to classify by exit code.
func (c Client) do(ctx context.Context, args []string) (cmdrun.Result, *Error) {
	run := c.Run
	if run == nil {
		run = cmdrun.Exec
	}
	bin := c.Binary
	if bin == "" {
		bin = Binary
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	res := run(ctx, cmdrun.Call{Name: bin, Args: args, Env: c.Env, Timeout: timeout})
	switch {
	case res.StartErr != nil:
		return res, &Error{Args: args, ExitCode: -1, Cause: res.StartErr}
	case res.CtxErr != nil:
		return res, &Error{Args: args, ExitCode: -1, Cause: res.CtxErr}
	case res.Signal != "":
		return res, &Error{Args: args, ExitCode: -1, Cause: fmt.Errorf("ended by signal %s", res.Signal)}
	}
	return res, nil
}

// fanOut runs a fan-out verb: exit 0 and 5 decode stdout into out; any other
// exit is an *Error.
func (c Client) fanOut(ctx context.Context, args []string, out any) error {
	res, err := c.do(ctx, args)
	if err != nil {
		return err
	}
	if res.Code != ExitOK && res.Code != ExitDegraded {
		return newExitError(args, res.Code, res.Stdout)
	}
	if derr := json.Unmarshal(res.Stdout, out); derr != nil {
		return &Error{Args: args, ExitCode: res.Code, Message: "undecodable stdout", Cause: derr}
	}
	return nil
}

// targeted runs a single-backend verb: exit 0 returns the envelope's result;
// any other exit (4 NotFound included) is an *Error.
func (c Client) targeted(ctx context.Context, args []string) (json.RawMessage, error) {
	res, err := c.do(ctx, args)
	if err != nil {
		return nil, err
	}
	if res.Code != ExitOK {
		return nil, newExitError(args, res.Code, res.Stdout)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if derr := json.Unmarshal(res.Stdout, &env); derr != nil {
		return nil, &Error{Args: args, ExitCode: res.Code, Message: "undecodable stdout", Cause: derr}
	}
	return env.Result, nil
}

// stdout runs a verb and returns its raw stdout on the accepted exit code.
func (c Client) stdout(ctx context.Context, args []string, accept int) (json.RawMessage, error) {
	res, err := c.do(ctx, args)
	if err != nil {
		return nil, err
	}
	if res.Code != accept {
		return nil, newExitError(args, res.Code, res.Stdout)
	}
	return json.RawMessage(res.Stdout), nil
}

func appendFlag(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

func appendBool(args []string, flag string, on bool) []string {
	if !on {
		return args
	}
	return append(args, flag)
}
