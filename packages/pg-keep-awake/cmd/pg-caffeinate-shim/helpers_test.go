package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// shimBin is the pg-caffeinate-shim binary the tests run as a child process,
// built once by TestMain.
var shimBin string

func TestMain(m *testing.M) {
	// A runner started under nohup (or as a non-interactive background job)
	// has SIGHUP/SIGINT ignored, and children inherit that. Holding a handler
	// here makes Go reset those signals to the default in every child it
	// execs, so the tests see the dispositions they assume. The tests that
	// need an ignored signal set it explicitly (trap '').
	keep := make(chan os.Signal, 1)
	signal.Notify(keep, syscall.SIGHUP, syscall.SIGINT)

	dir, err := os.MkdirTemp("", "pg-caffeinate-shim-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := func() int {
		defer os.RemoveAll(dir)
		goTool, err := exec.LookPath("go")
		if err != nil {
			goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
		}
		shimBin = filepath.Join(dir, "pg-caffeinate-shim")
		build := exec.Command(goTool, "build", "-o", shimBin, ".")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building the shim: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

// requireDarwin skips a test that needs the sysctl-backed process helpers.
func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("pg-caffeinate-shim needs macOS process inspection")
	}
}

// shimEnv is a hermetic environment: the state directory is a fresh temp dir.
func shimEnv(xdg string) []string {
	return []string{"XDG_STATE_HOME=" + xdg, "PATH=" + os.Getenv("PATH"), "HOME=" + xdg}
}

// proc is a started child with its exit observed on a channel.
type proc struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan struct{}
	status syscall.WaitStatus
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start runs cmd and registers a cleanup that kills it if it is still alive.
// Tests signal only processes started here.
func start(t *testing.T, cmd *exec.Cmd) *proc {
	t.Helper()
	p := &proc{cmd: cmd, stdout: newSyncBuffer(), stderr: newSyncBuffer(), done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
	}
	go func() {
		_ = cmd.Wait()
		if ps := cmd.ProcessState; ps != nil {
			p.status, _ = ps.Sys().(syscall.WaitStatus)
		}
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// startShim starts the shim binary with a hermetic environment.
func startShim(t *testing.T, xdg string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(shimBin, args...)
	cmd.Env = shimEnv(xdg)
	return start(t, cmd)
}

// running reports whether the process has not exited yet.
func (p *proc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// wait blocks until the process exits or the timeout passes.
func (p *proc) wait(t *testing.T, timeout time.Duration) syscall.WaitStatus {
	t.Helper()
	select {
	case <-p.done:
		return p.status
	case <-time.After(timeout):
		t.Fatalf("%v did not exit within %v (stderr %q)", p.cmd.Args, timeout, p.stderr.String())
		return 0
	}
}

// signal sends sig to the child this test started.
func (p *proc) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
}

// leasesDir is where the shim writes its lease files under xdg.
func leasesDir(xdg string) string {
	return filepath.Join(xdg, lease.DirName, "leases")
}

// leaseFiles lists the lease json files currently under xdg.
func leaseFiles(xdg string) []string {
	m, _ := filepath.Glob(filepath.Join(leasesDir(xdg), "shim.*.json"))
	return m
}

// waitForLease blocks until a lease file appears and returns it parsed.
func waitForLease(t *testing.T, xdg string, p *proc) *lease.Lease {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if files := leaseFiles(xdg); len(files) == 1 {
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			l := new(lease.Lease)
			if err := json.Unmarshal(data, l); err != nil {
				t.Fatalf("parsing %s: %v", files[0], err)
			}
			return l
		}
		if !p.running() {
			t.Fatalf("shim exited (%v) before writing a lease; stderr %q", p.status, p.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no lease file appeared under %s", leasesDir(xdg))
	return nil
}

// exitedWith reports whether ws is a normal exit with the given code.
func exitedWith(ws syscall.WaitStatus, code int) bool { return ws.Exited() && ws.ExitStatus() == code }

// diedOf reports whether ws is a death by sig.
func diedOf(ws syscall.WaitStatus, sig syscall.Signal) bool {
	return ws.Signaled() && ws.Signal() == sig
}

// describe renders a wait status for failure messages.
func describe(ws syscall.WaitStatus) string {
	switch {
	case ws.Signaled():
		return "killed by " + ws.Signal().String()
	case ws.Exited():
		return fmt.Sprintf("exit %d", ws.ExitStatus())
	}
	return fmt.Sprintf("status %#x", uint32(ws))
}

// noLeases fails when any lease file is left.
func noLeases(t *testing.T, xdg string) {
	t.Helper()
	if files := leaseFiles(xdg); len(files) != 0 {
		t.Errorf("lease files left behind: %s", strings.Join(files, ", "))
	}
}
