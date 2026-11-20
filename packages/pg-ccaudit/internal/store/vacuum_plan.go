package store

import (
	"fmt"
	"os"
)

// VacuumPlan is the free-space budget for pruning a database.
//
// SQLite documents that VACUUM may need up to about twice the database size in
// free space (a temporary copy of the database, then the copy written back
// through the journal, which in WAL mode is the WAL). The existing WAL is not
// reclaimed until a checkpoint, so it is added on top, plus a safety margin.
type VacuumPlan struct {
	DBBytes     int64 `json:"db_bytes"`
	WALBytes    int64 `json:"wal_bytes"`
	MarginBytes int64 `json:"margin_bytes"`
	// NeededBytes is 2 x DBBytes + WALBytes + MarginBytes.
	NeededBytes int64 `json:"needed_bytes"`
}

const gib = int64(1) << 30

// PlanVacuum measures the database file and its WAL and returns the budget:
// needed = 2 x db + wal + margin, margin = max(10 percent of db, 1 GiB).
func PlanVacuum(dbPath string) (VacuumPlan, error) {
	var p VacuumPlan
	fi, err := os.Stat(dbPath)
	if err != nil {
		return p, fmt.Errorf("stat database: %w", err)
	}
	p.DBBytes = fi.Size()
	if wi, err := os.Stat(dbPath + "-wal"); err == nil {
		p.WALBytes = wi.Size()
	}
	p.MarginBytes = max(p.DBBytes/10, gib)
	p.NeededBytes = 2*p.DBBytes + p.WALBytes + p.MarginBytes
	return p, nil
}
