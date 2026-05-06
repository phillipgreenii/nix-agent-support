package aggregate

import (
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

type SessionEnrichment struct {
	ContextTokens int
	Model         string
	FirstPrompt   string
	SubagentCount int
	SubshellCount int
	SessionTokens int     // cumulative output_tokens across session
	BurnRateShort float64 // tokens/min, short window
	BurnRateLong  float64 // tokens/min, long window
	CostUSD       float64 // estimated share, filled by Build
	AwaitingInput     bool      // true when last assistant turn contains unresolved AskUserQuestion
	RateLimitResetsAt time.Time // non-zero: session paused; window resets at this time
}

type Directory struct {
	Path         string
	Branch       string
	PRInfo       *session.PRInfo
	Sessions     []*SessionView
	WorkingN     int
	IdleN        int
	DormantN     int
	TotalTokens  int
	TotalCostUSD float64
}

type SessionView struct {
	*session.Session
	SessionEnrichment
}

type Tree struct {
	Dirs          []*Directory
	ActiveBlock   *ccusage.Block
	PlanCapUSD    float64
	GeneratedAt   time.Time
	CCUsageProbed  bool      // true once the first ccusage probe has run
	CCUsageErr     error     // non-nil if ccusage exec failed
	WindowResetsAt time.Time // global: max RateLimitResetsAt across all sessions (zero = none)
}

// TopupShouldDisplay returns true when the current 5h block's actual cost has
// reached or exceeded the plan cap — meaning the user is consuming top-up tokens.
func (t *Tree) TopupShouldDisplay() bool {
	if t.ActiveBlock == nil || t.PlanCapUSD <= 0 {
		return false
	}
	return t.ActiveBlock.CostUSD >= t.PlanCapUSD
}
