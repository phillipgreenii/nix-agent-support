package signal_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// TestEnrichCmdErr_IncludesStderr verifies that a failed subprocess error is
// augmented with the command's captured stderr. exec.ExitError.Error() renders
// only "exit status N" and drops Stderr, so a cmux enumerate failure logged
// "cmux --json top --processes: exit status 1" with no clue WHY. The enriched
// error must surface cmux's own stderr so the failing path is diagnosable.
func TestEnrichCmdErr_IncludesStderr(t *testing.T) {
	// A real *exec.ExitError with populated Stderr, exactly as .Output() yields.
	_, err := exec.Command("sh", "-c", "echo cmux-boom >&2; exit 1").Output()
	if err == nil {
		t.Fatal("expected a non-nil error from a command that exits 1")
	}
	got := signal.EnrichCmdErrForTest(err)
	if !strings.Contains(got.Error(), "cmux-boom") {
		t.Errorf("enriched error = %q, want it to include the captured stderr 'cmux-boom'", got.Error())
	}
	if !strings.Contains(got.Error(), "exit status 1") {
		t.Errorf("enriched error = %q, want it to preserve the exit status", got.Error())
	}
}

// TestEnrichCmdErr_PassesThroughNonExitError verifies non-ExitError errors
// (context timeout, binary-not-found) are returned unchanged.
func TestEnrichCmdErr_PassesThroughNonExitError(t *testing.T) {
	base := errors.New("context deadline exceeded")
	if got := signal.EnrichCmdErrForTest(base); got != base {
		t.Errorf("non-ExitError should pass through unchanged; got %v, want %v", got, base)
	}
}

// stubEnv returns a LookupEnv whose values come from m.
func stubEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// fakeCmuxRun returns a RunCmd that responds to `cmux --json top --processes`
// with a synthesized JSON envelope built from `surfaces`, and to
// `cmux send --workspace ... --surface ... <text>` /
// `cmux send-key --workspace ... --surface ... enter` by appending the full
// argv (joined by spaces) to *sentCalls.
//
// surfaces is a slice of (workspaceRef, surfaceRef, ttyProcessPIDs) triples; the
// fake nests them inside one window with one pane per surface — that nesting
// shape mirrors real `cmux --json top --processes` output and is sufficient
// because the parser flattens via `.windows[].workspaces[].panes[].surfaces[]`.
type fakeSurface struct {
	workspaceRef string
	surfaceRef   string
	ttyPIDs      []int
}

func fakeCmuxRun(surfaces []fakeSurface, sentCalls *[]string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "cmux" {
			return nil, fmt.Errorf("unexpected command: %s", name)
		}
		// Match the `cmux --json top --processes` invocation precisely.
		if len(args) >= 3 && args[0] == "--json" && args[1] == "top" && args[2] == "--processes" {
			// Group surfaces by workspace so the JSON has one workspace object per
			// distinct workspaceRef, each containing all that workspace's surfaces.
			byWS := map[string][]fakeSurface{}
			var order []string
			for _, s := range surfaces {
				if _, ok := byWS[s.workspaceRef]; !ok {
					order = append(order, s.workspaceRef)
				}
				byWS[s.workspaceRef] = append(byWS[s.workspaceRef], s)
			}
			var wsObjs []string
			for _, w := range order {
				var paneObjs []string
				for i, s := range byWS[w] {
					pidParts := make([]string, len(s.ttyPIDs))
					for j, p := range s.ttyPIDs {
						pidParts[j] = fmt.Sprintf("%d", p)
					}
					paneObjs = append(paneObjs, fmt.Sprintf(
						`{"ref":"pane:%d-%d","surfaces":[{"ref":%q,"pane_ref":"pane:%d-%d","type":"terminal","tty":"ttysX","tty_process_pids":[%s]}]}`,
						len(wsObjs), i, s.surfaceRef, len(wsObjs), i, strings.Join(pidParts, ","),
					))
				}
				wsObjs = append(wsObjs, fmt.Sprintf(`{"ref":%q,"panes":[%s]}`, w, strings.Join(paneObjs, ",")))
			}
			body := fmt.Sprintf(`{"windows":[{"ref":"window:1","workspaces":[%s]}]}`, strings.Join(wsObjs, ","))
			return []byte(body), nil
		}
		if len(args) >= 1 && (args[0] == "send" || args[0] == "send-key") {
			if sentCalls != nil {
				*sentCalls = append(*sentCalls, "cmux "+strings.Join(args, " "))
			}
			return []byte(""), nil
		}
		return nil, fmt.Errorf("unexpected cmux args: %v", args)
	}
}

func TestCmuxSendFindsSurfaceInOwnWorkspace(t *testing.T) {
	// Agent pid 1000 is one of surface:4's tty processes in workspace:1.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:4", []int{100, 500, 1000}},
		{"workspace:1", "surface:5", []int{200, 600}},
	}
	var sent []string
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, &sent),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	if err := sig.Send(1000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 cmux calls (send + send-key), got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "send --workspace workspace:1 --surface surface:4 continue") {
		t.Errorf("call[0] = %q, want cmux send targeting workspace:1 surface:4 with text", sent[0])
	}
	if !strings.Contains(sent[1], "send-key --workspace workspace:1 --surface surface:4 enter") {
		t.Errorf("call[1] = %q, want cmux send-key Enter targeting workspace:1 surface:4", sent[1])
	}
}

func TestCmuxSendCrossesWorkspaces(t *testing.T) {
	// Caller (pa-monitor) runs in workspace:1.
	// Agent pid 2000 lives in workspace:2, surface:7.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:1", []int{100, 1000}},
		{"workspace:1", "surface:2", []int{200, 1100}},
		{"workspace:2", "surface:7", []int{300, 2000}},
		{"workspace:2", "surface:8", []int{400, 2100}},
	}
	var sent []string
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, &sent),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	if err := sig.Send(2000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 cmux calls, got %d: %v", len(sent), sent)
	}
	for i, call := range sent {
		if !strings.Contains(call, "--workspace workspace:2") {
			t.Errorf("call[%d] = %q, want --workspace workspace:2 (not caller's workspace:1)", i, call)
		}
		if !strings.Contains(call, "--surface surface:7") {
			t.Errorf("call[%d] = %q, want --surface surface:7", i, call)
		}
	}
}

func TestCmuxSendErrorsWhenNoSurfaceFound(t *testing.T) {
	// Agent pid 1000 is in no surface's tty_process_pids.
	surfaces := []fakeSurface{
		{"workspace:1", "surface:1", []int{9001, 9002}},
	}
	sig := &signal.CmuxSignaler{
		RunCmd:    fakeCmuxRun(surfaces, nil),
		LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "workspace:1"}),
	}
	err := sig.Send(1000, "continue")
	if err == nil {
		t.Fatal("Send should return error when no surface matches pid")
	}
	if !strings.Contains(err.Error(), "no cmux surface found for pid 1000") {
		t.Errorf("error = %q, want it to mention pid 1000", err.Error())
	}
}

// Detect ancestry-based behaviour lives in cmux_detect_test.go.
