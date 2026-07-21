package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// recentNudgeWindow is how long after a successful nudge fire the tree row
// continues to show the "✉" marker. Picked long enough to survive normal poll
// jitter and let an operator notice between glances, but short enough that
// it doesn't pollute steady-state rows.
const recentNudgeWindow = 60 * time.Second

// nowFn lets tests inject a deterministic clock for the recent-nudge marker
// logic. Defaults to time.Now.
var nowFn = time.Now

const (
	col1Width    = 12 // model name (sessions) or counts (rollups). Sized so "99● 99○ 99✕" fits without wrap.
	colPctWidth  = 5  // "100%"
	colBarWidth  = 5  // 5-cell bar
	colAmtWidth  = 10 // FmtTok(...) or "$X.XX"
	colBurnWidth = 7  // "1.2M/m"
)

var (
	styleCol1 = lipgloss.NewStyle().Width(col1Width).Align(lipgloss.Right)
	stylePct  = lipgloss.NewStyle().Width(colPctWidth).Align(lipgloss.Right)
	styleBar  = lipgloss.NewStyle().Width(colBarWidth).Align(lipgloss.Right)
	styleAmt  = lipgloss.NewStyle().Width(colAmtWidth).Align(lipgloss.Right)
	styleBurn = lipgloss.NewStyle().Width(colBurnWidth).Align(lipgloss.Right)
)

// renderStatsBlock returns the five-column stats string applied to any row.
// All five values are pre-formatted strings; this helper applies the
// right-alignment styling and joins with single spaces. The total visible
// width equals statsBlockCols.
func renderStatsBlock(col1, pct, bar, amount, burn string) string {
	return fmt.Sprintf(
		"%s %s %s %s %s",
		styleCol1.Render(col1),
		stylePct.Render(pct),
		styleBar.Render(bar),
		styleAmt.Render(amount),
		styleBurn.Render(burn),
	)
}

func labelStyle(termWidth int) lipgloss.Style {
	w := minLabelWidth
	if termWidth > 0 {
		// Account for the 2-col "  " separator the session format places
		// between the styled label and the stats block (in addition to
		// prefixCols, which only covers cursor + branch glyph + the single
		// space after it).
		if dyn := termWidth - prefixCols - 2 - statsBlockCols; dyn > w {
			w = dyn
		}
	}
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Left)
}

type TreeOpts struct {
	ShowAll  bool
	ForceID  bool
	CostMode bool
	// Width is terminal width in columns. When >0 the label column expands so
	// stats columns sit flush right. When 0 (tests/headless) a fixed width is used.
	Width              int
	Cursor             int  // flat 0-based index of highlighted row across all visible sessions
	HasCursor          bool // true shows the cursor marker; false when detail view is open
	Theme              Theme
	TotalSessionTokens int // sum of SessionTokens across all visible sessions; 0 = hide pct
}

// statsBlockCols is the total width of the right-side stats block including
// the single space between each of the five columns:
//
//	col1(12) + sp + pct(5) + sp + bar(5) + sp + amount(10) + sp + burn(7) = 43
const statsBlockCols = col1Width + 1 + colPctWidth + 1 + colBarWidth + 1 + colAmtWidth + 1 + colBurnWidth

// prefixCols accounts for the cursor mark ("  " or "> ") plus the branch glyph
// ("├─") plus the space after it: 2 + 2 + 1 = 5.
const prefixCols = 5

// minLabelWidth keeps rows readable on narrow terminals.
const minLabelWidth = 32

// isDormant reports whether a session renders as "dormant" (the ADR 0024 idle
// age-refinement): an Idle session whose transcript is older than the display
// threshold. Working/Blocked are never dormant. Derived from TranscriptMTime
// (which is on the wire) so display clients need no separate stored flag.
func isDormant(s *aggregate.SessionView) bool {
	return s.Status == session.Idle &&
		session.IsLongIdle(nowFn(), s.TranscriptMTime, session.LongIdleThreshold)
}

func visibleSessions(ss []*aggregate.SessionView, showAll bool) []*aggregate.SessionView {
	if showAll {
		return ss
	}
	out := ss[:0:len(ss)]
	for _, s := range ss {
		if !isDormant(s) {
			out = append(out, s)
		}
	}
	return out
}

// countsString formats the rollup counts column ("3● 1◐ 1○"), omitting zero
// counts (ADR 0024 {working, blocked, idle}). Dormant is no longer a rollup
// bucket — a long-idle session counts as idle. showAll is retained for
// signature stability; it no longer gates a dormant count.
func countsString(workingN, blockedN, idleN int, showAll bool) string {
	parts := []string{}
	if workingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", workingN))
	}
	if blockedN > 0 {
		parts = append(parts, fmt.Sprintf("%d◐", blockedN))
	}
	if idleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", idleN))
	}
	return strings.Join(parts, " ")
}

func renderSession(s *aggregate.SessionView, opts TreeOpts, prefix string, selected bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	sym := sessionGlyph(s, opts.Theme)
	var label string
	if !s.RateLimitResetsAt.IsZero() {
		resetStr := s.SessionEnrichment.RateLimitResetsAt.Local().Format("15:04")
		label = fmt.Sprintf("%s %s %s", sym, resetStr, s.Label(opts.ForceID))
	} else {
		label = fmt.Sprintf("%s %s", sym, s.Label(opts.ForceID))
	}

	col1 := shortModel(s.Model)
	pct := sessionSharePct(s.SessionTokens, opts.TotalSessionTokens)
	pctStr := fmt.Sprintf("%.0f%%", pct)
	barStr := progressBar(pct, colBarWidth)
	amount := FmtTok(s.SessionTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", s.CostUSD)
	}
	burn := fmt.Sprintf("%sk/m", fmtK(s.BurnRateShort))

	tail := ""
	if s.SubagentCount > 0 {
		tail += fmt.Sprintf(" %d🤖", s.SubagentCount)
	}
	if s.SubshellCount > 0 {
		tail += fmt.Sprintf(" %d🐚", s.SubshellCount)
	}

	out := fmt.Sprintf(
		"%s%s %s  %s%s\n",
		cursorMark,
		prefix,
		labelStyle(opts.Width).Render(label),
		renderStatsBlock(col1, pctStr, barStr, amount, burn),
		tail,
	)
	return out
}

func symbol(st session.Status, dormant, awaiting, rateLimited bool, theme Theme) string {
	if rateLimited {
		return "⏸"
	}
	switch st {
	case session.Working:
		return theme.Working.Render("●")
	case session.Blocked:
		// Blocked on human input (AskUserQuestion / permission prompt) renders
		// the distinct "?" awaiting glyph — the human-actionable state the legend
		// documents. Other blockers use the ◐ base: sessionGlyph overlays a more
		// specific error glyph (⊘ auth / ⚠ retryable / ✗ non-retryable) from
		// LastError when the blocker is an error/authn; usage_limit blockers
		// normally carry a RateLimitResetsAt and short-circuit above as ⏸.
		if awaiting {
			return theme.Awaiting.Render("?")
		}
		return theme.Awaiting.Render("◐")
	case session.Idle:
		if awaiting {
			return theme.Awaiting.Render("?")
		}
		if dormant {
			// ☾ (moon, single-width) evokes "sleeping" and is visually distinct
			// from the ✗ non-retryable-error glyph, which ✕ was easily confused
			// with in the legend/tree.
			return theme.Dormant.Render("☾")
		}
		return theme.Idle.Render("○")
	default:
		return theme.Dormant.Render("✕")
	}
}

// authFailed reports a terminal authentication failure (non-retryable; run /login).
func authFailed(le *transcript.ErrorRecord) bool {
	return le != nil && le.IsTerminal && le.Kind == transcript.ErrAuthFailed
}

// sessionGlyph returns the status glyph for a session row, incorporating error
// and nudge-queued indicators. Precedence:
//  1. Working / rate-limited → existing symbol (overrides everything).
//  2. LastError terminal + auth failure → ⊘ (run /login).
//  3. LastError terminal + retryable → ⚠ (retryable error).
//  4. LastError terminal + non-retryable → ✗ (non-retryable / escalated).
//  5. Otherwise → normal idle/dormant glyph.
//
// Nudge markers (independent of the error precedence above) appended to the
// primary glyph when the row is NOT Working / rate-limited:
//   - "↪" when PendingNudge has sources (nudge queued, not yet fired).
//   - "✉" when LastNudgedAt is within recentNudgeWindow AND no nudge is
//     currently pending. Lets the operator see a fire actually happened.
//
// Both markers can co-occur in practice (a fresh queue immediately after a
// fire), and PendingNudge takes visual priority since it represents
// imminent action.
func sessionGlyph(s *aggregate.SessionView, theme Theme) string {
	rateLimited := !s.RateLimitResetsAt.IsZero()
	primary := symbol(s.Status, isDormant(s), s.AwaitingInput, rateLimited, theme)

	// Working (and rate-limited pause which also short-circuits symbol) takes
	// precedence: no error or nudge marker.
	if s.Status == session.Working || rateLimited {
		return primary
	}

	// Apply error glyph when terminal.
	le := s.LastError
	if le != nil && le.IsTerminal {
		switch {
		case authFailed(le):
			primary = theme.Error.Render("⊘") // auth failure — run /login
		case s.LastErrorRetryable:
			primary = "⚠"
		default:
			primary = "✗"
		}
	}

	// Nudge markers.
	hasPending := s.PendingNudge != nil && len(s.PendingNudge.Sources) > 0
	switch {
	case hasPending:
		primary += "↪"
	case !s.LastNudgedAt.IsZero() &&
		nowFn().Sub(s.LastNudgedAt) < recentNudgeWindow:
		primary += "✉"
	}

	return primary
}

func shortModel(m string) string {
	switch {
	case strings.HasPrefix(m, "claude-opus-4-7"):
		return "opus-4-7"
	case strings.HasPrefix(m, "claude-opus"):
		return "opus"
	case strings.HasPrefix(m, "claude-sonnet"):
		return "sonnet"
	case strings.HasPrefix(m, "claude-haiku"):
		return "haiku"
	default:
		return m
	}
}

func sessionSharePct(sessionTokens, totalTokens int) float64 {
	if totalTokens == 0 {
		return 0
	}
	return 100 * float64(sessionTokens) / float64(totalTokens)
}

func FmtTok(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// osc8Link wraps text in an OSC 8 terminal hyperlink to url, using the ST
// (ESC-backslash) terminator. The pinned github.com/charmbracelet/x/ansi
// exposes only SetHyperlink/ResetHyperlink (BEL-terminated) and no Hyperlink
// helper; the ST form is the one render/wrap's ansi.Truncate is proven to keep
// balanced under truncation (wrap.TestLinePreservesOSC8Link) and that
// lipgloss.Width counts as zero-width — so the visible text is all that occupies
// columns. Terminals that don't support OSC 8 simply render the visible text.
func osc8Link(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// RenderPathNode renders one PathNode row with collapse glyph, indentation, and rollup stats.
// selected controls the cursor mark prefix. collapsed controls the ▶/▼ glyph.
func RenderPathNode(n *aggregate.PathNode, opts TreeOpts, selected, collapsed bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	glyph := "▼"
	if collapsed {
		glyph = "▶"
	}
	indent := strings.Repeat("  ", n.Depth)
	// Glyph sits AFTER the depth indent so the collapse structure mirrors the tree.
	label := indent + glyph + " " + n.DisplayPath
	// Branch + clickable PR link (F2). Both are appended to the visible label;
	// the OSC-8 hyperlink is zero-width under lipgloss.Width (the URL and escape
	// bytes don't count), so the row-width math below is unaffected, and the
	// View boundary's wrap.Line (ansi.Truncate) keeps the link balanced under
	// truncation. Theme.Branch styles both (previously defined but unused).
	if n.Branch != "" {
		label += "  " + opts.Theme.Branch.Render(n.Branch)
	}
	if pr := n.PRInfo; pr != nil && pr.Number > 0 {
		prText := fmt.Sprintf("PR#%d", pr.Number)
		if pr.URL != "" {
			prText = osc8Link(pr.URL, prText)
		}
		label += " " + opts.Theme.Branch.Render(prText)
	}

	col1, pct, bar, amount, burn := nodeRollupCols(n, opts)
	stats := renderStatsBlock(col1, pct, bar, amount, burn)

	// Match the session row width (see renderDirRow for the math).
	rowWidth := prefixCols + minLabelWidth + 2 + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	available := rowWidth - 2 // subtract cursor-mark width
	leftWidth := max(available-lipgloss.Width(stats)-1, lipgloss.Width(label))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return cursorMark + pathStyle.Render(label) + " " + stats + "\n"
}

// nodeRollupCols formats the five stat columns for a path-node rollup row.
func nodeRollupCols(n *aggregate.PathNode, opts TreeOpts) (col1, pct, bar, amount, burn string) {
	col1 = countsString(n.WorkingN, n.BlockedN, n.IdleN, opts.ShowAll)

	rollupPct := 0.0
	if opts.TotalSessionTokens > 0 {
		rollupPct = 100 * float64(n.TotalTokens) / float64(opts.TotalSessionTokens)
	}
	pct = fmt.Sprintf("%.0f%%", rollupPct)
	bar = progressBar(rollupPct, colBarWidth)

	amount = FmtTok(n.TotalTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", n.TotalCostUSD)
	}
	burn = fmt.Sprintf("%sk/m", fmtK(n.BurnRateSum))
	return
}
