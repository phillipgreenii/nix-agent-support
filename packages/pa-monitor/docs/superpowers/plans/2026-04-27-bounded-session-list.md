# Bounded Session List Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Constrain the session list body to the terminal height, auto-scroll to keep the cursor visible, pin directory headers when their sessions scroll into view, and show ↑/↓ session-count indicators when content is clipped.

**Architecture:** New `internal/render/rows.go` flattens the tree into typed `Row` values. New `internal/render/window.go` renders a height-bounded window with sticky dir headers and scroll indicators. `Model` drops the unused `viewport.Model` (and its `charmbracelet/bubbles` dependency) and gains `scrollOffset int`. `syncScroll()` in `update.go` advances the offset after every cursor move. `view.go` calls `RenderWindow` when terminal height is known.

**Tech Stack:** Go 1.24, lipgloss v1.1.0, charmbracelet/bubbletea v1.3.10

---

## File map

| File                             | Action | Role                                                                             |
| -------------------------------- | ------ | -------------------------------------------------------------------------------- |
| `internal/render/rows.go`        | Create | `Row` type, `FlattenRows()`                                                      |
| `internal/render/rows_test.go`   | Create | flatten correctness tests                                                        |
| `internal/render/window.go`      | Create | `RenderWindow()`, `LastVisibleIdx()`, helpers                                    |
| `internal/render/window_test.go` | Create | window render tests                                                              |
| `internal/tui/model.go`          | Modify | remove viewport field/import; add `scrollOffset int`                             |
| `internal/tui/update.go`         | Modify | remove viewport update; add `syncScroll`; call it on cursor move and tree update |
| `internal/tui/view.go`           | Modify | call `RenderWindow` when height is known                                         |
| `internal/tui/model_test.go`     | Modify | add scroll-sync tests                                                            |

---

## Task 1: Row model

**Files:**

- Create: `internal/render/rows.go`
- Create: `internal/render/rows_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/render/rows_test.go`:

```go
package render

import (
	"fmt"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

func TestFlattenRowsEmptyTree(t *testing.T) {
	rows := FlattenRows(&aggregate.Tree{}, TreeOpts{})
	if len(rows) != 0 {
		t.Errorf("expected empty rows, got %d", len(rows))
	}
}

func TestFlattenRowsSkipsEmptyDirs(t *testing.T) {
	empty := &aggregate.Directory{Path: "/empty"}
	active := &aggregate.Directory{
		Path: "/active",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
		},
		WorkingN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{empty, active}}
	rows := FlattenRows(tree, TreeOpts{})
	for _, r := range rows {
		if r.DirIdx == 0 {
			t.Error("empty dir should not appear in rows")
		}
	}
}

func TestFlattenRowsStructure(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{})
	// Expected: DirHeaderKind, SessionKind, SessionKind, BlankKind
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d: %v", len(rows), rows)
	}
	if rows[0].Kind != DirHeaderKind {
		t.Errorf("rows[0] should be DirHeaderKind, got %v", rows[0].Kind)
	}
	if rows[1].Kind != SessionKind || rows[1].SessIdx != 0 || rows[1].FlatIdx != 0 {
		t.Errorf("rows[1]: want SessionKind/SessIdx=0/FlatIdx=0, got %+v", rows[1])
	}
	if rows[2].Kind != SessionKind || rows[2].SessIdx != 1 || rows[2].FlatIdx != 1 {
		t.Errorf("rows[2]: want SessionKind/SessIdx=1/FlatIdx=1, got %+v", rows[2])
	}
	if rows[3].Kind != BlankKind {
		t.Errorf("rows[3] should be BlankKind, got %v", rows[3].Kind)
	}
}

func TestFlattenRowsLineCountWithPrompt(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{
				Session:           &session.Session{SessionID: "a", Status: session.Working},
				SessionEnrichment: aggregate.SessionEnrichment{FirstPrompt: "do the thing"},
			},
			{
				Session: &session.Session{SessionID: "b", Status: session.Working},
			},
		},
		WorkingN: 2,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{})
	if rows[1].LineCount != 2 {
		t.Errorf("session with FirstPrompt: want LineCount=2, got %d", rows[1].LineCount)
	}
	if rows[2].LineCount != 1 {
		t.Errorf("session without FirstPrompt: want LineCount=1, got %d", rows[2].LineCount)
	}
}

func TestFlattenRowsDormantFiltered(t *testing.T) {
	d := &aggregate.Directory{
		Path: "/p",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Dormant}},
		},
		WorkingN: 1,
		DormantN: 1,
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
	rows := FlattenRows(tree, TreeOpts{ShowAll: false})
	n := 0
	for _, r := range rows {
		if r.Kind == SessionKind {
			n++
		}
	}
	if n != 1 {
		t.Errorf("dormant filtered: want 1 session row, got %d", n)
	}
}

func TestFlattenRowsFlatIdxSpansMultipleDirs(t *testing.T) {
	d1 := &aggregate.Directory{
		Path: "/p1",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "a", Status: session.Working}},
			{Session: &session.Session{SessionID: "b", Status: session.Working}},
		},
	}
	d2 := &aggregate.Directory{
		Path: "/p2",
		Sessions: []*aggregate.SessionView{
			{Session: &session.Session{SessionID: "c", Status: session.Working}},
		},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d1, d2}}
	rows := FlattenRows(tree, TreeOpts{})
	var got []int
	for _, r := range rows {
		if r.Kind == SessionKind {
			got = append(got, r.FlatIdx)
		}
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("FlatIdx should be 0,1,2 across dirs; got %v", got)
	}
}

func TestFlattenRowsDirIdxCorrect(t *testing.T) {
	d0 := &aggregate.Directory{
		Path:     "/p0",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "x", Status: session.Working}}},
	}
	d1 := &aggregate.Directory{
		Path:     "/p1",
		Sessions: []*aggregate.SessionView{{Session: &session.Session{SessionID: "y", Status: session.Working}}},
	}
	tree := &aggregate.Tree{Dirs: []*aggregate.Directory{d0, d1}}
	rows := FlattenRows(tree, TreeOpts{})
	// Find the session row for d1
	found := false
	for _, r := range rows {
		if r.Kind == SessionKind && r.DirIdx == 1 {
			found = true
		}
	}
	if !found {
		t.Error("expected a SessionKind row with DirIdx=1")
	}
	_ = fmt.Sprintf // keep fmt import used
}
```

- [ ] **Step 2: Run to verify failure**

```bash
cd packages/claude-agents-tui && go test ./internal/render/ -run TestFlattenRows -v
```

Expected: `FAIL — undefined: FlattenRows, DirHeaderKind, SessionKind, BlankKind`

- [ ] **Step 3: Implement `rows.go`**

Create `internal/render/rows.go`:

```go
package render

import "github.com/phillipgreenii/claude-agents-tui/internal/aggregate"

// RowKind identifies what kind of content a Row represents in the session list.
type RowKind int

const (
	DirHeaderKind RowKind = iota
	SessionKind
	BlankKind // blank separator line after each directory group
)

// Row is one logical element in the rendered session list.
type Row struct {
	Kind      RowKind
	DirIdx    int // index into tree.Dirs
	SessIdx   int // SessionKind only: index within dir's visible sessions
	FlatIdx   int // SessionKind only: global session index matching TreeOpts.Cursor
	LineCount int // terminal lines this row occupies (1 or 2)
}

// FlattenRows converts a Tree into an ordered slice of Rows for window rendering.
// Empty dirs (no visible sessions under the current opts) are omitted.
func FlattenRows(tree *aggregate.Tree, opts TreeOpts) []Row {
	var rows []Row
	flatIdx := 0
	for dirIdx, d := range tree.Dirs {
		visible := visibleSessions(d.Sessions, opts.ShowAll)
		if len(visible) == 0 {
			continue
		}
		rows = append(rows, Row{Kind: DirHeaderKind, DirIdx: dirIdx, LineCount: 1})
		for sessIdx, s := range visible {
			lc := 1
			if s.SessionEnrichment.FirstPrompt != "" {
				lc = 2
			}
			rows = append(rows, Row{
				Kind:      SessionKind,
				DirIdx:    dirIdx,
				SessIdx:   sessIdx,
				FlatIdx:   flatIdx,
				LineCount: lc,
			})
			flatIdx++
		}
		rows = append(rows, Row{Kind: BlankKind, DirIdx: dirIdx, LineCount: 1})
	}
	return rows
}
```

- [ ] **Step 4: Run to verify pass**

```bash
go test ./internal/render/ -run TestFlattenRows -v
```

Expected: all `TestFlattenRows*` tests `PASS`

- [ ] **Step 5: Commit**

```bash
git add internal/render/rows.go internal/render/rows_test.go
git commit -m "feat(tui): add Row model and FlattenRows for window rendering"
```

---

## Task 2: Window renderer

**Files:**

- Create: `internal/render/window.go`
- Create: `internal/render/window_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/render/window_test.go`:

```go
package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)

// nSessions builds a tree with one dir "/p" containing n working sessions.
func nSessions(n int) *aggregate.Tree {
	d := &aggregate.Directory{Path: "/p"}
	for i := 0; i < n; i++ {
		d.Sessions = append(d.Sessions, &aggregate.SessionView{
			Session: &session.Session{
				SessionID: fmt.Sprintf("id-%d", i),
				Status:    session.Working,
			},
		})
		d.WorkingN++
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestLastVisibleIdxAllFit(t *testing.T) {
	rows := []Row{
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	if got := LastVisibleIdx(rows, 0, 10); got != 2 {
		t.Errorf("want 2, got %d", got)
	}
}

func TestLastVisibleIdxNoneFit(t *testing.T) {
	rows := []Row{{Kind: SessionKind, LineCount: 2}}
	if got := LastVisibleIdx(rows, 0, 1); got != -1 {
		t.Errorf("want -1, got %d", got)
	}
}

func TestLastVisibleIdxPartialFit(t *testing.T) {
	rows := []Row{
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	if got := LastVisibleIdx(rows, 0, 2); got != 1 {
		t.Errorf("want 1, got %d", got)
	}
}

func TestLastVisibleIdxWithOffset(t *testing.T) {
	rows := []Row{
		{Kind: DirHeaderKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
		{Kind: SessionKind, LineCount: 1},
	}
	// offset=1, budget=1 → only rows[1] fits
	if got := LastVisibleIdx(rows, 1, 1); got != 1 {
		t.Errorf("want 1, got %d", got)
	}
}

func TestRenderWindowNoOverflow(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{})
	if strings.Contains(out, "↑") {
		t.Error("no top indicator expected at offset 0 with large budget")
	}
	if strings.Contains(out, "↓") {
		t.Error("no bottom indicator expected when all rows fit")
	}
	if !strings.Contains(out, "id-0") || !strings.Contains(out, "id-2") {
		t.Errorf("expected all sessions visible, got:\n%s", out)
	}
}

func TestRenderWindowBottomIndicator(t *testing.T) {
	tree := nSessions(10)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 4, TreeOpts{})
	if !strings.Contains(out, "↓") {
		t.Errorf("expected bottom indicator when sessions exceed budget, got:\n%s", out)
	}
	if strings.Contains(out, "↑") {
		t.Error("no top indicator expected at offset 0")
	}
}

func TestRenderWindowTopIndicator(t *testing.T) {
	tree := nSessions(10)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 3, 20, TreeOpts{})
	if !strings.Contains(out, "↑") {
		t.Errorf("expected top indicator at offset 3, got:\n%s", out)
	}
}

func TestRenderWindowBothIndicators(t *testing.T) {
	tree := nSessions(15)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 5, 5, TreeOpts{})
	if !strings.Contains(out, "↑") {
		t.Errorf("expected top indicator, got:\n%s", out)
	}
	if !strings.Contains(out, "↓") {
		t.Errorf("expected bottom indicator, got:\n%s", out)
	}
}

func TestRenderWindowStickyDirHeader(t *testing.T) {
	// One dir with 8 sessions; rows[0]=DirHeader, rows[1..8]=Sessions, rows[9]=Blank
	// scrollOffset=2 puts the dir header above the window.
	tree := nSessions(8)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 2, 6, TreeOpts{})
	// Dir path "/p" must appear (pinned header) even though it is above the offset.
	if !strings.Contains(out, "/p") {
		t.Errorf("expected sticky dir header '/p', got:\n%s", out)
	}
}

func TestRenderWindowNoStickyWhenHeaderInWindow(t *testing.T) {
	tree := nSessions(4)
	rows := FlattenRows(tree, TreeOpts{})
	// scrollOffset=0: dir header is naturally in the window.
	// The dir path should appear exactly once.
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{})
	if strings.Count(out, "/p") != 1 {
		t.Errorf("expected dir header exactly once, got:\n%s", out)
	}
}

func TestRenderWindowIndicatorSessionCount(t *testing.T) {
	// rows: [0]=DirHeader, [1]=Session(FlatIdx=0), [2]=Session(FlatIdx=1),
	//       [3]=Session(FlatIdx=2), [4]=Session(FlatIdx=3), [5]=Session(FlatIdx=4), [6]=Blank
	// scrollOffset=3 → sessions above = rows[1] and rows[2] → 2 sessions
	tree := nSessions(5)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 3, 6, TreeOpts{})
	if !strings.Contains(out, "↑ 2 sessions") {
		t.Errorf("expected '↑ 2 sessions', got:\n%s", out)
	}
}

func TestRenderWindowSingularSession(t *testing.T) {
	// 2 sessions; scrollOffset=2 → 1 session above → "↑ 1 session" (not "sessions")
	tree := nSessions(2)
	rows := FlattenRows(tree, TreeOpts{})
	// rows: [0]=DirHeader, [1]=Session(FlatIdx=0), [2]=Session(FlatIdx=1), [3]=Blank
	// scrollOffset=2: 1 session above (rows[1])
	out := RenderWindow(tree, rows, 2, 6, TreeOpts{})
	if !strings.Contains(out, "↑ 1 session") || strings.Contains(out, "↑ 1 sessions") {
		t.Errorf("expected '↑ 1 session' (singular), got:\n%s", out)
	}
}

func TestRenderWindowEmpty(t *testing.T) {
	out := RenderWindow(&aggregate.Tree{}, []Row{}, 0, 20, TreeOpts{})
	if out != "" {
		t.Errorf("expected empty output for empty rows, got: %q", out)
	}
}

func TestRenderWindowZeroHeight(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	out := RenderWindow(tree, rows, 0, 0, TreeOpts{})
	if out != "" {
		t.Errorf("expected empty output for zero bodyHeight, got: %q", out)
	}
}

func TestRenderWindowCursorSelected(t *testing.T) {
	tree := nSessions(3)
	rows := FlattenRows(tree, TreeOpts{})
	// Cursor=1 (second session), HasCursor=true
	out := RenderWindow(tree, rows, 0, 20, TreeOpts{HasCursor: true, Cursor: 1})
	lines := strings.Split(out, "\n")
	var sessionLines []string
	for _, l := range lines {
		if strings.Contains(l, "├─") || strings.Contains(l, "└─") {
			sessionLines = append(sessionLines, l)
		}
	}
	if len(sessionLines) < 2 {
		t.Fatalf("expected session lines, got:\n%s", out)
	}
	if !strings.HasPrefix(sessionLines[1], "> ") {
		t.Errorf("cursor=1: second session should start with '> ', got %q", sessionLines[1])
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/render/ -run "TestLastVisible|TestRenderWindow" -v
```

Expected: `FAIL — undefined: LastVisibleIdx, RenderWindow`

- [ ] **Step 3: Implement `window.go`**

Create `internal/render/window.go`:

```go
package render

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
)

// LastVisibleIdx returns the index of the last row from rows[offset:] that
// fits within budget terminal lines. Returns offset-1 when nothing fits.
func LastVisibleIdx(rows []Row, offset, budget int) int {
	last := offset - 1
	for i := offset; i < len(rows); i++ {
		if budget < rows[i].LineCount {
			break
		}
		budget -= rows[i].LineCount
		last = i
	}
	return last
}

// stickyDir returns the DirIdx whose header is above scrollOffset but whose
// sessions are within the visible window. Returns -1 when no pin is needed.
func stickyDir(rows []Row, scrollOffset int) int {
	if scrollOffset == 0 {
		return -1
	}
	for i := scrollOffset; i < len(rows); i++ {
		switch rows[i].Kind {
		case BlankKind:
			continue
		case DirHeaderKind:
			return -1 // dir header is in the window — no pin needed
		case SessionKind:
			dirIdx := rows[i].DirIdx
			for j := i - 1; j >= 0; j-- {
				if rows[j].Kind == DirHeaderKind && rows[j].DirIdx == dirIdx {
					if j < scrollOffset {
						return dirIdx
					}
					return -1
				}
			}
			return -1
		}
	}
	return -1
}

func countSessionRows(rows []Row, from, to int) int {
	n := 0
	for i := from; i < to && i < len(rows); i++ {
		if rows[i].Kind == SessionKind {
			n++
		}
	}
	return n
}

func pluralSession(n int) string {
	if n == 1 {
		return fmt.Sprintf("%d session", n)
	}
	return fmt.Sprintf("%d sessions", n)
}

// RenderWindow renders a height-bounded window of rows with scroll indicators
// and a pinned dir header when its sessions have scrolled past the top edge.
func RenderWindow(tree *aggregate.Tree, rows []Row, scrollOffset, bodyHeight int, opts TreeOpts) string {
	if len(rows) == 0 || bodyHeight <= 0 {
		return ""
	}

	budget := bodyHeight

	topInd := scrollOffset > 0
	if topInd {
		budget--
	}

	sticky := stickyDir(rows, scrollOffset)
	if sticky >= 0 {
		budget--
	}

	lastVis := LastVisibleIdx(rows, scrollOffset, budget)
	botInd := lastVis < len(rows)-1
	if botInd {
		budget--
		lastVis = LastVisibleIdx(rows, scrollOffset, budget)
	}

	var sb strings.Builder

	if topInd {
		n := countSessionRows(rows, 0, scrollOffset)
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↑ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	if sticky >= 0 {
		sb.WriteString(renderDirRow(tree.Dirs[sticky], opts))
	}

	for i := scrollOffset; i <= lastVis; i++ {
		row := rows[i]
		switch row.Kind {
		case DirHeaderKind:
			sb.WriteString(renderDirRow(tree.Dirs[row.DirIdx], opts))
		case BlankKind:
			sb.WriteString("\n")
		case SessionKind:
			d := tree.Dirs[row.DirIdx]
			visible := visibleSessions(d.Sessions, opts.ShowAll)
			s := visible[row.SessIdx]
			isLast := row.SessIdx == len(visible)-1
			prefix, cont := "├─", "│"
			if isLast {
				prefix, cont = "└─", " "
			}
			selected := opts.HasCursor && row.FlatIdx == opts.Cursor
			sb.WriteString(renderSession(s, opts, prefix, cont, selected))
		}
	}

	if botInd {
		n := countSessionRows(rows, lastVis+1, len(rows))
		sb.WriteString(opts.Theme.Prompt.Render(fmt.Sprintf("  ↓ %s", pluralSession(n))))
		sb.WriteString("\n")
	}

	return sb.String()
}
```

- [ ] **Step 4: Run to verify pass**

```bash
go test ./internal/render/ -run "TestLastVisible|TestRenderWindow" -v
```

Expected: all `TestLastVisible*` and `TestRenderWindow*` tests `PASS`

- [ ] **Step 5: Run full render package tests**

```bash
go test ./internal/render/... -v
```

Expected: all pass — including pre-existing `TestTree*` and `TestDirRow*` tests

- [ ] **Step 6: Commit**

```bash
git add internal/render/window.go internal/render/window_test.go
git commit -m "feat(tui): add RenderWindow with scroll indicators and sticky dir headers"
```

---

## Task 3: Remove viewport; add syncScroll

**Files:**

- Modify: `internal/tui/model.go`
- Modify: `internal/tui/update.go`
- Modify: `internal/tui/model_test.go`

- [ ] **Step 1: Write the failing scroll-sync tests**

Add to `internal/tui/model_test.go`:

```go
import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/session"
)
```

Replace the existing import block with the one above (adds `"fmt"`), then add these two test functions:

```go
func makeLargeTree(n int) *aggregate.Tree {
	d := &aggregate.Directory{Path: "/p"}
	for i := 0; i < n; i++ {
		d.Sessions = append(d.Sessions, &aggregate.SessionView{
			Session: &session.Session{
				SessionID: fmt.Sprintf("id-%d", i),
				Status:    session.Working,
			},
		})
		d.WorkingN++
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{d}}
}

func TestSyncScrollAdvancesOffsetWhenCursorExitsViewport(t *testing.T) {
	m := NewModel(Options{Tree: makeLargeTree(20)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	for i := 0; i < 15; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	if m.scrollOffset == 0 {
		t.Errorf("expected scrollOffset > 0 after cursor moves past viewport, cursor=%d", m.cursor)
	}
}

func TestSyncScrollReturnsToZeroWhenCursorReturnsToTop(t *testing.T) {
	m := NewModel(Options{Tree: makeLargeTree(20)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	for i := 0; i < 15; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	for i := 0; i < 15; i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}

	if m.scrollOffset != 0 {
		t.Errorf("expected scrollOffset=0 after cursor returns to top, got %d", m.scrollOffset)
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/tui/ -run "TestSyncScroll" -v
```

Expected: `FAIL — m.scrollOffset undefined` (field doesn't exist yet)

- [ ] **Step 3: Update `model.go`**

In `internal/tui/model.go`, make these changes:

Remove the import line:

```go
"github.com/charmbracelet/bubbles/viewport"
```

Remove the field from `Model`:

```go
viewport      viewport.Model
```

Add `scrollOffset int` to the `Model` struct after `cursor int`:

```go
cursor        int
scrollOffset  int
```

Remove this line from `NewModel`:

```go
viewport:   viewport.New(80, 20),
```

The full updated `model.go`:

```go
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/caffeinate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

type Options struct {
	Tree       *aggregate.Tree
	Poller     Poller
	Interval   time.Duration
	Caffeinate *caffeinate.Manager
}

type Model struct {
	tree          *aggregate.Tree
	showAll       bool
	forceID       bool
	costMode      bool
	caffeinateOn  bool
	width, height int
	selected      *aggregate.SessionView
	cursor        int
	scrollOffset  int
	theme         render.Theme

	poller     Poller
	interval   time.Duration
	caffeinate *caffeinate.Manager
	lastErr    error
	anyWorking bool
}

func NewModel(o Options) *Model {
	return &Model{
		tree:       o.Tree,
		poller:     o.Poller,
		interval:   o.Interval,
		caffeinate: o.Caffeinate,
		theme:      render.NewTheme(render.DetectColors()),
	}
}

func (m *Model) Init() tea.Cmd {
	if m.poller == nil || m.interval <= 0 {
		return nil
	}
	return tea.Batch(m.pollNow(), tickCmd(m.interval))
}

func (m *Model) clampCursor() {
	maxIdx := 0
	if m.tree != nil {
		for _, d := range m.tree.Dirs {
			maxIdx += len(d.Sessions)
		}
	}
	if m.cursor >= maxIdx {
		m.cursor = maxIdx - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) sessionAt(idx int) *aggregate.SessionView {
	i := 0
	if m.tree == nil {
		return nil
	}
	for _, d := range m.tree.Dirs {
		for _, s := range d.Sessions {
			if i == idx {
				return s
			}
			i++
		}
	}
	return nil
}
```

- [ ] **Step 4: Update `update.go`**

Replace the entire `internal/tui/update.go` with:

```go
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

type TreeUpdatedMsg struct{ Tree *aggregate.Tree }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if isQuit(msg) {
			return m, tea.Quit
		}
		switch msg.String() {
		case "t":
			m.costMode = !m.costMode
		case "a":
			m.showAll = !m.showAll
		case "n":
			m.forceID = !m.forceID
		case "C":
			m.caffeinateOn = !m.caffeinateOn
		case "down", "j":
			m.cursor++
			m.clampCursor()
			m.syncScroll()
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.syncScroll()
		case "enter":
			m.selected = m.sessionAt(m.cursor)
		case "esc":
			m.selected = nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case pollTickMsg:
		return m, tea.Batch(m.pollNow(), tickCmd(m.interval))
	case pollResultMsg:
		m.tree = msg.tree
		m.anyWorking = msg.anyWorking
		if m.caffeinate != nil {
			m.caffeinate.SetToggle(m.caffeinateOn)
			m.caffeinate.Tick(msg.anyWorking)
		}
		m.clampCursor()
		m.syncScroll()
		if m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil {
			m.cursor = 0
			m.selected = nil
			m.scrollOffset = 0
		}
	case pollErrMsg:
		m.lastErr = msg.err
	case TreeUpdatedMsg:
		m.tree = msg.Tree
		m.clampCursor()
		m.syncScroll()
	}
	return m, nil
}

// syncScroll adjusts scrollOffset so that the cursor's row is within the
// visible window. Uses a conservative body-height estimate when the terminal
// size is not yet known exactly (before the first WindowSizeMsg).
func (m *Model) syncScroll() {
	if m.tree == nil || m.height == 0 {
		return
	}
	opts := render.TreeOpts{ShowAll: m.showAll}
	rows := render.FlattenRows(m.tree, opts)
	if len(rows) == 0 {
		return
	}
	// Subtract max header lines (7) + legend (1) for a conservative estimate.
	bodyHeight := m.height - 8
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	cursorRowIdx := -1
	for i, r := range rows {
		if r.Kind == render.SessionKind && r.FlatIdx == m.cursor {
			cursorRowIdx = i
			break
		}
	}
	if cursorRowIdx < 0 {
		return
	}
	if cursorRowIdx < m.scrollOffset {
		m.scrollOffset = cursorRowIdx
		return
	}
	for m.scrollOffset < len(rows) && render.LastVisibleIdx(rows, m.scrollOffset, bodyHeight) < cursorRowIdx {
		m.scrollOffset++
	}
}
```

- [ ] **Step 5: Run to verify pass**

```bash
go test ./internal/tui/ -v
```

Expected: all pass including new `TestSyncScroll*` tests

- [ ] **Step 6: Confirm full build**

```bash
go build ./...
```

Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add internal/tui/model.go internal/tui/update.go internal/tui/model_test.go
git commit -m "feat(tui): remove viewport; add scrollOffset and syncScroll for cursor-following scroll"
```

---

## Task 4: Wire view pipeline

**Files:**

- Modify: `internal/tui/view.go`

- [ ] **Step 1: Replace `view.go`**

The new view calls `render.RenderWindow` when the terminal height is known, and falls back to `render.Tree` (unbounded) before the first `WindowSizeMsg`.

Replace the entire `internal/tui/view.go` with:

```go
package tui

import (
	"strings"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

func (m *Model) View() string {
	if m.tree == nil {
		return "loading…"
	}
	if m.selected != nil {
		return RenderDetails(m.selected)
	}
	header := render.Header(m.tree, render.HeaderOpts{
		CaffeinateOn: m.caffeinateOn,
		ShowAll:      m.showAll,
		CostMode:     m.costMode,
		ForceID:      m.forceID,
		Theme:        m.theme,
	})
	legend := "● working  ○ idle  ? awaiting  ✕ dormant   🤖 subagents  🐚 shells  🌿 branch       [↑↓] nav  [enter] details"

	var body string
	noBlock := m.tree.CCUsageProbed && m.tree.ActiveBlock == nil && m.tree.CCUsageErr == nil
	if noBlock {
		body = "Sessions not shown — no active block.\n"
	} else {
		visibleCount := 0
		for _, d := range m.tree.Dirs {
			visibleCount += d.WorkingN + d.IdleN
			if m.showAll {
				visibleCount += d.DormantN
			}
		}
		if visibleCount == 0 {
			body = "No active sessions.\n"
		} else {
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
			rows := render.FlattenRows(m.tree, opts)
			if m.height > 0 {
				headerLines := strings.Count(header, "\n")
				bodyHeight := m.height - headerLines - 1 // 1 for legend
				if bodyHeight < 1 {
					bodyHeight = 1
				}
				body = render.RenderWindow(m.tree, rows, m.scrollOffset, bodyHeight, opts)
			} else {
				body = render.Tree(m.tree, opts)
			}
		}
	}
	return strings.Join([]string{header, body, legend}, "\n")
}
```

- [ ] **Step 2: Run all tests**

```bash
go test ./... -v 2>&1 | tail -40
```

Expected: all pass

- [ ] **Step 3: Build binary**

```bash
go build ./cmd/claude-agents-tui/
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add internal/tui/view.go
git commit -m "feat(tui): wire RenderWindow into View; body bounded to terminal height"
```

---

## Task 5: Remove bubbles dependency and Nix build

**Files:**

- Modify: `go.mod`, `go.sum` (via tooling)
- Modify: `default.nix` (via tooling)

- [ ] **Step 1: Remove bubbles from go.mod**

```bash
cd packages/claude-agents-tui && go mod tidy
```

Expected: `github.com/charmbracelet/bubbles` removed from `go.mod` and `go.sum`

Verify:

```bash
grep "charmbracelet/bubbles" go.mod go.sum
```

Expected: no output (dependency fully removed)

- [ ] **Step 2: Verify all Go tests still pass**

```bash
go test ./...
```

Expected: all pass

- [ ] **Step 3: Update vendorHash and verify Nix build**

```bash
cd packages/claude-agents-tui && ./update-deps.sh
```

Expected output ends with:

```
✓ Success! Dependencies updated and vendorHash refreshed.
  Updated: go.mod, go.sum
  Updated: default.nix (vendorHash)
```

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum default.nix
git commit -m "chore(tui): remove charmbracelet/bubbles dependency (viewport was unused)"
```

---

## Self-Review

**Spec coverage:**

- Row model (`rows.go`, `FlattenRows`) — Task 1 ✓
- `BlankKind` for inter-dir spacing — Task 1 ✓
- `LastVisibleIdx` exported for `syncScroll` — Task 2 ✓
- `RenderWindow` with top/bottom indicators, sticky dir header — Task 2 ✓
- Remove `viewport.Model` + bubbles import — Task 3 ✓
- `scrollOffset int` on Model — Task 3 ✓
- `syncScroll` on cursor move and tree update — Task 3 ✓
- `scrollOffset` reset to 0 on no-active-block reset — Task 3 ✓
- `view.go` uses `RenderWindow` when height known, falls back to `Tree` otherwise — Task 4 ✓
- `bodyHeight = m.height - headerLines - 1` — Task 4 ✓
- Remove `charmbracelet/bubbles` from `go.mod`/`go.sum` — Task 5 ✓
- `vendorHash` update via `update-deps.sh` — Task 5 ✓

**Placeholder scan:** None found.

**Type consistency:**

- `render.Row` defined Task 1; used in Task 2 (`window.go`), Task 3 (`syncScroll`), Task 4 (`view.go`) ✓
- `render.FlattenRows(tree, opts) []Row` — consistent across all call sites ✓
- `render.RenderWindow(tree, rows, scrollOffset, bodyHeight, opts) string` — consistent ✓
- `render.LastVisibleIdx(rows, offset, budget int) int` — consistent ✓
- `render.SessionKind` exported constant — used in `syncScroll` ✓
- `m.scrollOffset int` zero-value is correct default (start at top) ✓

**Risk note:** `syncScroll` uses a conservative `bodyHeight = m.height - 8` estimate (8 = max header lines + legend). The actual header can be 3–7 lines depending on block state, so the estimate may be 1–4 lines off. This means the cursor could be 1–4 rows away from the visible edge in the worst case before the next render corrects it. Acceptable — the exact sync happens at render time via `m.scrollOffset` feeding into `RenderWindow`.
