package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
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
			// ADR 0024 D1: qualify a blocked session with its blocker
			// ("blocked/usage_limit") so the reason is visible in the table.
			if s := formatStatusWithBlocker(v); s != "" {
				r.status = s
			}
		}
		if le := sd.GetLastError(); le != nil && le.GetIsTerminal() {
			if apiErrorIsAuthFailure(le) {
				r.errKind = "auth"
			} else {
				r.errKind = le.GetKind()
			}
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
		fmt.Fprintf(&sb, "  %-*s  %-*s  %-*s  %-*s  %s\n",
			nameW, r.name,
			termW, r.term,
			statusW, r.status,
			errW, r.errKind,
			r.nudge)
	}
	return sb.String()
}

// formatStatusWithBlocker renders a SessionView's ADR 0024 status qualified by
// its blocker — "blocked/usage_limit", "blocked/human_input" — and the bare
// status otherwise (a blocker is present ONLY when status == "blocked", D1).
//
// This is the single status/blocker word form for every CLI surface: the
// `status` subcommand's session table and the `info session:` header both call
// it, so the two can never drift. Returns "" when the view carries no status at
// all, letting each caller supply its own placeholder.
func formatStatusWithBlocker(v *pb.SessionView) string {
	if v == nil {
		return ""
	}
	st := v.GetStatus()
	if st == "" {
		return ""
	}
	if b := v.GetBlocker(); b != "" {
		return st + "/" + b
	}
	return st
}

// dirSessionCounts returns the ADR 0024 {working, blocked, idle} rollup for one
// wire Directory.
//
// The retired dormant_n (proto field 7, ADR 0024 R8) is FOLDED INTO IDLE and is
// never reported on its own: it has no writer anywhere, so a current daemon
// always sends 0, and an older daemon's dormant sessions are plain idle under
// the new model. Reading it as a standalone count prints a permanent 0 and
// hides the blocked sessions entirely (bead pg2-vsrxf).
func dirSessionCounts(d *pb.Directory) (working, blocked, idle int) {
	if d == nil {
		return 0, 0, 0
	}
	return int(d.GetWorkingN()),
		int(d.GetBlockedN()),
		int(d.GetIdleN()) + int(d.GetDormantN())
}

// formatSessionCounts renders the ADR 0024 {working, blocked, idle} rollup in
// the one word form shared by the `status` and `info path:` surfaces.
func formatSessionCounts(working, blocked, idle int) string {
	return fmt.Sprintf("%d working, %d blocked, %d idle", working, blocked, idle)
}

// formatPathRollup renders the whole `info path:` directory block. The session
// line carries the ADR 0024 {working, blocked, idle} counts via
// dirSessionCounts — never the retired dormant count.
func formatPathRollup(d *pb.Directory) string {
	if d == nil {
		return ""
	}
	working, blocked, idle := dirSessionCounts(d)
	var sb strings.Builder
	fmt.Fprintf(&sb, "path:     %s\n", d.GetPath())
	fmt.Fprintf(&sb, "branch:   %s\n", d.GetBranch())
	fmt.Fprintf(&sb, "sessions: %s\n", formatSessionCounts(working, blocked, idle))
	fmt.Fprintf(&sb, "tokens:   %d\n", d.GetTotalTokens())
	fmt.Fprintf(&sb, "cost:     $%.2f\n", d.GetTotalCostUsd())
	return sb.String()
}

// formatSessionInfoHeader renders the fixed header lines of `info session:`.
// The status line uses formatStatusWithBlocker, so a blocked session shows the
// blocker in the same form the `status` subcommand's table uses (ADR 0024 D1).
// The trailing last_error / pending_nudge sections live in formatSessionInfo.
func formatSessionInfoHeader(v *pb.SessionView) string {
	if v == nil {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "session_id:     %s\n", v.GetSessionId())
	fmt.Fprintf(&sb, "status:         %s\n", formatStatusWithBlocker(v))
	fmt.Fprintf(&sb, "model:          %s\n", v.GetModel())
	fmt.Fprintf(&sb, "cwd:            %s\n", v.GetCwd())
	fmt.Fprintf(&sb, "branch:         %s\n", v.GetBranch())
	// workspace.scope from the persisted label set (pg2-4xbrm); shown only when
	// present so an unlabeled session has no empty line.
	if scope := v.GetLabels()["workspace.scope"]; scope != "" {
		fmt.Fprintf(&sb, "scope:          %s\n", scope)
	}
	fmt.Fprintf(&sb, "context_tokens: %d\n", v.GetContextTokens())
	fmt.Fprintf(&sb, "subagents:      %d\n", v.GetSubagentCount())
	fmt.Fprintf(&sb, "subshells:      %d\n", v.GetSubshellCount())
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

// terminalAbbrev is a thin alias kept for call-site readability. The actual
// mapping lives on the session package so the daemon's OTel emitter shares it.
func terminalAbbrev(host string) string {
	return session.TerminalAbbrev(host)
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
		if apiErrorIsAuthFailure(le) {
			kindStr += " — run /login"
		} else if apiErrorIsEscalated(le) {
			kindStr += "  (escalated)"
		}
		fmt.Fprintf(&sb, "last_error:     %s\n", kindStr)
		errText := le.GetText()
		if len(errText) > 200 {
			errText = errText[:200] + "…"
		}
		if errText != "" {
			fmt.Fprintf(&sb, "                %s\n", errText)
		}
		if ts := le.GetAt(); ts != nil {
			fmt.Fprintf(&sb, "                %s\n", humanizeAgeCLI(time.Since(ts.AsTime())))
		}
	}

	// Pending nudge section.
	pn := sd.GetPendingNudge()
	if pn != nil && len(pn.GetSources()) > 0 {
		fmt.Fprintf(&sb, "pending_nudge:  [%s]\n", strings.Join(pn.GetSources(), ", "))
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

// apiErrorIsAuthFailure reports a terminal HTTP-401 authentication failure.
// Literal kind string matches the apiErrorIsEscalated convention (no
// cmd → internal/core/transcript import).
func apiErrorIsAuthFailure(e *pb.ApiError) bool {
	return e != nil && e.GetIsTerminal() && e.GetKind() == "authentication_failed"
}

// formatAuthFailureBanner returns a prominent one-line warning when any session
// has a terminal auth failure, else "". A 401 is account-wide, so this is shown
// near the top of `status`.
func formatAuthFailureBanner(sessions []*pb.SessionDetail) string {
	n := 0
	for _, sd := range sessions {
		if sd == nil {
			continue
		}
		if apiErrorIsAuthFailure(sd.GetLastError()) {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	noun := "session"
	if n != 1 {
		noun = "sessions"
	}
	return fmt.Sprintf("⊘ authentication failure — run /login (%d %s)\n", n, noun)
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
