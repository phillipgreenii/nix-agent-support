package status

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RenderJSON writes the report as one indented JSON document, the --json form.
func RenderJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Render writes the human form of the report: the same values as the JSON
// document, one "key: value" line each, in sections. Equal reports render to
// equal bytes.
func Render(w io.Writer, r Report) error {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("health: %s", r.Health)
	for _, reason := range r.Reasons {
		line("  reason: %s", reason)
	}
	line("source: %s", r.Source)
	line("version: %s", r.Version)
	line("build_id: %s", r.BuildID)
	if r.StartedAt != nil {
		line("started_at: %s", ts(r.StartedAt))
	}
	if r.UptimeSeconds != nil {
		line("uptime_seconds: %d", *r.UptimeSeconds)
	}
	line("state_dir: %s", r.StateDir)
	line("socket: %s", r.Socket)
	line("store: %s", r.Store.Path)
	line("  present: %t", r.Store.Present)
	line("  size_bytes: %d", r.Store.SizeBytes)
	if r.Store.ModifiedAt != nil {
		line("  modified_at: %s", ts(r.Store.ModifiedAt))
	}
	line("  age_seconds: %d", r.Store.AgeSeconds)
	if r.Store.SchemaVersion != 0 {
		line("  schema_version: %d", r.Store.SchemaVersion)
	}
	if r.Store.Identity != "" {
		line("  identity: %s", r.Store.Identity)
	}
	line("logs:")
	line("  events: %s", r.Logs.Events)
	line("  daemon: %s", r.Logs.Daemon)
	line("  client: %s", r.Logs.Client)
	line("supervisor: %s", r.Supervisor.Kind)
	line("  label: %s", r.Supervisor.Label)
	line("  state: %s", r.Supervisor.State)
	if r.Supervisor.RestartHint != "" {
		line("  restart_hint: %s", r.Supervisor.RestartHint)
	}
	if rq := r.Requests; rq != nil {
		line("requests: total=%d", rq.Total)
		line("  window_spent=%d background_spent=%d in_flight=%d cap=%d window_seconds=%d",
			rq.WindowSpent, rq.BackgroundSpent, rq.InFlight, rq.Cap, rq.WindowSeconds)
	}
	if r.Pause != nil {
		line("pause: %s", r.Pause.Reason)
		line("  until: %s", ts(&r.Pause.Until))
	}
	if rl := r.RateLimit; rl != nil {
		line("rate_limit: floor=%d", rl.Floor)
		if rl.ReadingAgeSeconds != nil {
			line("  last_status=%d reading_age_seconds=%d below_floor=%t", rl.LastStatus, *rl.ReadingAgeSeconds, rl.BelowFloor)
		}
		if rl.Remaining != nil {
			line("  remaining=%d headroom=%d", *rl.Remaining, *rl.Headroom)
		}
		if rl.Limit != nil {
			line("  limit=%d", *rl.Limit)
		}
		if rl.NearLimit != nil {
			line("  near_limit=%t", *rl.NearLimit)
		}
		if rl.ResetAt != nil {
			line("  reset_at=%s", ts(rl.ResetAt))
		}
		if rl.Reason != "" {
			line("  reason=%s", rl.Reason)
		}
	}
	if r.Queue != nil {
		line("queue: running=%d", r.Queue.Running)
		for _, c := range r.Queue.Classes {
			line("  class %d %s: depth=%d oldest_overdue_seconds=%d", c.Class, c.Name, c.Depth, c.OldestOverdueSeconds)
		}
	}
	if len(r.Queries) > 0 {
		line("queries:")
		for _, q := range r.Queries {
			ok := "never"
			if q.LastSuccessAt != nil {
				ok = ts(q.LastSuccessAt)
			}
			line("  %s: last_success_at=%s", q.Name, ok)
			if q.LastSuccessAgeSeconds != nil {
				line("    last_success_age_seconds: %d", *q.LastSuccessAgeSeconds)
			}
			if q.LastError != "" {
				line("    last_error: %s", q.LastError)
			}
			if q.BaselineAt != nil {
				line("    baseline_at: %s", ts(q.BaselineAt))
			}
			if q.BaselineOpenAgeSeconds != nil {
				line("    baseline_open_age_seconds: %d", *q.BaselineOpenAgeSeconds)
			}
		}
	}
	if a := r.Auth; a != nil {
		line("auth: %s", a.State)
		line("  identity: %s", a.Identity)
		if a.Detail != "" {
			line("  detail: %s", a.Detail)
		}
	}
	if c := r.Changes; c != nil {
		line("changes:")
		line("  head_seq: %d", c.HeadSeq)
		line("  pending_settle: %d", c.PendingSettle)
		line("  settle_timeouts_last_hour: %d", c.SettleTimeoutsLastHour)
		line("  settle_failures_last_hour: %d", c.SettleFailuresLastHour)
		if len(c.Consumers) > 0 {
			line("  consumers:")
			for _, k := range c.Consumers {
				line("    consumer=%s view=%s scope=%s: acked_seq=%d head_seq=%d lag_rows=%d oldest_unacked_age_seconds=%d seen_at=%s",
					k.Consumer, k.View, k.Scope, k.AckedSeq, k.HeadSeq, k.LagRows, k.OldestUnackedAgeSeconds, ts(&k.SeenAt))
			}
		}
	}
	if t := r.Tracked; t != nil {
		line("tracked: members=%d other_open=%d closed_grace=%d", t.Members, t.OtherOpen, t.ClosedGrace)
		line("  watched: %d", t.Watched)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
