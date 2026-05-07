package render

import "github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"

// Legend returns the bottom-of-screen legend string sized for the given
// terminal width. Content is information-preserving at every tier — narrower
// terminals see condensed labels rather than mid-word truncation.
func Legend(width int) string {
	switch wrap.Tier(width) {
	case wrap.TierWide:
		return "● working  ○ idle  ⏸ paused  ? awaiting  ✕ dormant  🤖 subagents  🐚 shells  🌿 branch  [↑↓jk] nav  [enter] details"
	case wrap.TierNarrow:
		return "●working ○idle ⏸paused ?awaiting ✕dormant  🤖subs 🐚sh 🌿br  [enter] details"
	default:
		return "●○⏸?✕ 🤖🐚🌿  [↑↓][enter]"
	}
}
