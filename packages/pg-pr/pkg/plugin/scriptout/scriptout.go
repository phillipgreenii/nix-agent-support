// Package scriptout implements the stdin/stdout JSON protocol that lets
// external providers be packaged as standalone binaries. A provider binary
// calls one of the Serve* functions in its main(); pg-pr's core spawns the
// binary, writes a single request JSON object to its stdin, reads a single
// response JSON object from its stdout, and waits for it to exit.
//
// Protocol (one request, one response):
//
//	stdin:  {"op": "<name>", "args": {...}}
//	stdout: {"result": ...}             on success (exit 0)
//	stdout: {"error":  "..."}           on error   (exit 1)
//
// Each Serve* function dispatches on `op` to a method on the wrapped
// provider; unknown ops yield a structured error. The wire shapes for args
// are documented per-op below.
package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/issues"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// ErrNotImplemented is retained for backwards compatibility. The Phase 0
// stubs used to return it; current code dispatches real ops. The variable
// stays exported so existing callers checking with errors.Is continue to
// compile; it is not returned anywhere in the current implementation.
var ErrNotImplemented = errors.New("scriptout: not implemented in this phase")

// Op constants name the protocol operations. Both ends use these strings.
const (
	// Common / meta ops.
	OpAuthStatus = "auth_status"

	// VCS ops.
	OpGetPR         = "get_pr"
	OpListMyPRs     = "list_my_prs"
	OpListTeamPRs   = "list_team_prs"
	OpCreatePR      = "create_pr"
	OpUpdatePR      = "update_pr"
	OpSetDraft      = "set_draft"
	OpSetAutomerge  = "set_automerge"
	OpMerge         = "merge"
	OpClose         = "close"
	OpListComments  = "list_comments"
	OpAddComment    = "add_comment"
	OpReplyToThread = "reply_to_thread"
	OpResolveThread = "resolve_thread"
	OpPostReview    = "post_review"
	OpListReviews   = "list_reviews"

	// CICD ops.
	OpListRuns    = "list_runs"
	OpGetLogs     = "get_logs"
	OpRerunFailed = "rerun_failed"

	// Issues ops.
	OpGetIssue = "get_issue"
)

// Request is the wire shape read from stdin.
type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the wire shape written to stdout. Exactly one of Result or
// Error is populated; the JSON encoder will emit the appropriate key.
type Response struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// AuthStatusState enumerates the standard auth states a provider may return.
type AuthStatusState string

const (
	AuthOK                 AuthStatusState = "OK"
	AuthMissing            AuthStatusState = "MISSING"
	AuthExpired            AuthStatusState = "EXPIRED"
	AuthInsufficientScopes AuthStatusState = "INSUFFICIENT_SCOPES"
)

// AuthStatus is the result payload of the auth_status op.
type AuthStatus struct {
	State  AuthStatusState `json:"state"`
	Detail string          `json:"detail,omitempty"`
}

// AuthChecker is an optional interface a provider may implement to surface
// a richer auth-status response. Providers that don't implement it return
// AuthOK from auth_status (since they were constructed successfully).
type AuthChecker interface {
	AuthStatus(ctx context.Context) AuthStatus
}

// PostReviewArgs is the args shape for the post_review op. Marshaled and
// unmarshaled by callers via json; declared here so both ends can share the
// definition.
type PostReviewArgs struct {
	Repo     string        `json:"repo"`
	Number   int           `json:"number"`
	CommitID string        `json:"commit_id,omitempty"`
	Body     string        `json:"body"`
	Comments []api.Comment `json:"comments"`
}

// CreatePRArgs is the args shape for create_pr.
type CreatePRArgs struct {
	Repo      string   `json:"repo"`
	Draft     bool     `json:"draft"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Branch    string   `json:"branch"`
	Base      string   `json:"base"`
	Reviewers []string `json:"reviewers,omitempty"`
	Labels    []string `json:"labels,omitempty"`
}

// dispatchEnv bundles stdin/stdout/stderr so tests can swap them out.
type dispatchEnv struct {
	in  io.Reader
	out io.Writer
}

func defaultEnv() dispatchEnv {
	return dispatchEnv{in: os.Stdin, out: os.Stdout}
}

// readRequest reads exactly one JSON object from r.
func readRequest(r io.Reader) (*Request, error) {
	dec := json.NewDecoder(r)
	var req Request
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("scriptout: empty stdin (expected one JSON request)")
		}
		return nil, fmt.Errorf("scriptout: decode request: %w", err)
	}
	if req.Op == "" {
		return nil, errors.New("scriptout: request is missing required field \"op\"")
	}
	return &req, nil
}

// writeResponse encodes one Response JSON object to w.
func writeResponse(w io.Writer, resp Response) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(resp)
}

// runServe is the shared driver: it reads one request, calls dispatch,
// writes one response, and returns the exit code for main() to use.
// dispatch should return (result, error). On error, the error string lands
// in Response.Error and we exit non-zero.
func runServe(env dispatchEnv, dispatch func(*Request) (any, error)) int {
	req, err := readRequest(env.in)
	if err != nil {
		_ = writeResponse(env.out, Response{Error: err.Error()})
		return 1
	}
	result, err := dispatch(req)
	if err != nil {
		_ = writeResponse(env.out, Response{Error: err.Error()})
		return 1
	}
	if err := writeResponse(env.out, Response{Result: result}); err != nil {
		// At this point stdout is unwritable; nothing we can do but exit 1.
		return 1
	}
	return 0
}

// ----------------------------------------------------------------------
// ServeVCS
// ----------------------------------------------------------------------

// ServeVCS reads one JSON op from stdin, dispatches to p, and writes the
// response to stdout. Returns nil on success; on error the error has been
// reported via stdout already and the binary should still os.Exit non-zero.
// Existing callers that wrap ServeVCS in a main() and pass the returned
// error to os.Exit get the right behaviour: a non-nil error from ServeVCS
// means the binary should exit non-zero, but the structured error JSON has
// already been written.
func ServeVCS(p vcs.Provider) error {
	return serveWithCode(defaultEnv(), func(req *Request) (any, error) {
		return dispatchVCS(context.Background(), p, req)
	})
}

func dispatchVCS(ctx context.Context, p vcs.Provider, req *Request) (any, error) {
	switch req.Op {
	case OpAuthStatus:
		return checkAuth(ctx, p), nil
	case OpGetPR:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.GetPR(ctx, a.Repo, a.Number)
	case OpListMyPRs:
		var a struct {
			Repo string `json:"repo"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ListMyPRs(ctx, a.Repo)
	case OpListTeamPRs:
		var a struct {
			Repo    string   `json:"repo"`
			Members []string `json:"members"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ListTeamPRs(ctx, a.Repo, a.Members)
	case OpCreatePR:
		var a CreatePRArgs
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.CreatePR(ctx, a.Repo, a.Draft, a.Title, a.Body, a.Branch, a.Base, a.Reviewers, a.Labels)
	case OpUpdatePR:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Body   string `json:"body"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.UpdatePR(ctx, a.Repo, a.Number, a.Body)
	case OpSetDraft:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Draft  bool   `json:"draft"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.SetDraft(ctx, a.Repo, a.Number, a.Draft)
	case OpSetAutomerge:
		var a struct {
			Repo    string `json:"repo"`
			Number  int    `json:"number"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.SetAutomerge(ctx, a.Repo, a.Number, a.Enabled)
	case OpMerge:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.Merge(ctx, a.Repo, a.Number)
	case OpClose:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.Close(ctx, a.Repo, a.Number)
	case OpListComments:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ListComments(ctx, a.Repo, a.Number)
	case OpAddComment:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
			Body   string `json:"body"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.AddComment(ctx, a.Repo, a.Number, a.Body)
	case OpReplyToThread:
		var a struct {
			Repo     string `json:"repo"`
			ThreadID string `json:"thread_id"`
			Body     string `json:"body"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ReplyToThread(ctx, a.Repo, a.ThreadID, a.Body)
	case OpResolveThread:
		var a struct {
			Repo     string `json:"repo"`
			ThreadID string `json:"thread_id"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.ResolveThread(ctx, a.Repo, a.ThreadID)
	case OpPostReview:
		var a PostReviewArgs
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.PostReview(ctx, a.Repo, a.Number, a.CommitID, a.Body, a.Comments)
	case OpListReviews:
		var a struct {
			Repo   string `json:"repo"`
			Number int    `json:"number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ListReviews(ctx, a.Repo, a.Number)
	default:
		return nil, fmt.Errorf("scriptout: unknown VCS op %q", req.Op)
	}
}

// ----------------------------------------------------------------------
// ServeCICD
// ----------------------------------------------------------------------

// ServeCICD reads one JSON op from stdin, dispatches to p, writes the
// response to stdout.
func ServeCICD(p cicd.Provider) error {
	return serveWithCode(defaultEnv(), func(req *Request) (any, error) {
		return dispatchCICD(context.Background(), p, req)
	})
}

func dispatchCICD(ctx context.Context, p cicd.Provider, req *Request) (any, error) {
	switch req.Op {
	case OpAuthStatus:
		return checkAuth(ctx, p), nil
	case OpListRuns:
		var a struct {
			Repo     string `json:"repo"`
			PRNumber int    `json:"pr_number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.ListRuns(ctx, a.Repo, a.PRNumber)
	case OpGetLogs:
		var a struct {
			RunID string `json:"run_id"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		b, err := p.GetLogs(ctx, a.RunID)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case OpRerunFailed:
		var a struct {
			Repo     string `json:"repo"`
			PRNumber int    `json:"pr_number"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return nil, p.RerunFailed(ctx, a.Repo, a.PRNumber)
	default:
		return nil, fmt.Errorf("scriptout: unknown CICD op %q", req.Op)
	}
}

// ----------------------------------------------------------------------
// ServeIssues
// ----------------------------------------------------------------------

// ServeIssues reads one JSON op from stdin, dispatches to p, writes the
// response to stdout.
func ServeIssues(p issues.Provider) error {
	return serveWithCode(defaultEnv(), func(req *Request) (any, error) {
		return dispatchIssues(context.Background(), p, req)
	})
}

func dispatchIssues(ctx context.Context, p issues.Provider, req *Request) (any, error) {
	switch req.Op {
	case OpAuthStatus:
		return checkAuth(ctx, p), nil
	case OpGetIssue:
		var a struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(req, &a); err != nil {
			return nil, err
		}
		return p.GetIssue(ctx, a.ID)
	default:
		return nil, fmt.Errorf("scriptout: unknown Issues op %q", req.Op)
	}
}

// ----------------------------------------------------------------------
// Shared helpers
// ----------------------------------------------------------------------

// decodeArgs decodes req.Args into v. An empty/missing args object is
// treated as the zero value (some ops have no args).
func decodeArgs(req *Request, v any) error {
	if len(req.Args) == 0 || string(req.Args) == "null" {
		return nil
	}
	if err := json.Unmarshal(req.Args, v); err != nil {
		return fmt.Errorf("scriptout: decode args for op %q: %w", req.Op, err)
	}
	return nil
}

// checkAuth returns a provider's auth status. If the provider implements
// AuthChecker, we delegate; otherwise we report AuthOK (the provider was
// constructed successfully, so credentials were at least present).
func checkAuth(ctx context.Context, p any) AuthStatus {
	if ac, ok := p.(AuthChecker); ok {
		return ac.AuthStatus(ctx)
	}
	return AuthStatus{State: AuthOK}
}

// serveWithCode runs dispatch and translates the exit code into a Go
// error so callers that pass our return value to os.Exit (after writing
// nothing else) terminate non-zero on protocol errors. The structured
// JSON error has already been written to stdout.
func serveWithCode(env dispatchEnv, dispatch func(*Request) (any, error)) error {
	if runServe(env, dispatch) != 0 {
		// Don't print the error to stderr — the JSON-on-stdout contract is
		// the protocol. main()'s wrapping `fmt.Fprintln(os.Stderr, err)` is
		// fine to fire too: it surfaces the same string for humans.
		return errors.New("scriptout: request failed (see JSON response on stdout)")
	}
	return nil
}
