package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/api/participant"
	apistatus "github.com/phillipgreenii/pg-router/api/status"
	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/textsafe"
	"github.com/phillipgreenii/pg-router/schemas"
)

// runStatus implements `pg-router status [--json]` (Task 3.8): the operator
// front door onto the INTF-CLI `status` verb — resolved configuration, live
// deliveries, and per-type queue depths (register row bead pg2-xa44k), plus
// the additive field set the wire reply now carries.
//
// It sends NO `since` — that request field is a long-poll affordance for
// Task 4.0's TUI only (Task 3.8 Binding decisions, Step 6); this subcommand
// always asks for the ring's own default window.
//
// EXIT CODES follow every other operator subcommand: 0 ok, 2 usage, 1
// anything else — BUSY sits at 9 and never collides with usage (ADR 0042's
// Decision). With no core running it FAILS with a "no running core"
// diagnostic and the remedy; it never starts one (ADR 0036).
func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves
	asJSON := fs.Bool("json", false, "emit JSON instead of human-readable text")
	socket := fs.String("socket", "", "path to the running core's socket (overrides discovery)")
	token := fs.String("token", "", "auth token for the running core (with --socket)")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Println(helpText)
		return exitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "status:", err)
		return participant.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "status: unexpected argument:", fs.Arg(0))
		return participant.ExitUsage
	}

	ref, err := locateCore(*socket, *token)
	if err != nil {
		reportNoCore(os.Stderr, core.SubcommandStatus, err)
		return exitUnavailable
	}
	return status(os.Stdout, os.Stderr, *asJSON, ref)
}

// status runs the status call against an already-resolved core ref, so the
// outcome rendering is testable without the process's real stdout/stderr or
// flags.
func status(stdout, stderr io.Writer, asJSON bool, ref core.Ref) int {
	client, err := core.Dial(context.Background(), ref, core.DefaultProbeTimeout)
	if err != nil {
		reportNoCore(stderr, core.SubcommandStatus, err)
		return exitUnavailable
	}
	defer func() { _ = client.Close() }()

	request := []byte(`{"schemaVersion":"` + schemas.SchemaVersion + `"}`)
	reply, code, err := client.Call(context.Background(), core.SubcommandStatus, request, core.CallOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return participant.ExitError
	}
	if code == participant.ExitBusy {
		// The core's own admission-control refusal (Task 3.10): a saturated
		// read semaphore declines a status/mon.read call immediately rather
		// than blocking. Unlike a participant's own pre-accept busy decline
		// (a body-less reply), this refusal always carries a human-readable
		// cli.error envelope — render it and preserve the wire's own exit
		// code, rather than falling into core.DiscriminateReply's generic
		// "core refused" -> exit 1 mapping below, which would otherwise
		// swallow this call's true exit 9.
		fmt.Fprintf(stderr, "status: %s\n", busyRefusalMessage(reply))
		return code
	}

	var st statusReply
	if diagErr := core.DiscriminateReply(reply, core.StatusReplySchema, &st); diagErr != nil {
		fmt.Fprintf(stderr, "status: %v\n", diagErr)
		return participant.ExitError
	}
	if asJSON {
		fmt.Fprintln(stdout, string(reply))
	} else {
		renderStatusText(stdout, ref.Socket, st)
	}
	return code
}

// busyRefusalMessage extracts the human-readable text from the core's
// cli.error envelope on an exit-9 admission refusal (Task 3.10 Binding
// decisions, Step 5), falling back to a generic message if the reply is
// somehow empty or not that shape — defensive; the core always sends the
// envelope for this refusal.
func busyRefusalMessage(reply []byte) string {
	const fallback = "too many concurrent status/mon.read calls in flight; retry"
	if len(reply) == 0 {
		return fallback
	}
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &errBody); err != nil || errBody.Error == "" {
		return fallback
	}
	return errBody.Error
}

// The status reply and its parts are the published contract
// (packages/pg-router/api/status); these names keep the renderers short.
type (
	statusReply        = apistatus.Reply
	queueLogView       = apistatus.QueueLog
	lastCompactionView = apistatus.LastCompaction
	failureView        = apistatus.Failure
	listenerView       = apistatus.Listener
	progressView       = apistatus.Progress
	sourceView         = apistatus.Source
	calendarView       = apistatus.Calendar
	levelView          = apistatus.Level
	pausedRoleView     = apistatus.PausedRole
	failingClauseView  = apistatus.FailingClause
	requiresClauseView = apistatus.RequiresClause
	registryView       = apistatus.Registration
)

// lastCompactionText renders it as one operator line: when, what triggered it, and what it did.
func lastCompactionText(l lastCompactionView) string {
	return fmt.Sprintf("%s (%s): %s -> %s, %d -> %d records, %s",
		shortTimeOf(l.At), textsafe.Sanitize(l.Trigger), humanBytes(l.BytesBefore), humanBytes(l.BytesAfter),
		l.RecordsBefore, l.RecordsAfter, (time.Duration(l.DurationMs) * time.Millisecond).Round(time.Millisecond))
}

// limitView converts v for the shared operator-notice wording (core.LogLimitNotice).
func queueLogLimitView(v queueLogView) core.LogLimitView {
	return core.LogLimitView{
		Bytes: v.Bytes, LimitBytes: v.LimitBytes, SoftBytes: v.SoftBytes, State: v.State,
		RejectedLogFull: v.Rejected.LogFull, RejectedLogUnwritable: v.Rejected.LogUnwritable, Detail: v.Detail,
	}
}

// line renders the one operator line of a calendar query, for example
//
//	weekly-scan  calendar 03:30 sun America/Los_Angeles  last: 2026-11-01T03:30-08:00 fired | next: 2026-11-08T03:30-08:00 | on_missed=run-now
//	weekly-scan  calendar 03:30 sun America/Los_Angeles  last: 2026-11-01T03:30-08:00 SKIPPED (on_missed=next-slot, router not running) | next: ...
//
// The stored outcome fired renders as fired (with its reason, for example late
// by 5s), catch_up as catch-up, skipped as SKIPPED; the reason is shown as stored.
func calendarLine(c calendarView, name string) string {
	last := "no slot handled yet"
	suffix := " | on_missed=" + c.OnMissed
	if l := c.Last; l != nil {
		switch l.Outcome {
		case calstate.OutcomeSkipped:
			// The policy moves inside the parentheses, next to the reason.
			detail := "on_missed=" + c.OnMissed
			if l.Reason != "" {
				detail += ", " + l.Reason
			}
			last = fmt.Sprintf("%s SKIPPED (%s)", l.Slot, detail)
			suffix = ""
		case calstate.OutcomeCatchUp:
			last = withReason(l.Slot+" catch-up", l.Reason)
		default:
			last = withReason(l.Slot+" "+l.Outcome, l.Reason)
		}
	}
	return fmt.Sprintf("%s  %s  last: %s | next: %s%s", name, c.Schedule, last, c.Next, suffix)
}

func withReason(s, reason string) string {
	if reason == "" {
		return s
	}
	return s + " (" + reason + ")"
}

// activityRenderLimit is the human-form "ACTIVITY (last 10)" cap (Task 3.8
// Binding decisions, the operator's 2026-09-01 ruling) — the wire reply
// itself carries the ring's own, larger default window; this is a rendering
// choice of the human-output path only, layered on top of the same read.
const activityRenderLimit = 10

// renderStatusText writes the human-readable status view: header
// (core/socket/config/gates/mode), then QUEUES, DELIVERIES (live), ACTIVITY
// (last 10), LISTENERS, SOURCES, UNMATCHED BINDINGS — the operator's
// 2026-09-01 ruling reordering the frozen text for incident scanning (Task
// 3.8 Binding decisions, superseding both "Flagged for operator"
// questions). No box drawing. Every section other than gates/QUEUES
// (rendered unconditionally) prints an explicit empty marker instead of
// vanishing when it has nothing to say, so "nothing configured" reads
// differently from "the composer silently failed to enumerate this". Every
// rendered dynamic string is routed through textsafe.Sanitize (Task 3.7)
// before it is written; "-" stands in for an absent individual field.
func renderStatusText(w io.Writer, socket string, st statusReply) {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return textsafe.Sanitize(s)
	}

	fmt.Fprintf(w, "socket: %s\n", dash(socket))
	if st.Core != (apistatus.CoreInfo{}) {
		fmt.Fprintf(w, "core: state=%s version=%s pid=%d startedAt=%s configPath=%s\n",
			dash(st.Core.State), dash(st.Core.Version), st.Core.PID, dash(timeText(st.Core.StartedAt)), dash(st.Core.ConfigPath))
	} else {
		fmt.Fprintln(w, "core: -")
	}
	fmt.Fprintf(w, "mode: %s\n", dash(st.Mode))
	if rc := st.ResolvedConfig; rc != nil {
		poll := "-"
		if rc.PollIntervalMs > 0 {
			poll = fmt.Sprintf("%dms", rc.PollIntervalMs)
		}
		fmt.Fprintf(w, "config: repoRoot=%s beadsPrefix=%s pollInterval=%s activeRoles=%d activeQueries=%d\n",
			dash(rc.RepoRoot), dash(rc.BeadsPrefix), poll, rc.ActiveRoles, rc.ActiveQueries)
	} else {
		fmt.Fprintln(w, "config: -")
	}

	if ql := st.QueueLog; ql != (apistatus.QueueLog{}) {
		fmt.Fprintf(w, "queue log: bytes=%d (%s) compactions=%d", ql.Bytes, humanBytes(ql.Bytes), ql.Compactions)
		if ql.LimitBytes > 0 {
			fmt.Fprintf(w, " limit=%d (%s) used=%.1f%% state=%s", ql.LimitBytes, humanBytes(ql.LimitBytes), ql.Percent, dash(ql.State))
		}
		fmt.Fprintln(w)
		if ql.LastCompaction != nil {
			fmt.Fprintf(w, "  last compaction: %s\n", lastCompactionText(*ql.LastCompaction))
		} else {
			fmt.Fprintln(w, "  last compaction: none since this core started")
		}
		// The soft step is not a level, so nothing in LEVELS says it is in force:
		// state what is halted, what still runs, why, and the remedies here.
		for _, line := range core.LogLimitNotice(queueLogLimitView(ql)) {
			fmt.Fprintf(w, "  ! %s\n", line)
		}
	} else {
		fmt.Fprintln(w, "queue log: -")
	}

	fmt.Fprintln(w, "LEVELS:")
	renderLevels(w, st.Levels, time.Now())
	renderPausedRoles(w, st.PausedRoles, time.Now())

	fmt.Fprintln(w, "QUEUES:")
	if len(st.Queues) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, q := range st.Queues {
		fmt.Fprintf(w, "  %s: depth=%d\n", dash(q.Type), q.Depth)
	}

	renderBusy(w, st)

	renderSection(w, "DELIVERIES (live)", len(st.Deliveries), func() {
		for _, d := range st.Deliveries {
			fmt.Fprintf(w, "  %s: handler=%s event=%s\n", dash(d.ID), dash(d.Handler), dash(d.Event))
		}
	})

	activityHeader := "ACTIVITY (last 10)"
	if st.ActivityDropped {
		activityHeader += " (dropped: entries evicted since your last read)"
	}
	renderSection(w, activityHeader, len(st.Activity), func() {
		start := 0
		if n := len(st.Activity); n > activityRenderLimit {
			start = n - activityRenderLimit
		}
		for _, a := range st.Activity[start:] {
			fmt.Fprintf(w, "  #%d %s: %s\n", a.Seq, dash(a.Type), dash(a.Outcome))
		}
	})

	renderSection(w, "LISTENERS", len(st.Listeners), func() {
		for _, l := range st.Listeners {
			backoff := "-"
			if l.Backoff != nil {
				backoff = fmt.Sprintf("streak=%d nextEligible=%s", l.Backoff.Streak, dash(timeText(l.Backoff.NextEligible)))
			}
			fmt.Fprintf(w, "  %s: binds=%v enabled=%t excluded=%t delivered=%d declined=%d backoff=%s",
				dash(l.Role), l.Binds, l.Enabled, l.Excluded, l.Delivered, l.Declined, backoff)
			if l.UnavailableReason != "" {
				fmt.Fprintf(w, " unavailable=%s", dash(l.UnavailableReason))
			}
			fmt.Fprintln(w)
		}
	})

	renderSection(w, "SOURCES", len(st.Sources), func() {
		for _, s := range st.Sources {
			failure := "-"
			if s.Failure != nil {
				failure = fmt.Sprintf("count=%d nextEligible=%s", s.Failure.Count, dash(timeText(s.Failure.NextEligible)))
			}
			fmt.Fprintf(w, "  %s: type=%s enabled=%t excluded=%t mode=%s lastTick=%s failure=%s\n",
				dash(s.Name), dash(s.Type), s.Enabled, s.Excluded, dash(s.Mode), dash(timeText(s.LastTick)), failure)
			if s.Calendar != nil {
				fmt.Fprintf(w, "    %s\n", textsafe.Sanitize(calendarLine(*s.Calendar, s.Name)))
			}
		}
	})

	renderSection(w, "UNMATCHED BINDINGS", len(st.UnmatchedBindings), func() {
		for _, b := range st.UnmatchedBindings {
			fmt.Fprintf(w, "  %s\n", dash(b))
		}
	})
}

// renderPausedRoles writes the PAUSED ROLES block: one line per role a level
// requirement blocks, "<role>  <clause> (<resource> <LEVEL>, <reading> since
// <time>); ...", with "(none)" when no role is paused. A pause stops admission
// only; running work is never touched.
func renderPausedRoles(w io.Writer, paused []pausedRoleView, now time.Time) {
	renderSection(w, fmt.Sprintf("PAUSED ROLES (%d)", len(paused)), len(paused), func() {
		for _, p := range paused {
			fmt.Fprintf(w, "  %s  %s\n", textsafe.Sanitize(p.Role), pausedRoleSummary(p, now))
		}
	})
}

// renderBusy writes the BUSY section (bead pg2-8ij5o): every role and source the
// core calls busy, each with what makes it so. A role is busy while an offer to
// it is in flight, or while busy-kind declines have been counted since its last
// progress; a source is busy while a fetch of it is in flight. The reasons come
// from the same reply rows, never from a decline's reason text.
func renderBusy(w io.Writer, st statusReply) {
	type row struct{ kind, name, why string }
	var rows []row
	for _, l := range st.Listeners {
		if !l.Busy {
			continue
		}
		var why []string
		if l.InFlight {
			why = append(why, "offer in flight")
		}
		if l.Progress != nil && l.Progress.Busy > 0 {
			why = append(why, fmt.Sprintf("%d busy declines since last progress", l.Progress.Busy))
		}
		rows = append(rows, row{"role", l.Role, strings.Join(why, ", ")})
	}
	for _, s := range st.Sources {
		if s.Busy {
			rows = append(rows, row{"source", s.Name, "fetch in flight"})
		}
	}
	renderSection(w, "BUSY", len(rows), func() {
		for _, r := range rows {
			fmt.Fprintf(w, "  %s %s: %s\n", r.kind, textsafe.Sanitize(r.name), r.why)
		}
	})
}

// renderSection prints "NAME:" then either "  (none)" or body() — the
// explicit-empty-marker half of the operator's 2026-09-01 ruling: every
// section other than gates/QUEUES (rendered unconditionally in
// renderStatusText) is silent about NOTHING, never silent by omission.
func renderSection(w io.Writer, name string, n int, body func()) {
	fmt.Fprintln(w, name+":")
	if n == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	body()
}

// humanBytes renders n as a short binary-unit size ("31.7 MiB").
func humanBytes(n int64) string {
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
