package daemon

import (
	"context"
	"os"
	"path/filepath"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/service"
)

// GCSweeper reconciles the sessions directory against the DB, soft-deletes
// orphaned rows, and hard-deletes rows that have been soft-deleted past the
// retention cutoff. It also drives orphan cleanup for blocks and weeks.
type GCSweeper struct {
	// SessionsDir is the directory where Claude Code writes <pid>.json registry
	// files. The sweeper reads each file's sessionId field to determine which
	// sessions are still alive.
	SessionsDir string
	// WriteService receives all mutations.
	WriteService *service.WriteService
	// Interval is how often RunOnce is called by Run. Defaults to 1h.
	Interval time.Duration
	// HardDeleteAfter is the retention window after soft-deletion. Defaults to 24h.
	HardDeleteAfter time.Duration
}

// RunOnce performs one full GC sweep:
//  1. File reconciliation: soft-delete sessions whose files are gone, revive
//     sessions whose files have reappeared.
//  2. Hard-delete sessions soft-deleted before now-HardDeleteAfter.
//  3. Soft-delete orphan blocks/weeks, revive blocks/weeks that have gained
//     contributions back.
//  4. Hard-delete blocks/weeks soft-deleted before now-HardDeleteAfter.
func (g *GCSweeper) RunOnce(ctx context.Context) error {
	// --- Stage 1: file reconciliation ---
	keepIDs, err := listSessionFiles(g.SessionsDir)
	if err != nil {
		// Directory missing or unreadable: keep all rows alive rather than
		// mass-deleting. Return nil so the sweeper doesn't crash.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if err := g.WriteService.MarkSessionsDeleted(ctx, keepIDs); err != nil {
		return err
	}
	if err := g.WriteService.MarkSessionsRevived(ctx, keepIDs); err != nil {
		return err
	}

	// --- Stage 2: hard-delete sessions past retention window ---
	hardDeleteAfter := g.HardDeleteAfter
	if hardDeleteAfter <= 0 {
		hardDeleteAfter = 24 * time.Hour
	}
	cutoff := time.Now().UTC().Add(-hardDeleteAfter)
	if _, err := g.WriteService.HardDeleteSessions(ctx, cutoff); err != nil {
		return err
	}

	// --- Stage 3: orphan blocks / weeks ---
	if _, err := g.WriteService.MarkBlockOrphansDeleted(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkBlocksRevived(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkWeekOrphansDeleted(ctx); err != nil {
		return err
	}
	if _, err := g.WriteService.MarkWeeksRevived(ctx); err != nil {
		return err
	}

	// --- Stage 4: hard-delete blocks / weeks past retention window ---
	if _, err := g.WriteService.HardDeleteBlocks(ctx, cutoff); err != nil {
		return err
	}
	if _, err := g.WriteService.HardDeleteWeeks(ctx, cutoff); err != nil {
		return err
	}

	return nil
}

// Run is a ticker loop that calls RunOnce on every interval until ctx is
// cancelled. It always runs RunOnce once immediately on start.
func (g *GCSweeper) Run(ctx context.Context) {
	interval := g.Interval
	if interval <= 0 {
		interval = time.Hour
	}

	// Run immediately, then on each tick.
	_ = g.RunOnce(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = g.RunOnce(ctx)
		}
	}
}

// listSessionFiles lists dir and returns the session IDs named by the
// sessionId field inside each Claude Code registry file. Registry files are
// named <pid>.json (not <sessionId>.json), so the filename MUST NOT be used as
// the session id: doing so soft-deleted every row on every sweep (pg2-4g02s).
//
// It mirrors session.Discoverer.Discover: only *.json files are read, via
// ct.ReadSessionFile; malformed files and files without a sessionId are
// skipped. Status-line / session-mode / compaction siblings
// (session.IsStatusSiblingFile, ADR 0021 §2) are skipped even though they may
// be valid JSON, and <pid>.<hash>.key sidecars are ignored by extension.
// Returns an empty slice (not error) when the directory holds no registry
// files. Returns the os.ReadDir error (os.ErrNotExist if the directory is
// missing) so the caller can distinguish "no sessions" from "dir gone".
func listSessionFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if session.IsStatusSiblingFile(name) || filepath.Ext(name) != ".json" {
			continue
		}
		r, err := ct.ReadSessionFile(filepath.Join(dir, name))
		if err != nil || r.SessionID == "" {
			continue
		}
		ids = append(ids, r.SessionID)
	}
	return ids, nil
}
