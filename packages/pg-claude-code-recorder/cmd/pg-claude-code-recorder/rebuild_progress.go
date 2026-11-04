package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// defaultRebuildProgressEvery is how often the rebuild command logs a
// progress line.
const defaultRebuildProgressEvery = 30 * time.Second

// rebuildSample is one reading of the rebuild's counters.
type rebuildSample struct {
	records    int64
	commits    int
	filesDone  int
	filesTotal int
}

// rebuildReporter logs a progress line every interval and a final summary.
// The rebuild opens no metrics endpoint, so the numbers are log fields only.
type rebuildReporter struct {
	log      *slog.Logger
	dir      string
	interval time.Duration
	read     func() rebuildSample
	start    time.Time
	stop     chan struct{}
	done     chan struct{}
}

func startRebuildReporter(log *slog.Logger, dir string, interval time.Duration, read func() rebuildSample) *rebuildReporter {
	r := &rebuildReporter{log: log, dir: dir, interval: interval, read: read, start: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	go r.loop()
	return r
}

func (r *rebuildReporter) loop() {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	prev, prevAt := rebuildSample{}, r.start
	for {
		select {
		case <-r.stop:
			return
		case now := <-t.C:
			cur := r.read()
			secs := now.Sub(prevAt).Seconds()
			r.log.Info("rebuild_progress",
				"records_parsed", cur.records,
				"records_per_sec", rate(cur.records-prev.records, secs),
				"commits", cur.commits,
				"commits_per_sec", rate(int64(cur.commits-prev.commits), secs),
				"files_done", cur.filesDone, "files_total", cur.filesTotal,
				"index_db_bytes", fileSize(filepath.Join(r.dir, "index.db")),
				"wal_bytes", fileSize(filepath.Join(r.dir, "index.db-wal")),
				"elapsed_sec", int64(now.Sub(r.start).Seconds()))
			prev, prevAt = cur, now
		}
	}
}

// finish stops the progress lines and logs the summary.
func (r *rebuildReporter) finish() {
	close(r.stop)
	<-r.done
	cur := r.read()
	wall := time.Since(r.start)
	r.log.Info("rebuild_summary",
		"records_parsed", cur.records,
		"commits", cur.commits,
		"files_done", cur.filesDone, "files_total", cur.filesTotal,
		"index_db_bytes", fileSize(filepath.Join(r.dir, "index.db")),
		"wall_sec", wall.Seconds(),
		"records_per_sec", rate(cur.records, wall.Seconds()),
		"commits_per_sec", rate(int64(cur.commits), wall.Seconds()))
}

func rate(n int64, secs float64) float64 {
	if secs <= 0 {
		return 0
	}
	return float64(n) / secs
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// logRebuildProfile logs the finished rebuild's wall time, rate and short
// profile as one rebuild_profile line and stores the same numbers in index.db
// (meta key last_rebuild_profile), so the operator can read them after the
// live rebuild. wall covers the parse and the finish; parseWall is the parse.
func logRebuildProfile(log *slog.Logger, st *store.Store, dir string, wall, parseWall time.Duration) {
	recs, _ := st.BulkTotals()
	p, err := st.RecordRebuildSummary(wall, recs)
	if err != nil {
		log.Warn("rebuild_profile_not_stored", "err", err.Error())
	}
	log.Info("rebuild_profile",
		"wall_sec", wall.Seconds(),
		"parse_sec", parseWall.Seconds(),
		"finish_sec", p.FinishSec,
		"records", recs,
		"records_per_sec", rate(recs, wall.Seconds()),
		"batch_commits", p.Commits,
		"batch_commit_sec", p.CommitSec,
		"index_build_sec", p.IndexBuildSec,
		"fts_optimize_sec", p.FTSOptimize,
		"checkpoint_sec", p.CheckpointSec,
		"index_db_bytes", fileSize(filepath.Join(dir, "index.db")),
		"pre_rebuild_bytes", fileSize(filepath.Join(dir, "index.db.pre-rebuild")))
}
