//go:build integration

package originguard

// Integration tier: the real binary under real git and ssh, a real OpenSSH
// ssh-agent and ssh-keygen with a throwaway CA, a pseudo-terminal, and real
// process groups. Nothing here reaches a network origin, a real login tool,
// a browser or the operator's agent. Run with: go test -tags integration .

import (
	"bytes"
	"context"
	"fmt"
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

	"github.com/creack/pty"
)

var (
	guardBin string
	fakeBin  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pog-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	guardBin = filepath.Join(dir, "pg-origin-guard")
	fakeBin = filepath.Join(dir, "fakeproxy")
	for out, pkg := range map[string]string{guardBin: "./cmd/pg-origin-guard", fakeBin: "./testdata/fakeproxy"} {
		if b, err := exec.CommandContext(context.Background(), "go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, b)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type itRig struct {
	t        *testing.T
	root     string
	sock     string
	env      []string
	pidfile  string
	marker   string
	portfile string
	ttyfile  string
	presence string
}

type itOpts struct {
	attemptTimeout string
	backoff        string
	validCert      bool
	presence       string
}

func newITRig(t *testing.T, o itOpts) *itRig {
	t.Helper()
	if o.attemptTimeout == "" {
		o.attemptTimeout = "3m"
	}
	if o.backoff == "" {
		o.backoff = `"5m"`
	}
	if o.presence == "" {
		o.presence = "present"
	}
	root := t.TempDir()
	short, err := os.MkdirTemp("/tmp", "pog")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	r := &itRig{
		t: t, root: root, sock: filepath.Join(short, "agent.sock"),
		pidfile: filepath.Join(root, "pid"), marker: filepath.Join(root, "marker"),
		portfile: filepath.Join(root, "port"), ttyfile: filepath.Join(root, "tty"),
		presence: filepath.Join(root, "userpresence.json"),
	}
	r.startAgent(o.validCert)
	r.writePresence(o.presence)
	cfg := fmt.Sprintf(`[presence]
file = %q
[realm.zr]
match = { host = "example.invalid", user = "git" }
[realm.zr.login]
attempt_timeout = %q
backoff = [%s]
`, r.presence, o.attemptTimeout, o.backoff)
	cfgDir := filepath.Join(root, "config", "pg-origin-guard")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r.env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"SSH_AUTH_SOCK=" + r.sock,
		"FP_PIDFILE=" + r.pidfile, "FP_MARKER=" + r.marker,
		"FP_PORTFILE=" + r.portfile, "FP_TTYFILE=" + r.ttyfile,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0",
	}
	return r
}

func (r *itRig) writePresence(state string) {
	doc := fmt.Sprintf(`{"schemaVersion":1,"state":%q,"writtenAt":%q}`+"\n", state, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(r.presence, []byte(doc), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *itRig) sh(name string, args ...string) {
	r.t.Helper()
	c := exec.CommandContext(context.Background(), name, args...)
	c.Env = append(os.Environ(), "SSH_AUTH_SOCK="+r.sock)
	if b, err := c.CombinedOutput(); err != nil {
		r.t.Fatalf("%s %v: %v\n%s", name, args, err, b)
	}
}

// startAgent starts a throwaway ssh-agent. With validCert it holds a
// certificate signed by a throwaway CA that is valid now; otherwise it holds
// one that is already expired, which ssh-add ignores, so the agent is empty.
func (r *itRig) startAgent(validCert bool) {
	t := r.t
	agent := exec.CommandContext(context.Background(), "ssh-agent", "-D", "-a", r.sock)
	agent.Env = os.Environ()
	if err := agent.Start(); err != nil {
		t.Skipf("no ssh-agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Process.Kill(); _, _ = agent.Process.Wait() })
	waitFor(t, "agent socket", func() bool { _, err := os.Stat(r.sock); return err == nil })
	keys := filepath.Join(r.root, "keys")
	if err := os.MkdirAll(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	r.sh("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(keys, "ca"))
	r.sh("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(keys, "user"))
	validity := "-2h:-1h"
	if validCert {
		validity = "-1h:+1h"
	}
	r.sh("ssh-keygen", "-q", "-s", filepath.Join(keys, "ca"), "-I", "test", "-n", "user", "-V", validity, filepath.Join(keys, "user.pub"))
	c := exec.CommandContext(context.Background(), "ssh-add", filepath.Join(keys, "user"))
	c.Env = append(os.Environ(), "SSH_AUTH_SOCK="+r.sock)
	_ = c.Run() // an expired certificate makes ssh-add complain; the agent stays empty of user certs
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fileExists(p string) func() bool {
	return func() bool { _, err := os.Stat(p); return err == nil }
}

func readPID(t *testing.T, p string) int {
	t.Helper()
	waitFor(t, "pid file "+p, func() bool { b, err := os.ReadFile(p); return err == nil && len(b) > 0 })
	b, _ := os.ReadFile(p)
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitDead(t *testing.T, what string, pid int) {
	t.Helper()
	waitFor(t, what+" to end", func() bool { return !alive(pid) })
}

func (r *itRig) wrapperArgs(extra []string, fp ...string) []string {
	args := append([]string{"proxy", "--realm", "zr"}, extra...)
	args = append(args, "--", fakeBin)
	return append(args, fp...)
}

// pipes starts the wrapper with no controlling terminal (a new session) and pipes.
func (r *itRig) pipes(extra []string, fp ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	c := exec.CommandContext(context.Background(), guardBin, r.wrapperArgs(extra, fp...)...)
	c.Env = r.env
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	return c, &out, &errb
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -99
}

type ptyRun struct {
	cmd *exec.Cmd
	f   *os.File
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *ptyRun) out() string { p.mu.Lock(); defer p.mu.Unlock(); return p.buf.String() }

// onPTY starts cmd with a controlling pseudo-terminal.
func onPTY(t *testing.T, c *exec.Cmd) *ptyRun {
	t.Helper()
	f, err := pty.Start(c)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	p := &ptyRun{cmd: c, f: f}
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			if n > 0 {
				p.mu.Lock()
				p.buf.Write(b[:n])
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = f.Close(); _ = c.Process.Kill() })
	return p
}

func (r *itRig) ptyWrapper(extra []string, fp ...string) *ptyRun {
	c := exec.CommandContext(context.Background(), guardBin, r.wrapperArgs(extra, fp...)...)
	c.Env = r.env
	return onPTY(r.t, c)
}

func waitCmd(t *testing.T, c *exec.Cmd) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return exitCode(err)
	case <-time.After(30 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("wrapper did not exit")
		return -1
	}
}

// gitSSH runs real git over real ssh with the guard as the ProxyCommand.
func (r *itRig) gitSSH(c *exec.Cmd, fp string) {
	proxy := fmt.Sprintf("%s proxy --realm zr -- %s %s", guardBin, fakeBin, fp)
	sshCmd := fmt.Sprintf("ssh -F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o ProxyCommand='%s'", proxy)
	c.Args = []string{"git", "ls-remote", "ssh://git@example.invalid/repo.git"}
	c.Env = append(append([]string{}, r.env...), "GIT_SSH_COMMAND="+sshCmd)
}

func TestRealGitSSHPipesAsleepIsRefused(t *testing.T) {
	r := newITRig(t, itOpts{presence: "asleep"})
	c := exec.CommandContext(context.Background(), "git")
	r.gitSSH(c, "exit")
	c.Path, _ = exec.LookPath("git")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var errb bytes.Buffer
	c.Stderr = &errb
	if err := c.Run(); err == nil {
		t.Fatal("git must fail when the guard refuses")
	}
	if !strings.Contains(errb.String(), "pg-origin-guard: refused (no-human)") {
		t.Fatalf("git stderr = %q", errb.String())
	}
	if fileExists(r.marker)() {
		t.Fatal("the proxy command must not run when refused")
	}
}

func TestRealGitSSHPTYRunsTheCommand(t *testing.T) {
	r := newITRig(t, itOpts{presence: "asleep"})
	c := exec.CommandContext(context.Background(), "git")
	r.gitSSH(c, "exit")
	c.Path, _ = exec.LookPath("git")
	p := onPTY(t, c)
	code := waitCmd(t, c)
	if !fileExists(r.marker)() {
		t.Fatalf("a terminal caller must be allowed through (git exit %d, out %q)", code, p.out())
	}
	if strings.Contains(p.out(), "refused") {
		t.Fatalf("terminal caller was refused: %q", p.out())
	}
}

func TestRealGitSSHValidCertRunsWithoutTerminal(t *testing.T) {
	r := newITRig(t, itOpts{presence: "asleep", validCert: true})
	c := exec.CommandContext(context.Background(), "git")
	r.gitSSH(c, "exit")
	c.Path, _ = exec.LookPath("git")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = c.Run()
	if !fileExists(r.marker)() {
		t.Fatal("a valid cert needs no presence: the proxy command must run")
	}
}

func TestForegroundCtrlCReachesChildAndWrapper(t *testing.T) {
	r := newITRig(t, itOpts{})
	p := r.ptyWrapper(nil, "sleep")
	pid := readPID(t, r.pidfile)
	if _, err := p.f.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	code := waitCmd(t, p.cmd)
	waitDead(t, "child", pid)
	if code != 130 {
		t.Fatalf("wrapper exit = %d, want 130 (the child's SIGINT status passed through)", code)
	}
}

func TestSIGKILLedWrapperChildEndsWhenStdinCloses(t *testing.T) {
	r := newITRig(t, itOpts{})
	c, _, _ := r.pipes(nil, "cat")
	c.Stdout, c.Stderr = nil, nil // no copy goroutines: the orphaned child must not hold Wait
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, r.pidfile)
	_ = c.Process.Kill()
	_ = c.Wait()
	if !alive(pid) {
		t.Fatal("setup: the orphaned child should still be reading its stdin")
	}
	_ = in.Close()
	waitDead(t, "orphaned child after stdin closed", pid)
}

func TestTerminalCallerChildStaysInTheForegroundGroupAndWritesToTTY(t *testing.T) {
	r := newITRig(t, itOpts{})
	p := r.ptyWrapper(nil, "tty")
	waitCmd(t, p.cmd)
	b, err := os.ReadFile(r.ttyfile)
	if err != nil {
		t.Fatal(err)
	}
	var self, parent int
	if _, err := fmt.Sscanf(string(b), "%d %d", &self, &parent); err != nil || self != parent {
		t.Fatalf("child pgid %d must equal the wrapper's %d (no separate group): %v", self, parent, err)
	}
	waitFor(t, "tty text", func() bool { return strings.Contains(p.out(), "from-child") })
}

func TestTerminalWaiterPreemptsStuckHolder(t *testing.T) {
	r := newITRig(t, itOpts{attemptTimeout: "10m"})
	holder, _, _ := r.pipes(nil, "sleep")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	holderChild := readPID(t, r.pidfile)
	// the holder persisted its attempt before the waiter arrives
	waitFor(t, "holder state", func() bool {
		b, _ := os.ReadFile(filepath.Join(r.root, "state", "pg-origin-guard", "realms", "zr.json"))
		return bytes.Contains(b, []byte("holder_token"))
	})
	_ = os.Remove(r.pidfile)
	waiter := r.ptyWrapper([]string{"--terminal-wait", "1s"}, "exit")
	if code := waitCmd(t, waiter.cmd); code != 0 {
		t.Fatalf("waiter exit = %d, out %q", code, waiter.out())
	}
	if !strings.Contains(waiter.out(), "waiting for a login already in progress") {
		t.Fatalf("no status line: %q", waiter.out())
	}
	waitDead(t, "stuck holder child", holderChild)
	if code := waitCmd(t, holder); code != 143 {
		t.Fatalf("holder wrapper exit = %d, want 143", code)
	}
}

func TestTerminalLessAttemptTimeoutKillsTheWholeGroup(t *testing.T) {
	r := newITRig(t, itOpts{attemptTimeout: "2s"})
	c, _, errb := r.pipes(nil, "listener")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "grandchild port", fileExists(r.portfile))
	waitFor(t, "port content", func() bool { b, _ := os.ReadFile(r.portfile); return len(b) > 0 })
	b, _ := os.ReadFile(r.portfile)
	addr := "127.0.0.1:" + strings.TrimSpace(string(b))
	if conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", addr); err != nil {
		t.Fatalf("grandchild should be listening: %v", err)
	} else {
		_ = conn.Close()
	}
	if code := waitCmd(t, c); code != 143 {
		t.Fatalf("exit = %d, want 143; stderr %q", code, errb.String())
	}
	waitFor(t, "port released by the group kill", func() bool {
		conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(context.Background(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return false
		}
		return true
	})
}

func TestLoginLockRaceAdmitsExactlyOneAttempt(t *testing.T) {
	r := newITRig(t, itOpts{backoff: `"10m"`})
	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		c, _, _ := r.pipes(nil, "marker")
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = exitCode(c.Run())
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(r.marker)
	if got := strings.Count(string(b), "ran"); got != 1 {
		t.Fatalf("%d callers ran the command, want exactly 1 (codes %v)", got, codes)
	}
	refused := 0
	for _, c := range codes {
		if c == 3 {
			refused++
		}
	}
	if refused != n-1 {
		t.Fatalf("codes = %v, want %d refusals", codes, n-1)
	}
}
