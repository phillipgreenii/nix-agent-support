package status

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// WriteJSON writes the report as one indented JSON document, the --json form.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes the human form of the report: the same values as the JSON
// document, one "key: value" line each, in sections.
func WriteText(w io.Writer, r Report) error {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("health: %s", r.Health)
	for _, reason := range r.Reasons {
		line("  reason: %s", reason)
	}
	line("source: %s", r.Source)
	line("version: %s", r.Version)
	line("build_id: %s", r.BuildID)
	if r.ClientBuildID != "" {
		line("client_build_id: %s", r.ClientBuildID)
		line("build_differs: %t", r.BuildDiffers)
	}
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
	line("logs:")
	line("  events: %s", r.Logs.Events)
	line("  daemon: %s", r.Logs.Daemon)
	line("  client: %s", r.Logs.Client)
	line("supervisor: %s", r.Supervisor.Kind)
	line("  label: %s", r.Supervisor.Label)
	line("  state: %s", r.Supervisor.State)
	line("instances:")
	for _, i := range r.Instances {
		line("  %s: %s", i.Name, i.Instance)
	}

	if len(r.Budget) > 0 {
		line("budget:")
		for _, bk := range r.Budget {
			line("  %s: spent=%d background_spent=%d in_flight=%d cap=%d background_cap=%d reserve=%d window_seconds=%d",
				bk.Bucket, bk.Spent, bk.BackgroundSpent, bk.InFlight, bk.Cap, bk.BackgroundCap, bk.Reserve, bk.WindowSeconds)
			if bk.Remaining != nil {
				line("    remaining: %d of %d", *bk.Remaining, *bk.Limit)
			}
			if bk.ResetAt != nil {
				line("    reset_at: %s", ts(bk.ResetAt))
			}
			line("    paused: %t", bk.Paused)
			if bk.Paused {
				line("    pause_reason: %s", bk.PauseReason)
				line("    paused_until: %s", ts(bk.PausedUntil))
			}
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
			if q.LastError != "" {
				line("    last_error: %s", q.LastError)
			}
		}
	}
	if r.Auth != nil {
		line("auth: %s", r.Auth.State)
		line("  viewer: %s", r.Auth.Viewer)
		line("  unauthenticated: %t", r.Auth.Unauthenticated)
		if r.Auth.Detail != "" {
			line("  detail: %s", r.Auth.Detail)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func ts(t *time.Time) string { return t.UTC().Format(time.RFC3339) }
