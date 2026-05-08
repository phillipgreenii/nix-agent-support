# TUI Header Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the multi-line `render.Header` + standalone `render.Legend` with four single-row, tier-aware renderers (`Controls`, `BlockRow`, `Alerts`, `Footer`), so the TUI's header zone always emits exactly one row of controls plus one row of 5h block info, the alert zone is a single optional row, and the footer pairs a tier-aware legend (left) with a right-aligned Updated clock.

**Architecture:** Four pure-function renderers in `internal/render/`, each consuming a small `Opts` struct and returning a single line of text. `internal/tui/view.go`'s zone list becomes 5-zone (controls, block, alert?, body, footer). Old `Header` is deleted at the end of theme B. Headless mode is rewritten to call the new renderers instead of `Header`.

**Tech Stack:** Go, lipgloss, existing `internal/render/wrap` package (`wrap.Tier`, `wrap.Line`).

---

## Spec reference

Implements `docs/superpowers/specs/2026-05-08-tui-header-redesign-design.md`.

## File structure

| File | Status | Responsibility |
|------|--------|----------------|
| `packages/claude-agents-tui/internal/render/controls.go` | new | `Controls(opts) string` — 1 row, tier-aware controls + auto-resume + `[?]` + `[q]` |
| `packages/claude-agents-tui/internal/render/controls_test.go` | new | Three-tier content tests + 1-line + width-fits tests |
| `packages/claude-agents-tui/internal/render/block_row.go` | new | `BlockRow(tree, opts) string` — 1 row, tier-aware 5h block + bar + % + cost + burn + resets + exhaust |
| `packages/claude-agents-tui/internal/render/block_row_test.go` | new | Tier × block-state matrix |
| `packages/claude-agents-tui/internal/render/alerts.go` | new | `Alerts(tree, opts) string` — `""` or pipe-joined active alerts |
| `packages/claude-agents-tui/internal/render/alerts_test.go` | new | Empty / single / multi-alert composition + tier compaction |
| `packages/claude-agents-tui/internal/render/footer.go` | new | `Footer(width, updatedAt) string` — left=Legend, right=Updated, tier-aware |
| `packages/claude-agents-tui/internal/render/footer_test.go` | new | Updated placement + tier dropping |
| `packages/claude-agents-tui/internal/render/header.go` | DELETED in commit 2 | Old multi-line renderer |
| `packages/claude-agents-tui/internal/render/header_test.go` | DELETED in commit 2 | Old tests |
| `packages/claude-agents-tui/internal/render/header_tier_test.go` | DELETED in commit 2 | Old tests |
| `packages/claude-agents-tui/internal/tui/view.go` | modify | Zones list expands to 5; consumes the four new renderers |
| `packages/claude-agents-tui/internal/headless/headless.go` | modify | Calls new renderers in sequence instead of `render.Header` |

---

## Commit 1 — Four single-row renderers + tests

### Task 1: `Controls` renderer + tests

**Files:**
- Create: `packages/claude-agents-tui/internal/render/controls.go`
- Create: `packages/claude-agents-tui/internal/render/controls_test.go`

- [ ] **Step 1: Write the failing test (`controls_test.go`)**

```go
package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestControlsTierContent(t *testing.T) {
	cases := []struct {
		name     string
		width    int
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "wide",
			width:    140,
			wantAll:  []string{"[C] ●", "tokens", "cost", "active", "all", "name", "id", "[R] ●", "[M] now", "[?]", "[q]"},
			wantNone: []string{"[C]●", "[t]tok"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"[C]●", "[t] tok", "cost", "[a] act", "all", "[n] nm", "id", "[R]●", "[M]now", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "tokens", "active", "[M] now"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"[C]●", "[t]tok", "[a]act", "[n]nm", "[R]●", "[M]now", "[?]", "[q]"},
			wantNone: []string{"[C] ●", "[C]affeinate", "tokens", "active", "[M] now", "tok · cost"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Controls(ControlsOpts{Width: tc.width})
			for _, want := range tc.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in controls (tier=%s):\n%s", want, tc.name, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("should not contain %q (tier=%s):\n%s", none, tc.name, out)
				}
			}
			if strings.Contains(out, "\n") {
				t.Errorf("controls must be single line, got newlines:\n%s", out)
			}
		})
	}
}

func TestControlsFitsAtTierFloor(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		got := Controls(ControlsOpts{Width: w})
		if width := lipgloss.Width(got); width > w {
			t.Errorf("Controls(%d) width = %d, want <= %d; got %q", w, width, w, got)
		}
	}
}

func TestControlsCaffeineGraceCountdown(t *testing.T) {
	out := Controls(ControlsOpts{Width: 200, CaffeinateOn: true, GraceRemaining: 55 * time.Second})
	if !strings.Contains(out, "55s") {
		t.Errorf("expected grace countdown '55s' at WIDE, got:\n%s", out)
	}
	tiny := Controls(ControlsOpts{Width: 60, CaffeinateOn: true, GraceRemaining: 55 * time.Second})
	if strings.Contains(tiny, "55s") {
		t.Errorf("grace should drop at TINY, got:\n%s", tiny)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run from `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui`:
```bash
go test ./internal/render/... -run "TestControls" -v
```
Expected: FAIL with `Controls undefined`.

- [ ] **Step 3: Write `controls.go`**

```go
package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// ControlsOpts carries everything Controls needs to render the toggle row.
type ControlsOpts struct {
	CaffeinateOn   bool
	GraceRemaining time.Duration
	ShowAll        bool
	CostMode       bool
	ForceID        bool
	AutoResume     bool
	Theme          Theme
	Width          int
}

// Controls returns a single-row, tier-aware controls line:
//
//   WIDE   ≥120  [C] ● on  [t] tokens · cost  [a] active · all  [n] name · id  [R] ● on  [M] now  [?]  [q]
//   NARROW 80–119 [C]●  [t] tok · cost  [a] act · all  [n] nm · id  [R]●  [M]now  [?][q]
//   TINY   <80   [C]●  [t]tok  [a]act  [n]nm  [R]●  [M]now  [?][q]
//
// The active half of each toggle is highlighted via theme.ActiveToggle.
// At TINY only the active half of each toggle is shown.
func Controls(opts ControlsOpts) string {
	th := opts.Theme

	caffWide := "○ off"
	caffGlyph := "○"
	if opts.CaffeinateOn {
		caffGlyph = "●"
		if opts.GraceRemaining > 0 {
			caffWide = fmt.Sprintf("● on %ds", int(opts.GraceRemaining.Seconds()))
		} else {
			caffWide = "● on"
		}
	}

	autoResumeWide := "○ off"
	autoResumeGlyph := "○"
	if opts.AutoResume {
		autoResumeWide = th.ActiveToggle.Render("● on")
		autoResumeGlyph = th.ActiveToggle.Render("●")
	}

	var sb strings.Builder
	switch wrap.Tier(opts.Width) {
	case wrap.TierWide:
		tokLabel, costLabel := "tokens", "cost"
		if opts.CostMode {
			costLabel = th.ActiveToggle.Render("cost")
		} else {
			tokLabel = th.ActiveToggle.Render("tokens")
		}
		actLabel, allLabel := "active", "all"
		if opts.ShowAll {
			allLabel = th.ActiveToggle.Render("all")
		} else {
			actLabel = th.ActiveToggle.Render("active")
		}
		nameLabel, idLabel := "name", "id"
		if opts.ForceID {
			idLabel = th.ActiveToggle.Render("id")
		} else {
			nameLabel = th.ActiveToggle.Render("name")
		}
		fmt.Fprintf(&sb, "[C] %s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R] %s  [M] now  [?]  [q]",
			caffWide, tokLabel, costLabel, actLabel, allLabel, nameLabel, idLabel, autoResumeWide)
	case wrap.TierNarrow:
		tokLabel, costLabel := "tok", "cost"
		if opts.CostMode {
			costLabel = th.ActiveToggle.Render("cost")
		} else {
			tokLabel = th.ActiveToggle.Render("tok")
		}
		actLabel, allLabel := "act", "all"
		if opts.ShowAll {
			allLabel = th.ActiveToggle.Render("all")
		} else {
			actLabel = th.ActiveToggle.Render("act")
		}
		nmLabel, idLabel := "nm", "id"
		if opts.ForceID {
			idLabel = th.ActiveToggle.Render("id")
		} else {
			nmLabel = th.ActiveToggle.Render("nm")
		}
		fmt.Fprintf(&sb, "[C]%s  [t] %s · %s  [a] %s · %s  [n] %s · %s  [R]%s  [M]now  [?][q]",
			caffGlyph, tokLabel, costLabel, actLabel, allLabel, nmLabel, idLabel, autoResumeGlyph)
	default: // TierTiny
		tokOrCost := "tok"
		if opts.CostMode {
			tokOrCost = "cost"
		}
		actOrAll := "act"
		if opts.ShowAll {
			actOrAll = "all"
		}
		nmOrID := "nm"
		if opts.ForceID {
			nmOrID = "id"
		}
		fmt.Fprintf(&sb, "[C]%s  [t]%s  [a]%s  [n]%s  [R]%s  [M]now  [?][q]",
			caffGlyph, th.ActiveToggle.Render(tokOrCost), th.ActiveToggle.Render(actOrAll), th.ActiveToggle.Render(nmOrID), autoResumeGlyph)
	}
	return sb.String()
}
```

- [ ] **Step 4: Run test to verify pass**

```bash
go test ./internal/render/... -run "TestControls" -v
```
Expected: PASS for all three tier subtests + `TestControlsFitsAtTierFloor` + `TestControlsCaffeineGraceCountdown`.

If `TestControlsFitsAtTierFloor` fails because the WIDE line exceeds 120, **shorten it before continuing** by removing the trailing space before `[q]` or using single-space separators between tokens/cost groups. Re-measure: `lipgloss.Width(out) <= w`.

- [ ] **Step 5: No commit yet — moving to Task 2**

---

### Task 2: `BlockRow` renderer + tests

**Files:**
- Create: `packages/claude-agents-tui/internal/render/block_row.go`
- Create: `packages/claude-agents-tui/internal/render/block_row_test.go`

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
)

func TestBlockRowPreActiveStates(t *testing.T) {
	cases := []struct {
		name string
		tree *aggregate.Tree
		want string
	}{
		{"loading", &aggregate.Tree{CCUsageProbed: false}, "5h loading…"},
		{"unavailable", &aggregate.Tree{CCUsageProbed: true, CCUsageErr: errors.New("not found")}, "5h unavailable"},
		{"no active block", &aggregate.Tree{CCUsageProbed: true}, "5h no active block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := BlockRow(tc.tree, BlockRowOpts{Width: 200, Now: time.Now()})
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in output, got: %q", tc.want, out)
			}
			if strings.Contains(out, "\n") {
				t.Errorf("BlockRow must be single line, got: %q", out)
			}
		})
	}
}

func TestBlockRowActiveBlockTiers(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    100,
		ActiveBlock: &ccusage.Block{
			CostUSD:    35,
			EndTime:    time.Date(2026, 5, 8, 1, 0, 0, 0, time.UTC),
			BurnRate:   ccusage.BurnRate{TokensPerMinute: 1_200_000, CostPerHour: 30},
			Projection: ccusage.Projection{RemainingMinutes: 90},
		},
	}
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		width    int
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "wide",
			width:    140,
			wantAll:  []string{"5h", "█", "35%", "$35.00", "1.2M/m", "resets", "ex"},
			wantNone: []string{"5h Block"},
		},
		{
			name:     "narrow",
			width:    100,
			wantAll:  []string{"5h", "█", "35%", "$35.00", "resets", "ex"},
			wantNone: []string{"M/m", "5h Block"},
		},
		{
			name:     "tiny",
			width:    60,
			wantAll:  []string{"5h", "35%", "resets"},
			wantNone: []string{"█", "$", "M/m", "ex"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := BlockRow(tree, BlockRowOpts{Width: tc.width, Now: now})
			for _, want := range tc.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in output (tier=%s):\n%s", want, tc.name, out)
				}
			}
			for _, none := range tc.wantNone {
				if strings.Contains(out, none) {
					t.Errorf("should not contain %q (tier=%s):\n%s", none, tc.name, out)
				}
			}
			if strings.Contains(out, "\n") {
				t.Errorf("BlockRow must be single line, got: %q", out)
			}
		})
	}
}

func TestBlockRowFitsAtTierFloor(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    100,
		ActiveBlock: &ccusage.Block{
			CostUSD:    35,
			EndTime:    time.Date(2026, 5, 8, 1, 0, 0, 0, time.UTC),
			BurnRate:   ccusage.BurnRate{TokensPerMinute: 1_200_000, CostPerHour: 30},
			Projection: ccusage.Projection{RemainingMinutes: 90},
		},
	}
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	for _, w := range []int{60, 80, 120} {
		got := BlockRow(tree, BlockRowOpts{Width: w, Now: now})
		if width := lipgloss.Width(got); width > w {
			t.Errorf("BlockRow(%d) width = %d, want <= %d; got %q", w, width, w, got)
		}
	}
}
```

- [ ] **Step 2: Run test (verify failure)**

```bash
go test ./internal/render/... -run "TestBlockRow" -v
```
Expected: FAIL with `BlockRow undefined`.

- [ ] **Step 3: Write `block_row.go`**

```go
package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// BlockRowOpts carries time + width context for BlockRow.
type BlockRowOpts struct {
	Now   time.Time
	Width int
}

// blockRowBarWidth is the bar width at WIDE/NARROW tiers. TINY drops the bar.
const blockRowBarWidth = 18

// BlockRow returns a single-row, tier-aware 5h block summary.
//
// Pre-active states (any tier):
//
//   "5h loading…"
//   "5h unavailable — `ccusage` not on PATH"
//   "5h no active block"
//   "5h $X.XX  resets HH:MM  (plan cap unknown)"   when PlanCapUSD <= 0
//
// Active-block tier shapes:
//
//   WIDE   "5h ████████░░░░░░░░░░ 35%  $30.10  1.2M/m  resets 01:00  ex 22:21 ⚠"
//   NARROW "5h ████████░░░░░░░░░░ 35%  $30.10  resets 01:00  ex 22:21"
//   TINY   "5h 35%  resets 01:00"
//
// Drop priority (most-droppable first): burn → bar → cost → exhaust → reset.
// Percent and reset always survive (when an active block exists).
func BlockRow(tree *aggregate.Tree, opts BlockRowOpts) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case !tree.CCUsageProbed:
		return "5h loading…"
	case tree.CCUsageErr != nil:
		return "5h unavailable — `ccusage` not on PATH"
	case tree.ActiveBlock == nil:
		return "5h no active block"
	}
	block := tree.ActiveBlock

	if tree.PlanCapUSD <= 0 {
		return fmt.Sprintf("5h $%.2f  resets %s  (plan cap unknown)",
			block.CostUSD, block.EndTime.Local().Format("15:04"))
	}

	pct := 100 * block.CostUSD / tree.PlanCapUSD
	tier := wrap.Tier(opts.Width)

	var sb strings.Builder
	sb.WriteString("5h ")

	if tier != wrap.TierTiny {
		sb.WriteString(progressBar(pct, blockRowBarWidth))
		sb.WriteString(" ")
	}
	fmt.Fprintf(&sb, "%.0f%%", pct)

	if tier != wrap.TierTiny {
		fmt.Fprintf(&sb, "  $%.2f", block.CostUSD)
	}

	if tier == wrap.TierWide {
		fmt.Fprintf(&sb, "  %sM/m", fmtM(block.BurnRate.TokensPerMinute))
	}

	fmt.Fprintf(&sb, "  resets %s", block.EndTime.Local().Format("15:04"))

	if tier != wrap.TierTiny {
		exhaust := tree.ProjectedExhaust(now)
		if !exhaust.IsZero() {
			warn := ""
			if exhaust.Before(block.EndTime) {
				warn = " ⚠"
			}
			fmt.Fprintf(&sb, "  ex %s%s", exhaust.Local().Format("15:04"), warn)
		}
	}

	return sb.String()
}

// fmtM formats tokens-per-minute as "1.2" (millions). Used in BlockRow's burn segment.
func fmtM(tokensPerMinute float64) string {
	return fmt.Sprintf("%.1f", tokensPerMinute/1_000_000)
}
```

- [ ] **Step 4: Run test (verify pass)**

```bash
go test ./internal/render/... -run "TestBlockRow" -v
```
Expected: PASS for all subtests.

If `TestBlockRowFitsAtTierFloor` fails at any width, reduce the bar width (`blockRowBarWidth = 16` instead of 18) or shorten labels (`"resets HH:MM"` → `"r HH:MM"` at NARROW). Re-run.

If `TestBlockRowActiveBlockTiers/tiny` fails because `5h $X` accidentally appears, the cost branch must be inside `tier != wrap.TierTiny` — re-check the source.

- [ ] **Step 5: No commit yet**

---

### Task 3: `Alerts` renderer + tests

**Files:**
- Create: `packages/claude-agents-tui/internal/render/alerts.go`
- Create: `packages/claude-agents-tui/internal/render/alerts_test.go`

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
)

func TestAlertsEmptyWhenNoneActive(t *testing.T) {
	tree := &aggregate.Tree{}
	out := Alerts(tree, AlertsOpts{Now: time.Now(), Width: 200})
	if out != "" {
		t.Errorf("expected empty alerts, got: %q", out)
	}
}

func TestAlertsAutoResumeCountdown(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{WindowResetsAt: now.Add(75 * time.Second)}
	out := Alerts(tree, AlertsOpts{
		Now:             now,
		Width:           200,
		AutoResume:      true,
		WindowResetsAt:  tree.WindowResetsAt,
		AutoResumeDelay: 0,
	})
	if !strings.Contains(out, "⏸") {
		t.Errorf("expected pause glyph in alerts, got: %q", out)
	}
	if !strings.Contains(out, "1:15") {
		t.Errorf("expected countdown 1:15, got: %q", out)
	}
}

func TestAlertsTopupShows(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    50,
		ActiveBlock:   &ccusage.Block{CostUSD: 75},
	}
	out := Alerts(tree, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		TopupPoolUSD:  20,
		TopupConsumed: 5,
	})
	if !strings.Contains(out, "Top-up") {
		t.Errorf("expected Top-up segment, got: %q", out)
	}
	if !strings.Contains(out, "$15") {
		t.Errorf("expected remaining amount $15, got: %q", out)
	}
}

func TestAlertsPipeJoinedWhenMultiple(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{
		CCUsageProbed:  true,
		PlanCapUSD:     50,
		ActiveBlock:    &ccusage.Block{CostUSD: 75},
		WindowResetsAt: now.Add(60 * time.Second),
	}
	out := Alerts(tree, AlertsOpts{
		Now:            now,
		Width:          200,
		AutoResume:     true,
		WindowResetsAt: tree.WindowResetsAt,
		TopupPoolUSD:   20,
		TopupConsumed:  5,
	})
	if !strings.Contains(out, " | ") {
		t.Errorf("expected pipe separator between alerts, got: %q", out)
	}
	if !strings.Contains(out, "⏸") || !strings.Contains(out, "Top-up") {
		t.Errorf("expected both auto-resume and top-up segments, got: %q", out)
	}
}

func TestAlertsSingleLineNoTrailingNewline(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{WindowResetsAt: now.Add(30 * time.Second)}
	out := Alerts(tree, AlertsOpts{Now: now, Width: 200, AutoResume: true, WindowResetsAt: tree.WindowResetsAt})
	if strings.Contains(out, "\n") {
		t.Errorf("Alerts must be single line, got: %q", out)
	}
}
```

- [ ] **Step 2: Run test (verify failure)**

```bash
go test ./internal/render/... -run "TestAlerts" -v
```
Expected: FAIL with `Alerts undefined`.

- [ ] **Step 3: Write `alerts.go`**

```go
package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// AlertsOpts carries the inputs needed to compose the alert row.
type AlertsOpts struct {
	Now             time.Time
	Width           int
	AutoResume      bool
	WindowResetsAt  time.Time
	AutoResumeDelay time.Duration
	TopupPoolUSD    float64
	TopupConsumed   float64
}

// Alerts returns "" when no alert is active, otherwise a single-line,
// pipe-joined summary in priority order:
//
//   ⏸ resuming in N:NN          (when AutoResume && WindowResetsAt > Now)
//   Top-up $X / $Y remaining    (when tree.TopupShouldDisplay() && TopupPoolUSD > 0)
//
// Tier-aware compaction shortens labels at NARROW/TINY.
func Alerts(tree *aggregate.Tree, opts AlertsOpts) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	tier := wrap.Tier(opts.Width)

	var segs []string

	if opts.AutoResume && !opts.WindowResetsAt.IsZero() {
		fireAt := opts.WindowResetsAt.Add(opts.AutoResumeDelay)
		remaining := fireAt.Sub(now)
		if remaining > 0 {
			mins := int(remaining.Minutes())
			secs := int(remaining.Seconds()) - mins*60
			switch tier {
			case wrap.TierWide:
				segs = append(segs, fmt.Sprintf("⏸ resuming in %d:%02d", mins, secs))
			case wrap.TierNarrow:
				segs = append(segs, fmt.Sprintf("⏸ resume %d:%02d", mins, secs))
			default:
				segs = append(segs, fmt.Sprintf("⏸ %d:%02d", mins, secs))
			}
		} else if remaining > -5*time.Second {
			segs = append(segs, "⏸ resuming…")
		}
	}

	if tree.TopupShouldDisplay() && opts.TopupPoolUSD > 0 {
		remaining := opts.TopupPoolUSD - opts.TopupConsumed
		switch tier {
		case wrap.TierWide:
			segs = append(segs, fmt.Sprintf("Top-up $%.0f / $%.0f remaining", remaining, opts.TopupPoolUSD))
		case wrap.TierNarrow:
			segs = append(segs, fmt.Sprintf("Top-up $%.0f/$%.0f", remaining, opts.TopupPoolUSD))
		default:
			segs = append(segs, fmt.Sprintf("T $%.0f/$%.0f", remaining, opts.TopupPoolUSD))
		}
	}

	return strings.Join(segs, " | ")
}
```

- [ ] **Step 4: Run test (verify pass)**

```bash
go test ./internal/render/... -run "TestAlerts" -v
```
Expected: PASS.

- [ ] **Step 5: No commit yet**

---

### Task 4: `Footer` renderer + tests

**Files:**
- Create: `packages/claude-agents-tui/internal/render/footer.go`
- Create: `packages/claude-agents-tui/internal/render/footer_test.go`

- [ ] **Step 1: Write the failing test**

```go
package render

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestFooterUpdatedRightAligned(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(140, updated)
	if !strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("expected 'Updated 21:05:43' at WIDE, got: %q", out)
	}
	if w := lipgloss.Width(out); w != 140 {
		t.Errorf("Footer(140) width = %d, want 140; got %q", w, out)
	}
	if !strings.HasSuffix(out, "Updated 21:05:43") {
		t.Errorf("Updated must hug right edge, got: %q", out)
	}
}

func TestFooterShortUpdatedAtNarrow(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(100, updated)
	if !strings.Contains(out, "21:05:43") {
		t.Errorf("expected '21:05:43' at NARROW, got: %q", out)
	}
	if strings.Contains(out, "Updated 21:05:43") {
		t.Errorf("at NARROW should drop 'Updated' prefix, got: %q", out)
	}
	if !strings.HasSuffix(out, "21:05:43") {
		t.Errorf("clock must hug right edge, got: %q", out)
	}
}

func TestFooterDropsUpdatedAtTiny(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	out := Footer(60, updated)
	if strings.Contains(out, "21:05") {
		t.Errorf("at TINY should drop Updated entirely, got: %q", out)
	}
}

func TestFooterContainsLegend(t *testing.T) {
	updated := time.Date(2026, 5, 8, 21, 5, 43, 0, time.UTC)
	for _, w := range []int{60, 100, 140} {
		out := Footer(w, updated)
		// Legend always contains at least one of the symbol glyphs.
		if !strings.ContainsAny(out, "●○⏸?✕🤖🐚🌿") {
			t.Errorf("Footer(%d) missing legend symbols: %q", w, out)
		}
	}
}

func TestFooterSingleLine(t *testing.T) {
	out := Footer(120, time.Now())
	if strings.Contains(out, "\n") {
		t.Errorf("Footer must be single line, got: %q", out)
	}
}
```

- [ ] **Step 2: Run test (verify failure)**

```bash
go test ./internal/render/... -run "TestFooter" -v
```
Expected: FAIL with `Footer undefined`.

- [ ] **Step 3: Write `footer.go`**

```go
package render

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

// Footer renders the bottom row of the TUI: legend on the left, Updated clock
// on the right. At TINY the clock is dropped and the full row is legend.
//
//   WIDE   ≥120: <legend, padded to width-18>  Updated 21:05:43
//   NARROW 80–119: <legend, padded to width-10>  21:05:43
//   TINY   <80:   <legend>
func Footer(width int, updatedAt time.Time) string {
	tier := wrap.Tier(width)
	if tier == wrap.TierTiny {
		return Legend(width)
	}

	var rightLabel string
	switch tier {
	case wrap.TierWide:
		rightLabel = "Updated " + updatedAt.Format("15:04:05") // 16 cols
	default: // TierNarrow
		rightLabel = updatedAt.Format("15:04:05") // 8 cols
	}
	rightWidth := lipgloss.Width(rightLabel)
	gap := 2
	legendWidth := width - rightWidth - gap
	if legendWidth < 1 {
		// Fall back: full-width legend, no clock.
		return Legend(width)
	}

	leftStyled := lipgloss.NewStyle().Width(legendWidth).Align(lipgloss.Left).Render(Legend(legendWidth))
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Align(lipgloss.Right).Render(rightLabel)
	return leftStyled + strings.Repeat(" ", gap) + rightStyled
}
```

- [ ] **Step 4: Run test (verify pass)**

```bash
go test ./internal/render/... -run "TestFooter" -v
```
Expected: PASS.

If `TestFooterUpdatedRightAligned` fails because `lipgloss.Width(out) != 140`, the issue is the `lipgloss.NewStyle().Width(legendWidth)` may pad with trailing spaces past the legend's natural width, then the gap spaces, then the right side — total should be exactly `legendWidth + gap + rightWidth = width`. Confirm via `fmt.Println(lipgloss.Width(leftStyled), lipgloss.Width(rightStyled))`.

- [ ] **Step 5: No commit yet**

---

### Task 5: Verify suite + commit B1

- [ ] **Step 1: Full test sweep**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent. Old `header_test.go`/`header_tier_test.go` still pass because old `Header` is untouched.

- [ ] **Step 2: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/controls.go \
        packages/claude-agents-tui/internal/render/controls_test.go \
        packages/claude-agents-tui/internal/render/block_row.go \
        packages/claude-agents-tui/internal/render/block_row_test.go \
        packages/claude-agents-tui/internal/render/alerts.go \
        packages/claude-agents-tui/internal/render/alerts_test.go \
        packages/claude-agents-tui/internal/render/footer.go \
        packages/claude-agents-tui/internal/render/footer_test.go
git commit -m "$(cat <<'EOF'
feat(render): tier-aware Controls/BlockRow/Alerts/Footer renderers

Adds four single-row, tier-aware renderers that will replace the
multi-line render.Header and the standalone render.Legend call in
the TUI's View. Each renderer:

- Returns exactly one line of output (no trailing newline).
- Honors lipgloss.Width(line) <= tierFloor at every tier.
- Has unit tests covering WIDE/NARROW/TINY content and width
  invariants, plus per-renderer state matrices.

No callers yet; render.Header is untouched. The next commit
wires View() and headless mode to the new renderers and deletes
the old header.

Implements docs/superpowers/specs/2026-05-08-tui-header-redesign-design.md
(theme B, commit 1 of 2).
EOF
)"
```

---

## Commit 2 — Wire View() + headless, delete old `Header`

### Task 6: Rewire `View()` to consume new renderers

**Files:**
- Modify: `packages/claude-agents-tui/internal/tui/view.go`

- [ ] **Step 1: Read the current `view.go`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
cat internal/tui/view.go
```

- [ ] **Step 2: Replace `View()` with the 5-zone version**

```go
package tui

import (
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
	"github.com/phillipgreenii/claude-agents-tui/internal/render/wrap"
)

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return wrap.Block(RenderDetails(m.selected, m.width), wrap.EffectiveWidth(m.width))
	}

	now := time.Now()
	controls := render.Controls(render.ControlsOpts{
		CaffeinateOn: m.caffeinateOn,
		ShowAll:      m.showAll,
		CostMode:     m.costMode,
		ForceID:      m.forceID,
		AutoResume:   m.autoResume,
		Theme:        m.theme,
		Width:        m.width,
	})
	blockRow := render.BlockRow(m.tree, render.BlockRowOpts{Width: m.width, Now: now})
	alerts := render.Alerts(m.tree, render.AlertsOpts{
		Now:             now,
		Width:           m.width,
		AutoResume:      m.autoResume,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
	})
	footer := render.Footer(m.width, now)

	zones := []zoneSpec{
		{name: "controls", content: controls, dropOrder: 1},
		{name: "block", content: blockRow, dropOrder: 2},
	}
	if alerts != "" {
		zones = append(zones, zoneSpec{name: "alert", content: alerts, dropOrder: 3})
	}
	zones = append(zones,
		zoneSpec{
			name: "body",
			fill: true,
			renderFill: func(h int) string {
				return m.renderBody(h)
			},
		},
		zoneSpec{name: "footer", content: footer, dropOrder: 4},
	)

	return layoutZones(zones, wrap.EffectiveWidth(m.width), m.height)
}

// renderBody returns up to `height` rows of session list content.
// When height is 0 (test/headless), all rows render.
func (m *Model) renderBody(height int) string {
	if len(m.flatRows) == 0 {
		return "No active sessions."
	}
	totalTok := 0
	for _, d := range m.tree.Dirs {
		totalTok += d.TotalTokens
	}
	opts := render.TreeOpts{
		ShowAll:            m.showAll,
		ForceID:            m.forceID,
		CostMode:           m.costMode,
		Width:              m.width,
		Cursor:             m.cursor,
		HasCursor:          m.selected == nil,
		Theme:              m.theme,
		TotalSessionTokens: totalTok,
	}
	if height <= 0 {
		return render.RenderWindowTree(m.pathNodes, m.flatRows, 0, 10000, opts)
	}
	return render.RenderWindowTree(m.pathNodes, m.flatRows, m.scrollOffset, height, opts)
}
```

Key changes vs theme A's version:
- New imports: `"time"`. Removed: `"strings"` is no longer needed (TrimRight on Header/Legend gone).
- `render.Header(...)` and `render.Legend(...)` calls removed.
- Four new renderer calls: `Controls`, `BlockRow`, `Alerts`, `Footer`.
- Zone list expands to 5 (with optional alert).

- [ ] **Step 3: Build to confirm compile**

```bash
go build ./...
```

If it errors with `Header is undefined` from `headless.go`, that's expected — Task 7 fixes it. Continue without committing.

- [ ] **Step 4: Run TUI tests**

```bash
go test ./internal/tui/... -count=1
```

Expected: PASS. The matrix `TestViewLineWidthInvariant` should still pass because each new renderer is exactly 1 line and the layoutZones helper handles the rest.

If `TestViewNoPhantomBlankRowsBetweenZones` fails: a new renderer is leaking a trailing newline somewhere. Add `strings.TrimRight(..., "\n")` defensively in `view.go` if needed, but the renderers are designed to never include `\n`.

---

### Task 7: Rewire headless mode

**Files:**
- Modify: `packages/claude-agents-tui/internal/headless/headless.go`

- [ ] **Step 1: Replace the `render.Header` call**

Locate around `headless.go:35`. Replace:

```go
		} else {
			fmt.Fprint(o.Writer, render.Header(tree, render.HeaderOpts{}))
			fmt.Fprint(o.Writer, render.Tree(tree, render.TreeOpts{}))
		}
```

With:

```go
		} else {
			now := time.Now()
			fmt.Fprintln(o.Writer, render.Controls(render.ControlsOpts{}))
			fmt.Fprintln(o.Writer, render.BlockRow(tree, render.BlockRowOpts{Now: now}))
			if a := render.Alerts(tree, render.AlertsOpts{Now: now}); a != "" {
				fmt.Fprintln(o.Writer, a)
			}
			fmt.Fprint(o.Writer, render.Tree(tree, render.TreeOpts{}))
		}
```

`time` may already be imported; verify before adding.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: compile success.

- [ ] **Step 3: Run headless tests**

```bash
go test ./internal/headless/... -count=1
```

Expected: PASS. Existing headless tests don't check substrings that disappeared from the new renderers (verified in theme A by `grep`).

---

### Task 8: Delete old `Header`

**Files:**
- Delete: `packages/claude-agents-tui/internal/render/header.go`
- Delete: `packages/claude-agents-tui/internal/render/header_test.go`
- Delete: `packages/claude-agents-tui/internal/render/header_tier_test.go`

- [ ] **Step 1: Confirm zero callers remain**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
grep -rn "render\.Header\b\|render\.HeaderOpts\b" cmd/ internal/
```

Expected: no matches. If matches remain, fix them before deleting.

- [ ] **Step 2: Delete the files**

```bash
rm internal/render/header.go
rm internal/render/header_test.go
rm internal/render/header_tier_test.go
```

- [ ] **Step 3: Verify residual references**

The old `header.go` defined helpers (`progressBar`, `humanDur`, `fmtK`) that may be used by other files. After deletion:

```bash
go build ./...
```

If `progressBar undefined`, that helper is now used by `block_row.go` (we wrote `progressBar(pct, blockRowBarWidth)` there). Re-add `progressBar` and `fmtK` to a new helper file:

```go
// packages/claude-agents-tui/internal/render/format.go
package render

import (
	"fmt"
	"strings"
)

// progressBar renders an N-cell horizontal bar at the given percent (0–100).
func progressBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// fmtK formats a value as thousands. "1234" -> "1".
func fmtK(v float64) string {
	return fmt.Sprintf("%.0f", v/1000)
}
```

The `humanDur` helper is no longer used after `Header` deletion — confirm via `grep humanDur internal/`. If unused, leave it deleted.

- [ ] **Step 4: Build and test**

```bash
go build ./...
go test ./... -count=1
go vet ./...
```

Expected: all green, vet silent.

---

### Task 9: Verify + commit B2

- [ ] **Step 1: Full sweep**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./... -count=1
go vet ./...
```

Expected: all packages PASS, vet silent.

- [ ] **Step 2: Manual sanity (optional but recommended)**

Build the binary and inspect the headless output to confirm it renders sensibly:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build -o /tmp/cat-tui ./cmd/claude-agents-tui
timeout 30 /tmp/cat-tui -wait-until-idle -time-between-checks 5 -maximum-wait 30 -consecutive-idle-checks 0 2>&1 | head -10
```

Expected: first 2 lines show the new controls + 5h block. Sessions follow.

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add -A packages/claude-agents-tui/internal/render/ \
       packages/claude-agents-tui/internal/tui/view.go \
       packages/claude-agents-tui/internal/headless/headless.go
git commit -m "$(cat <<'EOF'
feat(tui): wire View and headless to single-row renderers; delete old Header

View() now constructs five zones (controls, block, alert?, body, footer)
and consumes the four renderers introduced in commit B1. Headless mode
calls the same renderers in sequence instead of the multi-line Header.

The old render.Header / render.HeaderOpts / progressBar / humanDur /
header tests are deleted. progressBar and fmtK move to a small format.go
helper since BlockRow uses them. humanDur was unused outside Header and
is dropped.

Implements docs/superpowers/specs/2026-05-08-tui-header-redesign-design.md
(theme B, commit 2 of 2).
EOF
)"
```

---

## Self-Review Checklist (run before handing off)

- [ ] Spec coverage: Controls (Task 1) + BlockRow (Task 2) + Alerts (Task 3) + Footer (Task 4) cover all four renderer specs. View() rewire (Task 6) and headless rewire (Task 7) cover the consumer specs. Old Header deletion (Task 8) closes out the spec's "DELETED" file list. Migration two-commit shape matches the spec's recommendation (with commit 2 absorbing footer, since the spec's three-commit version would have left Updated absent between commit 2 and 3).
- [ ] No placeholders: each task has runnable commands, complete code, and expected outputs.
- [ ] Type / identifier consistency: `ControlsOpts`, `BlockRowOpts`, `AlertsOpts` (no Footer opts struct — `Footer(width, updatedAt time.Time)`), `progressBar`, `fmtK`, `fmtM`, `blockRowBarWidth`. Used consistently across tasks.
- [ ] TDD ordering preserved per task (failing test first when introducing a new renderer).
- [ ] Two commits, both independently revertable. No intermediate state where Updated is missing or where an existing test fails.
