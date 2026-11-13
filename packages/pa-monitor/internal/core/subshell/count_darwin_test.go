//go:build darwin

package subshell

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// startReady starts a command whose stdout prints one line once it is running
// as its final program image, and waits for that line.
func startReady(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %s: %v", name, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatalf("%s did not signal readiness: %v", name, err)
	}
	return cmd
}

// TestDarwinInProcessMatchesRealPgrep forks real children of the test process
// and pins the in-process p_comm count to what real `pgrep -P -l` reports
// through the old filter.
func TestDarwinInProcessMatchesRealPgrep(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available")
	}
	self := os.Getpid()
	// Each shell prints a line and then keeps running a non-final command, so
	// the shell does not exec-replace itself with sleep.
	const script = "echo ready; sleep 30; :"
	shA := startReady(t, "sh", "-c", script)
	startReady(t, "bash", "-c", script)
	if _, err := exec.LookPath("zsh"); err == nil {
		startReady(t, "zsh", "-c", script)
	}
	// A direct non-shell child (exec has completed when Start returns).
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	pgrepCount := func() int {
		out, err := DefaultRunPs(self)
		if err != nil {
			t.Fatal(err)
		}
		return CountPgrepOutput(out)
	}
	inProc := func() int {
		n, err := (&Counter{}).Count(self)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	want := pgrepCount()
	if want < 2 {
		t.Fatalf("real pgrep counted %d shells, expected at least sh and bash", want)
	}
	if got := inProc(); got != want {
		t.Fatalf("in-process count = %d, real pgrep through old filter = %d", got, want)
	}

	// After one shell child exits it is no longer counted.
	_ = shA.Process.Kill()
	_ = shA.Wait()
	want2 := pgrepCount()
	if want2 != want-1 {
		t.Fatalf("pgrep after exit = %d, want %d", want2, want-1)
	}
	if got := inProc(); got != want2 {
		t.Fatalf("in-process after exit = %d, want %d", got, want2)
	}
}

// TestDarwinLaunchesNoPgrep puts a recording fake pgrep first on PATH and
// shows the default darwin path never launches it.
func TestDarwinLaunchesNoPgrep(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "launched")
	fake := "#!/bin/sh\necho launched >> " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "pgrep"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Control: the recorder works, so an absent marker is meaningful.
	if _, err := DefaultRunPs(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: fake pgrep was not launched: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if _, err := (&Counter{}).Count(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("default darwin Count launched pgrep (stat err=%v)", err)
	}
}
