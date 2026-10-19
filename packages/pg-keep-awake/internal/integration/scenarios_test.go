package integration

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// TestS1KillKeeperNewKeeperAdopts: SIGKILL the keeper; the anchor and
// caffeinate survive and a new keeper adopts them with unchanged pids.
func TestS1KillKeeperNewKeeperAdopts(t *testing.T) {
	e := newEnv(t)
	a := e.runningKeeper()
	e.acquire("t", "a", "120s")
	first := e.waitHeld()

	e.signal(a.pid(), syscall.SIGKILL)
	a.wait(t, 10*time.Second)
	for _, when := range []string{"immediately", "after 1s"} {
		if when == "after 1s" {
			time.Sleep(time.Second)
		}
		if !alive(first.Anchor.PID) || !alive(first.Caffeinate.PID) || !held(first.Caffeinate.PID) {
			t.Fatalf("pair did not survive the keeper's SIGKILL (%s)", when)
		}
	}

	b := e.startKeeper()
	e.waitReady(b)
	got := e.assertion()
	if !samePair(first, got) {
		t.Fatalf("keeper B did not adopt: recorded %+v / %+v, now %+v / %+v",
			first.Anchor, first.Caffeinate, got.Anchor, got.Caffeinate)
	}
	if !held(got.Caffeinate.PID) {
		t.Fatal("adopted caffeinate is no longer held")
	}
	// Nothing but the adopted anchor carries this state directory.
	for _, pid := range anchorsOf(t, e.stateDir) {
		if pid != first.Anchor.PID {
			t.Errorf("a second anchor pid %d carries --state-dir %s", pid, e.stateDir)
		}
	}
}

// anchorsOf lists the pids whose argv is `anchor ... --state-dir dir`.
func anchorsOf(t *testing.T, dir string) []int {
	t.Helper()
	pids, err := lease.ListPIDs()
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, pid := range pids {
		info, err := lease.ProcessInfo(pid)
		if err != nil {
			continue
		}
		if slices.Contains(info.Argv, "anchor") && argvHas(info.Argv, "--state-dir", dir) {
			out = append(out, pid)
		}
	}
	return out
}

// TestS2FlagSetChangeNoGap: a lease with a new flag replaces the pair
// make-before-break.
func TestS2FlagSetChangeNoGap(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("t", "a", "120s", "-i")
	first := e.waitHeld()

	var (
		mu    sync.Mutex
		gaps  int
		stop  = make(chan struct{})
		done  = make(chan struct{})
		seenC = map[int]bool{first.Caffeinate.PID: true}
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Read assertion.json before AND after the pmset snapshot: the
			// keeper writes the new pair before it stops the old one, so a
			// pair the file names by the end of the snapshot is a candidate.
			if a := e.assertionNoLog(); a != nil {
				seenC[a.Caffeinate.PID] = true
			}
			set := heldPIDs()
			if a := e.assertionNoLog(); a != nil {
				seenC[a.Caffeinate.PID] = true
			}
			anyHeld := false
			for pid := range seenC {
				if set[pid] {
					anyHeld = true
					break
				}
			}
			if !anyHeld {
				mu.Lock()
				gaps++
				mu.Unlock()
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	e.acquire("t", "b", "120s", "-s")
	var second *lease.AssertionFile
	waitFor(t, "a new pair and the old pair gone", mustHappen, func() bool {
		second = e.assertion()
		return second != nil && second.Anchor.PID != first.Anchor.PID &&
			!alive(first.Anchor.PID) && !alive(first.Caffeinate.PID)
	})
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if gaps != 0 {
		t.Errorf("%d samples with no held caffeinate during the flag-set change", gaps)
	}
	info, err := lease.ProcessInfo(second.Caffeinate.PID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-i", "-s", "-w", strconv.Itoa(second.Anchor.PID)}
	if !slices.Equal(info.Argv[1:], want) {
		t.Errorf("new caffeinate argv after the executable = %v, want %v", info.Argv[1:], want)
	}
}

// assertionNoLog is assertion() safe for a goroutine (never touches t).
func (e *testEnv) assertionNoLog() *lease.AssertionFile {
	d, err := lease.CheckDir(e.stateDir)
	if err != nil {
		return nil
	}
	a, err := d.ReadAssertion()
	if err != nil {
		return nil
	}
	return a
}

// TestS3KilledCaffeinateRestarted: only the caffeinate is killed.
func TestS3KilledCaffeinateRestarted(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("t", "a", "120s")
	first := e.waitHeld()

	e.signal(first.Caffeinate.PID, syscall.SIGKILL)
	var got *lease.AssertionFile
	waitFor(t, "a different held caffeinate", mustHappen, func() bool {
		got = e.assertion()
		return got != nil && got.Caffeinate.PID != first.Caffeinate.PID && held(got.Caffeinate.PID)
	})
	if got.Anchor.PID != first.Anchor.PID || got.Anchor.StartUS != first.Anchor.StartUS || !alive(got.Anchor.PID) {
		t.Errorf("anchor changed: was %+v, now %+v", first.Anchor, got.Anchor)
	}
}

// TestS4ExpiredLeaseReaped: a 3 s timer lease ends the assertion.
func TestS4ExpiredLeaseReaped(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("e", "k", "3s")
	a := e.waitHeld()
	if !e.leaseExists("e.k") {
		t.Fatal("leases/e.k.json does not exist before expiry")
	}
	waitFor(t, "the expired lease file gone", mustHappen, func() bool { return !e.leaseExists("e.k") })
	waitFor(t, "the caffeinate released", mustHappen, func() bool {
		return !held(a.Caffeinate.PID) && !alive(a.Caffeinate.PID)
	})
	waitFor(t, "LastRelease no_leases", mustHappen, func() bool {
		st := e.status()
		return st != nil && st.LastRelease != nil && st.LastRelease.Reason == lease.ReasonNoLeases
	})
}

// TestS5KilledShimLeaseReaped: a SIGKILLed shim leaves a file that is reaped
// and never held anything.
func TestS5KilledShimLeaseReaped(t *testing.T) {
	e := newEnv(t)
	cmd := exec.Command(shimBin, "-i")
	cmd.Env = e.env()
	shim := e.spawn(cmd)

	glob := filepath.Join(e.stateDir, "leases", "shim.*.json")
	waitFor(t, "exactly one shim lease file", mustHappen, func() bool {
		m, _ := filepath.Glob(glob)
		if len(m) == 0 && !shim.running() {
			t.Fatalf("shim exited early (%v); stderr %q", shim.status, shim.stderr.String())
		}
		return len(m) == 1
	})
	e.signal(shim.pid(), syscall.SIGKILL)
	shim.wait(t, 10*time.Second)
	time.Sleep(time.Second)
	if m, _ := filepath.Glob(glob); len(m) != 1 {
		t.Fatalf("shim lease files after 1s = %v, want the one the dead shim left", m)
	}

	e.runningKeeper()
	waitFor(t, "the dead shim's lease reaped", mustHappen, func() bool {
		m, _ := filepath.Glob(glob)
		return len(m) == 0
	})
	holdsFor(t, "no assertion.json for 2s", 2*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(e.stateDir, lease.FileAssertion))
		return errors.Is(err, fs.ErrNotExist)
	})
}

// TestS6ZombieClientDoesNotHold: a lease bound to a client that dies
// unreaped (a zombie) stops holding.
func TestS6ZombieClientDoesNotHold(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	child := e.spawnSleeper(false)
	pid := child.Process.Pid
	e.mustCLI("acquire", "--owner", "z", "--pid", strconv.Itoa(pid))
	a := e.waitHeld()
	if !e.leaseExists("z.default") {
		t.Fatal("leases/z.default.json does not exist")
	}

	e.signal(pid, syscall.SIGKILL)
	waitFor(t, "the killed client to be a zombie", 5*time.Second, func() bool {
		st := lease.SysProber{}.Probe(pid)
		return st.Exists && st.Zombie
	})
	if st := (lease.SysProber{}).Probe(pid); !st.Exists || !st.Zombie {
		t.Fatalf("client %d is not an unreaped zombie (%+v): the test would prove nothing", pid, st)
	}
	waitFor(t, "the zombie client's lease gone", mustHappen, func() bool { return !e.leaseExists("z.default") })
	waitFor(t, "the caffeinate released", mustHappen, func() bool { return !held(a.Caffeinate.PID) })
}

// TestS7WatchdogFiresWithFrozenHeartbeat: SIGSTOP the keeper.
func TestS7WatchdogFiresWithFrozenHeartbeat(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("3s", "0s", "")
	k := e.runningKeeper()
	e.acquire("w", "default", "120s")
	first := e.waitHeld()

	// Negative control: a 200 ms heartbeat never trips a 3 s watchdog.
	time.Sleep(6 * time.Second)
	if got := e.assertion(); !samePair(first, got) || !held(first.Caffeinate.PID) {
		t.Fatalf("assertion changed or dropped during the control window: %+v", got)
	}

	e.signal(k.pid(), syscall.SIGSTOP)
	waitFor(t, "the anchor and caffeinate to die", mustHappen, func() bool {
		return !alive(first.Anchor.PID) && !alive(first.Caffeinate.PID)
	})
	waitFor(t, "anchor-exit.json with the watchdog reason", 5*time.Second, func() bool {
		x, err := e.dir().ReadAnchorExit()
		return err == nil && x.Reason == lease.ReasonWatchdog
	})
	e.signal(k.pid(), syscall.SIGCONT)
	waitFor(t, "LastRelease watchdog", mustHappen, func() bool {
		st := e.status()
		return st != nil && st.LastRelease != nil && st.LastRelease.Reason == lease.ReasonWatchdog
	})
}

// TestS8BatteryRelease: 19 percent frees the assertion.
func TestS8BatteryRelease(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("b", "default", "120s")
	a := e.waitHeld()

	e.setBattery("19 battery")
	waitFor(t, "released at 19%", mustHappen, func() bool {
		st := e.status()
		return !held(a.Caffeinate.PID) && st != nil && !st.Assertion.Held &&
			st.Battery.Gate == "released" && st.LastRelease != nil &&
			st.LastRelease.Reason == lease.ReasonReleaseThreshold
	})
	holdsFor(t, "released", mustNot, func() bool {
		st := e.status()
		return !held(a.Caffeinate.PID) && st != nil && !st.Assertion.Held
	})
}

// TestS9BlindRule covers the release below hold and no release above it.
func TestS9BlindRule(t *testing.T) {
	t.Run("release at or below hold", func(t *testing.T) {
		e := newEnv(t)
		e.runningKeeper()
		e.acquire("b", "default", "120s")
		first := e.waitHeld()

		e.setBattery("25 battery") // between release 20 and hold 30: kept
		time.Sleep(time.Second)
		if !held(first.Caffeinate.PID) {
			t.Fatal("assertion dropped at 25%, between release and hold")
		}
		e.removeBattery()
		waitFor(t, "blind release", mustHappen, func() bool {
			st := e.status()
			return !held(first.Caffeinate.PID) && st != nil && st.LastRelease != nil &&
				st.LastRelease.Reason == lease.ReasonBlindThreshold
		})
	})
	t.Run("no release above hold", func(t *testing.T) {
		e := newEnv(t)
		e.runningKeeper()
		e.acquire("b", "default", "120s")
		first := e.waitHeld()

		e.setBattery("50 battery")
		time.Sleep(time.Second)
		e.removeBattery()
		holdsFor(t, "the assertion held with unchanged pids", 6*time.Second, func() bool {
			return samePair(first, e.assertion()) && held(first.Caffeinate.PID)
		})
		st := e.status()
		if st == nil || st.Battery.ReadOK || st.Battery.Gate != "unknown" {
			t.Errorf("status battery = %+v, want ReadOK false and gate unknown", st)
		}
	})
}

// TestS10StartupAndShutdownSweeps: the startup sweep keeps the heartbeat, the
// shutdown sweep removes it.
func TestS10StartupAndShutdownSweeps(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("3s", "0s", "")
	e.acquire("s", "old", "1s") // creates the state directory
	time.Sleep(1500 * time.Millisecond)
	if err := os.WriteFile(e.heartbeatPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ino := inodeOf(t, e.heartbeatPath())

	k := e.runningKeeper()
	e.acquire("s", "hold", "120s")
	first := e.waitHeld()
	if got := inodeOf(t, e.heartbeatPath()); got != ino {
		t.Errorf("keeper.heartbeat inode %d != %d: the startup sweep replaced it", got, ino)
	}
	if e.leaseExists("s.old") {
		t.Error("the expired leases/s.old.json was not swept")
	}

	e.signal(k.pid(), syscall.SIGTERM)
	if ws := k.wait(t, 10*time.Second); !ws.Exited() || ws.ExitStatus() != 0 {
		t.Fatalf("keeper exit after SIGTERM = %v, want 0", ws)
	}
	if _, err := os.Stat(e.heartbeatPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("keeper.heartbeat after the shutdown sweep: %v, want removed", err)
	}
	if !e.leaseExists("s.hold") {
		t.Error("the shutdown sweep removed leases/s.hold.json")
	}
	if e.assertionNoLog() == nil {
		t.Error("assertion.json is gone after the keeper's shutdown")
	}
	if !alive(first.Anchor.PID) || !alive(first.Caffeinate.PID) {
		t.Error("the pair did not survive the graceful shutdown")
	}
	waitFor(t, "the anchor to exit by watchdog", mustHappen, func() bool {
		x, err := e.dir().ReadAnchorExit()
		return err == nil && x.Reason == lease.ReasonWatchdog && !alive(first.Anchor.PID)
	})
	gone(t, first.Caffeinate.PID)
}

// TestS11ConcurrentAcquireAndReap: no lease is lost to a reap.
func TestS11ConcurrentAcquireAndReap(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("t", "hold", "120s")
	first := e.waitHeld()

	for n := 1; n <= 3; n++ {
		key := "k" + strconv.Itoa(n)
		id := "r." + key
		d := e.dir()
		var lk *lease.Lock
		waitFor(t, "leases/.lock", mustHappen, func() bool {
			var err error
			lk, err = d.TakeLock()
			if errors.Is(err, lease.ErrLockTimeout) {
				return false
			}
			if err != nil {
				t.Fatal(err)
			}
			return true
		})
		exp := time.Now().Add(time.Second)
		l := &lease.Lease{Owner: "r", Key: key, Kind: lease.KindAcquire, Flags: []string{"i"}, ExpiresAt: &exp}
		if err := lk.Put(l); err != nil {
			t.Fatal(err)
		}
		time.Sleep(4 * time.Second)
		refreshed := time.Now().Add(5 * time.Minute).UTC()
		l.ExpiresAt = &refreshed
		if err := lk.Put(l); err != nil {
			t.Fatal(err)
		}
		if err := lk.Unlock(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		got, err := e.dir().ReadLease(id)
		if err != nil {
			t.Fatalf("cycle %d: %s is gone after the unlock: %v", n, id, err)
		}
		if got.ExpiresAt == nil || !got.ExpiresAt.Equal(refreshed) {
			t.Fatalf("cycle %d: expires_at = %v, want the refreshed %v", n, got.ExpiresAt, refreshed)
		}
		if a := e.assertion(); !samePair(first, a) || !held(first.Caffeinate.PID) {
			t.Fatalf("cycle %d: the assertion changed or dropped", n)
		}
	}

	// Smoke phase: 8 clients write while 20 expired leases are reaped.
	for i := 0; i < 20; i++ {
		e.acquire("x", "e"+strconv.Itoa(i), "1s")
	}
	time.Sleep(800 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				code, _, stderr := e.cliNoFatal("acquire", "--owner", "c", "--key", "k"+strconv.Itoa(i), "--for", "60s")
				if code != 0 {
					errs <- "acquire c.k" + strconv.Itoa(i) + " exited " + strconv.Itoa(code) + ": " + stderr
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	for i := 0; i < 8; i++ {
		if !e.leaseExists("c.k" + strconv.Itoa(i)) {
			t.Errorf("leases/c.k%d.json was lost", i)
		}
	}
	waitFor(t, "the 20 expired leases reaped", mustHappen, func() bool {
		for i := 0; i < 20; i++ {
			if e.leaseExists("x.e" + strconv.Itoa(i)) {
				return false
			}
		}
		return true
	})
}

// cliNoFatal is cli safe for a goroutine: it reports through the exit code.
func (e *testEnv) cliNoFatal(args ...string) (int, string, string) {
	cmd := exec.Command(keepAwakeBin, args...)
	cmd.Env = e.env()
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), out.String(), errb.String()
		}
		return -1, out.String(), err.Error()
	}
	return 0, out.String(), errb.String()
}

// TestS12StoppedClientHoldingLock: a stopped process holding leases/.lock
// delays the tick by at most the lock bound and the heartbeat still advances.
func TestS12StoppedClientHoldingLock(t *testing.T) {
	e := newEnv(t)
	e.runningKeeper()
	e.acquire("t", "hold", "120s")
	e.waitHeld()

	helper := exec.Command(os.Args[0])
	helper.Env = e.env(envHelper+"=holdlock", envHelperDir+"="+filepath.Join(e.stateDir, "leases"))
	h := e.spawn(helper)
	waitFor(t, "the helper to hold the lock", mustHappen, func() bool {
		return strings.Contains(h.stdout.String(), "locked")
	})
	e.signal(h.pid(), syscall.SIGSTOP) // a stopped process keeps its flock

	type result struct {
		code   int
		stderr string
		took   time.Duration
	}
	res := make(chan result, 1)
	go func() {
		start := time.Now()
		code, _, stderr := e.cliNoFatal("acquire", "--owner", "t", "--key", "x", "--for", "5s")
		res <- result{code, stderr, time.Since(start)}
	}()

	var (
		last   time.Time
		lastAt time.Time
		values int
		maxGap time.Duration
	)
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		if fi, err := os.Stat(e.heartbeatPath()); err == nil && !fi.ModTime().Equal(last) {
			now := time.Now()
			if !lastAt.IsZero() && now.Sub(lastAt) > maxGap {
				maxGap = now.Sub(lastAt)
			}
			last, lastAt = fi.ModTime(), now
			values++
		}
		time.Sleep(50 * time.Millisecond)
	}
	if values < 3 {
		t.Errorf("heartbeat mtime took %d distinct values in 10 s, want at least 3", values)
	}
	if maxGap > 4*time.Second {
		t.Errorf("largest gap between heartbeat updates = %v, want at most 4s", maxGap)
	}
	r := <-res
	if r.code != 3 || r.took > 4*time.Second || !strings.Contains(r.stderr, "leases/.lock") {
		t.Errorf("acquire under a held lock: exit %d after %v, stderr %q; want exit 3 within 4s naming leases/.lock",
			r.code, r.took, r.stderr)
	}

	e.signal(h.pid(), syscall.SIGKILL) // works on a stopped process
	h.wait(t, 10*time.Second)
	e.acquire("t", "later", "5s")
}

// TestS13FifoAndSymlinkIgnored: neither a FIFO nor a symlink in leases/ is
// read, followed or removed.
func TestS13FifoAndSymlinkIgnored(t *testing.T) {
	heartbeatAdvances := func(t *testing.T, e *testEnv, check func()) {
		t.Helper()
		var last time.Time
		advances := 0
		end := time.Now().Add(4 * time.Second)
		for time.Now().Before(end) {
			if fi, err := os.Stat(e.heartbeatPath()); err == nil && !fi.ModTime().Equal(last) {
				if !last.IsZero() {
					advances++
				}
				last = fi.ModTime()
			}
			if check != nil {
				check()
			}
			time.Sleep(50 * time.Millisecond)
		}
		if advances < 5 {
			t.Errorf("keeper.heartbeat advanced %d times in 4 s, want at least 5", advances)
		}
	}
	noOwnerX := func(t *testing.T, e *testEnv) {
		t.Helper()
		if st := e.status(); st != nil {
			for _, l := range st.Leases {
				if l.Owner == "x" {
					t.Errorf("status.json lists lease %+v with owner x", l)
				}
			}
		}
	}

	t.Run("fifo", func(t *testing.T) {
		e := newEnv(t)
		e.runningKeeper()
		e.acquire("t", "hold", "120s")
		e.waitHeld()
		fifo := filepath.Join(e.stateDir, "leases", "x.json")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		heartbeatAdvances(t, e, nil)
		fi, err := os.Lstat(fifo)
		if err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			t.Errorf("x.json is no longer a FIFO: %v, %v", fi, err)
		}
		noOwnerX(t, e)
	})

	t.Run("symlink", func(t *testing.T) {
		e := newEnv(t)
		e.runningKeeper() // no other lease: the assertion must never appear
		sleeper := e.spawnSleeper(true)
		ref, err := lease.Ref(sleeper.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		l := &lease.Lease{Owner: "x", Key: "k", Kind: lease.KindAcquire, Flags: []string{"i"}, Client: &ref}
		if err := l.Validate(); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		secret := filepath.Join(t.TempDir(), "secret.json")
		if err := os.WriteFile(secret, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(secret)
		link := filepath.Join(e.stateDir, "leases", "x.k.json")
		if err := os.Symlink(secret, link); err != nil {
			t.Fatal(err)
		}
		heartbeatAdvances(t, e, func() {
			if _, err := os.Stat(filepath.Join(e.stateDir, lease.FileAssertion)); err == nil {
				t.Error("assertion.json appeared: the keeper followed the symlink")
			}
		})
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("x.k.json is no longer a symlink: %v, %v", fi, err)
		}
		after, err := os.Stat(secret)
		if err != nil || !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("secret.json mtime changed: %v, %v", after, err)
		}
		if got, _ := os.ReadFile(secret); string(got) != string(append(data, '\n')) {
			t.Error("secret.json content changed")
		}
		noOwnerX(t, e)
	})
}

// TestS14ConfigChangeThenAdoption: a changed anchor parameter replaces the
// pair; a parameter the pair does not record is adopted.
func TestS14ConfigChangeThenAdoption(t *testing.T) {
	restart := func(t *testing.T, e *testEnv) (*lease.AssertionFile, *keeperProc) {
		t.Helper()
		a := e.runningKeeper()
		e.acquire("t", "a", "120s")
		first := e.waitHeld()
		e.signal(a.pid(), syscall.SIGTERM)
		a.wait(t, 10*time.Second)
		return first, a
	}

	t.Run("params change", func(t *testing.T) {
		e := newEnv(t)
		first, _ := restart(t, e)
		e.writeConfig("45s", "0s", "")
		e.startKeeper()
		var got *lease.AssertionFile
		waitFor(t, "a new pair with watchdog 45", mustHappen, func() bool {
			got = e.assertion()
			return got != nil && got.Anchor.PID != first.Anchor.PID &&
				got.Params.WatchdogSeconds == 45 && held(got.Caffeinate.PID)
		})
		gone(t, first.Anchor.PID)
		gone(t, first.Caffeinate.PID)
	})

	t.Run("no param change", func(t *testing.T) {
		e := newEnv(t)
		first, _ := restart(t, e)
		e.writeConfig("30s", "1s", "")
		b := e.startKeeper()
		e.waitReady(b)
		got := e.assertion()
		if !samePair(first, got) {
			t.Fatalf("keeper B did not adopt: recorded %+v / %+v, now %+v / %+v",
				first.Anchor, first.Caffeinate, got.Anchor, got.Caffeinate)
		}
		if !held(got.Caffeinate.PID) || !alive(got.Anchor.PID) {
			t.Error("the adopted pair is not held")
		}
	})
}

// TestS15RunExitStatusPassthrough: run passes the command's status through.
func TestS15RunExitStatusPassthrough(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		name string
		argv []string
		want int
	}{
		{"exit 7", []string{"/bin/sh", "-c", "exit 7"}, 7},
		{"not found", []string{"/nonexistent-pgka-cmd"}, 127},
		{"signal", []string{"/bin/sh", "-c", "kill -TERM $$"}, 143},
	} {
		args := append([]string{"run", "--owner", "t", "--"}, c.argv...)
		code, _, stderr := e.cli(args...)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", c.name, code, c.want, stderr)
		}
		if m, _ := filepath.Glob(filepath.Join(e.stateDir, "leases", "t.*.json")); len(m) != 0 {
			t.Errorf("%s: lease files left behind: %v", c.name, m)
		}
	}
}

// TestS16ForeignAnchorNeverTouched: an anchor with another state directory
// survives the keeper's start scan and is not adopted.
func TestS16ForeignAnchorNeverTouched(t *testing.T) {
	e := newEnv(t)
	f := t.TempDir()
	if err := os.Chmod(f, 0o700); err != nil {
		t.Fatal(err)
	}
	hb := filepath.Join(f, "hb")
	if err := os.WriteFile(hb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(keepAwakeBin, "anchor",
		"--release-pct", "0", "--blind-pct", "0", "--watchdog", "120s",
		"--heartbeat", hb, "--interval", "1s", "--state-dir", f)
	cmd.Env = e.env()
	foreign := e.spawn(cmd)
	waitFor(t, "the foreign anchor's argv", 5*time.Second, func() bool {
		info, err := lease.ProcessInfo(foreign.pid())
		return err == nil && argvHas(info.Argv, "--state-dir", f)
	})

	e.runningKeeper()
	e.acquire("t", "hold", "120s")
	a := e.waitHeld()
	if !foreign.running() || !alive(foreign.pid()) {
		t.Fatalf("the foreign anchor %d died (%v)", foreign.pid(), foreign.status)
	}
	if a.Anchor.PID == foreign.pid() || a.Caffeinate.PID == foreign.pid() {
		t.Fatalf("assertion.json adopted the foreign anchor %d", foreign.pid())
	}
}
