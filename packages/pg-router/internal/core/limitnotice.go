package core

import (
	"fmt"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// This file is the ONE place the operator-facing wording of the event-log size
// limit lives (bead pg2-5d3ui). `pg-router status` and the TUI both render it from
// the status reply's `queueLog` object, so the two surfaces cannot drift into two
// different accounts of what is halted and what still runs.
//
// The soft step is NOT a gate, so nothing else (no gate list, no PAUSED banner)
// tells the operator it is in force — this text is its whole surface, and it MUST
// say plainly what is HALTED, what still RUNS, why, and what to do.

// LogLimitView is the `queueLog` object of the status reply as a consumer holds
// it. The zero value (State "" or "ok") renders no notice.
type LogLimitView struct {
	Bytes, LimitBytes, SoftBytes int64
	State                        string
	RejectedLogFull              int64
	RejectedLogUnwritable        int64
	Detail                       string
}

// LogLimitRemedies is the actionable remedy text shared by every non-ok state.
// There is deliberately no "purge" command: the real remedies are these.
// `pg-router log compact` only reclaims dead (evicted) history, never a queued
// event, so it helps when the log is large because of churn and not when the
// backlog itself is.
const LogLimitRemedies = "wait for queued events to expire (the log is compacted automatically); " +
	"run `pg-router log compact` (or restart pg-router) to compact the log now - `pg-router log compact --dry-run` previews what it would reclaim; " +
	"raise PG_ROUTER_MAX_LOG_BYTES (or [pool].max_log_bytes) and restart; " +
	"or stop the daemon and move queue.jsonl aside (this LOSES the queued events)"

// LogLimitHealthy reports whether v is in the ok state (or carries none).
func LogLimitHealthy(v LogLimitView) bool {
	return v.State == "" || v.State == eventqueue.StateOK
}

// LogLimitNotice returns the notice lines for v, or nil when the log is healthy.
// Line 1 is the headline (state and numbers), then HALTED, STILL RUNNING and the
// remedies, each as its own line so a renderer can wrap or clip them.
func LogLimitNotice(v LogLimitView) []string {
	if LogLimitHealthy(v) {
		return nil
	}
	size := fmt.Sprintf("%s of %s", humanSize(v.Bytes), humanSize(v.LimitBytes))
	if v.LimitBytes > 0 {
		size += fmt.Sprintf(" (%.0f%%)", float64(v.Bytes)*100/float64(v.LimitBytes))
	}
	const polled = "polled command-source emitters"
	switch v.State {
	case eventqueue.StateEmittersHalted:
		return []string{
			fmt.Sprintf("LOG LIMIT: the event log is at %s, past the soft threshold of %s.", size, humanSize(v.SoftBytes)),
			"HALTED: " + polled + " are NOT being polled, so no new work is discovered from them.",
			"STILL RUNNING: listener dispatch and drain, timer emitters, and pushed events. Only the hard limit stops those.",
			"Remedies: " + LogLimitRemedies + ".",
		}
	case eventqueue.StateLogFull:
		return []string{
			fmt.Sprintf("LOG FULL: the event log is at %s, at its hard limit.", size),
			fmt.Sprintf("HALTED: %s, AND all new events — timer emitters and pushed events are rejected with `log_full` (%d rejected so far).", polled, v.RejectedLogFull),
			"STILL RUNNING: listener dispatch and drain; accept, evict and gate records still append, so space can be reclaimed.",
			"Remedies: " + LogLimitRemedies + ".",
		}
	case eventqueue.StateLogUnwritable:
		detail := v.Detail
		if detail == "" {
			detail = "write failed"
		}
		return []string{
			fmt.Sprintf("LOG UNWRITABLE: pg-router cannot write its event log (%s); it is at %s.", detail, size),
			fmt.Sprintf("HALTED: %s, AND all new events — timer emitters and pushed events are rejected with `log_unwritable` (%d rejected so far).", polled, v.RejectedLogUnwritable),
			"STILL RUNNING: listener dispatch of events already queued; accept and evict records may not persist, so some events can be re-offered after a restart.",
			"Recovery is automatic: the log is re-probed every tick and resumes once writable. Free disk space or fix permissions on the log directory; " +
				"if the log itself is oversized, " + LogLimitRemedies + ".",
		}
	}
	return nil
}

// humanSize renders n bytes in binary units ("31.7 MiB"); 0 limit renders "no limit".
func humanSize(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
