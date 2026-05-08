package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

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
	return fmt.Sprintf("%s %s %s %s %s",
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
//   col1(12) + sp + pct(5) + sp + bar(5) + sp + amount(10) + sp + burn(7) = 43
const statsBlockCols = col1Width + 1 + colPctWidth + 1 + colBarWidth + 1 + colAmtWidth + 1 + colBurnWidth

// prefixCols accounts for the cursor mark ("  " or "> ") plus the branch glyph
// ("├─") plus the space after it: 2 + 2 + 1 = 5.
const prefixCols = 5

// minLabelWidth keeps rows readable on narrow terminals.
const minLabelWidth = 32

func Tree(tree *aggregate.Tree, opts TreeOpts) string {
	var sb strings.Builder
	rowIdx := 0
	for _, d := range tree.Dirs {
		visible := visibleSessions(d.Sessions, opts.ShowAll)
		if len(visible) == 0 {
			continue
		}
		sb.WriteString(renderDirRow(d, opts))
		for i, s := range visible {
			prefix := "├─"
			cont := "│"
			if i == len(visible)-1 {
				prefix = "└─"
				cont = " "
			}
			selected := opts.HasCursor && rowIdx == opts.Cursor
			sb.WriteString(renderSession(s, opts, prefix, cont, selected))
			rowIdx++
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func visibleSessions(ss []*aggregate.SessionView, showAll bool) []*aggregate.SessionView {
	if showAll {
		return ss
	}
	out := ss[:0:len(ss)]
	for _, s := range ss {
		if s.Status != session.Dormant {
			out = append(out, s)
		}
	}
	return out
}

// dirRollupCols formats the five stat columns for a directory rollup row.
// Counts go into col1; %, bar, amount, and burn share the same grid as session rows.
func dirRollupCols(d *aggregate.Directory, opts TreeOpts) (col1, pct, bar, amount, burn string) {
	col1 = countsString(d.WorkingN, d.IdleN, d.DormantN, opts.ShowAll)

	rollupPct := 0.0
	if opts.TotalSessionTokens > 0 {
		rollupPct = 100 * float64(d.TotalTokens) / float64(opts.TotalSessionTokens)
	}
	pct = fmt.Sprintf("%.0f%%", rollupPct)
	bar = progressBar(rollupPct, colBarWidth)

	amount = FmtTok(d.TotalTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", d.TotalCostUSD)
	}
	burn = fmt.Sprintf("%sk/m", fmtK(d.BurnRateSum))
	return
}

// countsString formats the rollup counts column ("3● 1○ 1✕"), omitting zero
// counts and dormant unless ShowAll is on.
func countsString(workingN, idleN, dormantN int, showAll bool) string {
	parts := []string{}
	if workingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", workingN))
	}
	if idleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", idleN))
	}
	if dormantN > 0 && showAll {
		parts = append(parts, fmt.Sprintf("%d✕", dormantN))
	}
	return strings.Join(parts, " ")
}

func renderDirRow(d *aggregate.Directory, opts TreeOpts) string {
	col1, pct, bar, amount, burn := dirRollupCols(d, opts)
	stats := renderStatsBlock(col1, pct, bar, amount, burn)

	// Match the session row's visible width: prefix(5) + label(32) + "  "(2)
	// + stats(41). prefixCols already covers cursor + branch glyph + the
	// single space after it; the extra 2 cols here are the "  " separator
	// the session format puts between the label and the stats block.
	rowWidth := prefixCols + minLabelWidth + 2 + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}

	branchStr := ""
	if d.Branch != "" {
		branchStr = "  🌿 " + opts.Theme.Branch.Render(d.Branch)
		if d.PRInfo != nil {
			prNum := osc8Link(d.PRInfo.URL, fmt.Sprintf("#%d", d.PRInfo.Number))
			prTitle := wrap.Line(d.PRInfo.Title, 40)
			branchStr += "  →  " + prNum + " " + prTitle
		}
	}

	leftWidth := max(rowWidth-lipgloss.Width(stats)-1, lipgloss.Width(d.Path))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return pathStyle.Render(d.Path+branchStr) + " " + stats + "\n"
}

func renderSession(s *aggregate.SessionView, opts TreeOpts, prefix, cont string, selected bool) string {
	cursorMark := "  "
	if selected {
		cursorMark = opts.Theme.Cursor.Render(">") + " "
	}
	sym := symbol(s.Status, s.SessionEnrichment.AwaitingInput, !s.SessionEnrichment.RateLimitResetsAt.IsZero(), opts.Theme)
	var label string
	if !s.SessionEnrichment.RateLimitResetsAt.IsZero() {
		resetStr := s.SessionEnrichment.RateLimitResetsAt.Local().Format("15:04")
		label = fmt.Sprintf("%s %s %s", sym, resetStr, s.Label(opts.ForceID))
	} else {
		label = fmt.Sprintf("%s %s", sym, s.Label(opts.ForceID))
	}

	col1 := shortModel(s.SessionEnrichment.Model)
	pct := sessionSharePct(s.SessionEnrichment.SessionTokens, opts.TotalSessionTokens)
	pctStr := fmt.Sprintf("%.0f%%", pct)
	barStr := progressBar(pct, colBarWidth)
	amount := FmtTok(s.SessionEnrichment.SessionTokens)
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", s.SessionEnrichment.CostUSD)
	}
	burn := fmt.Sprintf("%sk/m", fmtK(s.SessionEnrichment.BurnRateShort))

	tail := ""
	if s.SessionEnrichment.SubagentCount > 0 {
		tail += fmt.Sprintf(" %d🤖", s.SessionEnrichment.SubagentCount)
	}
	if s.SessionEnrichment.SubshellCount > 0 {
		tail += fmt.Sprintf(" %d🐚", s.SessionEnrichment.SubshellCount)
	}

	out := fmt.Sprintf("%s%s %s  %s%s\n",
		cursorMark,
		prefix,
		labelStyle(opts.Width).Render(label),
		renderStatsBlock(col1, pctStr, barStr, amount, burn),
		tail,
	)
	if s.SessionEnrichment.FirstPrompt != "" {
		out += fmt.Sprintf("  %s    ↳ %s\n", cont, opts.Theme.Prompt.Render(fmt.Sprintf("%q", wrap.Line(s.SessionEnrichment.FirstPrompt, 80))))
	}
	return out
}

func symbol(st session.Status, awaiting bool, rateLimited bool, theme Theme) string {
	if rateLimited {
		return "⏸"
	}
	switch st {
	case session.Working:
		return theme.Working.Render("●")
	case session.Idle:
		if awaiting {
			return theme.Awaiting.Render("?")
		}
		return theme.Idle.Render("○")
	default:
		return theme.Dormant.Render("✕")
	}
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

// osc8Link wraps text in an OSC 8 terminal hyperlink. Terminals that support
// OSC 8 (iTerm2, Kitty, WezTerm, GNOME Terminal) render text as a clickable
// link. lipgloss v1.1.0 treats OSC sequences as zero-width via charmbracelet/x/ansi.
func osc8Link(url, text string) string {
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
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
	col1 = countsString(n.WorkingN, n.IdleN, n.DormantN, opts.ShowAll)

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
