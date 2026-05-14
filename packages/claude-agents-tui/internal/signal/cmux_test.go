package signal_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// stubEnv returns a LookupEnv whose values come from m.
func stubEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestCmuxDetectReturnsTrueWhenWorkspaceEnvSet(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{"CMUX_WORKSPACE_ID": "ws-123"})}
	if !sig.Detect(1234) {
		t.Error("Detect = false, want true when CMUX_WORKSPACE_ID is set")
	}
}

func TestCmuxDetectReturnsFalseWhenWorkspaceEnvUnset(t *testing.T) {
	sig := &signal.CmuxSignaler{LookupEnv: stubEnv(map[string]string{})}
	if sig.Detect(1234) {
		t.Error("Detect = true, want false when CMUX_WORKSPACE_ID is unset")
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
