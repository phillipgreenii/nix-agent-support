package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The worktree census and the build_info gauges (INV-CCH-41). The janitor is a
// scheduled one-shot, so the census is one scan per pass, run AFTER the pass
// outcome, the heartbeat and the other gauges are settled, on its own context
// that never derives from the run's: a slow scan can neither flip the outcome
// to the deadline nor stop last_success from advancing.
const (
	gaugeWorktreeCount     = "ccpool_worktree_count"
	gaugeWorktreeBytes     = "ccpool_worktree_bytes"
	gaugeWorktreeTruncated = "ccpool_worktree_scan_truncated"
	gaugeWorktreeCensusSec = "ccpool_worktree_census_seconds"
	gaugeBuildInfo         = "ccpool_build_info"

	// buildInfoComponentHandler and buildInfoComponentCCPool are the component
	// label values of the two ccpool_build_info series.
	buildInfoComponentHandler = "pg-router-handler-ccpool"
	buildInfoComponentCCPool  = "ccpool"

	// censusMaxEntries bounds one scan's entry count.
	censusMaxEntries = 2_000_000
	// censusMaxBudget is the longest a census may run.
	censusMaxBudget = 180 * time.Second
	// censusProcessCeiling is how long after the process started the census
	// must be over: the 600 s schedule interval minus a margin, so the next
	// one-shot does not start on top of a running census.
	censusProcessCeiling = 540 * time.Second
)

// censusBudget is min(max, ceiling - sinceStart). It may be zero or negative,
// which makes the census report a truncated scan at once.
func censusBudget(max, sinceStart time.Duration) time.Duration {
	b := censusProcessCeiling - sinceStart
	if b > max {
		b = max
	}
	return b
}

// worktreeCensus is one scan's result.
type worktreeCensus struct {
	count     int64
	bytes     int64
	truncated bool
}

// scanWorktreeDirs counts the immediate subdirectories of every distinct dir
// (one per worktree) and sums the lstat size of every entry beneath them,
// without following symlinks. A missing dir is empty. The walk stops at
// maxEntries or when ctx expires, flagging the result truncated (bytes is then
// a lower bound).
func scanWorktreeDirs(ctx context.Context, dirs []string, maxEntries int) worktreeCensus {
	var st worktreeCensus
	var seen int
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			st.count++
			if st.truncated {
				continue
			}
			_ = filepath.WalkDir(filepath.Join(dir, e.Name()), func(_ string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // unreadable entry: skip, keep counting the rest
				}
				if seen++; seen > maxEntries || ctx.Err() != nil {
					st.truncated = true
					return filepath.SkipAll
				}
				if info, ierr := d.Info(); ierr == nil && !d.IsDir() {
					st.bytes += info.Size()
				}
				return nil
			})
		}
	}
	return st
}

// distinctWorktreeDirs returns each role's handler-wide worktree directory once.
func distinctWorktreeDirs(jroles []janitorRole) []string {
	var out []string
	seen := map[string]bool{}
	for _, jr := range jroles {
		d := jr.cfg.WorktreeDir
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// emitBuildInfo publishes the two ccpool_build_info series (value 1). An
// unstamped handler reports dev; a ccpool whose version cannot be read reports
// unknown. Never an error: it must not fail the pass.
func emitBuildInfo(ctx context.Context, passes janitorPasses) {
	hv := strings.TrimSpace(version)
	if hv == "" {
		hv = "dev"
	}
	cv := "unknown"
	if passes.ccpoolVersion != nil {
		if v, err := passes.ccpoolVersion(ctx); err != nil {
			slog.Warn("janitor: ccpool version failed", "err", err)
		} else if v = strings.TrimSpace(v); v != "" {
			cv = v
		}
	}
	for comp, v := range map[string]string{buildInfoComponentHandler: hv, buildInfoComponentCCPool: cv} {
		if err := passes.emit(ctx, gaugeBuildInfo, 1, map[string]string{"component": comp, "version": v}); err != nil {
			slog.Warn("janitor: gauge emit failed", "gauge", gaugeBuildInfo, "err", err)
		}
	}
}

// emitCensus runs the census and publishes its gauges. sinceStart is the time
// since the process started, by the injected clock. The scan runs on its own
// context from context.Background, so nothing here can touch the run's context.
func emitCensus(ctx context.Context, passes janitorPasses, jroles []janitorRole, sinceStart time.Duration) {
	dirs := distinctWorktreeDirs(jroles)
	if len(dirs) == 0 {
		return
	}
	max := passes.censusMaxBudget
	if max <= 0 {
		max = censusMaxBudget
	}
	maxEntries := passes.censusMaxEntries
	if maxEntries <= 0 {
		maxEntries = censusMaxEntries
	}
	// The census gets its own budget and must not be cut by the run context.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), censusBudget(max, sinceStart))
	defer cancel()
	t0 := time.Now()
	st := scanWorktreeDirs(cctx, dirs, maxEntries)
	took := time.Since(t0)
	trunc := 0.0
	if st.truncated {
		trunc = 1
	}
	for name, v := range map[string]float64{
		gaugeWorktreeCount:     float64(st.count),
		gaugeWorktreeBytes:     float64(st.bytes),
		gaugeWorktreeTruncated: trunc,
		gaugeWorktreeCensusSec: took.Seconds(),
	} {
		if err := passes.emit(ctx, name, v, nil); err != nil {
			slog.Warn("janitor: gauge emit failed", "gauge", name, "err", err)
		}
	}
}
