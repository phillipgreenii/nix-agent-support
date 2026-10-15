package keeper

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// FileHeartbeat is the keeper's heartbeat file in the state directory. The
// keeper creates it first thing at startup (adoption step 1) and touches it
// after every completed tick; only the graceful-shutdown sweep removes it.
const FileHeartbeat = "keeper.heartbeat"

// ReapReason says why a lease was reaped. The values are the metric label
// values of the reap counter.
type ReapReason string

// Reap reasons, in the order the classifier tests them.
const (
	// ReapExpired: expires_at is not in the future.
	ReapExpired ReapReason = "expired"
	// ReapClientDead: the client pid is gone, was recycled (start_us differs)
	// or is a zombie.
	ReapClientDead ReapReason = "client_dead"
	// ReapWatchedDead: the watched pid is gone or was recycled.
	ReapWatchedDead ReapReason = "watched_dead"
	// ReapOrphan: see Options.Orphan.
	ReapOrphan ReapReason = "orphan"
)

// ReapReasons lists every ReapReason.
var ReapReasons = []ReapReason{ReapExpired, ReapClientDead, ReapWatchedDead, ReapOrphan}

// RejectReason says why a lease file was skipped (not parsed as a lease). The
// values are the metric label values of the reject counter.
type RejectReason string

// Reject reasons.
const (
	// RejectUnreadable: the file is not usable as a lease: unparsable or
	// invalid content, a symlink, FIFO or other non-regular file, a file that
	// cannot be opened, or a schema_version newer than this reader supports.
	RejectUnreadable RejectReason = "unreadable"
	// RejectTooLarge: a regular file over 64 KiB.
	RejectTooLarge RejectReason = "too_large"
	// RejectOverCap: a *.json file beyond the 1024 considered per tick.
	RejectOverCap RejectReason = "over_cap"
)

// RejectReasons lists every RejectReason.
var RejectReasons = []RejectReason{RejectUnreadable, RejectTooLarge, RejectOverCap}

// LiveLease is a lease that survived the sweep and is live. It carries what
// desired-state (flags) and the status snapshot (the rest) need.
type LiveLease struct {
	ID        string
	Owner     string
	Key       string
	Kind      string
	Flags     []string
	ExpiresAt *time.Time
	Client    *lease.ProcRef
	Watch     *lease.ProcRef
}

// ReapedLease is a lease the sweep removed, with the reason.
type ReapedLease struct {
	Lease  *lease.Lease
	Reason ReapReason
}

// Result is what one sweep did.
type Result struct {
	// Live are the live leases, ordered by id. When the sweep was skipped
	// (LockSkipped) they come from the unlocked first read, so desired-state
	// still has an answer for the tick.
	Live []LiveLease
	// Reaped counts reaped leases per reason; every ReapReason has a key.
	Reaped map[ReapReason]int
	// ReapedLeases lists the reaped leases (what the backstop was given).
	ReapedLeases []ReapedLease
	// Rejected counts skipped lease files per reason; every RejectReason has a
	// key. It is measured on the first read, so it is reported even when the
	// sweep is skipped.
	Rejected map[RejectReason]int
	// JunkRemoved counts stale unparsable *.json and stale *.tmp files removed.
	JunkRemoved int
	// LockSkipped is true when leases/.lock could not be taken within its
	// 2 second bound: nothing was reaped, and the caller MUST still touch the
	// heartbeat so one stopped client cannot trip the watchdog.
	LockSkipped bool
	// Signalled are the pids the backstop sent SIGTERM.
	Signalled []int
	// HeartbeatRemoved is true when the shutdown sweep removed the heartbeat.
	HeartbeatRemoved bool
}

func newResult() *Result {
	r := &Result{Reaped: map[ReapReason]int{}, Rejected: map[RejectReason]int{}}
	for _, k := range ReapReasons {
		r.Reaped[k] = 0
	}
	for _, k := range RejectReasons {
		r.Rejected[k] = 0
	}
	return r
}

// Options injects the sweeper's seams. Every field is optional; the zero
// value is the production behaviour.
type Options struct {
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Prober answers pid liveness (default lease.SysProber).
	Prober lease.Prober
	// Info looks up a pid's executable name (default lease.ProcessInfo).
	Info func(pid int) (lease.ProcInfo, error)
	// Signal sends a signal (default syscall.Kill). Only the backstop uses it.
	Signal func(pid int, sig syscall.Signal) error
	// Self is a pid the backstop never signals (default os.Getpid()).
	Self int
	// Orphan decides whether an otherwise-live lease is an orphan. The default
	// (DefaultOrphan) is deliberately conservative.
	Orphan func(*lease.Lease) bool
}

// DefaultOrphan is the production orphan rule: a shim or run lease that
// records no client. Such a lease claims to be bound to the process that made
// it yet names none, so nothing can ever release it and only its expiry or
// watch could end it; the CLI never writes one, so it can only be a damaged
// or hand-made file. The rule never looks at a live process, so it cannot
// drop a held assertion because of a wrapper script, a renamed binary or a
// test harness. An acquire lease is never an orphan (a pure timer has no
// client by design).
func DefaultOrphan(l *lease.Lease) bool {
	return (l.Kind == lease.KindShim || l.Kind == lease.KindRun) && l.Client == nil
}

// Sweeper reaps dead, expired and orphaned leases. Only the holder of
// keeper.lock may run it.
type Sweeper struct {
	dir  *lease.Dir
	opts Options

	// afterFirstRead runs between the unlocked first read and taking the
	// lock; tests use it to race a writer against the reaper.
	afterFirstRead func()
}

// NewSweeper builds a Sweeper over a checked state directory.
func NewSweeper(dir *lease.Dir, opts Options) *Sweeper {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Prober == nil {
		opts.Prober = lease.SysProber{}
	}
	if opts.Info == nil {
		opts.Info = lease.ProcessInfo
	}
	if opts.Signal == nil {
		opts.Signal = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	}
	if opts.Self == 0 {
		opts.Self = os.Getpid()
	}
	if opts.Orphan == nil {
		opts.Orphan = DefaultOrphan
	}
	return &Sweeper{dir: dir, opts: opts}
}

// Sweep is the per-tick sweep: reap, remove stale junk, run the backstop.
// keeper.heartbeat is left alone.
func (s *Sweeper) Sweep() (*Result, error) { return s.run(false) }

// Startup is the startup sweep. It is Sweep under another name so the
// caller's intent is explicit: it keeps keeper.heartbeat, which the keeper
// created as step 1 of adoption.
func (s *Sweeper) Startup() (*Result, error) { return s.run(false) }

// Shutdown is the graceful-shutdown sweep (on SIGTERM). It does the same as
// Startup and then also removes keeper.heartbeat, which starts the anchor's
// watchdog countdown at shutdown. Valid leases, assertion.json, the anchor
// and caffeinate are kept.
func (s *Sweeper) Shutdown() (*Result, error) { return s.run(true) }

func (s *Sweeper) run(removeHeartbeat bool) (*Result, error) {
	res := newResult()
	var errs []error

	if err := s.sweepLeases(res, &errs); err != nil {
		errs = append(errs, err)
	}
	if removeHeartbeat {
		if err := s.dir.Remove(FileHeartbeat); err != nil {
			errs = append(errs, err)
		} else {
			res.HeartbeatRemoved = true
		}
	}
	return res, errors.Join(errs...)
}

func (s *Sweeper) sweepLeases(res *Result, errs *[]error) error {
	now := s.opts.Now()
	scan, err := s.dir.ListLeases()
	if err != nil {
		return err
	}
	s.countRejects(scan, res)

	var live, dead []*lease.Lease
	for _, l := range scan.Leases {
		if s.classify(l, now) == "" {
			live = append(live, l)
		} else {
			dead = append(dead, l)
		}
	}
	if s.afterFirstRead != nil {
		s.afterFirstRead()
	}

	lk, err := s.dir.TakeLock()
	switch {
	case errors.Is(err, lease.ErrLockTimeout):
		// Skip this tick's sweep; the loop still touches its heartbeat. The
		// dead leases stay on disk for the next tick, and are not live.
		res.LockSkipped = true
	case errors.Is(err, fs.ErrNotExist):
		// No leases/ directory yet: nothing to sweep.
	case err != nil:
		return err
	default:
		live = s.reapLocked(lk, dead, live, res, errs)
		if err := lk.Unlock(); err != nil {
			*errs = append(*errs, err)
		}
	}

	res.Live = toLive(live)
	res.Signalled = s.Backstop(res.ReapedLeases)
	return nil
}

// reapLocked runs under leases/.lock. For every lease the first read judged
// dead it re-reads the file and unlinks it only if what it now reads is still
// dead: a lease refreshed in between survives and is reported live.
func (s *Sweeper) reapLocked(lk *lease.Lock, dead, live []*lease.Lease, res *Result, errs *[]error) []*lease.Lease {
	for _, first := range dead {
		cur, err := s.dir.ReadLease(first.ID)
		if err != nil {
			// Gone already, or no longer a readable lease: not ours to delete
			// here (stale junk is the SweepJunk step below).
			continue
		}
		reason := s.classify(cur, s.opts.Now())
		if reason == "" {
			live = append(live, cur)
			continue
		}
		if err := lk.Remove(cur.ID); err != nil {
			*errs = append(*errs, err)
			continue
		}
		res.Reaped[reason]++
		res.ReapedLeases = append(res.ReapedLeases, ReapedLease{Lease: cur, Reason: reason})
	}
	n, err := lk.SweepJunk(s.opts.Now())
	res.JunkRemoved = n
	if err != nil {
		*errs = append(*errs, err)
	}
	return live
}

// classify returns why l is dead at now, or "" when it is live. It agrees with
// lease.Lease.Live for every lease the default orphan rule leaves alone.
func (s *Sweeper) classify(l *lease.Lease, now time.Time) ReapReason {
	if l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
		return ReapExpired
	}
	if l.Client != nil {
		st := s.opts.Prober.Probe(l.Client.PID)
		if !st.Exists || st.StartUS != l.Client.StartUS || st.Zombie {
			return ReapClientDead
		}
	}
	if l.Watch != nil {
		st := s.opts.Prober.Probe(l.Watch.PID)
		if !st.Exists || st.StartUS != l.Watch.StartUS {
			return ReapWatchedDead
		}
	}
	if s.opts.Orphan(l) {
		return ReapOrphan
	}
	return ""
}

func toLive(ls []*lease.Lease) []LiveLease {
	out := make([]LiveLease, 0, len(ls))
	for _, l := range ls {
		out = append(out, LiveLease{
			ID: l.ID, Owner: l.Owner, Key: l.Key, Kind: l.Kind,
			Flags:     slices.Clone(l.Flags),
			ExpiresAt: l.ExpiresAt, Client: l.Client, Watch: l.Watch,
		})
	}
	slices.SortFunc(out, func(a, b LiveLease) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// countRejects splits the scan's single Rejected total into the metric
// reasons. internal/lease reports one count, so the split re-reads the
// directory listing (names and sizes only, never contents).
func (s *Sweeper) countRejects(scan *lease.ScanResult, res *Result) {
	res.Rejected[RejectUnreadable] += len(scan.Unreadable)
	if scan.Rejected == 0 {
		return
	}
	entries, err := os.ReadDir(s.dir.LeasesPath())
	if err != nil {
		res.Rejected[RejectUnreadable] += scan.Rejected
		return
	}
	known := map[string]bool{}
	for _, l := range scan.Leases {
		known[l.ID+".json"] = true
	}
	for _, n := range scan.Unreadable {
		known[n] = true
	}
	attributed, idx := 0, 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || !strings.HasSuffix(name, ".json") {
			continue
		}
		i := idx
		idx++
		if known[name] {
			continue
		}
		switch fi, err := os.Lstat(filepath.Join(s.dir.LeasesPath(), name)); {
		case i >= lease.MaxLeaseFiles:
			res.Rejected[RejectOverCap]++
		case err == nil && fi.Mode().IsRegular() && fi.Size() > lease.MaxLeaseFileBytes:
			res.Rejected[RejectTooLarge]++
		default:
			res.Rejected[RejectUnreadable]++
		}
		attributed++
	}
	if d := scan.Rejected - attributed; d > 0 {
		res.Rejected[RejectUnreadable] += d // vanished between the two reads
	}
}
