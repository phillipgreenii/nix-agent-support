package hygiene

import (
	"context"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Janitor progress counters (INV-CCH-41, design D2): per pass and per role, how
// many candidates a run saw, how many it examined, skipped as cached, kept and
// removed, and how many it never reached.
//
// The counters ride on the pass's context, so the passes keep their signatures
// and a caller without counters (a dispatch, the inline sweeps) pays nothing:
// every method is a no-op on a nil *passCounts.

// Phases a pass may record, the names of the phase label of
// ccpool_janitor_phase_seconds.
const (
	phaseList  = "list"
	phaseProbe = "probe"
	phaseScan  = "scan"
)

// Stages of closed_bead_reconcile, named in a cut-stage record.
const (
	phasePurgeRetry = "purge_retry"
	phaseReconcile  = "reconcile"
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
	// ListMS, ProbeMS and ScanMS are the phase times a pass recorded with
	// setPhase; no pass records one today.
	ListMS  *int64 `json:"list_ms,omitempty"`
	ProbeMS *int64 `json:"probe_ms,omitempty"`
	ScanMS  *int64 `json:"scan_ms,omitempty"`
	// OldestUnexaminedAgeSeconds is the age of the longest-waiting candidate a
	// pass did not reach, and OldestUnexaminedID its id (the gauge carries the
	// id's stable hash); no pass records one today.
	OldestUnexaminedAgeSeconds float64 `json:"oldest_unexamined_age_seconds"`
	OldestUnexaminedID         string  `json:"oldest_unexamined_id,omitempty"`
	// Pool is the role's pool key; the list numbers below belong to it.
	Pool string `json:"pool,omitempty"`
	// ListCalls, ListTimeouts and ListWallMS count the `ccpool list` calls this
	// pass made, how many the per-call timeout killed, and their wall time.
	ListCalls    int   `json:"list_calls"`
	ListTimeouts int   `json:"list_timeouts"`
	ListWallMS   int64 `json:"list_wall_ms"`

	// phase is the stage the pass last entered (enter). watch is the pass's
	// context; the first enter after it ended freezes the stage that was
	// running as cutPhase (cutSet), because a pass keeps running, and entering
	// later stages, for a while after its share expires.
	phase    string
	watch    context.Context
	cutPhase string
	cutSet   bool
}

// enter records the stage the pass is now in. Nil-safe.
func (c *passCounts) enter(phase string) {
	if c == nil {
		return
	}
	if !c.cutSet && c.watch != nil && c.watch.Err() != nil {
		c.cutPhase, c.cutSet = c.phase, true
	}
	c.phase = phase
}

// cutStagePhase is the stage the pass was in when its context ended.
func (c *passCounts) cutStagePhase() string {
	if c.cutSet {
		return c.cutPhase
	}
	return c.phase
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

// keepTmpMarker marks the temp file writeFileAtomic renames into place.
const keepTmpMarker = ".tmp-"

// keepLikeFile maps a per-key record to a file under dir; characters outside
// [A-Za-z0-9._-] become "_" so a name can never escape dir.
func keepLikeFile(dir, prefix, suffix, key string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, key)
	return filepath.Join(dir, prefix+name+suffix)
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
