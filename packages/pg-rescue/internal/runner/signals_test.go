package runner_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

func signalLabel(s syscall.Signal) string { return strings.TrimPrefix(sigName(s), "SIG") }

func sigName(s syscall.Signal) string {
	switch s {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return "signal " + strconv.Itoa(int(s))
}

// waitForFile blocks until path exists (the fake publishes it atomically) and
// returns its content.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

// processAlive is the kill -0 probe.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// eventuallyDead waits briefly for a pid to disappear: the kernel needs a
// moment to reap an orphan.
func eventuallyDead(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// signalDuring runs the wrapper in the background, waits until the fake has
// published startedFile, then injects sig into the runner and waits for the
// run to end.
func (e *e2e) signalDuring(startedFile string, sig syscall.Signal, handlers string, cmd []string, opts ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	e.exec.Signals = make(chan os.Signal, 8)
	type out struct {
		code           int
		stdout, stderr string
	}
	done := make(chan out, 1)
	go func() {
		c, so, se := e.wrap(handlers, cmd, opts...)
		done <- out{c, so, se}
	}()
	waitForFile(e.t, startedFile)
	e.exec.Signals <- sig
	select {
	case o := <-done:
		return o.code, o.stdout, o.stderr
	case <-time.After(30 * time.Second):
		e.t.Fatal("the wrapper did not end after the signal")
		return
	}
}

// TestSignalMatrix covers {command, handler, verify} x {INT, TERM, HUP}: the
// wrapper exits 128+signo, the chain stops, the interrupted step is recorded
// in the in-memory result and in the report, and the child is gone.
func TestSignalMatrix(t *testing.T) {
	sigs := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
	for _, phase := range []runner.Phase{runner.PhaseCommand, runner.PhaseHandler, runner.PhaseVerify} {
		for _, sig := range sigs {
			t.Run(string(phase)+"/"+signalLabel(sig), func(t *testing.T) {
				dir := t.TempDir()
				started := filepath.Join(dir, "started")
				second := filepath.Join(dir, "second-handler-ran")
				later := hd{name: "later", argv: helperArgv("ran", "file="+second)}

				var first hd
				cmd := helperArgv("exit", "code=1")
				var opts []string
				switch phase {
				case runner.PhaseCommand:
					first = result("first", "declined")
					cmd = helperArgv("sleep", "started="+started)
				case runner.PhaseHandler:
					first = hd{name: "first", argv: helperArgv("sleep", "started="+started)}
				case runner.PhaseVerify:
					first = result("first", "resolved")
					opts = []string{"--verify", helperShell("sleep", "started="+started)}
				}
				e := newE2E(t, "", []hd{first, later}, chainOf("first", "later"))
				code, _, stderr := e.signalDuring(started, sig, "first,later", cmd, opts...)

				if want := 128 + int(sig); code != want {
					t.Errorf("exit = %d; want %d (stderr: %s)", code, want, stderr)
				}
				res := e.result()
				if res.Kind != runner.KindInterrupted || res.ExitCode != code {
					t.Errorf("kind=%s exit=%d", res.Kind, res.ExitCode)
				}
				in := res.Interrupted
				if in == nil || in.Phase != phase || in.Signal != sig {
					t.Fatalf("interruption = %+v; want phase %s signal %v", in, phase, sig)
				}
				if exists(second) {
					t.Error("the chain must stop: the next handler ran")
				}
				if !res.Kept {
					t.Error("an interrupted run keeps its directory")
				}
				if _, err := os.Stat(e.rdir()); err != nil {
					t.Errorf("run directory missing: %v", err)
				}
				pid, _ := strconv.Atoi(waitForFile(t, started))
				if !eventuallyDead(pid) {
					t.Errorf("the interrupted process %d is still alive", pid)
				}

				atts := res.Report.Attempts
				switch phase {
				case runner.PhaseCommand:
					if len(atts) != 0 || in.Handler != "" {
						t.Errorf("no handler may run: attempts=%+v handler=%q", atts, in.Handler)
					}
				case runner.PhaseHandler, runner.PhaseVerify:
					if in.Handler != "first" || in.Position != 1 {
						t.Errorf("interrupted handler = %q at %d", in.Handler, in.Position)
					}
					if len(atts) != 1 || atts[0].Handler != "first" || atts[0].Outcome != contract.Failed {
						t.Fatalf("attempts = %+v", atts)
					}
					wantReason := "interrupted by " + sigName(sig)
					if phase == runner.PhaseVerify {
						wantReason = "resolved → verify " + wantReason
					}
					if atts[0].Reason != wantReason {
						t.Errorf("reason = %q; want %q", atts[0].Reason, wantReason)
					}
					disk := e.onDisk()
					if len(disk.Attempts) != 1 || disk.Attempts[0].Reason != wantReason {
						t.Errorf("report.json attempts = %+v", disk.Attempts)
					}
				}
			})
		}
	}
}

// TestRealSignalsReachTheWrapper sends genuine signals to the process, so the
// production subscription (signal.Notify) is exercised, not just the seam.
func TestRealSignalsReachTheWrapper(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(signalLabel(sig), func(t *testing.T) {
			dir := t.TempDir()
			started := filepath.Join(dir, "started")
			e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
			done := make(chan int, 1)
			go func() {
				c, _, _ := e.wrap("h", helperArgv("sleep", "started="+started))
				done <- c
			}()
			waitForFile(t, started)
			// The runner subscribed before the command was spawned, so the
			// process-wide signal is caught, not fatal.
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case c := <-done:
				if c != 128+int(sig) {
					t.Errorf("exit = %d; want %d", c, 128+int(sig))
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the wrapper did not end")
			}
		})
	}
}

// TestSigintAtTheTerminalIsNotForwardedTwice: when the wrapper is in the
// terminal's foreground group a typed Ctrl-C already reached the command, so
// the wrapper must not send it again. The fake command counts what it gets.
func TestSigintAtTheTerminalIsNotForwardedTwice(t *testing.T) {
	for _, foreground := range []bool{true, false} {
		t.Run("foreground="+strconv.FormatBool(foreground), func(t *testing.T) {
			dir := t.TempDir()
			started := filepath.Join(dir, "started")
			e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
			e.exec.Foreground = func() bool { return foreground }
			// The command ignores SIGINT: it dies of the later TERM sent below
			// only if it did not see (and ignore) a forwarded INT. We detect
			// forwarding by having the command record received signals.
			e.exec.Signals = make(chan os.Signal, 8)
			done := make(chan int, 1)
			go func() {
				c, _, _ := e.wrap("h", helperArgv("count-signals", "started="+started, "file="+filepath.Join(dir, "got")))
				done <- c
			}()
			waitForFile(t, started)
			e.exec.Signals <- syscall.SIGINT
			time.Sleep(200 * time.Millisecond) // let a forwarded INT land
			e.exec.Signals <- syscall.SIGTERM  // always forwarded; ends the fake
			<-done
			got := waitForFile(t, filepath.Join(dir, "got"))
			if foreground && strings.Contains(got, "interrupt") {
				t.Errorf("INT was forwarded although the terminal already delivered it: %q", got)
			}
			if !foreground && !strings.Contains(got, "interrupt") {
				t.Errorf("INT was not forwarded outside the terminal's foreground group: %q", got)
			}
		})
	}
}

// TestSignalBeforeTheCommandStartsSpawnsNothing: a signal already pending when
// the run starts ends it without running the command.
func TestSignalBeforeTheCommandStartsSpawnsNothing(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	e.exec.Signals = make(chan os.Signal, 1)
	e.exec.Signals <- syscall.SIGTERM
	marker := filepath.Join(e.root, "ran")
	code, _, _ := e.wrap("h", helperArgv("ran", "file="+marker))
	if code != 143 || exists(marker) || e.result().Kind != runner.KindInterrupted {
		t.Errorf("exit=%d ran=%v kind=%s", code, exists(marker), e.result().Kind)
	}
}
