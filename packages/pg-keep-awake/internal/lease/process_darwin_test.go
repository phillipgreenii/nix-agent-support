//go:build darwin

package lease

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestOwnProcessStateAndStartTime(t *testing.T) {
	st, err := ProcStateOf(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.Zombie {
		t.Fatalf("own state = %+v", st)
	}
	now := time.Now().UnixMicro()
	if st.StartUS <= 0 || st.StartUS > now || now-st.StartUS > int64(24*time.Hour/time.Microsecond) {
		t.Errorf("own start_us %d implausible vs now %d", st.StartUS, now)
	}
	again, _ := ProcStateOf(os.Getpid())
	if again.StartUS != st.StartUS {
		t.Errorf("start_us not stable: %d vs %d", again.StartUS, st.StartUS)
	}
	ref, err := Ref(os.Getpid())
	if err != nil || ref.PID != os.Getpid() || ref.StartUS != st.StartUS {
		t.Errorf("Ref = %+v, %v", ref, err)
	}
}

func TestMissingPIDDoesNotExist(t *testing.T) {
	for _, pid := range []int{0, -5} {
		if _, err := ProcStateOf(pid); err != ErrNoProcess {
			t.Errorf("ProcStateOf(%d) = %v", pid, err)
		}
	}
	// A pid that just exited and was reaped.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if st := (SysProber{}).Probe(cmd.Process.Pid); st.Exists {
		t.Errorf("reaped pid %d still exists: %+v", cmd.Process.Pid, st)
	}
	if _, err := ProcessInfo(cmd.Process.Pid); err == nil {
		t.Error("ProcessInfo of a reaped pid succeeded")
	}
}

// spawn starts a long-sleeping child and kills+reaps it at cleanup, signalling
// only the pid it started.
func spawn(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if pid := cmd.Process.Pid; pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			_ = cmd.Wait()
		}
	})
	return cmd
}

func TestChildStateInfoAndEnumeration(t *testing.T) {
	child := spawn(t, "sleep", "47")
	pid := child.Process.Pid
	var st ProcState
	for i := 0; i < 100; i++ {
		st, _ = ProcStateOf(pid)
		if st.Exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.Exists || st.Zombie {
		t.Fatalf("child state = %+v", st)
	}
	self, _ := ProcStateOf(os.Getpid())
	if st.StartUS < self.StartUS {
		t.Errorf("child start_us %d precedes parent %d", st.StartUS, self.StartUS)
	}

	// exec may not have replaced the forked image yet; poll for the new name.
	var info ProcInfo
	var err error
	for i := 0; i < 200; i++ {
		info, err = ProcessInfo(pid)
		if err == nil && info.Name == "sleep" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || info.Name != "sleep" || !slices.Equal(info.Argv, []string{"sleep", "47"}) {
		t.Errorf("child info = %+v, %v", info, err)
	}

	self2, err := ProcessInfo(os.Getpid())
	if err != nil || self2.Name != filepath.Base(mustExe(t)) || len(self2.Argv) == 0 {
		t.Errorf("own info = %+v, %v", self2, err)
	}

	// The child is ours: its parent is this process, and its executable path
	// is the full path of sleep (what the claude-holder check compares).
	if st, _ := ProcStateOf(pid); st.PPID != os.Getpid() {
		t.Errorf("child PPID = %d, want %d", st.PPID, os.Getpid())
	}
	if !filepath.IsAbs(info.Exe) || filepath.Base(info.Exe) != "sleep" {
		t.Errorf("child Exe = %q", info.Exe)
	}
	if st, _ := ProcStateOf(os.Getpid()); st.PPID != os.Getppid() {
		t.Errorf("own PPID = %d, want %d", st.PPID, os.Getppid())
	}

	pids, err := ListPIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pids, pid) || !slices.Contains(pids, os.Getpid()) {
		t.Errorf("ListPIDs misses the test's own or child pid (%d pids)", len(pids))
	}
	for _, p := range pids {
		if p <= 0 {
			t.Fatalf("ListPIDs returned pid %d", p)
		}
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// The executable name is the basename of the full path (KERN_PROCARGS2), not
// the 16-byte p_comm, so pg-caffeinate-shim (18 characters) matches.
func TestProcessNameLongerThan16Chars(t *testing.T) {
	const long = "pg-caffeinate-shim"
	if len(long) <= 16 {
		t.Fatal("test name must exceed 16 characters")
	}
	src, err := os.ReadFile(mustExe(t))
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), long)
	if err := os.WriteFile(copyPath, src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(copyPath)
	cmd.Env = append(os.Environ(), "LEASE_TEST_HELPER=sleep")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pid := cmd.Process.Pid; pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	})
	var info ProcInfo
	for i := 0; i < 300; i++ {
		info, err = ProcessInfo(cmd.Process.Pid)
		if err == nil && info.Name == long {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || info.Name != long {
		t.Fatalf("Name = %q (%v), want %q", info.Name, err, long)
	}
	if len(info.Argv) != 1 || info.Argv[0] != copyPath {
		t.Errorf("argv = %v", info.Argv)
	}
}

func TestParseProcArgs2(t *testing.T) {
	// argc=2, exec path, padding NULs, argv, then an environment string.
	buf := []byte{2, 0, 0, 0}
	buf = append(buf, "/usr/bin/some-long-tool-name\x00\x00\x00\x00"...)
	buf = append(buf, "tool\x00--flag\x00FOO=bar\x00"...)
	info, err := parseProcArgs2(buf)
	if err != nil || info.Name != "some-long-tool-name" || !slices.Equal(info.Argv, []string{"tool", "--flag"}) {
		t.Errorf("parse = %+v, %v", info, err)
	}
	for _, bad := range [][]byte{nil, {1, 0}, {1, 0, 0, 0, 'x'}} {
		if _, err := parseProcArgs2(bad); err == nil {
			t.Errorf("parseProcArgs2(%v) accepted", bad)
		}
	}
}

// A SIGKILLed child that has not been reaped is a zombie and must not count
// as a live lease client.
func TestZombieClientIsNotLive(t *testing.T) {
	child := spawn(t, "sleep", "47")
	pid := child.Process.Pid
	ref, err := Ref(pid)
	if err != nil {
		t.Fatal(err)
	}
	l := &Lease{Owner: "o", Key: "k", Kind: KindRun, Client: &ref}
	if !l.Live(time.Now(), nil) {
		t.Fatal("running child must be live")
	}

	signalPID(t, pid, syscall.SIGKILL) // deliberately NOT Wait()ed yet
	var st ProcState
	for i := 0; i < 500; i++ {
		st = SysProber{}.Probe(pid)
		if st.Zombie {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !st.Exists || !st.Zombie {
		t.Fatalf("killed, unreaped child = %+v, want a zombie", st)
	}
	if st.StartUS != ref.StartUS {
		t.Errorf("zombie start_us %d != %d", st.StartUS, ref.StartUS)
	}
	if l.Live(time.Now(), nil) {
		t.Error("lease with a zombie client is live")
	}

	_ = child.Wait() // reap; the pid disappears
	if (SysProber{}).Probe(pid).Exists {
		t.Error("reaped child still exists")
	}
	if l.Live(time.Now(), nil) {
		t.Error("lease with a reaped client is live")
	}
}

func TestLiveAgainstRealProcesses(t *testing.T) {
	ref, err := Ref(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	ok := &Lease{Owner: "o", Key: "k", Kind: KindRun, Client: &ref, ExpiresAt: &future, Watch: &ref}
	if !ok.Live(time.Now(), nil) {
		t.Error("own process lease must be live")
	}
	reused := &Lease{Owner: "o", Key: "k", Kind: KindRun, Client: &ProcRef{PID: ref.PID, StartUS: ref.StartUS + 1}}
	if reused.Live(time.Now(), nil) {
		t.Error("start_us mismatch (pid reuse) must not be live")
	}
	gone := &Lease{Owner: "o", Key: "k", Kind: KindRun, Watch: &ProcRef{PID: 2_000_000_000, StartUS: 1}, ExpiresAt: &future}
	if gone.Live(time.Now(), nil) {
		t.Error("nonexistent watch must not be live")
	}
}

func TestSignalGuardRefusesPID1AndBelow(t *testing.T) {
	for _, pid := range []int{1, 0, -1, -1000} {
		if refusePID(pid) == nil {
			t.Errorf("pid %d not refused", pid)
		}
	}
	if refusePID(os.Getpid()) != nil {
		t.Error("a normal pid was refused")
	}
}

// refusePID is the guard every signalling path in these tests goes through:
// pid 1 or lower would signal init or a process group. Darwin-only, like its
// callers.
func refusePID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	return nil
}

// signalPID signals only a pid the test spawned.
func signalPID(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := refusePID(pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatalf("kill(%d, %v): %v", pid, sig, err)
	}
}
