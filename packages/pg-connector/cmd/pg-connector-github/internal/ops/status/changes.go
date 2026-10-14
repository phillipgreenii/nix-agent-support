package status

import (
	"context"
	"fmt"
	"time"
)

// Changes is the change engine's part of the report (JSON "changes", omitted
// when the engine has nothing to say: an empty log, no pending row, no settle
// timeout or failure in the last hour and no consumer). It is built from a
// ChangeSource and carried by the daemon's report only; the degraded local
// report has none of it.
//
//	head_seq                    the highest change-log sequence number ever
//	                            assigned (absent for an empty log)
//	oldest_seq                  the lowest retained sequence number (absent when
//	                            the log is empty)
//	pending_rows                [{id, awaited, detected_at, deadline}]: the
//	                            settle rows waiting for their groups, soonest
//	                            deadline first. id is the pull request's wire
//	                            reference owner/repo#n when the alias table
//	                            knows it and its node id otherwise; awaited is
//	                            the names of the groups still awaited
//	settle_timeouts_last_hour   the rows ended by their deadline in the last
//	                            hour
//	settle_failures_last_hour   the rows ended with a failed awaited group in
//	                            the last hour
//	consumers                   [{consumer, kind, query, acked_seq, lag_rows,
//	                            oldest_unacked_age_seconds, seen_at}]: one row
//	                            per stored cursor key, from the feed's own
//	                            position accounting
//
// The two last-hour counts are the settle lifecycle's in-memory counts: a
// daemon restart resets them, and the text form says "since start" beside them
// while the daemon has run less than an hour. Nothing here makes the report
// degraded: the section is reported, not judged.
type Changes struct {
	HeadSeq                int64        `json:"head_seq,omitempty"`
	OldestSeq              int64        `json:"oldest_seq,omitempty"`
	PendingRows            []PendingRow `json:"pending_rows,omitempty"`
	SettleTimeoutsLastHour int          `json:"settle_timeouts_last_hour,omitempty"`
	SettleFailuresLastHour int          `json:"settle_failures_last_hour,omitempty"`
	Consumers              []Consumer   `json:"consumers,omitempty"`
}

// PendingRow is one pending settle row.
type PendingRow struct {
	ID         string   `json:"id"`
	Awaited    []string `json:"awaited,omitempty"`
	DetectedAt Time     `json:"detected_at"`
	Deadline   Time     `json:"deadline"`
}

// Consumer is the position of one stored cursor key (consumer, kind, query).
type Consumer struct {
	Consumer                string `json:"consumer"`
	Kind                    string `json:"kind"`
	Query                   string `json:"query"`
	AckedSeq                int64  `json:"acked_seq"`
	LagRows                 int    `json:"lag_rows"`
	OldestUnackedAgeSeconds int64  `json:"oldest_unacked_age_seconds"`
	SeenAt                  Time   `json:"seen_at"`
}

// SettleStats are the settle lifecycle's counts of the last hour.
type SettleStats struct{ Timeouts, Failures int }

// ChangeSource is what the daemon's report reads of the change engine. The
// status package defines it and does not import the engine or the feed: an
// adapter outside the package (internal/changeview) satisfies it from the
// store, the settle lifecycle and the feed. Every time it returns is UTC at
// whole-second precision and every age is computed by the adapter with its own
// clock. A method may be called with no daemon-side lock held.
type ChangeSource interface {
	// ChangeLog is the head and the oldest retained sequence number; oldest is 0
	// for an empty log.
	ChangeLog(ctx context.Context) (head, oldest int64, err error)
	// PendingRows are the pending settle rows, soonest deadline first.
	PendingRows(ctx context.Context) ([]PendingRow, error)
	// SettleStats are the last hour's timeout and failure counts.
	SettleStats(ctx context.Context) (SettleStats, error)
	// Consumers are the positions of the stored cursor keys.
	Consumers(ctx context.Context) ([]Consumer, error)
}

// buildChanges reads the source into the report's section; nil when there is
// nothing to report.
func buildChanges(ctx context.Context, src ChangeSource) (*Changes, error) {
	head, oldest, err := src.ChangeLog(ctx)
	if err != nil {
		return nil, fmt.Errorf("change log: %w", err)
	}
	rows, err := src.PendingRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("pending rows: %w", err)
	}
	stats, err := src.SettleStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("settle stats: %w", err)
	}
	consumers, err := src.Consumers(ctx)
	if err != nil {
		return nil, fmt.Errorf("consumer positions: %w", err)
	}
	c := &Changes{
		HeadSeq: head, OldestSeq: oldest, PendingRows: rows,
		SettleTimeoutsLastHour: stats.Timeouts, SettleFailuresLastHour: stats.Failures,
		Consumers: consumers,
	}
	if c.HeadSeq == 0 && c.OldestSeq == 0 && len(c.PendingRows) == 0 && len(c.Consumers) == 0 &&
		c.SettleTimeoutsLastHour == 0 && c.SettleFailuresLastHour == 0 {
		return nil, nil
	}
	return c, nil
}

// settleWindow is the lifecycle's counting window; a daemon that has run for
// less than this reports its counts "since start".
const settleWindow = time.Hour
