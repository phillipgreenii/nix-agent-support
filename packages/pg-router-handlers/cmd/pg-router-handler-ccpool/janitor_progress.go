package main

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Janitor progress counters (INV-CCH-41, design D2): per pass and per role, how
// many candidates a run saw, how many it examined, skipped as cached, kept and
// removed, and how many it never reached; plus, for the worktree sweep, the
// time of each phase and the age of the longest-waiting unreached candidate.
//
// The counters ride on the pass's context, so the passes keep their signatures
// and a caller without counters (a dispatch, the inline sweeps) pays nothing:
// every method is a no-op on a nil *passCounts.

// Phases of the worktree sweep, the names of the phase label of
// ccpool_janitor_phase_seconds.
const (
	phaseList  = "list"
	phaseProbe = "probe"
	phaseScan  = "scan"
)

// passCounts is one pass's numbers for one role in one run. Unexamined is
// derived when the pass ends (Candidates - Examined - Cached), so the counters
// add up by construction; Kept is Examined - Removed.
type passCounts struct {
	Pass       string `json:"pass"`
	Role       string `json:"role"`
	Candidates int    `json:"candidates"`
	Examined   int    `json:"examined"`
	Cached     int    `json:"cached"`
	Kept       int    `json:"kept"`
	Removed    int    `json:"removed"`
	Unexamined int    `json:"unexamined"`
	// ListMS, ProbeMS and ScanMS are the phases of worktree_sweep only.
	ListMS  *int64 `json:"list_ms,omitempty"`
	ProbeMS *int64 `json:"probe_ms,omitempty"`
	ScanMS  *int64 `json:"scan_ms,omitempty"`
	// OldestUnexaminedAgeSeconds is the age of the longest-waiting candidate this
	// pass did not reach (worktree_sweep only; zero when it reached them all).
	OldestUnexaminedAgeSeconds float64 `json:"oldest_unexamined_age_seconds"`
	// OldestUnexaminedID is the id (the worktree's bead name) of that
	// longest-waiting candidate, empty when the pass reached them all; the
	// gauge carries its stable hash.
	OldestUnexaminedID string `json:"oldest_unexamined_id,omitempty"`
	// Pool is the role's pool key; the list numbers below belong to it.
	Pool string `json:"pool,omitempty"`
	// ListCalls, ListTimeouts and ListWallMS count the `ccpool list` calls this
	// pass made, how many the per-call timeout killed, and their wall time.
	ListCalls    int   `json:"list_calls"`
	ListTimeouts int   `json:"list_timeouts"`
	ListWallMS   int64 `json:"list_wall_ms"`
}

// listCall records one `ccpool list` call of d wall time; timedOut says the
// per-call timeout killed it.
func (c *passCounts) listCall(d time.Duration, timedOut bool) {
	if c == nil {
		return
	}
	c.ListCalls++
	c.ListWallMS += d.Milliseconds()
	if timedOut {
		c.ListTimeouts++
	}
}

// idHash is a stable numeric hash of a candidate id, small enough to be exact
// as a gauge value.
func idHash(id string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return float64(h.Sum32())
}

type countsKey struct{}

// withCounts returns ctx carrying c.
func withCounts(ctx context.Context, c *passCounts) context.Context {
	return context.WithValue(ctx, countsKey{}, c)
}

// countsFrom returns the counters carried by ctx, or nil.
func countsFrom(ctx context.Context) *passCounts {
	c, _ := ctx.Value(countsKey{}).(*passCounts)
	return c
}

func (c *passCounts) seen(n int) {
	if c != nil {
		c.Candidates += n
	}
}

func (c *passCounts) examined() {
	if c != nil {
		c.Examined++
	}
}

func (c *passCounts) cached() {
	if c != nil {
		c.Cached++
	}
}

func (c *passCounts) setPhase(phase string, d time.Duration) {
	if c == nil {
		return
	}
	ms := d.Milliseconds()
	switch phase {
	case phaseList:
		c.ListMS = &ms
	case phaseProbe:
		c.ProbeMS = &ms
	case phaseScan:
		c.ScanMS = &ms
	}
}

// finish fixes the derived numbers once the pass has returned how many things
// it removed.
func (c *passCounts) finish(removed int) {
	if c == nil {
		return
	}
	c.Removed = removed
	if c.Examined < c.Removed {
		c.Examined = c.Removed
	}
	c.Kept = c.Examined - c.Removed
	if c.Unexamined = c.Candidates - c.Examined - c.Cached; c.Unexamined < 0 {
		c.Unexamined = 0
	}
}

// trackUnexamined records the age of the candidates the pass did not reach and
// forgets the first-seen time of those it did. reached is keyed by bead.
func trackUnexamined(counts *passCounts, lockDir string, now time.Time, candidates []leakedWorktree, reached map[string]bool) {
	counts.seen(len(candidates))
	var oldest time.Duration
	var oldestID string
	for _, c := range candidates {
		if reached[c.bead] {
			clearFirstSeen(lockDir, c)
			continue
		}
		age := noteFirstSeen(lockDir, c, now)
		if age > oldest || oldestID == "" {
			oldest, oldestID = age, c.bead
		}
	}
	if counts != nil {
		counts.OldestUnexaminedAgeSeconds = oldest.Seconds()
		counts.OldestUnexaminedID = oldestID
	}
}

// First-seen record (design D2). A candidate the sweep sees but does not reach
// gets one small file beside the keep cache, one per worktree and shared by
// every role:
//
//	<lock dir>/worktree-seen--<bead>.seen
//
// It holds the time the candidate was first seen waiting. It is removed when any
// pass reaches the candidate (examines it or finds it cached) or stops seeing
// it as a candidate, and by sweepKeepCache once its worktree is gone, so its age
// is how long the candidate has gone unreached.
const (
	seenFilePrefix = "worktree-seen--"
	seenFileSuffix = ".seen"
)

type seenEntry struct {
	Path      string `json:"path"`
	FirstSeen int64  `json:"first_seen_unix_nano"`
}

func seenFile(dir, bead string) string {
	return keepLikeFile(dir, seenFilePrefix, seenFileSuffix, bead)
}

// readSeen parses a first-seen file; torn or foreign content is absent.
func readSeen(path string) (seenEntry, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return seenEntry{}, false
	}
	var e seenEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.Path == "" || e.FirstSeen == 0 {
		return seenEntry{}, false
	}
	return e, true
}

// noteFirstSeen returns how long c has been waiting, recording now as its first
// sighting when no record for this worktree exists. Best effort: a failed write
// reports age zero and the next run tries again.
func noteFirstSeen(lockDir string, c leakedWorktree, now time.Time) time.Duration {
	if lockDir == "" {
		return 0
	}
	dst := seenFile(lockDir, c.bead)
	if e, ok := readSeen(dst); ok && e.Path == c.path {
		if age := now.Sub(time.Unix(0, e.FirstSeen)); age > 0 {
			return age
		}
		return 0
	}
	raw, err := json.Marshal(seenEntry{Path: c.path, FirstSeen: now.UnixNano()})
	if err != nil {
		return 0
	}
	if err := writeFileAtomic(dst, raw); err != nil {
		slog.Warn("worktree sweep: cannot write the first-seen record", "bead", c.bead, "err", err)
	}
	return 0
}

// clearFirstSeen forgets c's first sighting (it was reached, or is no longer a
// candidate).
func clearFirstSeen(lockDir string, c leakedWorktree) {
	if lockDir == "" {
		return
	}
	_ = os.Remove(seenFile(lockDir, c.bead))
}

// writeFileAtomic writes raw to dst through a temp file and a rename, so a
// reader sees a whole file or none.
func writeFileAtomic(dst string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+keepTmpMarker+"*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
