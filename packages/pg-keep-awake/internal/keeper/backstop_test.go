package keeper

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// backstopFixture is one candidate for the backstop: a lease whose client is
// pid, alive with start_us 10 and executable exe.
type backstopFixture struct {
	name   string
	kind   string
	reason ReapReason
	pid    int
	start  int64 // start_us recorded in the lease
	proc   lease.ProcState
	exe    string
	want   bool
}

func (f backstopFixture) lease() *lease.Lease {
	past := epoch.Add(-time.Minute)
	return &lease.Lease{
		Owner: "o", Key: f.name, Kind: f.kind, ExpiresAt: &past,
		Client: &lease.ProcRef{PID: f.pid, StartUS: f.start},
	}
}

func TestBackstopSignalsOnlyVerifiedShimAndRunClients(t *testing.T) {
	cases := []backstopFixture{
		{"shim-expired", lease.KindShim, ReapExpired, 500, 10, alive(10), "pg-caffeinate-shim", true},
		{"run-expired", lease.KindRun, ReapExpired, 501, 10, alive(10), "pg-keep-awake", true},
		{"run-orphan", lease.KindRun, ReapOrphan, 502, 10, alive(10), "pg-keep-awake", true},
		{"acquire-expired", lease.KindAcquire, ReapExpired, 503, 10, alive(10), "pg-keep-awake", false},
		{"acquire-orphan", lease.KindAcquire, ReapOrphan, 504, 10, alive(10), "pg-caffeinate-shim", false},
		{"client-dead-reason", lease.KindShim, ReapClientDead, 505, 10, alive(10), "pg-caffeinate-shim", false},
		{"watched-dead-reason", lease.KindRun, ReapWatchedDead, 506, 10, alive(10), "pg-keep-awake", false},
		{"start-mismatch", lease.KindShim, ReapExpired, 507, 10, alive(11), "pg-caffeinate-shim", false},
		{"gone", lease.KindShim, ReapExpired, 508, 10, lease.ProcState{}, "pg-caffeinate-shim", false},
		{"zombie", lease.KindShim, ReapExpired, 509, 10, lease.ProcState{Exists: true, StartUS: 10, Zombie: true}, "pg-caffeinate-shim", false},
		{"other-exe", lease.KindRun, ReapExpired, 510, 10, alive(10), "pa-monitor", false},
		{"caffeinate-itself", lease.KindRun, ReapExpired, 511, 10, alive(10), "caffeinate", false},
		{"pid-1", lease.KindShim, ReapExpired, 1, 10, alive(10), "pg-caffeinate-shim", false},
		{"own-pid", lease.KindShim, ReapExpired, os.Getpid(), 10, alive(10), "pg-caffeinate-shim", false},
	}
	prober, names := fakeProber{}, map[int]string{}
	var reaped []ReapedLease
	for _, c := range cases {
		prober[c.pid], names[c.pid] = c.proc, c.exe
		reaped = append(reaped, ReapedLease{Lease: c.lease(), Reason: c.reason})
	}
	// An acquire lease whose pid is pa-monitor itself: never signalled.
	prober[600], names[600] = alive(10), "pa-monitor"
	past := epoch.Add(-time.Minute)
	reaped = append(reaped, ReapedLease{Reason: ReapExpired, Lease: &lease.Lease{Owner: "pa", Key: "k", Kind: lease.KindAcquire, ExpiresAt: &past, Client: &lease.ProcRef{PID: 600, StartUS: 10}}})
	// A shim lease without a client has nobody to signal.
	reaped = append(reaped, ReapedLease{Reason: ReapOrphan, Lease: &lease.Lease{Owner: "shim", Key: "1-1", Kind: lease.KindShim, ExpiresAt: &past}})
	reaped = append(reaped, ReapedLease{Reason: ReapExpired}) // nil lease must not panic

	s, rec := sweeperWith(t, newDir(t), prober, names)
	got := s.Backstop(reaped)

	var want []int
	for _, c := range cases {
		if c.want {
			want = append(want, c.pid)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("Backstop returned %v, want %v", got, want)
	}
	if !slices.Equal(rec.got(), want) {
		t.Errorf("signalled %v, want %v", rec.got(), want)
	}
}

func TestBackstopSignalsEachPidOnce(t *testing.T) {
	past := epoch.Add(-time.Minute)
	mk := func(key string) ReapedLease {
		return ReapedLease{Reason: ReapExpired, Lease: &lease.Lease{Owner: "o", Key: key, Kind: lease.KindRun, ExpiresAt: &past, Client: &lease.ProcRef{PID: 700, StartUS: 3}}}
	}
	s, rec := sweeperWith(t, newDir(t), fakeProber{700: alive(3)}, map[int]string{700: "pg-keep-awake"})
	if got := s.Backstop([]ReapedLease{mk("a"), mk("b")}); !slices.Equal(got, []int{700}) {
		t.Errorf("Backstop = %v", got)
	}
	if len(rec.got()) != 1 {
		t.Errorf("signals = %v", rec.got())
	}
}

// Through the sweep: an expired run lease whose client is still alive and is a
// pg-keep-awake is reaped as expired and its client signalled; a dead client
// is reaped without a signal; an acquire lease's pid is never signalled.
func TestSweepRunsTheBackstopForExpiredAndOrphanLeases(t *testing.T) {
	d := newDir(t)
	past := epoch.Add(-time.Minute)
	put(t, d, &lease.Lease{Owner: "o", Key: "stuck", Kind: lease.KindRun, ExpiresAt: &past, Client: &lease.ProcRef{PID: 800, StartUS: 5}})
	put(t, d, &lease.Lease{Owner: "o", Key: "dead", Kind: lease.KindRun, Client: &lease.ProcRef{PID: 801, StartUS: 5}})
	put(t, d, &lease.Lease{Owner: "o", Key: "timer", Kind: lease.KindAcquire, ExpiresAt: &past, Client: &lease.ProcRef{PID: 802, StartUS: 5}})
	put(t, d, &lease.Lease{Owner: "o", Key: "odd", Kind: lease.KindRun, Client: &lease.ProcRef{PID: 803, StartUS: 5}})

	rec := &recorder{t: t}
	s := NewSweeper(d, Options{
		Now:    fixedNow,
		Prober: fakeProber{800: alive(5), 802: alive(5), 803: alive(5)},
		Info:   infoNames(map[int]string{800: "pg-keep-awake", 801: "pg-keep-awake", 802: "pg-keep-awake", 803: "pg-keep-awake"}),
		Signal: rec.signal,
		// A caller-supplied orphan rule: this live run client is judged orphaned.
		Orphan: func(l *lease.Lease) bool { return l.Key == "odd" },
	})
	res, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if res.Reaped[ReapExpired] != 2 || res.Reaped[ReapClientDead] != 1 || res.Reaped[ReapOrphan] != 1 {
		t.Errorf("Reaped = %v", res.Reaped)
	}
	want := []int{800, 803}
	// Signals follow the scan's file-name order, which is not part of the contract.
	gotRes, gotRec := slices.Sorted(slices.Values(res.Signalled)), slices.Sorted(slices.Values(rec.got()))
	if !slices.Equal(gotRes, want) || !slices.Equal(gotRec, want) {
		t.Errorf("Signalled = %v / %v, want %v (never 801 dead, never 802 acquire)", res.Signalled, rec.got(), want)
	}
}
