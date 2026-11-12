package store

import (
	"context"
	"encoding/json"
	"time"
)

const metaRebuildProfile = "last_rebuild_profile"

// RebuildProfile is the short profile of a bulk rebuild: where its time went
// once the parse ended. The parse itself is the wall time minus these, and
// CommitSec is the part of the parse spent committing batches.
type RebuildProfile struct {
	Commits       int     `json:"commits"`
	CommitSec     float64 `json:"batch_commit_sec"`
	IndexBuildSec float64 `json:"index_build_sec"`
	FTSOptimize   float64 `json:"fts_optimize_sec"`
	CheckpointSec float64 `json:"checkpoint_sec"`
	FinishSec     float64 `json:"finish_sec"`
	// Steps is where the parse time went: seconds per step (read, parse,
	// insert, savepoint, commit; other is the remainder of the parse wall, set
	// by the caller that knows it), the insert table split and the work done.
	Steps StepStats `json:"steps"`
}

// RebuildProfile returns the timings measured so far by the batch commits and
// FinishRebuild.
func (s *Store) RebuildProfile() RebuildProfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.prof
	p.Commits = s.bCommits
	p.Steps = s.Steps()
	return p
}

// RecordRebuildSummary stores the rebuild's wall time, rate and profile as one
// JSON meta row (last_rebuild_profile), so the operator can read them from
// index.db after the daemon restarts, and returns the profile it stored.
func (s *Store) RecordRebuildSummary(wall time.Duration, records int64) (RebuildProfile, error) {
	ctx := context.Background()
	p := s.RebuildProfile()
	rate := 0.0
	if wall > 0 {
		rate = float64(records) / wall.Seconds()
	}
	b, err := json.Marshal(struct {
		WallSec       float64 `json:"wall_sec"`
		Records       int64   `json:"records"`
		RecordsPerSec float64 `json:"records_per_sec"`
		RebuildProfile
	}{wall.Seconds(), records, rate, p})
	if err != nil {
		return p, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.wdb.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, metaRebuildProfile, string(b))
	return p, err
}
