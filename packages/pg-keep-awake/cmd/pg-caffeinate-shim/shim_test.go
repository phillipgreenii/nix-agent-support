package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func TestVersionFlag(t *testing.T) {
	out, err := exec.Command(shimBin, "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), "pg-caffeinate-shim ") {
		t.Fatalf("--version = %q, %v", out, err)
	}
}

func TestTimedLeaseIsWrittenExpiresAndIsRemoved(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	before := time.Now()
	p := startShim(t, xdg, "-t", "3", "-i")
	l := waitForLease(t, xdg, p)
	seen := time.Now()

	if l.Owner != "shim" || l.Kind != "shim" || l.SchemaVersion != 1 {
		t.Errorf("owner/kind/schema = %q/%q/%d", l.Owner, l.Kind, l.SchemaVersion)
	}
	pid := p.cmd.Process.Pid
	ref, err := lease.Ref(pid)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%d-%d", pid, ref.StartUS); l.Key != want || l.ID != "shim."+want {
		t.Errorf("key/id = %q/%q, want %q", l.Key, l.ID, want)
	}
	if l.Client == nil || *l.Client != ref {
		t.Errorf("client = %+v, want %+v", l.Client, ref)
	}
	if l.Watch != nil {
		t.Errorf("watch = %+v, want nil", l.Watch)
	}
	if got := strings.Join(l.Flags, ""); got != "i" {
		t.Errorf("flags = %q", got)
	}
	if len(l.Argv) != 4 || l.Argv[0] != "caffeinate" || l.Argv[1] != "-t" {
		t.Errorf("argv = %q", l.Argv)
	}
	if l.ExpiresAt == nil {
		t.Fatal("expires_at is nil for -t 3")
	}
	if got := l.ExpiresAt.Sub(l.CreatedAt); got != 3*time.Second {
		t.Errorf("expires_at - created_at = %v, want exactly 3s", got)
	}
	// Wall clock: expires_at is now + N, taken between the test's two clocks.
	if l.ExpiresAt.Before(before.Add(3*time.Second)) || l.ExpiresAt.After(seen.Add(3*time.Second)) {
		t.Errorf("expires_at %v not within [%v, %v]", l.ExpiresAt, before.Add(3*time.Second), seen.Add(3*time.Second))
	}

	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) {
		t.Fatalf("timed shim: %s, stderr %q", describe(ws), p.stderr.String())
	}
	if elapsed := time.Since(before); elapsed < 2500*time.Millisecond {
		t.Errorf("exited after %v, want about 3s", elapsed)
	}
	noLeases(t, xdg)
}

func TestIndefiniteFormsHoldUntilSignalled(t *testing.T) {
	requireDarwin(t)
	forms := map[string][]string{
		"-t 0":         {"-t", "0"},
		"no arguments": nil,
		"-w 0":         {"-w", "0"},
		"--":           {"--"},
	}
	sigs := map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGHUP": syscall.SIGHUP, "SIGINT": syscall.SIGINT}
	for formName, args := range forms {
		for sigName, sig := range sigs {
			t.Run(formName+"/"+sigName, func(t *testing.T) {
				t.Parallel()
				xdg := t.TempDir()
				p := startShim(t, xdg, args...)
				l := waitForLease(t, xdg, p)
				if l.ExpiresAt != nil || l.Watch != nil || l.Client == nil || l.Client.PID != p.cmd.Process.Pid {
					t.Errorf("indefinite lease = expires %v watch %v client %v", l.ExpiresAt, l.Watch, l.Client)
				}
				if got := strings.Join(l.Flags, ""); got != "i" {
					t.Errorf("flags = %q, want the default i", got)
				}
				time.Sleep(300 * time.Millisecond)
				if !p.running() {
					t.Fatalf("shim exited on its own: %s, stderr %q", describe(p.status), p.stderr.String())
				}
				p.signal(t, sig)
				if ws := p.wait(t, 10*time.Second); !diedOf(ws, sig) {
					t.Errorf("after %s: %s, want death by that signal", sigName, describe(ws))
				}
				noLeases(t, xdg)
			})
		}
	}
}

func TestWaitStatusesAre143129130(t *testing.T) {
	requireDarwin(t)
	for sig, want := range map[syscall.Signal]int{syscall.SIGTERM: 143, syscall.SIGHUP: 129, syscall.SIGINT: 130} {
		xdg := t.TempDir()
		p := startShim(t, xdg)
		waitForLease(t, xdg, p)
		p.signal(t, sig)
		ws := p.wait(t, 10*time.Second)
		// A shell reports a signal death as 128+n; check that mapping.
		if !ws.Signaled() || 128+int(ws.Signal()) != want {
			t.Errorf("%v: %s, want shell status %d", sig, describe(ws), want)
		}
	}
}

func TestSIGKILLLeavesTheLeaseFile(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	p := startShim(t, xdg, "-t", "0")
	waitForLease(t, xdg, p)
	p.signal(t, syscall.SIGKILL)
	if ws := p.wait(t, 10*time.Second); !diedOf(ws, syscall.SIGKILL) {
		t.Fatalf("status %s", describe(ws))
	}
	if files := leaseFiles(xdg); len(files) != 1 {
		t.Errorf("lease files after SIGKILL = %v, want exactly the one the keeper will reap", files)
	}
}

func TestSignalIgnoredAtStartGetsNoHandler(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	// sh ignores INT and HUP, then execs the shim: ignored dispositions survive
	// exec, as for a non-interactive background job.
	cmd := exec.Command("sh", "-c", `trap '' INT HUP; exec "$0" "$@"`, shimBin, "-t", "60")
	cmd.Env = shimEnv(xdg)
	p := start(t, cmd)
	waitForLease(t, xdg, p)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGHUP} {
		p.signal(t, sig)
		time.Sleep(400 * time.Millisecond)
		if !p.running() {
			t.Fatalf("%v killed a shim that had it ignored at start: %s", sig, describe(p.status))
		}
		if len(leaseFiles(xdg)) != 1 {
			t.Fatalf("%v removed the lease of a shim that ignores it", sig)
		}
	}
	p.signal(t, syscall.SIGTERM)
	if ws := p.wait(t, 10*time.Second); !diedOf(ws, syscall.SIGTERM) {
		t.Errorf("SIGTERM (not ignored): %s", describe(ws))
	}
	noLeases(t, xdg)
}

func TestWatchRecordsPidAndEndsWhenProcessExits(t *testing.T) {
	requireDarwin(t)
	cases := []struct {
		name   string
		args   []string // {HELPER} stands for the helper's pid
		helper string   // seconds the helper sleeps
		within time.Duration
	}{
		{"-w", []string{"-w", "{HELPER}"}, "2", 8 * time.Second},
		{"-t and -w, watch fires first", []string{"-t", "30", "-w", "{HELPER}"}, "2", 8 * time.Second},
		{"-t and -w, timer fires first", []string{"-t", "1", "-w", "{HELPER}"}, "60", 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdg := t.TempDir()
			helper := start(t, exec.Command("sleep", tc.helper))
			hpid := helper.cmd.Process.Pid
			href, err := lease.Ref(hpid)
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			for _, a := range tc.args {
				args = append(args, strings.ReplaceAll(a, "{HELPER}", fmt.Sprint(hpid)))
			}
			began := time.Now()
			p := startShim(t, xdg, args...)
			l := waitForLease(t, xdg, p)
			if l.Watch == nil || *l.Watch != href {
				t.Errorf("watch = %+v, want %+v", l.Watch, href)
			}
			if ws := p.wait(t, tc.within); !exitedWith(ws, 0) {
				t.Fatalf("shim: %s, stderr %q", describe(ws), p.stderr.String())
			}
			if tc.helper == "60" && helper.running() == false {
				t.Error("the timer case must end while the watched process still runs")
			}
			if tc.helper == "2" && time.Since(began) > 6*time.Second {
				t.Errorf("took %v to notice the watched process exit", time.Since(began))
			}
			noLeases(t, xdg)
		})
	}
}

func TestNoLeaseOutcomesExitZeroAtOnce(t *testing.T) {
	requireDarwin(t)
	for _, args := range [][]string{{"-t", "-5"}, {"-t", "99999999999999999999"}, {"-w", "-1"}, {"-w", "99999999"}} {
		xdg := t.TempDir()
		p := startShim(t, xdg, args...)
		if ws := p.wait(t, 5*time.Second); !exitedWith(ws, 0) {
			t.Errorf("%v: %s", args, describe(ws))
		}
		if p.stderr.String() != "" {
			t.Errorf("%v: stderr %q, want none", args, p.stderr.String())
		}
		noLeases(t, xdg)
	}
}

func TestDashURejectedWithMessageAndExit1(t *testing.T) {
	xdg := t.TempDir()
	p := startShim(t, xdg, "-u", "-t", "5")
	if ws := p.wait(t, 5*time.Second); !exitedWith(ws, 1) {
		t.Fatalf("-u: %s", describe(ws))
	}
	if !strings.Contains(p.stderr.String(), "-u") {
		t.Errorf("stderr %q should name -u", p.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(xdg, lease.DirName)); err == nil {
		t.Error("a rejected -u created state")
	}
}

// utilityScript traps the three forwarded signals with distinct exit codes.
const utilityScript = `trap 'echo got-TERM; exit 9' TERM; trap 'echo got-HUP; exit 10' HUP; trap 'echo got-INT; exit 11' INT; echo ready; while :; do sleep 0.05; done`

func waitForOutput(t *testing.T, p *proc, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.stdout.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never saw %q on stdout (%q, stderr %q)", want, p.stdout.String(), p.stderr.String())
}

func TestUtilityFormForwardsSignalsAndRemovesLease(t *testing.T) {
	requireDarwin(t)
	cases := map[syscall.Signal]struct {
		name string
		exit int
	}{
		syscall.SIGTERM: {"TERM", 9},
		syscall.SIGHUP:  {"HUP", 10},
		syscall.SIGINT:  {"INT", 11},
	}
	for sig, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdg := t.TempDir()
			p := startShim(t, xdg, "-d", "sh", "-c", utilityScript)
			l := waitForLease(t, xdg, p)
			waitForOutput(t, p, "ready")
			if l.Client == nil || l.Client.PID != p.cmd.Process.Pid || l.ExpiresAt != nil || l.Watch != nil {
				t.Errorf("utility lease = %+v", l)
			}
			if got := strings.Join(l.Flags, ""); got != "d" {
				t.Errorf("flags = %q", got)
			}
			p.signal(t, sig)
			ws := p.wait(t, 10*time.Second)
			if !exitedWith(ws, tc.exit) {
				t.Errorf("%s: %s, want the child's exit %d", tc.name, describe(ws), tc.exit)
			}
			if !strings.Contains(p.stdout.String(), "got-"+tc.name) {
				t.Errorf("child never saw the forwarded signal: %q", p.stdout.String())
			}
			noLeases(t, xdg)
		})
	}
}

func TestUtilityFormPassesExitStatusThrough(t *testing.T) {
	requireDarwin(t)
	for _, code := range []int{0, 1, 7, 42} {
		xdg := t.TempDir()
		p := startShim(t, xdg, "sh", "-c", fmt.Sprintf("exit %d", code))
		if ws := p.wait(t, 10*time.Second); !exitedWith(ws, code) {
			t.Errorf("exit %d: %s", code, describe(ws))
		}
		noLeases(t, xdg)
	}
}

func TestUtilityFormReRaisesAChildSignalDeath(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	p := startShim(t, xdg, "sh", "-c", `kill -TERM $$; sleep 5`)
	if ws := p.wait(t, 10*time.Second); !diedOf(ws, syscall.SIGTERM) {
		t.Errorf("child died of TERM: shim %s", describe(ws))
	}
	noLeases(t, xdg)
}

func TestUtilityFormMissingCommandExits127(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	p := startShim(t, xdg, "-i", "pg-no-such-command-zz")
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 127) {
		t.Fatalf("missing command: %s", describe(ws))
	}
	if want := "pg-no-such-command-zz: No such file or directory\n"; !strings.HasSuffix(p.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to end with %q", p.stderr.String(), want)
	}
	noLeases(t, xdg)
}

func TestUtilityFormIgnoresTimeoutAndWatch(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	// The lease is bound to the command, so -t 1 must not cut it short.
	p := startShim(t, xdg, "-t", "1", "sh", "-c", "sleep 2; echo finished")
	waitForLease(t, xdg, p)
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) || p.stdout.String() != "finished\n" {
		t.Errorf("%s, stdout %q", describe(ws), p.stdout.String())
	}
}

func unwritableXDG(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("mode 0500 does not block root")
	}
	xdg := t.TempDir()
	if err := os.Chmod(xdg, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(xdg, 0o700) })
	return xdg
}

func TestUnwritableStateDirPureFormExits3WithOneLine(t *testing.T) {
	requireDarwin(t)
	xdg := unwritableXDG(t)
	p := startShim(t, xdg, "-t", "30")
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 3) {
		t.Fatalf("unwritable state dir: %s", describe(ws))
	}
	if got := p.stderr.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("stderr = %q, want exactly one line", got)
	}
}

func TestUnwritableStateDirUtilityFormWarnsAndRunsTheUtility(t *testing.T) {
	requireDarwin(t)
	xdg := unwritableXDG(t)
	p := startShim(t, xdg, "sh", "-c", "echo ran; exit 5")
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 5) {
		t.Fatalf("utility under an unwritable state dir: %s", describe(ws))
	}
	if p.stdout.String() != "ran\n" {
		t.Errorf("stdout = %q, the utility did not run", p.stdout.String())
	}
	if !strings.Contains(p.stderr.String(), "warning") {
		t.Errorf("stderr = %q, want a warning", p.stderr.String())
	}
}

func TestShimNeedsNoKeeperAndWritesNoKeeperState(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	p := startShim(t, xdg, "-t", "1")
	waitForLease(t, xdg, p)
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) {
		t.Fatalf("%s", describe(ws))
	}
	for _, name := range []string{lease.FileStatus, lease.FileAssertion, lease.FilePause} {
		if _, err := os.Stat(filepath.Join(xdg, lease.DirName, name)); err == nil {
			t.Errorf("the shim wrote keeper state %s", name)
		}
	}
}
