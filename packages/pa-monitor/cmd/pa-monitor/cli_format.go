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
// When at least one session has either field set, every session gets a line:
//
//	<id>  <status>  [error:<kind>]  [nudge:<sources>]
func formatStatusSessions(sessions []*pb.SessionDetail) string {
	if len(sessions) == 0 {
		return ""
	}
	// Determine whether any session has noteworthy data.
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

	var sb strings.Builder
	sb.WriteString("session errors/nudges:\n")
	for _, sd := range sessions {
		if sd == nil {
			continue
		}
		v := sd.GetView()
		sid := "?"
		status := "?"
		if v != nil {
			if v.GetSessionId() != "" {
				sid = v.GetSessionId()
			}
			if v.GetStatus() != "" {
				status = v.GetStatus()
			}
		}
		parts := []string{sid, status}

		le := sd.GetLastError()
		if le != nil && le.GetIsTerminal() {
			parts = append(parts, "error:"+le.GetKind())
		}

		pn := sd.GetPendingNudge()
		if pn != nil && len(pn.GetSources()) > 0 {
			parts = append(parts, "nudge:["+strings.Join(pn.GetSources(), ",")+"]")
		}

		sb.WriteString("  " + strings.Join(parts, "  ") + "\n")
	}
	return sb.String()
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
