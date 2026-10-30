//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// TestBinaryPrintsVersion builds the real binary and runs it as a subprocess,
// which makes it integration tier (V-4).
func TestBinaryPrintsVersion(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pg-claude-code-recorder")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "pg-claude-code-recorder ") {
		t.Fatalf("unexpected output %q", out)
	}
}

// ---- the daemon, end to end (docket pg2-v77tsx, phase 1d) ----
//
// These start the real binary over its socket against throwaway directories;
// they never touch the real Claude directory. The binary is built with the
// faults tag so PG_CLAUDE_CODE_RECORDER_FAULT=<point> can crash it as a kill
// would.

var (
	builtOnce sync.Once
	builtPath string
	builtErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if builtPath != "" {
		_ = os.RemoveAll(filepath.Dir(builtPath))
	}
	os.Exit(code)
}

func recorderBinary(t *testing.T) string {
	t.Helper()
	builtOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ccrec-bin")
		if err != nil {
			builtErr = err
			return
		}
		builtPath = filepath.Join(dir, "pg-claude-code-recorder")
		if out, err := exec.Command("go", "build", "-tags", "faults", "-o", builtPath, ".").CombinedOutput(); err != nil {
			builtErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if builtErr != nil {
		t.Fatal(builtErr)
	}
	return builtPath
}

type daemonEnv struct {
	root, claude, state, sock, cfg, logFile string
	env                                     []string
}

func newDaemonEnv(t *testing.T) *daemonEnv {
	t.Helper()
	tmp, err := os.MkdirTemp("/tmp", "ccrec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	root, err := filepath.EvalSymlinks(tmp) // FSEvents reports real paths
	if err != nil {
		t.Fatal(err)
	}
	d := &daemonEnv{
		root: root, claude: filepath.Join(root, "claude"), state: filepath.Join(root, "state"),
		sock: filepath.Join(root, "r.sock"), cfg: filepath.Join(root, "config.toml"), logFile: filepath.Join(root, "recorder.log"),
	}
	if err := os.MkdirAll(filepath.Join(d.claude, "projects", "example-proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := fmt.Sprintf("claudeDir = %q\nstateDir = %q\nsocket = %q\nlogFile = %q\n[copy]\nthrottle = \"0s\"\n",
		d.claude, d.state, d.sock, d.logFile)
	if err := os.WriteFile(d.cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	d.env = append(os.Environ(), "XDG_STATE_HOME="+filepath.Join(root, "xdg"), "PG_CLAUDE_CODE_RECORDER_SOCKET=")
	return d
}

func (d *daemonEnv) transcript() string {
	return filepath.Join(d.claude, "projects", "example-proj", "a.jsonl")
}

// proc is a started daemon with its single Wait.
type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	code int
}

func launch(t *testing.T, cmd *exec.Cmd) *proc {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
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

// start launches serve (extra env entries override) and returns the process.
func (d *daemonEnv) start(t *testing.T, extraEnv ...string) *proc {
	t.Helper()
	cmd := exec.Command(recorderBinary(t), "serve", "--config", d.cfg)
	cmd.Env = append(append([]string{}, d.env...), extraEnv...)
	return launch(t, cmd)
}

// cli runs a recorder subcommand against this daemon.
func (d *daemonEnv) cli(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(recorderBinary(t), append(args, "--config", d.cfg)...)
	cmd.Env = d.env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func (d *daemonEnv) coverageFiles(t *testing.T) (wire.Coverage, bool) {
	t.Helper()
	out, _, code := d.cli(t, "coverage", "--view", "files", "--json", "--allow-partial")
	var cov wire.Coverage
	if code != 0 || json.Unmarshal([]byte(out), &cov) != nil {
		return cov, false
	}
	return cov, true
}

// poll waits for cond up to a deadline; it never sleeps a fixed time.
func poll(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *daemonEnv) waitCaughtUp(t *testing.T) {
	t.Helper()
	poll(t, "the daemon to answer and report caught up", 60*time.Second, func() bool {
		cov, ok := d.coverageFiles(t)
		return ok && cov.CaughtUp
	})
}

// copyMatches reports whether some copy under the state directory holds exactly
// the bytes of path.
func (d *daemonEnv) copyMatches(t *testing.T, path string) bool {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	_ = filepath.WalkDir(filepath.Join(d.state, "copies"), func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() {
			if got, err := os.ReadFile(p); err == nil && bytes.Equal(got, want) {
				found = true
			}
		}
		return nil
	})
	return found
}

func (d *daemonEnv) offsetEqualsSize(t *testing.T, path string) bool {
	cov, ok := d.coverageFiles(t)
	if !ok {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, f := range cov.FileList {
		if f.Path == path {
			return f.CopyOffset == fi.Size() && f.CopyLagBytes == 0
		}
	}
	return false
}

func stop(t *testing.T, p *proc) {
	t.Helper()
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Fatal("daemon ignored SIGTERM")
	}
}

// A real-FSEvents burst of 1,000 appends ends with the copy offset equal to the
// file size, polled to a deadline and never slept.
func TestServe1000AppendsReachACopyOffsetEqualToTheSize(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := d.start(t)
	d.waitCaughtUp(t)
	f, err := os.OpenFile(d.transcript(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 1000; i++ {
		if _, err := fmt.Fprintf(f, "{\"n\":%d,\"text\":\"example line\"}\n", i); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	poll(t, "copy offset to equal the file size", 60*time.Second, func() bool { return d.offsetEqualsSize(t, d.transcript()) })
	stdout, _, code := d.cli(t, "status", "--json")
	var st wire.Status
	if code != 0 || json.Unmarshal([]byte(stdout), &st) != nil || !st.Healthy {
		t.Fatalf("status %d %s", code, stdout)
	}
	stop(t, cmd)
	if !d.copyMatches(t, d.transcript()) {
		t.Fatal("no copy holds the original's bytes")
	}
}

// A SIGKILL while a writer is appending and the copier is copying, then a
// restart, recovers: the copy catches up byte for byte.
func TestSIGKILLMidCopyThenRestartRecovers(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := d.start(t)
	d.waitCaughtUp(t)
	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		f, err := os.OpenFile(d.transcript(), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer func() { _ = f.Close() }()
		line := strings.Repeat("x", 4000) + "\n"
		for i := 0; ; i++ {
			select {
			case <-stopWriter:
				return
			default:
			}
			_, _ = f.WriteString(line)
			time.Sleep(time.Millisecond) // about 4 MB/s: faster than the copier needs, not a disk filler
		}
	}()
	poll(t, "some bytes copied", 60*time.Second, func() bool {
		cov, ok := d.coverageFiles(t)
		return ok && len(cov.FileList) == 1 && cov.FileList[0].CopyOffset > 1<<20
	})
	if err := cmd.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-cmd.done
	close(stopWriter)
	<-writerDone

	// Down: status still answers from disk, with exit 3.
	if _, errOut, code := d.cli(t, "status"); code != 3 || !strings.Contains(errOut, "daemon offline") {
		t.Fatalf("offline status %d %q", code, errOut)
	}
	cmd = d.start(t)
	poll(t, "the restarted copy to catch up", 60*time.Second, func() bool { return d.offsetEqualsSize(t, d.transcript()) })
	stop(t, cmd)
	if !d.copyMatches(t, d.transcript()) {
		t.Fatal("the recovered copy differs from the original")
	}
}

// The daemon dies at a named crash point after the copy bytes were fsynced and
// before the offset was committed; the restart trims and re-copies.
func TestCrashAtACopyPointThenRestartRecovers(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The first copy of a new file may be a clone, which never reaches the
	// append crash point; the append below does.
	cmd := d.start(t, "PG_CLAUDE_CODE_RECORDER_FAULT=copy.after-fsync")
	d.waitCaughtUp(t)
	f, err := os.OpenFile(d.transcript(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(strings.Repeat("{\"n\":1,\"text\":\"example\"}\n", 5000))
	_ = f.Close()
	select {
	case <-cmd.done:
	case <-time.After(60 * time.Second):
		t.Fatal("the armed crash point was never reached")
	}
	if cmd.code != 137 {
		t.Fatalf("daemon exit %d, want 137 (the crash point)", cmd.code)
	}
	cmd = d.start(t)
	poll(t, "recovery to finish the copy", 60*time.Second, func() bool { return d.offsetEqualsSize(t, d.transcript()) })
	stop(t, cmd)
	if !d.copyMatches(t, d.transcript()) {
		t.Fatal("the recovered copy differs from the original")
	}
}

// With no daemon, status still answers from disk: exit 3, the socket path, the
// launch agent state, the log path, the state size and an actionable line.
func TestOfflineStatusAnswersFromDisk(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.MkdirAll(d.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.state, "marker"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := d.cli(t, "status")
	if code != 3 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{d.sock, d.logFile, "2048 bytes", "launch agent:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("offline stdout lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "daemon offline: no answer on "+d.sock) {
		t.Fatalf("stderr %q", stderr)
	}
	if out, _, code := d.cli(t, "status", "--short"); code != 3 || strings.TrimSpace(out) != "down" {
		t.Fatalf("short %d %q", code, out)
	}
}

// --client names the caller in the daemon's JSONL log; the pprof flag opens a
// socket under $XDG_STATE_HOME/<instance>/ that answers.
func TestClientNameIsLoggedAndPprofSocketAnswers(t *testing.T) {
	d := newDaemonEnv(t)
	if err := os.WriteFile(d.transcript(), []byte("{\"n\":0}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(recorderBinary(t), "serve", "--config", d.cfg, "--pprof")
	c.Env = d.env
	launch(t, c)
	d.waitCaughtUp(t)
	if _, _, code := d.cli(t, "status", "--client", "example-check"); code != 0 {
		t.Fatalf("status exit %d", code)
	}
	logb, _ := os.ReadFile(d.logFile)
	if !strings.Contains(string(logb), `"client":"example-check"`) || !strings.Contains(string(logb), `"service":"pg-claude-code-recorder"`) {
		t.Fatalf("log:\n%s", logb)
	}
	pprofSock := filepath.Join(d.root, "xdg", "pg-claude-code-recorder", "pprof.sock")
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var nd net.Dialer
		return nd.DialContext(ctx, "unix", pprofSock)
	}}}
	resp, err := hc.Get("http://pprof/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("pprof status %d", resp.StatusCode)
	}
}
