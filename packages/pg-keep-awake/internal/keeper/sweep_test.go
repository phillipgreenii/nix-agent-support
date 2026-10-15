package keeper

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

var epoch = time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return epoch }

// sweeperWith builds a Sweeper over d with a fixed clock, a table prober and a
// recording signaller that never touches a real process.
func sweeperWith(t *testing.T, d *lease.Dir, p lease.Prober, names map[int]string) (*Sweeper, *recorder) {
	t.Helper()
	rec := &recorder{t: t}
	return NewSweeper(d, Options{
		Now: fixedNow, Prober: p, Info: infoNames(names), Signal: rec.signal, Self: os.Getpid(),
	}), rec
}

func runLease(key string, pid int, start int64) *lease.Lease {
	return &lease.Lease{Owner: "o", Key: key, Kind: lease.KindRun, Client: &lease.ProcRef{PID: pid, StartUS: start}, Flags: []string{"i"}}
}

func TestExpiredLeaseIsReaped(t *testing.T) {
	d := newDir(t)
	past, future := epoch.Add(-time.Second), epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "old", Kind: lease.KindAcquire, ExpiresAt: &past})
	put(t, d, &lease.Lease{Owner: "o", Key: "edge", Kind: lease.KindAcquire, ExpiresAt: tp(epoch)}) // not in the future
	put(t, d, &lease.Lease{Owner: "o", Key: "new", Kind: lease.KindAcquire, ExpiresAt: &future})

	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapExpired] != 2 {
		t.Errorf("Reaped = %v, want 2 expired", res.Reaped)
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.new"}) {
		t.Errorf("live = %v", got)
	}
	if exists(t, leaseFile(d, "o.old")) || exists(t, leaseFile(d, "o.edge")) || !exists(t, leaseFile(d, "o.new")) {
		t.Error("expired files must be gone and the future one kept")
	}
}

func TestClientDeadAndRecycledAndZombieAreReaped(t *testing.T) {
	d := newDir(t)
	put(t, d, runLease("gone", 100, 1))     // pid does not exist
	put(t, d, runLease("recycled", 101, 5)) // exists, but a different start_us
	put(t, d, runLease("zombie", 102, 7))
	put(t, d, runLease("ok", 103, 9))
	p := fakeProber{
		101: alive(6),
		102: {Exists: true, StartUS: 7, Zombie: true},
		103: alive(9),
	}
	s, _ := sweeperWith(t, d, p, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapClientDead] != 3 {
		t.Errorf("Reaped = %v, want 3 client_dead", res.Reaped)
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.ok"}) {
		t.Errorf("live = %v", got)
	}
}

func TestWatchedDeadIsReaped(t *testing.T) {
	d := newDir(t)
	future := epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "w1", Kind: lease.KindAcquire, ExpiresAt: &future, Watch: &lease.ProcRef{PID: 200, StartUS: 3}})
	put(t, d, &lease.Lease{Owner: "o", Key: "w2", Kind: lease.KindAcquire, ExpiresAt: &future, Watch: &lease.ProcRef{PID: 201, StartUS: 3}})
	put(t, d, &lease.Lease{Owner: "o", Key: "w3", Kind: lease.KindAcquire, Watch: &lease.ProcRef{PID: 202, StartUS: 3}})
	s, _ := sweeperWith(t, d, fakeProber{201: alive(4) /* recycled */, 202: alive(3)}, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapWatchedDead] != 2 {
		t.Errorf("Reaped = %v, want 2 watched_dead", res.Reaped)
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.w3"}) {
		t.Errorf("live = %v", got)
	}
}

func TestResultCarriesEveryReasonKeyAndLiveFields(t *testing.T) {
	d := newDir(t)
	future := epoch.Add(time.Hour)
	l := &lease.Lease{
		Owner: "o", Key: "full", Kind: lease.KindRun, Client: &lease.ProcRef{PID: 5, StartUS: 6},
		Watch: &lease.ProcRef{PID: 7, StartUS: 8}, ExpiresAt: &future, Flags: []string{"d", "s"},
	}
	put(t, d, l)
	s, _ := sweeperWith(t, d, fakeProber{5: alive(6), 7: alive(8)}, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ReapReasons {
		if _, ok := res.Reaped[k]; !ok {
			t.Errorf("Reaped lacks key %q", k)
		}
	}
	for _, k := range RejectReasons {
		if _, ok := res.Rejected[k]; !ok {
			t.Errorf("Rejected lacks key %q", k)
		}
	}
	if len(res.Live) != 1 {
		t.Fatalf("live = %v", res.Live)
	}
	got := res.Live[0]
	if got.ID != "o.full" || got.Owner != "o" || got.Key != "full" || got.Kind != lease.KindRun ||
		!slices.Equal(got.Flags, []string{"d", "s"}) || got.ExpiresAt == nil || !got.ExpiresAt.Equal(future) ||
		got.Client == nil || got.Client.PID != 5 || got.Watch == nil || got.Watch.PID != 7 {
		t.Errorf("live entry = %+v", got)
	}
	if res.LockSkipped || res.HeartbeatRemoved || len(res.Signalled) != 0 {
		t.Errorf("unexpected result flags: %+v", res)
	}
}

// The reaper's classification and lease.Live must agree on what is live.
func TestClassifyAgreesWithLeaseLive(t *testing.T) {
	p := fakeProber{1000: alive(1), 1001: {Exists: true, StartUS: 1, Zombie: true}, 1002: alive(2)}
	s := NewSweeper(newDir(t), Options{Now: fixedNow, Prober: p})
	past, future := epoch.Add(-time.Minute), epoch.Add(time.Minute)
	c := func(pid int, st int64) *lease.ProcRef { return &lease.ProcRef{PID: pid, StartUS: st} }
	cases := []*lease.Lease{
		{Client: c(1000, 1)},
		{Client: c(1000, 2)},
		{Client: c(1001, 1)},
		{Client: c(9, 1)},
		{ExpiresAt: &past},
		{ExpiresAt: &future},
		{Watch: c(1002, 2)},
		{Watch: c(1002, 3)},
		{Watch: c(9, 9)},
		{Client: c(1000, 1), ExpiresAt: &future, Watch: c(1002, 2)},
		{Client: c(1000, 1), ExpiresAt: &past, Watch: c(1002, 2)},
		{Client: c(1000, 1), ExpiresAt: &future, Watch: c(9, 2)},
	}
	for i, l := range cases {
		l.Kind, l.Owner, l.Key = lease.KindAcquire, "o", fmt.Sprint(i)
		if live, cls := l.Live(epoch, p), s.classify(l, epoch); live != (cls == "") {
			t.Errorf("case %d: Live=%v classify=%q", i, live, cls)
		}
	}
}

func TestStaleTmpRemovedFreshKeptAndJunkKeptUntilOld(t *testing.T) {
	d := newDir(t)
	now := time.Now()
	s := NewSweeper(d, Options{Prober: fakeProber{}}) // real clock: mtimes are real
	dir := d.LeasesPath()
	oldTmp, freshTmp := filepath.Join(dir, ".o.k.1.aaa.tmp"), filepath.Join(dir, ".o.k.2.bbb.tmp")
	junkOld, junkNew := filepath.Join(dir, "old.json"), filepath.Join(dir, "new.json")
	for _, p := range []string{oldTmp, freshTmp, junkOld, junkNew} {
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{oldTmp, junkOld} {
		if err := os.Chtimes(p, now.Add(-2*time.Minute), now.Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, oldTmp) || exists(t, junkOld) {
		t.Error("a *.tmp or unparsable *.json older than 60s must be removed")
	}
	if !exists(t, freshTmp) || !exists(t, junkNew) {
		t.Error("a young *.tmp or unparsable *.json must be kept")
	}
	if res.JunkRemoved != 2 {
		t.Errorf("JunkRemoved = %d, want 2", res.JunkRemoved)
	}
	if res.Rejected[RejectUnreadable] != 2 {
		t.Errorf("Rejected = %v, want 2 unreadable", res.Rejected)
	}
}

func TestFIFOAndSymlinkNamedJSONAreSkippedAndCounted(t *testing.T) {
	d := newDir(t)
	dir := d.LeasesPath()
	if err := syscall.Mkfifo(filepath.Join(dir, "x.json"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "y.json")); err != nil {
		t.Fatal(err)
	}
	future := epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "k", Kind: lease.KindAcquire, ExpiresAt: &future})

	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	done := make(chan struct{})
	var res *Result
	var err error
	go func() { res, err = s.Sweep(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep blocked on a FIFO")
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected[RejectUnreadable] != 2 {
		t.Errorf("Rejected = %v, want the FIFO and the symlink counted as unreadable", res.Rejected)
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.k"}) {
		t.Errorf("live = %v", got)
	}
	for _, n := range []string{"x.json", "y.json"} {
		if !exists(t, filepath.Join(dir, n)) {
			t.Errorf("%s was deleted; only a stale unparsable regular file may be", n)
		}
	}
	if !exists(t, secret) {
		t.Error("the symlink target must be untouched")
	}
}

func TestTooLargeAndOverCapAreCounted(t *testing.T) {
	d := newDir(t)
	future := epoch.Add(time.Hour)
	big := make([]byte, lease.MaxLeaseFileBytes+1)
	if err := os.WriteFile(filepath.Join(d.LeasesPath(), "a.big.json"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	// 1024 files are considered per scan; "a.big.json" sorts first and is one
	// of them, so 1023 valid leases fill the cap and 3 more are over it.
	const valid = lease.MaxLeaseFiles - 1
	for i := 0; i < valid+3; i++ {
		key := fmt.Sprintf("k%04d", i)
		writeRaw(t, d, "o."+key+".json", &lease.Lease{SchemaVersion: 1, ID: "o." + key, Owner: "o", Key: key, Kind: lease.KindAcquire, ExpiresAt: &future, CreatedAt: epoch})
	}
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected[RejectTooLarge] != 1 || res.Rejected[RejectOverCap] != 3 || res.Rejected[RejectUnreadable] != 0 {
		t.Errorf("Rejected = %v, want too_large=1 over_cap=3 unreadable=0", res.Rejected)
	}
	if len(res.Live) != valid {
		t.Errorf("live = %d, want %d", len(res.Live), valid)
	}
	if !exists(t, filepath.Join(d.LeasesPath(), "a.big.json")) {
		t.Error("an oversized file must not be deleted")
	}
}

func TestFutureSchemaIsUnreadableAndKept(t *testing.T) {
	d := newDir(t)
	writeRaw(t, d, "o.k.json", map[string]any{"schema_version": 99})
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected[RejectUnreadable] != 1 || !exists(t, leaseFile(d, "o.k")) {
		t.Errorf("Rejected = %v, file kept = %v", res.Rejected, exists(t, leaseFile(d, "o.k")))
	}
}

// A lease refreshed after the first read and before the unlink must survive:
// the reaper re-reads under the lock and unlinks only what is still dead.
func TestLeaseRefreshedBetweenReadAndUnlinkSurvives(t *testing.T) {
	d := newDir(t)
	past := epoch.Add(-time.Minute)
	put(t, d, &lease.Lease{Owner: "o", Key: "k", Kind: lease.KindAcquire, ExpiresAt: &past})
	put(t, d, &lease.Lease{Owner: "o", Key: "dead", Kind: lease.KindAcquire, ExpiresAt: &past})

	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	s.afterFirstRead = func() {
		// The client refreshes its lease through the real writer, as a
		// concurrent `acquire --for` would.
		put(t, d, &lease.Lease{Owner: "o", Key: "k", Kind: lease.KindAcquire, ExpiresAt: tp(epoch.Add(time.Hour))})
	}
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, leaseFile(d, "o.k")) {
		t.Fatal("the refreshed lease was unlinked")
	}
	if exists(t, leaseFile(d, "o.dead")) {
		t.Error("the still-dead lease must be reaped")
	}
	if res.Reaped[ReapExpired] != 1 || !slices.Equal(liveIDs(res), []string{"o.k"}) {
		t.Errorf("Reaped = %v live = %v", res.Reaped, liveIDs(res))
	}
	got, err := d.ReadLease("o.k")
	if err != nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(epoch.Add(time.Hour)) {
		t.Errorf("surviving lease = %+v, %v", got, err)
	}
}

// A lease removed by its owner between the first read and the lock is not an
// error and not counted as reaped.
func TestLeaseRemovedBetweenReadAndLockIsNotCounted(t *testing.T) {
	d := newDir(t)
	past := epoch.Add(-time.Minute)
	put(t, d, &lease.Lease{Owner: "o", Key: "k", Kind: lease.KindAcquire, ExpiresAt: &past})
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	s.afterFirstRead = func() {
		if err := d.RemoveLease("o.k"); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapExpired] != 0 || len(res.Live) != 0 {
		t.Errorf("Reaped = %v live = %v", res.Reaped, res.Live)
	}
}

// Writers acquiring leases while the reaper runs lose nothing.
func TestConcurrentAcquireAndReapLosesNoLease(t *testing.T) {
	d := newDir(t)
	past := epoch.Add(-time.Minute)
	for i := 0; i < 20; i++ {
		put(t, d, &lease.Lease{Owner: "dead", Key: fmt.Sprintf("d%02d", i), Kind: lease.KindAcquire, ExpiresAt: &past})
	}
	const writers, perWriter = 4, 25
	var wg sync.WaitGroup
	stop := make(chan struct{})
	reaperDone := make(chan struct{})
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	go func() {
		defer close(reaperDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.Sweep(); err != nil {
				t.Errorf("sweep: %v", err)
				return
			}
		}
	}()
	future := epoch.Add(time.Hour)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				l := &lease.Lease{Owner: fmt.Sprintf("w%d", w), Key: fmt.Sprintf("k%02d", i), Kind: lease.KindAcquire, ExpiresAt: &future}
				if err := d.PutLease(l); err != nil {
					t.Errorf("PutLease: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-reaperDone
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Live) != writers*perWriter {
		t.Errorf("live = %d, want %d: a lease was lost", len(res.Live), writers*perWriter)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			if id := fmt.Sprintf("w%d.k%02d", w, i); !exists(t, leaseFile(d, id)) {
				t.Errorf("lease %s lost", id)
			}
		}
	}
	for i := 0; i < 20; i++ {
		if id := fmt.Sprintf("dead.d%02d", i); exists(t, leaseFile(d, id)) {
			t.Errorf("dead lease %s survived", id)
		}
	}
}

// Holding leases/.lock makes the sweep give up within the 2 second bound, and
// say so, so the caller still touches its heartbeat.
func TestSweepSkippedWhenLockCannotBeTaken(t *testing.T) {
	d := newDir(t)
	past, future := epoch.Add(-time.Minute), epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "dead", Kind: lease.KindAcquire, ExpiresAt: &past})
	put(t, d, &lease.Lease{Owner: "o", Key: "ok", Kind: lease.KindAcquire, ExpiresAt: &future})
	held, err := d.TakeLock() // a second open file description conflicts even in-process
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()

	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	start := time.Now()
	res, err := s.Sweep()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("a skipped sweep is not an error: %v", err)
	}
	if !res.LockSkipped {
		t.Error("LockSkipped = false")
	}
	if took < time.Second || took > 4*time.Second {
		t.Errorf("sweep took %v, want about the 2s lock bound", took)
	}
	if !exists(t, leaseFile(d, "o.dead")) {
		t.Error("nothing may be reaped while the lock is out of reach")
	}
	if got := liveIDs(res); !slices.Equal(got, []string{"o.ok"}) {
		t.Errorf("live = %v: a skipped tick still reports the live leases it read", got)
	}
	if n := res.Reaped[ReapExpired]; n != 0 {
		t.Errorf("Reaped = %v", res.Reaped)
	}
}

func TestStartupKeepsHeartbeatShutdownRemovesIt(t *testing.T) {
	d := newDir(t)
	hb := filepath.Join(d.Path(), FileHeartbeat)
	asrt := filepath.Join(d.Path(), lease.FileAssertion)
	future := epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "o", Key: "ok", Kind: lease.KindAcquire, ExpiresAt: &future})
	if err := os.WriteFile(hb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteAssertion(&lease.AssertionFile{Anchor: lease.ProcDesc{PID: 4242, StartUS: 1}, Caffeinate: lease.ProcDesc{PID: 4243, StartUS: 1}}); err != nil {
		t.Fatal(err)
	}
	s, rec := sweeperWith(t, d, fakeProber{}, nil)

	res, err := s.Startup()
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, hb) || res.HeartbeatRemoved {
		t.Error("the startup sweep must keep keeper.heartbeat")
	}
	res, err = s.Shutdown()
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, hb) || !res.HeartbeatRemoved {
		t.Error("the shutdown sweep must remove keeper.heartbeat")
	}
	if !exists(t, leaseFile(d, "o.ok")) || !exists(t, asrt) {
		t.Error("both sweeps keep valid leases and assertion.json")
	}
	if got := rec.got(); len(got) != 0 {
		t.Errorf("sweeps signalled %v; the assertion's processes must be left alone", got)
	}
	// Idempotent: a second shutdown with the heartbeat already gone is fine.
	if _, err := s.Shutdown(); err != nil {
		t.Errorf("second shutdown: %v", err)
	}
}

// The shutdown sweep still removes the heartbeat when the lock is out of reach.
func TestShutdownRemovesHeartbeatEvenWhenSweepSkipped(t *testing.T) {
	d := newDir(t)
	hb := filepath.Join(d.Path(), FileHeartbeat)
	if err := os.WriteFile(hb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := d.TakeLock()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Shutdown()
	if err != nil || !res.LockSkipped || exists(t, hb) {
		t.Errorf("res = %+v err = %v heartbeat present = %v", res, err, exists(t, hb))
	}
}

func TestMissingLeasesDirectoryIsNotAnError(t *testing.T) {
	d := newDir(t)
	if err := os.RemoveAll(d.LeasesPath()); err != nil {
		t.Fatal(err)
	}
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Sweep()
	if err != nil || len(res.Live) != 0 {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

func TestDefaultOrphanRule(t *testing.T) {
	future := epoch.Add(time.Hour)
	cases := []struct {
		name string
		l    *lease.Lease
		want bool
	}{
		{"shim without client", &lease.Lease{Kind: lease.KindShim, ExpiresAt: &future}, true},
		{"run without client", &lease.Lease{Kind: lease.KindRun, ExpiresAt: &future}, true},
		{"acquire timer", &lease.Lease{Kind: lease.KindAcquire, ExpiresAt: &future}, false},
		{"run with client", &lease.Lease{Kind: lease.KindRun, Client: &lease.ProcRef{PID: 5, StartUS: 1}}, false},
	}
	for _, c := range cases {
		if got := DefaultOrphan(c.l); got != c.want {
			t.Errorf("%s: DefaultOrphan = %v, want %v", c.name, got, c.want)
		}
	}
	d := newDir(t)
	future2 := epoch.Add(time.Hour)
	put(t, d, &lease.Lease{Owner: "shim", Key: "1-1", Kind: lease.KindShim, ExpiresAt: &future2})
	s, _ := sweeperWith(t, d, fakeProber{}, nil)
	res, err := s.Sweep()
	if err != nil || res.Reaped[ReapOrphan] != 1 || len(res.Live) != 0 {
		t.Errorf("res = %+v err = %v", res, err)
	}
}

func TestLockBoundIsTwoSeconds(t *testing.T) {
	if lease.LockTimeout != 2*time.Second {
		t.Fatalf("LockTimeout = %v; the keeper's skip bound is 2 seconds", lease.LockTimeout)
	}
}
