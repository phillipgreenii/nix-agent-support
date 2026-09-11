package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// staleIssuesFilename is the exact top-level filename BeadsWatcher looks for
// inside a `.beads` directory (pg2-zjopv). Found live during a 2026-09-11
// drain session: a 20MB, ~2-week-stale `bd export` snapshot sitting next to
// an already-disabled sibling. Every workspace `.beads` dir has
// `export.auto: false`, so bd never refreshes this file on its own — it is
// written only by an explicit `bd export` (without -o) and then silently
// rots. The file gives no warning that it exists or that it's stale; if
// anything ever reads it (a script, a stray `bd import`, a future
// auto-import path) it can restore whole prior rows over current state with
// no warning at all.
const staleIssuesFilename = "issues.jsonl"

// defaultBeadsWatchThrottle bounds how often RunOnce re-alerts on the SAME
// still-present stale file, mirroring internal/otel's exportHealth
// first-failure-always-emits / steady-state-no-spam posture (the pattern
// pg2-zjopv points at) without sharing its single-streak state machine:
// several independent stale files can coexist, each throttled on its own
// clock. The FIRST sighting of any given path always alerts; a human who
// hasn't acted within this window gets reminded rather than paged every scan.
const defaultBeadsWatchThrottle = 24 * time.Hour

// defaultBeadsWatchInterval is the scan cadence when RunOptions.BeadsWatchInterval
// is unset. A stray issues.jsonl is a slow-moving hazard (it rotted for ~2
// weeks before being noticed live) — there is no need to scan every daemon
// tick (~5s); an hourly sweep, mirroring GCSweeper's default cadence, catches
// it promptly relative to how long it can otherwise sit unnoticed.
const defaultBeadsWatchInterval = time.Hour

// scanBeadsDirs checks each root, and each of its immediate non-dotfile
// subdirectories, for a `.beads` directory, and returns the sorted, de-duped
// full path to `issues.jsonl` for every `.beads` dir where that LITERAL file
// exists at the top level (a regular, non-directory file — so the
// already-defused `issues.jsonl.disabled-<date>` rename a human uses to
// resolve an alert never re-matches).
//
// This is a bounded, two-level directory LISTING (root itself, then its
// immediate children) — not a recursive tree walk and not a pn-workspace.toml
// parse. It mirrors the workspace layout the hazard was found in (pg2-zjopv):
// a pn-workspace root's OWN `.beads` sits directly under the root, and each
// repo's `.beads` sits directly under one of the root's immediate
// subdirectories. Dotfile/dot-directory children (.git, .worktrees,
// .workforests, .claude, …) are skipped: a checked-out repo under a
// pn-workspace root is never dot-prefixed, and descending into a worktree or
// workforest staging area risks matching a `.beads` that is only a symlink
// back to its parent repo's own.
//
// A missing or unreadable root (or child) is silently skipped, not an error —
// RunOnce must survive a transiently-unmounted or renamed workspace path
// without ever crashing the daemon's periodic loop.
func scanBeadsDirs(roots []string) []string {
	var found []string
	seen := map[string]struct{}{}
	checkBeadsDir := func(dir string) {
		candidate := filepath.Join(dir, ".beads", staleIssuesFilename)
		if _, ok := seen[candidate]; ok {
			return
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			return
		}
		seen[candidate] = struct{}{}
		found = append(found, candidate)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		checkBeadsDir(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			checkBeadsDir(filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(found)
	return found
}

// staleFileAlerts tracks, per exact file path, when it was last alerted, so a
// human who hasn't yet acted doesn't get paged on every single scan forever —
// while still guaranteeing the first-ever sighting of any given path always
// alerts immediately.
type staleFileAlerts struct {
	now      func() time.Time
	throttle time.Duration

	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func newStaleFileAlerts(now func() time.Time, throttle time.Duration) *staleFileAlerts {
	if now == nil {
		now = time.Now
	}
	if throttle <= 0 {
		throttle = defaultBeadsWatchThrottle
	}
	return &staleFileAlerts{now: now, throttle: throttle, lastSeen: map[string]time.Time{}}
}

// due reports whether path should alert now: true on its first-ever sighting,
// or once the throttle window has elapsed since it last alerted. Every call
// that returns true also records `now` as the new throttle anchor.
func (s *staleFileAlerts) due(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if last, seen := s.lastSeen[path]; seen && now.Sub(last) < s.throttle {
		return false
	}
	s.lastSeen[path] = now
	return true
}

// prune drops tracked paths absent from `present`, so a path that disappears
// (a human renames it away to resolve the alert) and later reappears — even
// under the exact same name — is treated as a brand-new first sighting rather
// than throttled against a stale memory of the earlier incident.
func (s *staleFileAlerts) prune(present []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make(map[string]struct{}, len(present))
	for _, p := range present {
		keep[p] = struct{}{}
	}
	for p := range s.lastSeen {
		if _, ok := keep[p]; !ok {
			delete(s.lastSeen, p)
		}
	}
}

// BeadsWatcher periodically scans a fixed set of root directories for a
// literal top-level `.beads/issues.jsonl` file and raises the TOP-LEVEL OTel
// alert (otel.Emitter.RecordStaleBeadsExport — an ERROR-severity log event
// plus a counter, not merely a metric point that could go unnoticed) whenever
// one is found (pg2-zjopv). It never deletes, renames, or otherwise touches
// the file — alert only; a human decides disposition (as happened live: a
// rename to `issues.jsonl.disabled-<date>`).
type BeadsWatcher struct {
	// Roots are the directories to scan (typically the pn-workspace root(s)).
	// Wired from [beads_watch].roots in config.toml via RunOptions; empty
	// disables the watcher entirely (Run returns immediately).
	Roots []string
	// Interval is how often RunOnce is called by Run. Defaults to 1h.
	Interval time.Duration
	// Emitter receives the alert. nil-safe (both Emitter methods used here
	// are nil-receiver-safe), so a watcher can run with OTel disabled — the
	// Warn stderr line still fires.
	Emitter *otel.Emitter
	// Warn, when non-nil, is also called with a human-readable line for every
	// alerted file — the daemon's own stderr (captured by launchd into
	// launchd-stderr.log), so the alert does not depend solely on the OTel
	// pipeline being up. Defaults to an os.Stderr line when nil.
	Warn func(line string)

	initOnce sync.Once
	alerts   *staleFileAlerts
}

func (w *BeadsWatcher) init() {
	w.initOnce.Do(func() {
		w.alerts = newStaleFileAlerts(time.Now, defaultBeadsWatchThrottle)
	})
}

// RunOnce performs one scan-and-alert pass: find every stale issues.jsonl
// under Roots, forget throttle memory for any that are no longer present,
// then alert (Emitter + Warn) for each one that is due per the throttle.
func (w *BeadsWatcher) RunOnce() {
	w.init()
	found := scanBeadsDirs(w.Roots)
	w.alerts.prune(found)
	warn := w.Warn
	if warn == nil {
		warn = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	for _, path := range found {
		if !w.alerts.due(path) {
			continue
		}
		w.Emitter.RecordStaleBeadsExport(map[string]string{"path": path})
		warn(fmt.Sprintf(
			"pa-monitor: ALERT: stale bd export snapshot found at %s -- "+
				"this .beads dir has export.auto: false, so bd never refreshes "+
				"this file; a stray reader (bd import, a future auto-import path) "+
				"could silently restore prior rows over current state. Not touched "+
				"automatically -- a human must decide disposition (e.g. rename to "+
				"issues.jsonl.disabled-<date>).",
			path,
		))
	}
}

// Run runs RunOnce immediately, then on every tick of Interval, until ctx is
// cancelled. A no-op (returns immediately, no ticker started) when Roots is
// empty — the watcher is off until roots are configured.
func (w *BeadsWatcher) Run(ctx context.Context) {
	if len(w.Roots) == 0 {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = defaultBeadsWatchInterval
	}

	w.RunOnce()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.RunOnce()
		}
	}
}
