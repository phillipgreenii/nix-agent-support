package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

var (
	styleModel  = lipgloss.NewStyle().Width(10).Align(lipgloss.Right)
	stylePct    = lipgloss.NewStyle().Width(5).Align(lipgloss.Right)
	styleBar    = lipgloss.NewStyle().Width(5).Align(lipgloss.Right)
	styleBurn   = lipgloss.NewStyle().Width(7).Align(lipgloss.Right)
	styleAmount = lipgloss.NewStyle().Width(8).Align(lipgloss.Right)
)

func labelStyle(termWidth int) lipgloss.Style {
	w := minLabelWidth
	if termWidth > 0 {
		if dyn := termWidth - prefixCols - statsBlockCols; dyn > w {
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

// statsBlockCols is the width of everything after the label:
// 2 spaces + model(10) + sp + pct(5) + sp + bar(5) + sp + burn(7) + sp + amount(8) = 41
const statsBlockCols = 41

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

func dirRollup(d *aggregate.Directory, opts TreeOpts) string {
	parts := []string{}
	if d.WorkingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", d.WorkingN))
	}
	if d.IdleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", d.IdleN))
	}
	if d.DormantN > 0 && opts.ShowAll {
		parts = append(parts, fmt.Sprintf("%d✕", d.DormantN))
	}
	if opts.CostMode {
		parts = append(parts, fmt.Sprintf("$%.2f", d.TotalCostUSD))
	} else {
		parts = append(parts, fmt.Sprintf("%s tok", FmtTok(d.TotalTokens)))
	}
	return strings.Join(parts, " · ")
}

func renderDirRow(d *aggregate.Directory, opts TreeOpts) string {
	rollup := dirRollup(d, opts)
	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	branchStr := ""
	if d.Branch != "" {
		branchStr = "  🌿 " + opts.Theme.Branch.Render(d.Branch)
		if d.PRInfo != nil {
			prNum := osc8Link(d.PRInfo.URL, fmt.Sprintf("#%d", d.PRInfo.Number))
			prTitle := truncate(d.PRInfo.Title, 40)
			branchStr += "  →  " + prNum + " " + prTitle
		}
	}
	leftWidth := max(rowWidth-lipgloss.Width(rollup)-1, lipgloss.Width(d.Path))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return pathStyle.Render(d.Path+branchStr) + " " + rollup + "\n"
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
	modelShort := shortModel(s.SessionEnrichment.Model)
	pct := sessionSharePct(s.SessionEnrichment.SessionTokens, opts.TotalSessionTokens)
	bar := progressBar(pct, 5)
	burn := fmt.Sprintf("%sk/m", fmtK(s.SessionEnrichment.BurnRateShort))
	tail := ""
	if s.SessionEnrichment.SubagentCount > 0 {
		tail += fmt.Sprintf(" %d🤖", s.SessionEnrichment.SubagentCount)
	}
	if s.SessionEnrichment.SubshellCount > 0 {
		tail += fmt.Sprintf(" %d🐚", s.SessionEnrichment.SubshellCount)
	}
	amount := ""
	if opts.CostMode {
		amount = fmt.Sprintf("$%.2f", s.SessionEnrichment.CostUSD)
	}
	out := fmt.Sprintf("%s%s %s  %s %s %s %s %s%s\n",
		cursorMark,
		prefix,
		labelStyle(opts.Width).Render(label),
		styleModel.Render(modelShort),
		stylePct.Render(fmt.Sprintf("%.0f%%", pct)),
		styleBar.Render(bar),
		styleBurn.Render(burn),
		styleAmount.Render(amount),
		tail,
	)
	if s.SessionEnrichment.FirstPrompt != "" {
		out += fmt.Sprintf("  %s    ↳ %s\n", cont, opts.Theme.Prompt.Render(fmt.Sprintf("%q", truncate(s.SessionEnrichment.FirstPrompt, 80))))
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
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
	label := glyph + " " + indent + n.DisplayPath
	rollup := nodeRollup(n, opts)

	rowWidth := prefixCols + minLabelWidth + statsBlockCols
	if opts.Width > 0 {
		rowWidth = opts.Width
	}
	// Subtract cursor mark width (2) from available space.
	available := rowWidth - 2
	leftWidth := max(available-lipgloss.Width(rollup)-1, lipgloss.Width(label))
	pathStyle := opts.Theme.DirRow.Width(leftWidth).Align(lipgloss.Left)
	return cursorMark + pathStyle.Render(label) + " " + rollup + "\n"
}

// nodeRollup formats the rollup statistics line for a PathNode row.
func nodeRollup(n *aggregate.PathNode, opts TreeOpts) string {
	var parts []string
	if n.WorkingN > 0 {
		parts = append(parts, fmt.Sprintf("%d●", n.WorkingN))
	}
	if n.IdleN > 0 {
		parts = append(parts, fmt.Sprintf("%d○", n.IdleN))
	}
	if n.DormantN > 0 && opts.ShowAll {
		parts = append(parts, fmt.Sprintf("%d✕", n.DormantN))
	}
	if opts.CostMode {
		parts = append(parts, fmt.Sprintf("$%.2f", n.TotalCostUSD))
	} else {
		parts = append(parts, fmt.Sprintf("%s tok", FmtTok(n.TotalTokens)))
	}
	parts = append(parts, fmt.Sprintf("%sk/m", fmtK(n.BurnRateSum)))
	return strings.Join(parts, "  ")
}
