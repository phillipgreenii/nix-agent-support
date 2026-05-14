package signal_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/signal"
)

// fakeMultiSocketRun supports the new multi-socket TmuxSignaler. It serves:
//
//   - `ps -A -o pid,comm,args`            -> psListAll output
//   - `ps -o ppid=,comm= -p <pid>`        -> processTree[<pid>]
//   - `tmux -L <name> list-panes -a -F …` -> panesBySocket[<name>] (no entry = error)
//   - `tmux -L <name> send-keys …`        -> records to sentKeys (always succeeds)
//
// processTree maps pid -> [ppid, comm]. panesBySocket maps socket name to the
// stdout body of list-panes (one line per pane: "<pane_pid> <pane_id>").
func fakeMultiSocketRun(
	psListAll string,
	processTree map[int][2]string,
	panesBySocket map[string]string,
	sentKeys *[]string,
) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			// Multi-socket survey: ps -A -o pid,comm,args
			if len(args) >= 1 && args[0] == "-A" {
				return []byte(psListAll), nil
			}
			// Ancestry walk: ps -o ppid=,comm= -p <pid>
			pidStr := args[len(args)-1]
			pid, _ := strconv.Atoi(pidStr)
			if entry, ok := processTree[pid]; ok {
				return []byte(entry[0] + " " + entry[1]), nil
			}
			return nil, fmt.Errorf("ps: no such pid %d", pid)
		case "tmux":
			if len(args) >= 3 && args[0] == "-L" && args[2] == "list-panes" {
				body, ok := panesBySocket[args[1]]
				if !ok {
					return nil, fmt.Errorf("tmux -L %s: no server", args[1])
				}
				return []byte(body), nil
			}
			if len(args) >= 3 && args[0] == "-L" && args[2] == "send-keys" {
				if sentKeys != nil {
					*sentKeys = append(*sentKeys, "tmux "+strings.Join(args, " "))
				}
				return []byte(""), nil
			}
			return nil, fmt.Errorf("tmux: unexpected args %v", args)
		}
		return nil, fmt.Errorf("unexpected command: %s", name)
	}
}

func TestTmuxDetectReturnsFalseWhenNoTmuxAncestor(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"1", "bash"},
	}
	sig := &signal.TmuxSignaler{RunCmd: fakeMultiSocketRun(psNoTmuxServers, tree, map[string]string{}, nil)}
	if sig.Detect(1000) {
		t.Error("Detect = true, want false when no tmux ancestor")
	}
}

func TestTmuxSendKeysFindsPaneByAncestor(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
	}
	panes := map[string]string{
		"default": "100 main:0.0\n200 main:0.1\n",
	}
	var sent []string
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleDefaultOnly, tree, panes, &sent),
	}
	if err := sig.Send(1000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "tmux -L default send-keys -t main:0.0 continue Enter") {
		t.Errorf("send call = %q, want -L default + -t main:0.0", sent[0])
	}
}

func TestTmuxSendErrorsWhenNoPaneFound(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"1", "bash"},
	}
	panes := map[string]string{
		"default": "999 other:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleDefaultOnly, tree, panes, nil),
	}
	err := sig.Send(1000, "continue")
	if err == nil {
		t.Error("Send should return error when no pane found for PID")
	}
}

func TestResolveSignalerReturnsFirstMatch(t *testing.T) {
	// TmuxSignaler where pid=1 is directly listed as a pane shell pid.
	// Uses fakeMultiSocketRun so the new pid-aware Detect (which calls
	// cachedPanes/ps -A) can be served.
	panes := map[string]string{
		"gc": "1 mayor:0.0\n",
	}
	always := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, nil, panes, nil),
	}
	never := &signal.CmuxSignaler{}
	got := signal.ResolveSignaler([]signal.Signaler{never, always}, 1)
	if got == nil || got.Name() != "tmux" {
		t.Errorf("ResolveSignaler = %v, want tmux signaler", got)
	}
}

func TestResolveSignalerReturnsNilWhenNoneMatch(t *testing.T) {
	cmux := &signal.CmuxSignaler{LookupEnv: func(string) (string, bool) { return "", false }}
	got := signal.ResolveSignaler([]signal.Signaler{cmux}, 42)
	if got != nil {
		t.Errorf("ResolveSignaler = %v, want nil", got)
	}
}

func TestStubSignalersSendNotImplemented(t *testing.T) {
	stubs := []signal.Signaler{&signal.GhosttySignaler{}, &signal.VSCodeSignaler{}}
	for _, s := range stubs {
		if s.Detect(1) {
			t.Errorf("%s.Detect returned true, want false (stub)", s.Name())
		}
		if err := s.Send(1, "hi"); err != signal.ErrNotImplemented {
			t.Errorf("%s.Send err = %v, want ErrNotImplemented", s.Name(), err)
		}
	}
}

func TestTmuxDetectReturnsFalseForLookalikeComm(t *testing.T) {
	// Process ancestry: 1000 (claude) → 500 (bash) → 100 (tmuxinator).
	// New Detect requires a pane match, not just a comm match — tmuxinator
	// has no tmux server so the pane map is empty and Detect returns false.
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
		100:  {"1", "tmuxinator"},
	}
	sig := &signal.TmuxSignaler{RunCmd: fakeMultiSocketRun(psNoTmuxServers, tree, map[string]string{}, nil)}
	if sig.Detect(1000) {
		t.Error("Detect = true for tmuxinator ancestor; want false (must match pane, not just comm)")
	}
}

const psNoTmuxServers = `99999 bash -bash
`

const psSampleDefaultOnly = `28346 tmux tmux new-session -d -s main
`

const psSampleSingleServer = `28346 tmux tmux -u -L gc new-session -d -s mayor
12345 zsh -zsh
67890 claude /usr/bin/claude
`

const psSampleTwoServers = `28346 tmux tmux -u -L gc new-session -d -s mayor
36990 tmux tmux -u -L work attach
99999 bash -bash
`

func TestTmuxEnumerateSingleServer(t *testing.T) {
	panes := map[string]string{
		"gc": "100 mayor:0.0\n200 mayor:0.1\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, nil, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if loc, ok := locs[100]; !ok || loc.SocketName != "gc" || loc.PaneID != "mayor:0.0" {
		t.Errorf("locs[100] = %+v, want {gc, mayor:0.0}", loc)
	}
	if loc, ok := locs[200]; !ok || loc.SocketName != "gc" || loc.PaneID != "mayor:0.1" {
		t.Errorf("locs[200] = %+v, want {gc, mayor:0.1}", loc)
	}
}

func TestTmuxEnumerateTwoServers(t *testing.T) {
	panes := map[string]string{
		"gc":   "100 mayor:0.0\n",
		"work": "300 dev:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, nil, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[100].SocketName != "gc" || locs[100].PaneID != "mayor:0.0" {
		t.Errorf("locs[100] = %+v, want {gc, mayor:0.0}", locs[100])
	}
	if locs[300].SocketName != "work" || locs[300].PaneID != "dev:0.0" {
		t.Errorf("locs[300] = %+v, want {work, dev:0.0}", locs[300])
	}
}

func TestTmuxEnumerationSkipsDeadSocket(t *testing.T) {
	// `work` socket has no entry → fakeMultiSocketRun returns an error.
	// Enumeration should still surface `gc`.
	panes := map[string]string{
		"gc": "100 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, nil, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[100].SocketName != "gc" {
		t.Errorf("locs[100] = %+v, want gc despite work failing", locs[100])
	}
	if _, hasWork := locs[300]; hasWork {
		t.Errorf("locs[300] present despite work socket failing")
	}
}

func TestTmuxEnumerateDefaultSocketWhenNoDashL(t *testing.T) {
	const psNoDashL = `28346 tmux tmux new-session -d -s default
`
	panes := map[string]string{
		"default": "500 mysession:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psNoDashL, nil, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[500].SocketName != "default" {
		t.Errorf("locs[500] = %+v, want default socket", locs[500])
	}
}

func TestTmuxDetectReturnsTrueOnlyWhenPidInPane(t *testing.T) {
	// agent 1000 -> bash 500 -> shell 100 (pane gc:mayor:0.0)
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
	}
	panes := map[string]string{
		"gc": "100 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil),
	}
	if !sig.Detect(1000) {
		t.Error("Detect(1000) = false, want true (pid in pane gc:mayor:0.0 via ancestor 100)")
	}
}

func TestTmuxDetectReturnsFalseWhenPidNotInAnyPane(t *testing.T) {
	// agent 1000 -> bash 500 -> tmux 200, but server's panes have shell pid
	// 999. Pid 1000's ancestry never reaches 999.
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"200", "bash"},
		200:  {"1", "tmux"},
	}
	panes := map[string]string{
		"gc": "999 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil),
	}
	if sig.Detect(1000) {
		t.Error("Detect(1000) = true, want false (no pane has 1000's ancestors)")
	}
}

func TestTmuxSendFindsPaneOnNonDefaultSocket(t *testing.T) {
	// agent 2000 -> bash 600 -> shell 300 (which is pane work:dev:0.0)
	tree := map[int][2]string{
		2000: {"600", "claude"},
		600:  {"300", "bash"},
	}
	panes := map[string]string{
		"gc":   "100 mayor:0.0\n",
		"work": "300 dev:0.0\n",
	}
	var sent []string
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, tree, panes, &sent),
	}
	if err := sig.Send(2000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "tmux -L work send-keys -t dev:0.0 continue Enter") {
		t.Errorf("send call = %q, want -L work + -t dev:0.0 with Enter", sent[0])
	}
}

func TestTmuxCachedPanesCachesAcrossCalls(t *testing.T) {
	psCalls := 0
	listCalls := 0
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps" && len(args) >= 1 && args[0] == "-A":
			psCalls++
			return []byte(psSampleSingleServer), nil
		case name == "tmux" && len(args) >= 3 && args[0] == "-L" && args[2] == "list-panes":
			listCalls++
			return []byte("100 mayor:0.0\n"), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	sig := &signal.TmuxSignaler{RunCmd: run}
	for i := 0; i < 5; i++ {
		if _, err := signal.CachedPanesForTest(sig); err != nil {
			t.Fatalf("CachedPanes #%d: %v", i, err)
		}
	}
	if psCalls != 1 {
		t.Errorf("ps -A ran %d times; want 1 (cache should coalesce)", psCalls)
	}
	if listCalls != 1 {
		t.Errorf("tmux list-panes ran %d times; want 1 (cache should coalesce)", listCalls)
	}
}
