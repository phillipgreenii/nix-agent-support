// Package tokenobs turns the credential guard's and the token file store's
// events into the daemon's log events and token metrics. It is the Recorder
// the guard takes (tokenguard.Recorder) and the Observer the store takes
// (tokenfile.Observer), so neither package imports telemetry or the log.
//
// Log events (daemon.log.jsonl): token_read, token_file_written,
// token_link_written, token_file_removed, token_file_stale_removed,
// token_file_touched, token_file_write_failed, token_file_remove_failed and
// auth_degraded. None carries the token or a digest. auth_degraded carries
// only reason, never error_code: the Loki rule that counts auth failures
// reads failure_class and error_code, so it does not see the transition (the
// pg-conn-jira-auth-degraded gauge rule does).
package tokenobs

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenfile"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
)

// Metrics is what the observer counts into (*telemetry.Telemetry).
type Metrics interface {
	TokenRead(reason, result, errorKind string)
	TokenFileOp(op, result string)
}

// Observer implements tokenguard.Recorder and, through StoreEvent,
// tokenfile.Observer. Either field may be nil.
type Observer struct {
	Metrics Metrics
	// Log writes one daemon.log.jsonl line (logs.StructuredLog.Log).
	Log func(change string, kv ...any)
	// FilePath and LinkPath are the token file and the stable link; they are
	// paths, not secrets.
	FilePath, LinkPath string
}

var _ tokenguard.Recorder = (*Observer)(nil)

func (o *Observer) log(change string, kv ...any) {
	if o != nil && o.Log != nil {
		o.Log(change, kv...)
	}
}

func (o *Observer) metrics() Metrics {
	if o == nil {
		return nil
	}
	return o.Metrics
}

// readReason maps the guard's read reasons onto the metric enum: a re-pull
// and a rewrite after the file vanished both follow a failed call.
func readReason(r string) string {
	switch r {
	case tokenguard.ReadStart:
		return "start"
	case tokenguard.ReadBackoff:
		return "backoff"
	default:
		return "auth_error"
	}
}

// Record handles one guard event.
func (o *Observer) Record(ev tokenguard.Event) {
	switch ev.Name {
	case "token_read":
		reason := readReason(ev.Reason)
		result, kind := "changed", "none"
		switch {
		case ev.Result != "ok":
			result, kind = "error", ev.Result
		case ev.Unchanged:
			result = "unchanged"
		}
		if m := o.metrics(); m != nil {
			m.TokenRead(reason, result, kind)
		}
		o.log("token_read", "reason", reason, "result", result, "error_kind", kind)
	case "token_file_write":
		if ev.Result == "ok" {
			if m := o.metrics(); m != nil {
				m.TokenFileOp("write", "ok")
			}
			o.log("token_file_written", "path", o.path(false))
			return
		}
		if m := o.metrics(); m != nil {
			m.TokenFileOp("write", "error")
		}
		o.log("token_file_write_failed", "path", o.path(false), "error", ev.Err)
	case "token_degraded":
		o.log("auth_degraded", "reason", ev.Reason)
	}
}

func (o *Observer) path(link bool) string {
	if o == nil {
		return ""
	}
	if link {
		return o.LinkPath
	}
	return o.FilePath
}

// StoreEvent handles one token file store event (a tokenfile.Observer).
func (o *Observer) StoreEvent(event string) {
	switch event {
	case tokenfile.EventLinkWritten:
		o.log("token_link_written", "path", o.path(true))
	case tokenfile.EventStaleRemoved:
		o.log("token_file_stale_removed", "path", o.path(false))
	case tokenfile.EventRemoved:
		if m := o.metrics(); m != nil {
			m.TokenFileOp("remove", "ok")
		}
		o.log("token_file_removed", "path", o.path(false))
	case tokenfile.EventRemoveFailed:
		if m := o.metrics(); m != nil {
			m.TokenFileOp("remove", "error")
		}
		o.log("token_file_remove_failed", "path", o.path(false))
	case tokenfile.EventTouched:
		o.log("token_file_touched", "path", o.path(false))
	}
}
