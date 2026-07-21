package poller

import (
	"context"
	"os"
	"sync"
	"time"
)

// ChangeSource decides when the producer should re-Assemble (design §6). The
// two-tier default polls the watched set (active transcripts + status siblings)
// on a fast interval for size/mtime changes — low-latency status/nudge — and
// fires a periodic full rescan on a slow interval so newly-in-window files
// (non-active pricing files) are still caught within a bounded lag.
type ChangeSource interface {
	// WaitForChange blocks until a rescan is warranted (a fast-tier size/mtime
	// change in the watched set, or the slow-tier period elapsed), returning true;
	// or until ctx is done, returning false.
	WaitForChange(ctx context.Context) bool
	// SetWatch replaces the fast-tier watched path set (the active transcripts +
	// status siblings from the last Assemble) and re-baselines their stats, so the
	// just-assembled state is not itself reported as a change.
	SetWatch(paths []string)
}

type fileStat struct {
	size  int64
	mtime time.Time
}

// twoTierChangeSource is the default ChangeSource. Change detection is size/mtime
// based (NOT mtime-only), so a same-mtime append is still seen (reconciles with
// the tail cache gate, C3).
type twoTierChangeSource struct {
	fast, slow time.Duration
	now        func() time.Time

	mu           sync.Mutex
	watch        []string
	last         map[string]fileStat
	slowDeadline time.Time
	slowInited   bool
}

func newTwoTierChangeSource(fast, slow time.Duration, now func() time.Time) *twoTierChangeSource {
	if now == nil {
		now = time.Now
	}
	if fast <= 0 {
		fast = time.Second
	}
	if slow <= 0 {
		slow = 5 * time.Second
	}
	return &twoTierChangeSource{fast: fast, slow: slow, now: now, last: map[string]fileStat{}}
}

func (cs *twoTierChangeSource) SetWatch(paths []string) {
	cs.mu.Lock()
	cs.watch = append([]string(nil), paths...)
	cs.last = statAll(cs.watch) // re-baseline: the just-assembled state is not a change
	cs.mu.Unlock()
}

func (cs *twoTierChangeSource) WaitForChange(ctx context.Context) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	cs.mu.Lock()
	if !cs.slowInited {
		cs.slowDeadline = cs.now().Add(cs.slow)
		cs.slowInited = true
	}
	cs.mu.Unlock()

	t := time.NewTicker(cs.fast)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			cs.mu.Lock()
			now := cs.now()
			if !now.Before(cs.slowDeadline) {
				cs.slowDeadline = now.Add(cs.slow)
				cs.mu.Unlock()
				return true // slow-tier periodic full rescan
			}
			cur := statAll(cs.watch)
			changed := !sameStats(cs.last, cur)
			cs.last = cur
			cs.mu.Unlock()
			if changed {
				return true // fast-tier size/mtime change
			}
		}
	}
}

// statAll stats each path into a size/mtime snapshot; a path that fails to stat
// (missing/unreadable) is omitted, so its appearance or disappearance registers
// as a change against the previous snapshot.
func statAll(paths []string) map[string]fileStat {
	out := make(map[string]fileStat, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			out[p] = fileStat{size: fi.Size(), mtime: fi.ModTime()}
		}
	}
	return out
}

func sameStats(a, b map[string]fileStat) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || bv.size != av.size || !bv.mtime.Equal(av.mtime) {
			return false
		}
	}
	return true
}
