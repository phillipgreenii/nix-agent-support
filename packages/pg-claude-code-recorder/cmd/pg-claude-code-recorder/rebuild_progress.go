package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
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
	steps      store.StepStats // cumulative step statistics
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
			r.log.Info("rebuild_progress", append([]any{
				"records_parsed", cur.records,
				"records_per_sec", rate(cur.records-prev.records, secs),
				"commits", cur.commits,
				"commits_per_sec", rate(int64(cur.commits-prev.commits), secs),
				"files_done", cur.filesDone, "files_total", cur.filesTotal,
				"index_db_bytes", fileSize(filepath.Join(r.dir, "index.db")),
				"wal_bytes", fileSize(filepath.Join(r.dir, "index.db-wal")),
				"elapsed_sec", int64(now.Sub(r.start).Seconds()),
			}, stepAttrs(cur.steps.Sub(prev.steps), secs)...)...)
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

// stepAttrs are the per-step seconds of one interval as log attributes. other
// is the interval less the five measured steps, since the registry's other is
// only known once ParseAll ends.
func stepAttrs(d store.StepStats, intervalSec float64) []any {
	known := 0.0
	var out []any
	for i := store.StepRead; i < store.StepOther; i++ {
		known += d.StepSec(i)
		out = append(out, "step_"+obs.Steps[i]+"_sec", d.StepSec(i))
	}
	return append(out, "step_other_sec", max(intervalSec-known, 0))
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
func logRebuildProfile(log *slog.Logger, st *store.Store, slow []parser.SlowFile, dir string, wall, parseWall time.Duration) {
	recs, _ := st.BulkTotals()
	p, err := st.RecordRebuildSummary(wall, recs)
	if err != nil {
		log.Warn("rebuild_profile_not_stored", "err", err.Error())
	}
	log.Info("rebuild_profile", append(profileAttrs(p, slow, wall, parseWall, recs),
		"index_db_bytes", fileSize(filepath.Join(dir, "index.db")),
		"pre_rebuild_bytes", fileSize(filepath.Join(dir, "index.db.pre-rebuild")))...)
}

// profileAttrs are the attributes of the rebuild_profile line. other is
// computed here as parse_sec less the five measured steps, so the six steps
// equal parse_sec by construction.
func profileAttrs(p store.RebuildProfile, slow []parser.SlowFile, wall, parseWall time.Duration, recs int64) []any {
	steps := p.Steps
	known := 0.0
	for i := store.StepRead; i < store.StepOther; i++ {
		known += steps.StepSec(i)
	}
	other := max(parseWall.Seconds()-known, 0)
	a := []any{
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
	}
	for i := store.StepRead; i < store.StepOther; i++ {
		a = append(a, "step_"+obs.Steps[i]+"_sec", steps.StepSec(i))
	}
	a = append(a, "step_other_sec", other,
		"step_other_share", share(other, parseWall.Seconds()),
		"bytes_read", steps.BytesRead, "lines", steps.Lines, "records_inserted", steps.Records)
	for i, n := range obs.InsertTables {
		a = append(a, "insert_"+n+"_sec", steps.TableSec(i), "insert_"+n+"_statements", steps.TableRows[i])
	}
	for i, f := range slow {
		a = append(a, "slow_"+strconv.Itoa(i+1), fmt.Sprintf("%s sec=%.3f bytes=%d", f.Hash, f.Sec, f.Bytes))
	}
	return a
}

func share(n, of float64) float64 {
	if of <= 0 {
		return 0
	}
	return n / of
}
