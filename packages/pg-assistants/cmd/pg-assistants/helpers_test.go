package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
	"github.com/phillipgreenii/x/jsonllogger/jsonlloggertest"
)

// The CLI tests call the subcommands in process through cli.main, with every
// seam (environment, the command runner, exec, the shutdown signal and hard
// exit) answered by the test. Nothing builds or runs the binary.

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const (
	tmuxBin    = "/example/store/bin/tmux"
	controlBin = "/example/store/bin/pg-claude-control"
	testPID    = 4242
)

// execCall is one call of the exec seam.
type execCall struct {
	argv0 string
	argv  []string
	env   []string
}

// testCLI is a cli whose seams the test owns. vars, environ and hook are set
// before the command runs and only read after.
type testCLI struct {
	*cli
	out, errb *jsonlloggertest.Buffer // goroutine-safe: the daemon runs in its own goroutine
	fake      *cmdruntest.Fake
	vars      map[string]string
	environv  []string
	// hook, when set, sees every runner call first; ok=true answers it
	// instead of the fake (a call that must block uses it).
	hook func(ctx context.Context, c cmdrun.Call) (res cmdrun.Result, ok bool)

	mu    sync.Mutex
	execs []execCall
	exits []int
}

func newTestCLI(t *testing.T) *testCLI {
	t.Helper()
	tc := &testCLI{
		out:  &jsonlloggertest.Buffer{},
		errb: &jsonlloggertest.Buffer{},
		fake: cmdruntest.New(t),
		vars: map[string]string{},
	}
	var ids atomic.Int64
	fakeRun := tc.fake.Runner()
	tc.cli = &cli{
		stdout:  tc.out,
		stderr:  tc.errb,
		getenv:  func(k string) string { return tc.vars[k] },
		environ: func() []string { return slices.Clone(tc.environv) },
		run: func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
			if tc.hook != nil {
				if res, ok := tc.hook(ctx, c); ok {
					return res
				}
			}
			return fakeRun(ctx, c)
		},
		exec: func(argv0 string, argv, env []string) error {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			tc.execs = append(tc.execs, execCall{argv0, slices.Clone(argv), slices.Clone(env)})
			return nil
		},
		notify: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		exit: func(code int) {
			tc.mu.Lock()
			defer tc.mu.Unlock()
			tc.exits = append(tc.exits, code)
		},
		pid:   testPID,
		newID: func() string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", ids.Add(1)) },
	}
	return tc
}

func (tc *testCLI) execCalls() []execCall {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return slices.Clone(tc.execs)
}

func (tc *testCLI) hardExits() []int {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return slices.Clone(tc.exits)
}

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	tc := newTestCLI(t)
	code = tc.main(t.Context(), args)
	return code, tc.out.String(), tc.errb.String()
}

func tempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// pathsTOML is a [paths] table with the fake tool paths and the given state
// dir and .claude.json.
func pathsTOML(stateDir, claudeJSON string) string {
	return fmt.Sprintf(`[paths]
tmux = %q
claude = "/example/store/bin/claude"
pg_claude_control = %q
shell = "/bin/zsh"
state_dir = %q
claude_json = %q
`, tmuxBin, controlBin, stateDir, claudeJSON)
}

// assistantTOML is one valid [[assistant]] entry; extra lines go into the
// entry before its context table.
func assistantTOML(id, cwd string, extra ...string) string {
	return fmt.Sprintf(`
[[assistant]]
id = %q
name = "Example"
model = "claude-sonnet-5-5"
effort = "medium"
cwd = %q
prompt = "Resume your duties per AGENTS.md."
%s
[assistant.context]
limit = 200000
nudge = 0.80
interrupt = 0.95
`, id, cwd, strings.Join(extra, "\n"))
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeTmux is the state behind the fake tmux binary: the sessions on the
// pg-assistants server, by name, with their pane pids. It answers the verbs
// x/tmux's Client sends. It runs under the cmdruntest.Fake's lock and never
// blocks.
type fakeTmux struct {
	mu       sync.Mutex
	sessions map[string]int
	nextPID  int
}

func newFakeTmux(names ...string) *fakeTmux {
	s := &fakeTmux{sessions: map[string]int{}, nextPID: 5000}
	for _, n := range names {
		s.sessions[n] = s.nextPID
		s.nextPID++
	}
	return s
}

func (s *fakeTmux) respond(c cmdrun.Call) cmdrun.Result {
	if len(c.Args) < 5 || c.Args[0] != "-L" || c.Args[2] != "-f" {
		return cmdruntest.Fail(1, fmt.Sprintf("unexpected tmux call %q", c.Args))
	}
	args := c.Args[4:]
	s.mu.Lock()
	defer s.mu.Unlock()
	switch args[0] {
	case "has-session": // has-session -t =<name>
		name := strings.TrimPrefix(args[2], "=")
		if _, ok := s.sessions[name]; ok {
			return cmdruntest.Out("")
		}
		return cmdruntest.Fail(1, "can't find session: "+name)
	case "display-message": // display-message -p -t =<name>: <format>
		name := strings.TrimSuffix(strings.TrimPrefix(args[3], "="), ":")
		pid, ok := s.sessions[name]
		if !ok {
			return cmdruntest.Fail(1, "can't find session: "+name)
		}
		switch args[4] {
		case "#{pane_dead}":
			return cmdruntest.Out("0\n")
		case "#{pane_pid}":
			return cmdruntest.Out(fmt.Sprintf("%d\n", pid))
		}
		return cmdruntest.Fail(1, "unexpected format "+args[4])
	case "new-session": // new-session -d -s <name> ...
		s.sessions[args[3]] = s.nextPID
		s.nextPID++
		return cmdruntest.Out("")
	case "list-sessions":
		if len(s.sessions) == 0 {
			return cmdruntest.Fail(1, "no server running on /private/tmp/tmux-501/pg-assistants")
		}
		var names []string
		for n := range s.sessions {
			names = append(names, n)
		}
		slices.Sort(names)
		return cmdruntest.Out(strings.Join(names, "\n") + "\n")
	case "kill-session":
		delete(s.sessions, strings.TrimPrefix(args[2], "="))
		return cmdruntest.Out("")
	}
	return cmdruntest.Fail(1, "unexpected tmux verb "+args[0])
}

// answerTools makes tc's fake answer tmux from srv and pg-claude-control with
// a small context reading and no blocks for usage, success for the rest.
func answerTools(tc *testCLI, srv *fakeTmux) {
	tc.fake.On(cmdruntest.MatchName(tmuxBin)).Respond(srv.respond)
	tc.fake.On(func(c cmdrun.Call) bool { return c.Name == controlBin && c.Args[0] == "usage" }).
		Return(cmdruntest.Out(`{"sent":true,"session":"example-session","result":{"context":{"tokens":1000}}}`))
	tc.fake.On(cmdruntest.MatchName(controlBin)).Return(cmdruntest.Out(""))
}

// tmuxCalls returns the fake's calls to tmux with the given verb.
func tmuxCalls(tc *testCLI, verb string) []cmdrun.Call {
	var out []cmdrun.Call
	for _, c := range tc.fake.Calls() {
		if c.Name == tmuxBin && len(c.Args) > 4 && c.Args[4] == verb {
			out = append(out, c)
		}
	}
	return out
}

// records decodes a JSONL file with jsonlloggertest.
func records(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := &jsonlloggertest.Buffer{}
	_, _ = buf.Write(data)
	return buf.Records(t)
}

// findEvent returns the first record named event whose fields include kv
// (key, value pairs compared as strings).
func findEvent(recs []map[string]any, event string, kv ...string) (map[string]any, bool) {
	for _, r := range recs {
		if r["event"] != event {
			continue
		}
		match := true
		for i := 0; i+1 < len(kv); i += 2 {
			if fmt.Sprint(r[kv[i]]) != kv[i+1] {
				match = false
				break
			}
		}
		if match {
			return r, true
		}
	}
	return nil, false
}

func eventNames(recs []map[string]any) []string {
	var out []string
	for _, r := range recs {
		out = append(out, fmt.Sprint(r["event"]))
	}
	return out
}
