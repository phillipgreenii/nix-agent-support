package signal

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// procTableTTL is how long one snapshot serves every ancestry walk. It is
	// shorter than the daemon tick (5 s) so each tick takes exactly one `ps`
	// snapshot, yet far longer than the milliseconds one tick's walks span.
	procTableTTL = 2 * time.Second
	// procTableMaxStale bounds how long a previous snapshot is reused after
	// refreshes fail. Past it, ancestry resolves as unknown rather than being
	// answered from data that may describe a long-gone process tree.
	procTableMaxStale = 30 * time.Second
)

// ProcessTable resolves a process's parent from an in-memory table. It is the
// seam that lets a later in-process kern.proc.all reader replace the `ps`
// snapshot without touching any ancestry walk.
type ProcessTable interface {
	// Parent returns pid's parent. ok is false when pid is unknown, when its
	// recorded parent started after it did (the pid was reused), or when no
	// usable snapshot exists. Ancestry is never invented.
	Parent(pid int) (ppid int, ok bool)
	// ParentOf is Parent keyed by (pid, start time): it also fails when pid's
	// current start time differs from wantStart ("" matches any start).
	ParentOf(pid int, wantStart string) (ppid int, ok bool)
}

// procEntry is one row of the snapshot. start is the `lstart` text (identity
// together with the pid; the kernel may reuse a pid, never a (pid, start)).
type procEntry struct {
	ppid  int
	start time.Time
	raw   string
}

// PSProcTable is a ProcessTable backed by ONE ids-only `ps -ax -o
// pid=,ppid=,lstart=` per refresh (no argv, so nothing sensitive is read).
// Run, TTL, MaxStale and Now are injectable; zero values use exec, the
// package defaults and time.Now.
type PSProcTable struct {
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	TTL      time.Duration
	MaxStale time.Duration
	Now      func() time.Time

	mu      sync.Mutex
	entries map[int]procEntry
	goodAt  time.Time // when entries were last refreshed successfully
	triedAt time.Time // last attempt, successful or not
}

// NewPSProcTable returns a table that execs the real ps.
func NewPSProcTable() *PSProcTable { return &PSProcTable{} }

var _ ProcessTable = (*PSProcTable)(nil)

func (p *PSProcTable) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *PSProcTable) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}
	return procTableTTL
}

func (p *PSProcTable) maxStale() time.Duration {
	if p.MaxStale > 0 {
		return p.MaxStale
	}
	return procTableMaxStale
}

// table returns the snapshot to answer from: a fresh one, the previous one
// while within MaxStale of its last success, else nil (unknown).
func (p *PSProcTable) table() map[int]procEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.triedAt.IsZero() || now.Sub(p.triedAt) >= p.ttl() {
		p.triedAt = now
		if m, err := p.snapshot(); err == nil {
			p.entries, p.goodAt = m, now
		}
	}
	if p.entries == nil || now.Sub(p.goodAt) > p.maxStale() {
		return nil
	}
	return p.entries
}

func (p *PSProcTable) snapshot() (map[int]procEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := p.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	out, err := run(ctx, "ps", "-ax", "-o", "pid=,ppid=,lstart=")
	if err != nil {
		return nil, err
	}
	m := parseProcSnapshot(string(out))
	if len(m) == 0 {
		return nil, errors.New("signal: empty process snapshot")
	}
	return m, nil
}

// parseProcSnapshot parses `pid ppid lstart...` rows, skipping junk.
func parseProcSnapshot(out string) map[int]procEntry {
	m := map[int]procEntry{}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		raw := strings.Join(f[2:], " ")
		e := procEntry{ppid: ppid, raw: raw}
		// lstart renders like "Mon Nov  2 10:00:00 2026".
		if t, err := time.Parse("Mon Jan 2 15:04:05 2006", raw); err == nil {
			e.start = t
		}
		m[pid] = e
	}
	return m
}

// Parent implements ProcessTable.
func (p *PSProcTable) Parent(pid int) (int, bool) { return p.ParentOf(pid, "") }

// ParentOf implements ProcessTable.
func (p *PSProcTable) ParentOf(pid int, wantStart string) (int, bool) {
	tbl := p.table()
	e, ok := tbl[pid]
	if !ok || e.ppid < 1 {
		return 0, false
	}
	if wantStart != "" && strings.Join(strings.Fields(wantStart), " ") != e.raw {
		return 0, false
	}
	// A parent that started after its child is a reused pid, not the parent.
	if pe, ok := tbl[e.ppid]; ok && !e.start.IsZero() && pe.start.After(e.start) {
		return 0, false
	}
	return e.ppid, true
}

// StartOf returns pid's start-time text in the current snapshot, for callers
// that key a cached ancestry by (pid, start).
func (p *PSProcTable) StartOf(pid int) (string, bool) {
	e, ok := p.table()[pid]
	return e.raw, ok
}

// defaultProcTable is the one table every default signaler and the provider
// cache's parent probe share, so a tick costs one snapshot in total.
var defaultProcTable = NewPSProcTable()

// SnapshotParentPID is ParentPID answered from the shared per-tick snapshot.
func SnapshotParentPID(pid int) (int, bool) { return defaultProcTable.Parent(pid) }

// hopFromTable resolves one ancestry hop from tbl; unknown yields 0 (stop).
func hopFromTable(tbl ProcessTable, pid int) (int, error) {
	ppid, _ := tbl.Parent(pid)
	return ppid, nil
}
