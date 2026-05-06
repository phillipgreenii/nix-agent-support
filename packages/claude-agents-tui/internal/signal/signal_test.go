package signal_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// fakeRun returns a RunCmd that handles "ps" (process tree lookup) and "tmux" commands.
// processTree maps pid → [ppid, commName].
func fakeRun(processTree map[int][2]string, paneList string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			pidStr := args[len(args)-1]
			pid, _ := strconv.Atoi(pidStr)
			if entry, ok := processTree[pid]; ok {
				return []byte(entry[0] + " " + entry[1]), nil
			}
			return nil, fmt.Errorf("ps: no such pid %d", pid)
		case "tmux":
			if len(args) >= 2 && args[0] == "list-panes" {
				return []byte(paneList), nil
			}
			if len(args) >= 2 && args[0] == "send-keys" {
				return []byte(""), nil
			}
			return nil, fmt.Errorf("tmux: unexpected args %v", args)
		}
		return nil, fmt.Errorf("unexpected command: %s", name)
	}
}

func TestTmuxDetectReturnsTrueWhenTmuxIsAncestor(t *testing.T) {
	// Process tree: 1000 (claude) → 500 (bash) → 100 (tmux)
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
		100:  {"1", "tmux"},
	}
	sig := &signal.TmuxSignaler{RunCmd: fakeRun(tree, "")}
	if !sig.Detect(1000) {
		t.Error("Detect = false, want true when tmux is ancestor")
	}
}

func TestTmuxDetectReturnsFalseWhenNoTmuxAncestor(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"1", "bash"},
	}
	sig := &signal.TmuxSignaler{RunCmd: fakeRun(tree, "")}
	if sig.Detect(1000) {
		t.Error("Detect = true, want false when no tmux ancestor")
	}
}

func TestTmuxSendKeysFindsPaneByAncestor(t *testing.T) {
	// Process tree: 1000 (claude) → 500 (bash) → 100 (tmux pane shell)
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
	}
	paneList := "100 main:0.0\n200 main:0.1\n"
	var sentKeys []string
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "send-keys" {
			sentKeys = append(sentKeys, strings.Join(args, " "))
			return []byte(""), nil
		}
		return fakeRun(tree, paneList)(ctx, name, args...)
	}
	sig := &signal.TmuxSignaler{RunCmd: run}
	if err := sig.Send(1000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sentKeys) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d", len(sentKeys))
	}
	if !strings.Contains(sentKeys[0], "main:0.0") {
		t.Errorf("send-keys target = %q, want pane main:0.0", sentKeys[0])
	}
}

func TestTmuxSendErrorsWhenNoPaneFound(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"1", "bash"},
	}
	sig := &signal.TmuxSignaler{RunCmd: fakeRun(tree, "999 other:0.0\n")}
	err := sig.Send(1000, "continue")
	if err == nil {
		t.Error("Send should return error when no pane found for PID")
	}
}

func TestResolveSignalerReturnsFirstMatch(t *testing.T) {
	// TmuxSignaler with pid=1 having tmux ancestor
	always := &signal.TmuxSignaler{RunCmd: fakeRun(map[int][2]string{1: {"0", "tmux"}}, "")}
	never := &signal.CmuxSignaler{}
	got := signal.ResolveSignaler([]signal.Signaler{never, always}, 1)
	if got == nil || got.Name() != "tmux" {
		t.Errorf("ResolveSignaler = %v, want tmux signaler", got)
	}
}

func TestResolveSignalerReturnsNilWhenNoneMatch(t *testing.T) {
	got := signal.ResolveSignaler([]signal.Signaler{&signal.CmuxSignaler{}}, 42)
	if got != nil {
		t.Errorf("ResolveSignaler = %v, want nil", got)
	}
}

func TestStubSignalersSendNotImplemented(t *testing.T) {
	stubs := []signal.Signaler{&signal.CmuxSignaler{}, &signal.GhosttySignaler{}, &signal.VSCodeSignaler{}}
	for _, s := range stubs {
		if s.Detect(1) {
			t.Errorf("%s.Detect returned true, want false (stub)", s.Name())
		}
		if err := s.Send(1, "hi"); err != signal.ErrNotImplemented {
			t.Errorf("%s.Send err = %v, want ErrNotImplemented", s.Name(), err)
		}
	}
}
