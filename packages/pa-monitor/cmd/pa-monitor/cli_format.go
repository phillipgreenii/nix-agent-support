package main

import (
	"fmt"
	"strings"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

// formatStatusSessions renders per-session annotation lines for the `status`
// subcommand. It returns an empty string when no session has a terminal error
// or a pending nudge, keeping the table clean in the common case.
//
// When at least one session has either field set, every session gets a row in
// an aligned table:
//
//	NAME            TERM  STATUS   ERROR   NUDGE
//	feature-x       TMUX  working  -       [auto]
//	a1b2c3d4        CMUX  idle     api     -
//
// NAME is the session's name when set, otherwise the first 8 characters of
// SessionID (mirrors session.Label()). TERM is the upper-cased terminal host
// abbreviation (CMUX, TMUX, GHOSTTY, VSCODE, UNKN). Columns are space-padded
// to the widest value seen so output reads as a table.
func formatStatusSessions(sessions []*pb.SessionDetail) string {
	if len(sessions) == 0 {
		return ""
	}
	anyAnnotated := false
	for _, sd := range sessions {
		if sd == nil {
			continue
		}
		le := sd.GetLastError()
		if le != nil && le.GetIsTerminal() {
			anyAnnotated = true
			break
		}
		pn := sd.GetPendingNudge()
		if pn != nil && len(pn.GetSources()) > 0 {
			anyAnnotated = true
			break
		}
	}
	if !anyAnnotated {
		return ""
	}

	type row struct {
		name, term, status, errKind, nudge string
	}
	header := row{name: "NAME", term: "TERM", status: "STATUS", errKind: "ERROR", nudge: "NUDGE"}
	rows := []row{header}
	for _, sd := range sessions {
		if sd == nil {
			continue
		}
		v := sd.GetView()
		r := row{name: "?", term: "UNKN", status: "?", errKind: "-", nudge: "-"}
		if v != nil {
			r.name = sessionLabel(v)
			r.term = terminalAbbrev(v.GetTerminalHost())
			if v.GetStatus() != "" {
				r.status = v.GetStatus()
			}
		}
		if le := sd.GetLastError(); le != nil && le.GetIsTerminal() {
			r.errKind = le.GetKind()
		}
		if pn := sd.GetPendingNudge(); pn != nil && len(pn.GetSources()) > 0 {
			r.nudge = "[" + strings.Join(pn.GetSources(), ",") + "]"
		}
		rows = append(rows, r)
	}

	nameW, termW, statusW, errW := 0, 0, 0, 0
	for _, r := range rows {
		nameW = max(nameW, len(r.name))
		termW = max(termW, len(r.term))
		statusW = max(statusW, len(r.status))
		errW = max(errW, len(r.errKind))
	}

	var sb strings.Builder
	sb.WriteString("session errors/nudges:\n")
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s  %s\n",
			nameW, r.name,
			termW, r.term,
			statusW, r.status,
			errW, r.errKind,
			r.nudge))
	}
	return sb.String()
}

// sessionLabel returns the display label for a SessionView: Name when set,
// otherwise the first 8 chars of SessionID (or the whole id if shorter).
// Mirrors session.Session.Label() behavior for the CLI.
func sessionLabel(v *pb.SessionView) string {
	if name := v.GetName(); name != "" {
		return name
	}
	sid := v.GetSessionId()
	if len(sid) > 8 {
		return sid[:8]
	}
	if sid == "" {
		return "?"
	}
	return sid
}

// terminalAbbrev maps the daemon-reported terminal_host string into the short
// abbreviations used in CLI tables: CMUX/TMUX/GHOSTTY/VSCODE/UNKN. The cmux
// refinements ("cmux (bridge disconnected)" etc.) collapse to CMUX so the
// column width stays bounded.
func terminalAbbrev(host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.HasPrefix(host, "cmux"):
		return "CMUX"
	case host == "tmux":
		return "TMUX"
	case host == "ghostty":
		return "GHOSTTY"
	case host == "vscode":
		return "VSCODE"
	default:
		return "UNKN"
	}
}

// formatSessionInfo renders the Last error and Pending nudge sections for the
// `info` subcommand. It appends to the base info lines already printed.
// Returns an empty string when neither section applies.
func formatSessionInfo(sd *pb.SessionDetail) string {
	if sd == nil {
		return ""
	}
	var sb strings.Builder

	// Last error section — gated on IsTerminal.
	le := sd.GetLastError()
	if le != nil && le.GetIsTerminal() {
		kindStr := le.GetKind()
		if apiErrorIsEscalated(le) {
			kindStr += "  (escalated)"
		}
		sb.WriteString(fmt.Sprintf("last_error:     %s\n", kindStr))
		errText := le.GetText()
		if len(errText) > 200 {
			errText = errText[:200] + "…"
		}
		if errText != "" {
			sb.WriteString(fmt.Sprintf("                %s\n", errText))
		}
		if ts := le.GetAt(); ts != nil {
			sb.WriteString(fmt.Sprintf("                %s\n", humanizeAgeCLI(time.Since(ts.AsTime()))))
		}
	}

	// Pending nudge section.
	pn := sd.GetPendingNudge()
	if pn != nil && len(pn.GetSources()) > 0 {
		sb.WriteString(fmt.Sprintf("pending_nudge:  [%s]\n", strings.Join(pn.GetSources(), ", ")))
	}

	return sb.String()
}

// apiErrorIsEscalated reports whether an ApiError was escalated by the daemon:
// the kind is inherently retryable (unknown or server_error) but the daemon
// has flipped IsRetryable to false.
func apiErrorIsEscalated(e *pb.ApiError) bool {
	if e == nil {
		return false
	}
	kindRetryable := e.GetKind() == "unknown" || e.GetKind() == "server_error"
	return kindRetryable && !e.GetIsRetryable()
}

// humanizeAgeCLI formats a duration as a human-readable age string.
// Mirrors tui.humanizeAge but is local to the CLI package to avoid coupling.
func humanizeAgeCLI(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < 2*time.Minute:
		return "1 minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "1 hour ago"
	default:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
}
