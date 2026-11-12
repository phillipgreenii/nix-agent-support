//go:build lockorder

package settle

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sasha-s/go-deadlock"
	"go.uber.org/goleak"
)

// Lock-order checks. A build with the `lockorder` tag swaps the settler's run
// lock and the copier lock for go-deadlock's Mutex (internal/lockcheck; the
// production build never sees it). Run them with
//
//	go test -tags lockorder -run TestLockOrder ./internal/settle/
//
// The contract (docs/superpowers/plans/2026-11-10-settle-split.md):
//   - the lock order is the run lock, then the copier lock;
//   - Dirty and LiveChanged MUST NOT take the run lock, because Dirty runs
//     inside the copier lock through SetOnFileChange;
//   - no lock is held so long that a caller queued for it waits past
//     lockHeldLimit (go-deadlock reports a wait that long as a potential
//     deadlock, with the place where the holder took the lock).

// lockHeldLimit is generous: the scenarios below take milliseconds, so a wait
// this long is a real stall and never host load.
const lockHeldLimit = 10 * time.Second

// detector records what go-deadlock reports while a test runs.
type detector struct {
	n       atomic.Int32
	log     bytes.Buffer
	tripped chan struct{} // closed by the first report
	once    sync.Once
}

// detect turns the detector on with lockHeldLimit and makes a report count
// instead of exiting the process. The returned report fails the test when
// anything was reported.
func detect(t *testing.T) (d *detector, report func()) {
	t.Helper()
	d = &detector{tripped: make(chan struct{})}
	prevOpts := deadlock.Opts
	deadlock.Opts.Disable = false
	deadlock.Opts.DisableLockOrderDetection = false
	deadlock.Opts.DeadlockTimeout = lockHeldLimit
	deadlock.Opts.OnPotentialDeadlock = func() {
		d.n.Add(1)
		d.once.Do(func() { close(d.tripped) })
	}
	deadlock.Opts.LogBuf = &d.log
	restored := false
	restore := func() {
		if !restored {
			restored = true
			deadlock.Opts.OnPotentialDeadlock = prevOpts.OnPotentialDeadlock
			deadlock.Opts.LogBuf = prevOpts.LogBuf
			deadlock.Opts.DeadlockTimeout = prevOpts.DeadlockTimeout
		}
	}
	t.Cleanup(restore)
	return d, func() {
		t.Helper()
		restore()
		if n := d.n.Load(); n != 0 {
			t.Fatalf("go-deadlock reported %d potential deadlock(s):\n%s", n, d.log.String())
		}
	}
}

// noLeaks fails the test when it leaves a goroutine behind. It registers its
// cleanup first, so it runs after the env has closed the store.
func noLeaks(t *testing.T) {
	t.Helper()
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() {
		if err := goleak.Find(opt); err != nil {
			t.Errorf("goroutine leak: %v", err)
		}
	})
}

// within runs body on its own goroutine and returns when it finishes. A lock
// that go-deadlock reports (a recursive acquisition or an inverted order can
// block the goroutine for good) fails the test at once instead of hanging it.
func (d *detector) within(t *testing.T, report func(), body func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); body() }()
	select {
	case <-done:
	case <-d.tripped:
		report()
	}
}

// lockScenario drives every operation that takes the run lock, and through it
// the copier lock, on an env in event mode with removal on: ingest (the copier
// lock, then Dirty through SetOnFileChange), Tick, Wake, Remove, Extract,
// Doctor and both Recover calls.
func lockScenario(t *testing.T, e *env) {
	t.Helper()
	e.write(e.transcript(), line(1)+line(2), e.clk.Now().Add(-48*time.Hour), false)
	e.ingest(e.transcript())
	e.sett.Dirty(9999, 0)
	e.sett.LiveChanged()
	e.clk.Advance(25 * time.Hour)
	if _, err := e.sett.Tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatalf("wake: %v", err)
	}
	e.write(e.transcript(), line(3), e.clk.Now(), true)
	e.ingest(e.transcript())
	e.clk.Advance(31 * 24 * time.Hour)
	if _, err := e.sett.Tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := e.sett.Wake(e.clk.Now()); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if _, err := e.sett.Remove(RemoveOptions{}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// A Conversation that is not removed or has its file back is refused by design.
	_, _ = e.sett.Extract(sess, "")
	if _, err := e.sett.Doctor(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if err := e.cp.Recover(); err != nil {
		t.Fatalf("copier recover: %v", err)
	}
	if err := e.sett.Recover(); err != nil {
		t.Fatalf("settler recover: %v", err)
	}
}

func lockEnv(t *testing.T) *env {
	return newEnvWith(t, true, func(e *env) {
		e.removeOn = true
		e.skipInv = true
		e.extra = func(c *Config) { c.Mode = ModeEvent }
	})
}

// The scenario runs while a second goroutine queues for the run lock and the
// copier lock over and over (the way a status call or a wake queues behind a
// pass). Checks (a) and (c): go-deadlock must see only the order run lock then
// copier lock, and no caller may wait on either lock past lockHeldLimit.
func TestLockOrderRunThenCopierAndNoStall(t *testing.T) {
	noLeaks(t)
	d, report := detect(t)
	e := lockEnv(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			unlock, _ := e.sett.lockRunWait("lockorder_probe")
			unlock()
			_ = e.cp.WithLock(func() error { return nil })
			time.Sleep(time.Millisecond)
		}
	}()
	d.within(t, report, func() { lockScenario(t, e) })
	close(stop)
	wg.Wait()
	report()
}

// Check (b): Dirty and LiveChanged MUST NOT take the run lock. With the run
// lock held, Dirty, LiveChanged and an ingest (which calls Dirty inside the
// copier lock through SetOnFileChange) all have to finish.
func TestLockOrderDirtyAndLiveChangedTakeNoRunLock(t *testing.T) {
	noLeaks(t)
	_, report := detect(t)
	e := lockEnv(t)
	e.write(e.transcript(), line(1), e.clk.Now(), false)

	unlock, _ := e.sett.lockRunWait("lockorder_hold")
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()

	for name, fn := range map[string]func(){
		"Dirty":       func() { e.sett.Dirty(9999, 0) },
		"LiveChanged": func() { e.sett.LiveChanged() },
		"ingest":      func() { e.ingest(e.transcript()) },
	} {
		done := make(chan struct{})
		go func() { defer close(done); fn() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			release()
			<-done
			t.Fatalf("%s blocked while the run lock was held: it takes the run lock", name)
		}
	}
	release()
	report()
}
