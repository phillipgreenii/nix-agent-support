// Package daemontest is the one daemon test harness of the connector daemon
// kernel: the process lifecycle of a daemon under test (start, SIGTERM, SIGKILL,
// exit code, output), the socket client, the real-binary build cache, the
// single-instance lock wait, the read-only store and JSONL log readers and the
// scratch-directory and PATH helpers.
//
// It knows nothing of a connector. A connector's own servetest package wraps it
// with what is the connector's: the rendered config, the fake origin tool, the
// daemon's command line and the readiness probe. Everything that starts a real
// process is reached only through such a servetest package, so the unit-tier
// guard (which keys on the servetest package name) sees every launcher.
package daemontest

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	gobuild "go/build"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite" // read-only store inspection

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ---- the process ------------------------------------------------------------

// Proc is one daemon process under test. Embed it in a connector's Daemon: its
// zero value is "never started", and Start after the process ended is a restart.
type Proc struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan struct{}
	exit   int
	stderr *safeBuffer
}

// Start runs cmd (its Stdout and Stderr are replaced by one captured buffer) and
// ends it at the end of the test if it is still running. It fails the test when
// the previous process is still running.
func (p *Proc) Start(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	p.mu.Lock()
	if p.cmd != nil {
		select {
		case <-p.done:
		default:
			p.mu.Unlock()
			t.Fatal("servetest: the daemon is already running")
		}
	}
	buf := &safeBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		t.Fatalf("servetest: start the daemon: %v", err)
	}
	p.cmd, p.stderr, p.done, p.exit = cmd, buf, make(chan struct{}), -1
	done := p.done
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			code = -2
		}
		p.mu.Lock()
		p.exit = code
		p.mu.Unlock()
		close(done)
	}()
	p.mu.Unlock()
	t.Cleanup(p.KillNow)
}

// KillNow ends a still-running process without ceremony (test cleanup).
func (p *Proc) KillNow() {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	p.mu.Unlock()
	if cmd == nil {
		return
	}
	select {
	case <-done:
	default:
		_ = cmd.Process.Kill()
		<-done
	}
}

// Stderr is what the process wrote to stdout and stderr so far.
func (p *Proc) Stderr() string {
	p.mu.Lock()
	b := p.stderr
	p.mu.Unlock()
	if b == nil {
		return ""
	}
	return b.String()
}

// Exited reports whether the process ended, and its exit code.
func (p *Proc) Exited() (bool, int) {
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		return false, 0
	}
	select {
	case <-done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return true, p.exit
	default:
		return false, 0
	}
}

// Wait waits for the process to end (up to timeout) and returns its exit code.
func (p *Proc) Wait(t testing.TB, timeout time.Duration) int {
	t.Helper()
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done == nil {
		t.Fatal("servetest: the daemon was never started")
	}
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("servetest: the daemon did not exit within %s; output: %s", timeout, p.Stderr())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// Term sends SIGTERM and returns at once; Wait collects the exit code. A test
// uses it to stop the daemon while a request is still in flight.
func (p *Proc) Term(t testing.TB) {
	t.Helper()
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	p.mu.Unlock()
	if cmd == nil {
		t.Fatal("servetest: the daemon was never started")
	}
	select {
	case <-done:
	default:
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("servetest: SIGTERM: %v", err)
		}
	}
}

// Stop sends SIGTERM, waits up to 30 s for the drain and returns the exit code.
func (p *Proc) Stop(t testing.TB) int {
	t.Helper()
	p.Term(t)
	return p.Wait(t, 30*time.Second)
}

// KillAndAwaitLock ends the process with SIGKILL (a crash) and returns once it
// is gone: reaped, and its single-instance lock at lockPath can be taken again.
// The second matters on a loaded host: a child the daemon forked just before the
// kill holds a copy of the lock's descriptor until its exec closes it, so the
// lock outlives the daemon for as long as the host takes to run that exec, and a
// start straight after the kill would meet "another instance holds the lock".
func (p *Proc) KillAndAwaitLock(t testing.TB, lockPath string) {
	t.Helper()
	p.KillNow()
	if !AwaitLockFree(lockPath, 30*time.Second) {
		t.Fatalf("servetest: the killed daemon's lock %s is still held after 30s", lockPath)
	}
}

// AwaitLockFree waits until the flock at path can be taken (or does not exist)
// and reports whether that happened within max. It releases the lock again.
func AwaitLockFree(path string, max time.Duration) bool {
	lk := flock.New(path)
	deadline := time.Now().Add(max)
	for {
		locked, err := lk.TryLock()
		if err == nil && locked {
			_ = lk.Unlock()
			return true
		}
		if err != nil && os.IsNotExist(err) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WaitUntilReady polls ready every 20 ms for up to 30 s, failing if the process
// ends first or never becomes ready.
func (p *Proc) WaitUntilReady(t testing.TB, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ended, code := p.Exited(); ended {
			t.Fatalf("servetest: the daemon exited (%d) before it was ready; output: %s", code, p.Stderr())
		}
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("servetest: the daemon never became ready; output: %s", p.Stderr())
}

// ---- the socket client --------------------------------------------------------

// Call sends one request for an instance on the socket with the client's
// deadline and returns the daemon's answer; protocol is the socket protocol the
// frame claims.
func Call(socketPath, instance string, protocol int, req scriptout.Request, deadline time.Duration) (scriptout.Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return scriptout.Response{}, err
	}
	reply, err := Raw(socketPath, socket.RequestFrame{
		SocketProtocol: protocol,
		ClientBuild:    "servetest",
		Instance:       instance,
		DeadlineMS:     deadline.Milliseconds(),
		Request:        raw,
	})
	if err != nil {
		return scriptout.Response{}, err
	}
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		return scriptout.Response{}, fmt.Errorf("decode the response %q: %w", reply.Response, err)
	}
	return resp, nil
}

// Raw sends one frame as given and returns the reply frame.
func Raw(socketPath string, frame socket.RequestFrame) (socket.ReplyFrame, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		return socket.ReplyFrame{}, err
	}
	defer func() { _ = conn.Close() }()
	if err := socket.WriteFrame(conn, frame); err != nil {
		return socket.ReplyFrame{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	raw, err := socket.ReadFrame(bufio.NewReader(conn), 64<<20)
	if err != nil {
		return socket.ReplyFrame{}, err
	}
	var reply socket.ReplyFrame
	if err := json.Unmarshal(raw, &reply); err != nil {
		return socket.ReplyFrame{}, fmt.Errorf("decode the reply frame %q: %w", raw, err)
	}
	return reply, nil
}

// ---- the store and the logs -----------------------------------------------------

// OpenStoreRO opens the store file at path read-only. The daemon may be running
// (the store is a WAL database); the handle is closed at the end of the test.
func OpenStoreRO(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// OpenStoreRW opens the store file read-write with plain SQL, for a test that
// edits it while the daemon is stopped.
func OpenStoreRW(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ReadJSONL returns the rows of a JSONL file (nil when it does not exist).
func ReadJSONL(t testing.TB, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("%s: line %q is not JSON: %v", path, line, err)
		}
		rows = append(rows, row)
	}
	return rows
}

// ---- building the binary --------------------------------------------------------

// Builder builds one connector's real binary by import path, once per link-time
// flag string, into a temp directory a package's TestMain removes with Remove.
type Builder struct {
	// ImportPath is the import path of the binary's main package; BinName the
	// file name of the built binary; DirPrefix the prefix of the temp directory.
	ImportPath string
	BinName    string
	DirPrefix  string

	mu     sync.Mutex
	root   string
	builds map[string]string
}

// Remove deletes the built binaries.
func (b *Builder) Remove() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.root != "" {
		_ = os.RemoveAll(b.root)
		b.root = ""
		b.builds = nil
	}
}

// Build returns the path of the binary linked with ldflags, building it on first
// use.
func (b *Builder) Build(t testing.TB, ldflags string) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if bin, ok := b.builds[ldflags]; ok {
		return bin
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		cand := filepath.Join(gobuild.Default.GOROOT, "bin", "go")
		if _, serr := os.Stat(cand); serr != nil {
			t.Skipf("servetest: no go toolchain to build the real binary: %v", err)
		}
		goBin = cand
	}
	if b.root == "" {
		root, err := os.MkdirTemp("", b.DirPrefix)
		if err != nil {
			t.Fatal(err)
		}
		b.root = root
	}
	if b.builds == nil {
		b.builds = map[string]string{}
	}
	bin := filepath.Join(b.root, strconv.Itoa(len(b.builds)), b.BinName)
	args := []string{"build", "-o", bin}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
	// Build by import path from the inherited working directory (the directory
	// of the package under test, inside this module), never from a directory
	// derived from this file's compiled-in path, which -trimpath rewrites.
	args = append(args, b.ImportPath)
	cmd := exec.CommandContext(context.Background(), goBin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("servetest: go build: %v\n%s", err, out)
	}
	b.builds[ldflags] = bin
	return bin
}

// ModuleRoot is the directory holding the pg-connector go.mod, found by walking
// up from the working directory: go test runs a test binary in the directory of
// the package under test, and every caller of this package is inside the module.
// A path derived from this file's compiled-in location is not a real path under
// -trimpath.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	start, err := os.Getwd()
	if err != nil {
		t.Fatalf("servetest: working directory: %v", err)
	}
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("servetest: no go.mod above " + start)
		}
		dir = parent
	}
}

// ---- the client binary ------------------------------------------------------------

// ClientResult is one run of a binary.
type ClientResult struct {
	// Stdout is the raw answer, Stderr what the process wrote to stderr and Exit
	// its exit code. Resp is Stdout decoded (zero when it is not a response).
	Stdout string
	Stderr string
	Exit   int
	Resp   scriptout.Response
}

// RunBinary runs bin with args under exactly env, working directory "/" and
// stdin as given, and decodes its stdout as a response when it is one.
func RunBinary(t testing.TB, bin, stdin string, env []string, args ...string) ClientResult {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Dir = "/"
	cmd.Env = append([]string{}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := ClientResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	default:
		t.Fatalf("servetest: run %s: %v", bin, err)
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &res.Resp)
	return res
}

// ---- scratch and environment -------------------------------------------------------

// ShortDir returns a fresh directory with a short absolute path (a unix socket
// path is capped near 104 bytes, and t.TempDir can exceed that); prefix names it.
func ShortDir(t testing.TB, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		d, err = os.MkdirTemp("", prefix)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// DaemonPath is the PATH of a daemon and of the fake origin tool it runs: the
// minimal system dirs a service manager gives it, followed by the test
// process's own PATH. The tail matters in the nix build sandbox, where /usr/bin
// and /bin hold nothing but env and sh, while the coreutils a fake script needs
// (tr, cat, sed, sleep, ...) live only on the build's PATH.
func DaemonPath() string {
	if p := os.Getenv("PATH"); p != "" {
		return "/usr/bin:/bin:" + p
	}
	return "/usr/bin:/bin"
}

// safeBuffer is a bytes.Buffer safe for a test to read while the child writes.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
