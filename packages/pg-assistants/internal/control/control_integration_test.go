//go:build integration

package control_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
	"github.com/phillipgreenii/x/cmdrun"
)

// toolBin is the real pg-claude-control, named by the integration check.
func toolBin(t *testing.T) string {
	t.Helper()
	p := os.Getenv("PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL")
	if p == "" {
		if os.Getenv("PG_ASSISTANTS_REQUIRE_TOOLS") == "1" {
			t.Fatal("PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL is not set and PG_ASSISTANTS_REQUIRE_TOOLS=1")
		}
		t.Skip("PG_ASSISTANTS_TEST_PG_CLAUDE_CONTROL is not set")
	}
	return p
}

// rig is a hermetic machine for the real tool: a session registry under a
// temp CLAUDE_CONFIG_DIR, an inbox directory under a temp
// PG_CLAUDE_CONTROL_STATE_DIR, and a client whose runner records each argv
// before running the real binary.
type rig struct {
	t        *testing.T
	sessions string
	inbox    string
	client   control.Client
	mu       sync.Mutex
	calls    []cmdrun.Call
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, sessions: filepath.Join(dir, "claude", "sessions"), inbox: filepath.Join(dir, "state", "inbox")}
	for _, d := range []string{r.sessions, r.inbox} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The client passes the daemon's environment through, so these reach the tool.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	t.Setenv("PG_CLAUDE_CONTROL_STATE_DIR", filepath.Join(dir, "state"))
	r.client = control.Client{Bin: toolBin(t), Run: func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		r.mu.Lock()
		r.calls = append(r.calls, c)
		r.mu.Unlock()
		return cmdrun.Exec(ctx, c)
	}}
	return r
}

// lastArgs is the argv of the most recent call, after the binary.
func (r *rig) lastArgs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1].Args
}

// proc is a stand-in for the Claude process: a real child the tool can probe
// and signal. A reaper goroutine collects it the moment it ends, so its pid
// stops answering kill(pid, 0) (a zombie would still look alive).
type proc struct {
	pid     int
	session string
	done    chan *os.ProcessState
}

// claude starts a child and registers it as a running Claude. The entry has
// no procStart, so the tool checks pid liveness only and needs no ps.
func (r *rig) claude(session string) *proc {
	r.t.Helper()
	cmd := exec.CommandContext(r.t.Context(), "sleep", "600")
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	p := &proc{pid: cmd.Process.Pid, session: session, done: make(chan *os.ProcessState, 1)}
	go func() {
		_ = cmd.Wait()
		p.done <- cmd.ProcessState
	}()
	r.t.Cleanup(func() { _ = cmd.Process.Kill() })
	b, err := json.Marshal(map[string]any{"pid": p.pid, "sessionId": session, "name": "example-assistant", "cwd": "/example/project"})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.sessions, fmt.Sprintf("%d.json", p.pid)), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// terminated waits for p to end and checks SIGTERM ended it.
func (r *rig) terminated(p *proc) {
	r.t.Helper()
	select {
	case st := <-p.done:
		ws, ok := st.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			r.t.Fatalf("pid %d ended with %v, want SIGTERM", p.pid, st)
		}
	case <-time.After(10 * time.Second):
		r.t.Fatalf("pid %d still running 10s after the stop", p.pid)
	}
}

func (r *rig) fifo(p *proc) string { return filepath.Join(r.inbox, fmt.Sprintf("%d.fifo", p.pid)) }

// mod plays the control mod for p: a real FIFO held open read-write (as the
// listener does), every line sent on the returned channel, and respond's
// answer written atomically to the line's reply path when it has one (an
// empty answer writes nothing).
func (r *rig) mod(p *proc, respond func(line string) string) chan string {
	r.t.Helper()
	path := r.fifo(p)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		r.t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	lines := make(chan string, 16)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			select {
			case <-stop:
				return
			default:
			}
			line := sc.Text()
			lines <- line
			for _, tok := range strings.Fields(line) {
				if reply, ok := strings.CutPrefix(tok, "reply="); ok {
					if body := respond(line); body != "" {
						_ = os.WriteFile(reply+".tmp", []byte(body+"\n"), 0o600)
						_ = os.Rename(reply+".tmp", reply)
					}
				}
			}
		}
	}()
	r.t.Cleanup(func() {
		close(stop)
		_, _ = f.Write([]byte("\n")) // wake the blocked reader
		<-stopped
		_ = f.Close()
	})
	return lines
}

func next(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case l := <-ch:
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("no request line arrived")
		return ""
	}
}

// verbOf is the request line's verb and argument, without its id and paths.
func verbOf(line string) string {
	var keep []string
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, "id=") || strings.HasPrefix(tok, "reply=") || strings.HasPrefix(tok, "payload=") {
			continue
		}
		keep = append(keep, tok)
	}
	return strings.Join(keep, " ")
}

func hasReply(line string) bool { return strings.Contains(line, " reply=") }

func reply(ok bool, extra string) func(string) string {
	return func(string) string {
		if ok {
			return `{"ok":true` + extra + `}`
		}
		return `{"ok":false,"reason":` + extra + `}`
	}
}

const usageResult = `,"result":{"context":{"percent":41.5,"tokens":83000,"window":200000},` +
	`"rateLimits":[{"kind":"five_hour","percentUsed":62,"resetsAt":"2026-11-16T20:00:00.123Z"},{"kind":"spend","percentUsed":5}],"cost":{"usd":0.5}}`

// TestRealControlContract runs every client call against the real
// pg-claude-control: the tool accepts the argv, the request reaches the inbox
// as the mod would see it, and the client maps the tool's real stdout and exit
// codes. ErrAmbiguous is not reachable here: the client always targets pid:<n>.
func TestRealControlContract(t *testing.T) {
	const session = "9806c8aa-1111-4222-8333-444455556666"

	t.Run("usage", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		lines := r.mod(p, reply(true, usageResult))
		u, err := r.client.Usage(t.Context(), p.pid, now)
		if err != nil {
			t.Fatal(err)
		}
		if l := next(t, lines); verbOf(l) != "usage" || !hasReply(l) {
			t.Errorf("request = %q, want usage with a reply path", l)
		}
		if !slices.Equal(r.lastArgs(), []string{"usage", fmt.Sprintf("pid:%d", p.pid), "--json", "--timeout", "5s"}) {
			t.Errorf("argv = %q", r.lastArgs())
		}
		if u.SessionID != session || !u.HaveContext || u.ContextTokens != 83000 || u.ContextPct != 41.5 || u.Window != 200000 {
			t.Errorf("usage = %+v", u)
		}
		if len(u.Limits) != 1 || u.Limits[0].Kind != "five_hour" || u.Limits[0].Fraction != 0.62 ||
			!u.Limits[0].ResetsAt.Equal(time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)) { // .123 truncated to the second
			t.Errorf("limits = %+v", u.Limits)
		}
	})

	t.Run("usage refused", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		r.mod(p, reply(false, `"example refusal"`))
		if _, err := r.client.Usage(t.Context(), p.pid, now); !errors.Is(err, control.ErrRefused) || !strings.Contains(err.Error(), "example refusal") {
			t.Fatalf("err = %v, want ErrRefused naming the reason", err)
		}
	})

	t.Run("usage no reply", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		r.mod(p, func(string) string { return "" })
		if _, err := r.client.Usage(t.Context(), p.pid, now); !errors.Is(err, control.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply", err)
		}
	})

	t.Run("no registry entry", func(t *testing.T) {
		r := newRig(t)
		if _, err := r.client.Usage(t.Context(), os.Getpid(), now); !errors.Is(err, control.ErrNoProcess) {
			t.Fatalf("err = %v, want ErrNoProcess", err)
		}
	})

	t.Run("registry entry without an inbox", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		if _, err := r.client.Usage(t.Context(), p.pid, now); !errors.Is(err, control.ErrNoControl) {
			t.Fatalf("err = %v, want ErrNoControl", err)
		}
	})

	t.Run("inbox nobody reads", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		if err := syscall.Mkfifo(r.fifo(p), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.client.Submit(t.Context(), p.pid, "hello"); !errors.Is(err, control.ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
	})

	t.Run("submit", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		lines := r.mod(p, reply(true, ""))
		if err := r.client.Submit(t.Context(), p.pid, "hello from example"); err != nil {
			t.Fatal(err)
		}
		if l := next(t, lines); verbOf(l) != "submit hello from example" {
			t.Errorf("request = %q, want the text on the submit line", l)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		for name, tc := range map[string]struct {
			respond func(string) string
			want    error
		}{
			"aborted": {reply(true, ""), nil},
			"no turn": {reply(false, `"no turn"`), nil},
			"refused": {reply(false, `"example refusal"`), control.ErrRefused},
		} {
			t.Run(name, func(t *testing.T) {
				r := newRig(t)
				p := r.claude(session)
				lines := r.mod(p, tc.respond)
				err := r.client.Interrupt(t.Context(), p.pid)
				if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
				if l := next(t, lines); verbOf(l) != "interrupt" || !hasReply(l) {
					t.Errorf("request = %q, want interrupt with a reply path", l)
				}
			})
		}
	})

	t.Run("stop exits on request", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		lines := r.mod(p, func(line string) string {
			if verbOf(line) == "run exit" {
				_ = syscall.Kill(p.pid, syscall.SIGTERM) // the Claude runs /exit
			}
			return `{"ok":true}`
		})
		if err := r.client.Stop(t.Context(), p.pid, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if a, b := verbOf(next(t, lines)), verbOf(next(t, lines)); a != "interrupt" || b != "run exit" {
			t.Errorf("requests = %q, %q; want interrupt then run exit", a, b)
		}
		r.terminated(p)
	})

	t.Run("stop escalates", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		r.mod(p, reply(true, "")) // accepts /exit but never exits
		if err := r.client.Stop(t.Context(), p.pid, 500*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.lastArgs(), []string{"stop", fmt.Sprintf("pid:%d", p.pid), "--escalate", "500ms"}) {
			t.Errorf("argv = %q", r.lastArgs())
		}
		r.terminated(p)
	})

	t.Run("signal without the mod", func(t *testing.T) {
		r := newRig(t)
		p := r.claude(session)
		if err := r.client.Signal(t.Context(), p.pid); err != nil {
			t.Fatal(err)
		}
		r.terminated(p)
	})

	t.Run("signal gone", func(t *testing.T) {
		r := newRig(t)
		if err := r.client.Signal(t.Context(), os.Getpid()); !errors.Is(err, control.ErrNoProcess) {
			t.Fatalf("err = %v, want ErrNoProcess", err)
		}
	})
}
