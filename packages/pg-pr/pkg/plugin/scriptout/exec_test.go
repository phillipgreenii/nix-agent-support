package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// --------------------------------------------------------------------
// Test-helper-process pattern.
//
// We re-invoke the test binary with GO_WANT_HELPER_PROCESS=1 to act as a
// fake provider script. GO_HELPER_BEHAVIOR controls what it does.
// --------------------------------------------------------------------

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)
	helperMain()
}

// helperMain reads stdin, then writes a stdout response controlled by env.
func helperMain() {
	behavior := os.Getenv("GO_HELPER_BEHAVIOR")
	// Always read stdin so the parent's pipe doesn't EPIPE on close.
	stdin, _ := io.ReadAll(os.Stdin)
	_ = stdin

	switch behavior {
	case "ok_runs":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			Result: []api.CIRun{{ID: "r1", Name: "build", Status: "completed", Provider: "captains-log"}},
		})
		os.Exit(0)
	case "error":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			Error: "captains-log: 401 unauthorized",
		})
		os.Exit(1)
	case "non_json":
		_, _ = fmt.Fprintln(os.Stdout, "this is not json")
		os.Exit(0)
	case "ok_issue":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			Result: api.Issue{ID: "ZR-7", Title: "do thing", State: "Open"},
		})
		os.Exit(0)
	case "ok_logs":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			Result: "log line A\nlog line B\n",
		})
		os.Exit(0)
	case "ok_pr":
		_ = json.NewEncoder(os.Stdout).Encode(Response{
			Result: api.PR{Repo: "o/r", Number: 7, State: "OPEN"},
		})
		os.Exit(0)
	case "echo_op":
		// Decodes the request and returns its op back as the result, so
		// tests can confirm wiring of every method.
		var req Request
		if err := json.Unmarshal(stdin, &req); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(Response{Error: "decode: " + err.Error()})
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Response{Result: req.Op})
		os.Exit(0)
	case "stderr_only":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "unknown GO_HELPER_BEHAVIOR")
		os.Exit(99)
	}
}

// helperCmdFactory returns an execCmdFactory that re-invokes the test binary
// in helper mode with the given behavior env var.
func helperCmdFactory(behavior string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(
			os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"GO_HELPER_BEHAVIOR="+behavior,
		)
		return cmd
	}
}

func withFactory(t *testing.T, behavior string) {
	t.Helper()
	orig := execCmdFactory
	execCmdFactory = helperCmdFactory(behavior)
	t.Cleanup(func() { execCmdFactory = orig })
}

// --------------------------------------------------------------------
// exec_cicd tests
// --------------------------------------------------------------------

func TestExecCICD_ListRunsSuccess(t *testing.T) {
	withFactory(t, "ok_runs")
	p := NewExecCICDProvider("fake-binary")
	runs, err := p.ListRuns(context.Background(), "o/r", 42)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("runs: %+v", runs)
	}
}

func TestExecCICD_ListRunsError(t *testing.T) {
	withFactory(t, "error")
	p := NewExecCICDProvider("fake-binary")
	_, err := p.ListRuns(context.Background(), "o/r", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401 unauthorized") {
		t.Fatalf("error: %v", err)
	}
}

func TestExecCICD_NonJSONOutput(t *testing.T) {
	withFactory(t, "non_json")
	p := NewExecCICDProvider("fake-binary")
	_, err := p.ListRuns(context.Background(), "o/r", 1)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid-JSON error, got %v", err)
	}
}

func TestExecCICD_GetLogs(t *testing.T) {
	withFactory(t, "ok_logs")
	p := NewExecCICDProvider("fake-binary")
	logs, err := p.GetLogs(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if string(logs) != "log line A\nlog line B\n" {
		t.Fatalf("logs: %q", string(logs))
	}
}

func TestExecCICD_RerunFailedSuccess(t *testing.T) {
	withFactory(t, "ok_runs") // any non-error response works; result is ignored
	p := NewExecCICDProvider("fake-binary")
	if err := p.RerunFailed(context.Background(), "o/r", 1); err != nil {
		t.Fatalf("RerunFailed: %v", err)
	}
}

func TestExecCICD_BinaryNotFound(t *testing.T) {
	// Use the real exec.CommandContext so this exercises the
	// missing-binary error path on the real OS.
	orig := execCmdFactory
	execCmdFactory = exec.CommandContext
	t.Cleanup(func() { execCmdFactory = orig })

	p := NewExecCICDProvider("definitely-does-not-exist-xyz-987")
	_, err := p.ListRuns(context.Background(), "o/r", 1)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	// The wrapped error should mention either "executable file not found" or
	// "no such file"; both surface via cmd.Run().
	if !strings.Contains(err.Error(), "definitely-does-not-exist-xyz-987") {
		t.Fatalf("error should mention binary: %v", err)
	}
}

func TestExecCICD_EmptyBinary(t *testing.T) {
	p := NewExecCICDProvider("")
	_, err := p.ListRuns(context.Background(), "o/r", 1)
	if err == nil || !strings.Contains(err.Error(), "empty provider binary") {
		t.Fatalf("expected empty-binary error, got %v", err)
	}
}

// --------------------------------------------------------------------
// exec_issues tests
// --------------------------------------------------------------------

func TestExecIssues_GetIssue(t *testing.T) {
	withFactory(t, "ok_issue")
	p := NewExecIssuesProvider("fake-binary")
	iss, err := p.GetIssue(context.Background(), "ZR-7")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if iss.ID != "ZR-7" || iss.State != "Open" {
		t.Fatalf("issue: %+v", iss)
	}
}

func TestExecIssues_Error(t *testing.T) {
	withFactory(t, "error")
	p := NewExecIssuesProvider("fake-binary")
	_, err := p.GetIssue(context.Background(), "ZR-7")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --------------------------------------------------------------------
// exec_vcs tests
// --------------------------------------------------------------------

func TestExecVCS_GetPR(t *testing.T) {
	withFactory(t, "ok_pr")
	p := NewExecVCSProvider("fake-binary")
	pr, err := p.GetPR(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if pr.Number != 7 || pr.State != "OPEN" {
		t.Fatalf("pr: %+v", pr)
	}
}

func TestExecVCS_WriteOpsNoError(t *testing.T) {
	// Write methods (Update/Merge/Close/SetDraft/SetAutomerge/ResolveThread)
	// don't unmarshal the result, so any successful Response with a
	// non-error works. The echo_op helper returns a string Result; the
	// wrappers discard it.
	withFactory(t, "echo_op")
	p := NewExecVCSProvider("fake-binary")
	ctx := context.Background()

	if err := p.UpdatePR(ctx, "o/r", 1, "body"); err != nil {
		t.Fatalf("UpdatePR: %v", err)
	}
	if err := p.Merge(ctx, "o/r", 1); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := p.Close(ctx, "o/r", 1); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := p.SetDraft(ctx, "o/r", 1, true); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	if err := p.SetAutomerge(ctx, "o/r", 1, true); err != nil {
		t.Fatalf("SetAutomerge: %v", err)
	}
	if err := p.ResolveThread(ctx, "o/r", "t1"); err != nil {
		t.Fatalf("ResolveThread: %v", err)
	}
}

// --------------------------------------------------------------------
// invoke() unit tests
// --------------------------------------------------------------------

func TestInvoke_EmptyBinary(t *testing.T) {
	_, err := invoke(context.Background(), "", Request{Op: "x"})
	if err == nil || !strings.Contains(err.Error(), "empty provider binary") {
		t.Fatalf("expected empty-binary error, got %v", err)
	}
}

func TestUnmarshalInto_Null(t *testing.T) {
	var i int
	if err := unmarshalInto(nil, &i); err != nil {
		t.Fatalf("nil: %v", err)
	}
	if err := unmarshalInto(json.RawMessage("null"), &i); err != nil {
		t.Fatalf("null: %v", err)
	}
}

// Suppress unused-warning if errors becomes unused after edits.
var _ = errors.New
