package scriptout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// --------------------------------------------------------------------
// Fake providers used by Serve* tests.
// --------------------------------------------------------------------

type fakeCICD struct {
	runs    []api.CIRun
	logs    string
	runsErr error
}

func (f *fakeCICD) ListRuns(_ context.Context, _ string, _ int) ([]api.CIRun, error) {
	if f.runsErr != nil {
		return nil, f.runsErr
	}
	return f.runs, nil
}

func (f *fakeCICD) GetLogs(_ context.Context, _ string) ([]byte, error) {
	return []byte(f.logs), nil
}
func (f *fakeCICD) RerunFailed(_ context.Context, _ string, _ int) error { return nil }

type fakeIssues struct {
	issue *api.Issue
	err   error
}

func (f *fakeIssues) GetIssue(_ context.Context, _ string) (*api.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.issue, nil
}

// fakeIssuesWithAuth implements AuthChecker.
type fakeIssuesWithAuth struct {
	fakeIssues
	state AuthStatusState
}

func (f *fakeIssuesWithAuth) AuthStatus(_ context.Context) AuthStatus {
	return AuthStatus{State: f.state, Detail: "test"}
}

// --------------------------------------------------------------------
// dispatchCICD tests
// --------------------------------------------------------------------

func TestDispatchCICD_ListRuns(t *testing.T) {
	p := &fakeCICD{runs: []api.CIRun{{ID: "r1", Name: "build", Status: "success"}}}
	req := &Request{Op: OpListRuns, Args: json.RawMessage(`{"repo":"o/r","pr_number":42}`)}
	res, err := dispatchCICD(context.Background(), p, req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	runs, ok := res.([]api.CIRun)
	if !ok {
		t.Fatalf("result type: %T", res)
	}
	if len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestDispatchCICD_ListRunsError(t *testing.T) {
	p := &fakeCICD{runsErr: errors.New("captains-log: 401 unauthorized")}
	req := &Request{Op: OpListRuns, Args: json.RawMessage(`{"repo":"o/r","pr_number":1}`)}
	_, err := dispatchCICD(context.Background(), p, req)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected upstream error, got %v", err)
	}
}

func TestDispatchCICD_GetLogs(t *testing.T) {
	p := &fakeCICD{logs: "line1\nline2\n"}
	req := &Request{Op: OpGetLogs, Args: json.RawMessage(`{"run_id":"r1"}`)}
	res, err := dispatchCICD(context.Background(), p, req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	s, ok := res.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", res)
	}
	if s != "line1\nline2\n" {
		t.Fatalf("logs: %q", s)
	}
}

func TestDispatchCICD_UnknownOp(t *testing.T) {
	p := &fakeCICD{}
	req := &Request{Op: "bogus"}
	_, err := dispatchCICD(context.Background(), p, req)
	if err == nil || !strings.Contains(err.Error(), "unknown CICD op") {
		t.Fatalf("expected unknown-op error, got %v", err)
	}
}

func TestDispatchCICD_AuthStatusDefault(t *testing.T) {
	p := &fakeCICD{}
	req := &Request{Op: OpAuthStatus}
	res, err := dispatchCICD(context.Background(), p, req)
	if err != nil {
		t.Fatalf("auth_status: %v", err)
	}
	st, ok := res.(AuthStatus)
	if !ok {
		t.Fatalf("expected AuthStatus, got %T", res)
	}
	if st.State != AuthOK {
		t.Fatalf("expected OK, got %s", st.State)
	}
}

// --------------------------------------------------------------------
// dispatchIssues tests
// --------------------------------------------------------------------

func TestDispatchIssues_GetIssue(t *testing.T) {
	p := &fakeIssues{issue: &api.Issue{ID: "ZR-1", Title: "do the thing", State: "Open"}}
	req := &Request{Op: OpGetIssue, Args: json.RawMessage(`{"id":"ZR-1"}`)}
	res, err := dispatchIssues(context.Background(), p, req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	issue, ok := res.(*api.Issue)
	if !ok {
		t.Fatalf("result type: %T", res)
	}
	if issue.ID != "ZR-1" {
		t.Fatalf("issue: %+v", issue)
	}
}

func TestDispatchIssues_AuthChecker(t *testing.T) {
	p := &fakeIssuesWithAuth{state: AuthMissing}
	req := &Request{Op: OpAuthStatus}
	res, err := dispatchIssues(context.Background(), p, req)
	if err != nil {
		t.Fatalf("auth_status: %v", err)
	}
	st := res.(AuthStatus)
	if st.State != AuthMissing {
		t.Fatalf("expected MISSING, got %s", st.State)
	}
	if st.Detail != "test" {
		t.Fatalf("detail: %q", st.Detail)
	}
}

func TestDispatchIssues_BadArgs(t *testing.T) {
	p := &fakeIssues{}
	req := &Request{Op: OpGetIssue, Args: json.RawMessage(`{"id":123}`)} // type mismatch
	_, err := dispatchIssues(context.Background(), p, req)
	if err == nil || !strings.Contains(err.Error(), "decode args") {
		t.Fatalf("expected decode-args error, got %v", err)
	}
}

// --------------------------------------------------------------------
// runServe wire tests
// --------------------------------------------------------------------

func TestRunServe_SuccessJSON(t *testing.T) {
	in := strings.NewReader(`{"op":"list_runs","args":{"repo":"o/r","pr_number":1}}`)
	var out bytes.Buffer
	p := &fakeCICD{runs: []api.CIRun{{ID: "r"}}}
	code := runServe(dispatchEnv{in: in, out: &out}, func(req *Request) (any, error) {
		return dispatchCICD(context.Background(), p, req)
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s", code, out.String())
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestRunServe_ErrorJSON(t *testing.T) {
	in := strings.NewReader(`{"op":"list_runs","args":{"repo":"o/r","pr_number":1}}`)
	var out bytes.Buffer
	p := &fakeCICD{runsErr: errors.New("captains-log: 500 internal")}
	code := runServe(dispatchEnv{in: in, out: &out}, func(req *Request) (any, error) {
		return dispatchCICD(context.Background(), p, req)
	})
	if code != 1 {
		t.Fatalf("expected exit 1 on error, got %d (stdout=%s)", code, out.String())
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if !strings.Contains(resp.Error, "500 internal") {
		t.Fatalf("error: %q", resp.Error)
	}
}

func TestRunServe_EmptyStdin(t *testing.T) {
	in := strings.NewReader("")
	var out bytes.Buffer
	code := runServe(dispatchEnv{in: in, out: &out}, func(_ *Request) (any, error) {
		t.Fatal("dispatch should not run on empty stdin")
		return nil, nil
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(out.String(), "empty stdin") {
		t.Fatalf("expected empty-stdin error, got %s", out.String())
	}
}

func TestRunServe_MissingOp(t *testing.T) {
	in := strings.NewReader(`{"args":{}}`)
	var out bytes.Buffer
	code := runServe(dispatchEnv{in: in, out: &out}, func(_ *Request) (any, error) {
		t.Fatal("dispatch should not run")
		return nil, nil
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(out.String(), "missing required field") {
		t.Fatalf("expected missing-op error, got %s", out.String())
	}
}
