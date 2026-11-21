package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// shortDir is a state directory whose socket path fits the platform limit.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func newSpec(t *testing.T, rec *recorder, start func(context.Context) error) (daemon.Spec, *bytes.Buffer) {
	t.Helper()
	dir := shortDir(t)
	var stderr bytes.Buffer
	hook := func(name string) daemon.Hook {
		return daemon.Hook{Name: name, Run: func(context.Context) error { rec.add("drain:" + name); return nil }}
	}
	return daemon.Spec{
		Name:   "testd",
		Stderr: &stderr,
		Proc:   proc.Config{StateDir: dir, SocketPath: filepath.Join(dir, "s.sock")},
		Assemble: func() daemon.Assembly {
			rec.add("assemble")
			return daemon.Assembly{
				Handler: func(context.Context, net.Conn, proc.Peer) {},
				Drain:   []daemon.Hook{hook("first"), hook("second"), hook("third")},
				Start:   start,
			}
		},
	}, &stderr
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("socket %s never accepted", path)
}

// A clean start serves the socket, and the shutdown signal drains the hooks in
// the order the assembly gave, then exits OK.
func TestServeDrainsHooksInOrderOnShutdown(t *testing.T) {
	rec := &recorder{}
	started := make(chan struct{})
	spec, _ := newSpec(t, rec, func(context.Context) error { rec.add("start"); close(started); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- daemon.Serve(ctx, spec) }()

	<-started
	waitSocket(t, spec.Proc.SocketPath)
	cancel()
	select {
	case c := <-code:
		if c != daemon.ExitOK {
			t.Fatalf("exit %d, want %d", c, daemon.ExitOK)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not drain")
	}
	got := strings.Join(rec.get(), ",")
	if want := "assemble,start,drain:first,drain:second,drain:third"; got != want {
		t.Fatalf("events %q, want %q", got, want)
	}
}

// A startup failure with no shutdown signal is a refusal: the reason is
// reported, the drain still runs, and the exit is ExitRefused.
func TestServeStartFailureRefuses(t *testing.T) {
	rec := &recorder{}
	spec, stderr := newSpec(t, rec, func(context.Context) error { return errors.New("store too new") })
	if c := daemon.Serve(context.Background(), spec); c != daemon.ExitRefused {
		t.Fatalf("exit %d, want %d", c, daemon.ExitRefused)
	}
	if want := "testd serve: store too new"; !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr %q lacks %q", stderr.String(), want)
	}
	if got := strings.Join(rec.get(), ","); !strings.HasSuffix(got, "drain:third") {
		t.Fatalf("drain must run after a refused start: %q", got)
	}
}

// The startup race: a shutdown signal during startup makes startup fail, and
// that failure is not reported; the drain answers it and the exit is OK.
func TestServeStartFailureCausedByShutdownIsClean(t *testing.T) {
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	spec, stderr := newSpec(t, rec, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if c := daemon.Serve(ctx, spec); c != daemon.ExitOK {
		t.Fatalf("exit %d, want %d", c, daemon.ExitOK)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a startup cut short by the signal must not be reported: %q", stderr.String())
	}
	if got := strings.Join(rec.get(), ","); !strings.HasSuffix(got, "drain:third") {
		t.Fatalf("drain must run: %q", got)
	}
}

// A second instance on the same state directory refuses before it assembles.
func TestServeLockHeldRefusesBeforeAssemble(t *testing.T) {
	rec := &recorder{}
	started := make(chan struct{})
	first, _ := newSpec(t, rec, func(context.Context) error { close(started); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- daemon.Serve(ctx, first) }()
	<-started
	defer func() { cancel(); <-done }()

	rec2 := &recorder{}
	second, stderr := newSpec(t, rec2, nil)
	second.Proc = first.Proc
	if c := daemon.Serve(context.Background(), second); c != daemon.ExitRefused {
		t.Fatalf("exit %d, want %d", c, daemon.ExitRefused)
	}
	if len(rec2.get()) != 0 {
		t.Fatalf("a refused lock must not assemble: %v", rec2.get())
	}
	if !strings.Contains(stderr.String(), "testd serve:") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

// Run rejects a serve command line the daemon does not accept.
func TestRunUsageError(t *testing.T) {
	rec := &recorder{}
	spec, stderr := newSpec(t, rec, nil)
	spec.Args = []string{"--bogus"}
	if c := daemon.Run(context.Background(), spec); c != daemon.ExitUsage {
		t.Fatalf("exit %d, want %d", c, daemon.ExitUsage)
	}
	if len(rec.get()) != 0 || !strings.Contains(stderr.String(), "testd serve:") {
		t.Fatalf("events %v stderr %q", rec.get(), stderr.String())
	}
}

// WrapContext is applied before the daemon starts and released at the end.
func TestServeWrapContext(t *testing.T) {
	rec := &recorder{}
	spec, _ := newSpec(t, rec, func(context.Context) error { return errors.New("stop") })
	released := false
	spec.WrapContext = func(ctx context.Context) (context.Context, func()) {
		rec.add("wrap")
		return ctx, func() { released = true }
	}
	daemon.Serve(context.Background(), spec)
	if !released || rec.get()[0] != "wrap" {
		t.Fatalf("released=%v events=%v", released, rec.get())
	}
}

// Run puts the serve path under the run group and passes the group's phase
// reporter to the shell.
func TestRunDrainsOnParentCancel(t *testing.T) {
	rec := &recorder{}
	started := make(chan struct{})
	spec, _ := newSpec(t, rec, func(context.Context) error { close(started); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- daemon.Run(ctx, spec) }()
	<-started
	cancel()
	select {
	case c := <-done:
		if c != daemon.ExitOK {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not drain")
	}
}

// Run starts the opt-in pprof endpoint named by Spec.PprofEnv from the process
// environment, and reports it on the spec's stderr; Serve never does.
func TestRunStartsThePprofEndpointWhenTheVariableIsSet(t *testing.T) {
	const env = "X_TESTD_PPROF_ADDR"
	t.Setenv(env, "127.0.0.1:0")
	rec := &recorder{}
	started := make(chan struct{})
	spec, stderr := newSpec(t, rec, func(context.Context) error { close(started); return nil })
	spec.PprofEnv = env
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- daemon.Run(ctx, spec) }()
	<-started
	cancel()
	select {
	case c := <-done:
		if c != daemon.ExitOK {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not drain")
	}
	if !strings.Contains(stderr.String(), "pprof listening on 127.0.0.1:") {
		t.Fatalf("stderr %q lacks the pprof listening line", stderr.String())
	}
}
