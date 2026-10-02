package core

import (
	"io"
	"math"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/schemas"
)

// This file is the core's `log-compact` socket verb (bead pg2-maxn1, the CLI half
// of pg2-8e0m6): the request `pg-router log compact [--dry-run]` makes of a
// RUNNING daemon. The daemon owns the exclusive lock on queue.jsonl
// (eventqueue/lock.go), so a second process must never touch the file; the
// compaction therefore runs INSIDE the daemon, through the same Queue.compact path
// the startup and threshold triggers use (trigger "manual"), and is serialized
// against them by the store's own mutexes.
//
// With no daemon running the CLI compacts offline instead (eventqueue.
// CompactFileOffline), taking the lock itself; that path builds the SAME
// LogCompactView so a script reading --json cannot tell the two apart except by
// `via`.

// SubcommandLogCompact is the INTF-CLI socket verb behind `pg-router log compact`.
const SubcommandLogCompact = "log-compact"

// The message types backing the log-compact verb (schemas/, checked via package
// conformance — INV-INTF-2).
const (
	LogCompactRequestSchema = "cli.log-compact"
	LogCompactReplySchema   = "cli.log-compact-reply"
)

// LogCompactCallTimeout bounds one log-compact round trip: the core's connection
// deadline for the verb (serverDeadlineFor) and the CLI's call timeout. The default
// 5 s of every other verb is too short for a fold-write-fsync-rename of a large log,
// and a reply lost to a deadline would hide a compaction that did happen.
const LogCompactCallTimeout = 2 * time.Minute

// LogCompact `via` values: where a compaction ran.
const (
	ViaDaemon  = "daemon"
	ViaOffline = "offline"
)

// LogCompactView is the cli.log-compact-reply object, as the daemon sends it, the
// offline CLI builds it and a consumer reads it.
type LogCompactView struct {
	SchemaVersion string   `json:"schemaVersion"`
	DryRun        bool     `json:"dryRun"`
	Compacted     bool     `json:"compacted"`
	NoProgress    bool     `json:"noProgress"`
	Via           string   `json:"via"`
	BytesBefore   int64    `json:"bytesBefore"`
	BytesAfter    int64    `json:"bytesAfter"`
	RecordsBefore int      `json:"recordsBefore"`
	RecordsAfter  int      `json:"recordsAfter"`
	EventsKept    int      `json:"eventsKept"`
	EventsDropped int      `json:"eventsDropped"`
	GatesKept     int      `json:"gatesKept"`
	Torn          bool     `json:"torn,omitempty"`
	DurationMs    *int64   `json:"durationMs,omitempty"`
	LimitBytes    int64    `json:"limitBytes,omitempty"`
	PercentBefore *float64 `json:"percentBefore,omitempty"`
	PercentAfter  *float64 `json:"percentAfter,omitempty"`
	WouldRefuse   string   `json:"wouldRefuse,omitempty"`
}

// NewLogCompactView renders a compaction result for the wire. limitBytes is
// max_log_bytes (0: not configured, so no percentages). For a real run the sizes
// and record counts are the run's own; the event and gate counts are the plan's
// (the fold the run was about to execute).
func NewLogCompactView(res eventqueue.CompactResult, limitBytes int64, via string) LogCompactView {
	v := LogCompactView{
		SchemaVersion: schemas.SchemaVersion,
		DryRun:        res.DryRun,
		Compacted:     res.Compacted,
		NoProgress:    res.Plan.NoProgress(),
		Via:           via,
		BytesBefore:   res.Plan.BytesBefore,
		BytesAfter:    res.Plan.BytesAfter,
		RecordsBefore: res.Plan.RecordsBefore,
		RecordsAfter:  res.Plan.RecordsAfter,
		EventsKept:    res.Plan.EventsKept,
		EventsDropped: res.Plan.EventsDropped,
		GatesKept:     res.Plan.GatesKept,
		Torn:          res.Plan.Torn,
	}
	if res.Compacted {
		v.BytesBefore, v.BytesAfter = res.Stats.BytesBefore, res.Stats.BytesAfter
		v.RecordsBefore, v.RecordsAfter = res.Stats.RecordsBefore, res.Stats.RecordsAfter
		ms := res.Stats.Duration.Milliseconds()
		v.DurationMs = &ms
	}
	if limitBytes > 0 {
		v.LimitBytes = limitBytes
		before, after := percentOf(v.BytesBefore, limitBytes), percentOf(v.BytesAfter, limitBytes)
		v.PercentBefore, v.PercentAfter = &before, &after
	}
	return v
}

// percentOf is n as a percentage of limit, rounded to one decimal (the precision
// status reports its own percent at).
func percentOf(n, limit int64) float64 {
	return math.Round(float64(n)*1000/float64(limit)) / 10
}

// handleLogCompact runs the `log-compact` socket verb.
func (s *Service) handleLogCompact(stdin io.Reader, stdout io.Writer) int {
	var req struct {
		DryRun bool `json:"dryRun"`
	}
	if !decodeVerb(stdin, stdout, SubcommandLogCompact, LogCompactRequestSchema, &req) {
		return conformance.ExitError
	}
	res, err := s.q.CompactManual(req.DryRun)
	if err != nil {
		writeBody(stdout, errorReply(SubcommandLogCompact+": "+err.Error()))
		return conformance.ExitError
	}
	view := NewLogCompactView(res, s.q.LimitStatus().HardBytes, ViaDaemon)
	return writeJSONValue(stdout, SubcommandLogCompact, view)
}

// lastCompactionStatus renders the status reply's queueLog.lastCompaction object,
// or nil before this process's first compaction.
func lastCompactionStatus(q *eventqueue.Queue) map[string]any {
	info, ok := q.LastCompaction()
	if !ok {
		return nil
	}
	return map[string]any{
		"at":            info.At.UTC().Format(time.RFC3339Nano),
		"trigger":       info.Trigger,
		"bytesBefore":   info.BytesBefore,
		"bytesAfter":    info.BytesAfter,
		"recordsBefore": info.RecordsBefore,
		"recordsAfter":  info.RecordsAfter,
		"durationMs":    info.Duration.Milliseconds(),
	}
}
