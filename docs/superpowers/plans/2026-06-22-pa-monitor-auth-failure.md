# pa-monitor Authentication-Failure Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Claude Code HTTP-401 `authentication_failed` errors a visible, red, actionable (`run /login`), account-wide, alertable condition across pa-monitor's TUI, CLI, and Grafana (dashboard banner + provisioned alert rule).

**Architecture:** Detection/classification/no-retry already work (`authentication_failed` → `ClassTerminal` → `Retryable()==false`); this plan only adds display + alerting. New code is thin: a render-package glyph + alert-bar segment, an aggregate `AuthFailedCount()` helper, three CLI helpers, a Grafana Stat banner, a provisioned alert rule, and minimal `alertRuleFiles` provisioning in the observability nix module.

**Tech Stack:** Go (lipgloss/bubbletea TUI, protobuf RPC), Grafana (provisioned dashboard JSON + unified-alerting YAML), Nix (nix-darwin modules). Tests: Go `testing`; `jq`/`yq` for JSON/YAML; `nix flake check` for nix.

**Repos / working dirs:**

- `PM = /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support` (pa-monitor + its darwin module). Branch: `pa-monitor-auth-failure` (already created).
- `OBS = /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps` (observability/Grafana provisioning module).
- Go module root: `$PM/packages/pa-monitor` — run all `go` commands from there.

**Spec:** `$PM/docs/superpowers/specs/2026-06-22-pa-monitor-auth-failure-design.md`

**Conventions:** Per repo CLAUDE.md — `nix fmt`/pre-commit must pass before each commit (treefmt reformats Markdown/Nix). Commit hygiene to avoid prek's stash/restore dropping new files: stage **only** your changed files, and if treefmt reformats on commit, `git add` the reformatted file and re-commit. Prefer `jq`/`yq` over `python3 -c`. There are unrelated pre-existing unstaged changes under `packages/claude-extended-tool-approver/` — do not stage or revert them.

---

## File Structure

**`$PM/packages/pa-monitor` (Go):**

- `internal/render/theme.go` — add `Error` (red) style to `Theme`.
- `internal/render/tree.go` — `authFailed` predicate + `⊘` glyph in `sessionGlyph`.
- `internal/render/alerts.go` — `Theme` field on `AlertsOpts` + highest-priority auth segment.
- `internal/render/modals.go` — legend rows for `⊘`/`⚠`/`✗`.
- `internal/core/aggregate/tree.go` — `AuthFailedCount()` method.
- `internal/core/transcript/classifier_test.go` — contract guard (auth is non-retryable).
- `internal/tui/view.go` — pass `Theme` into `AlertsOpts`.
- `cmd/pa-monitor/cli_format.go` — `apiErrorIsAuthFailure`, `formatAuthFailureBanner`, `auth` column, info hint.
- `cmd/pa-monitor/cli.go` — print the banner in `runStatus`.
- `grafana/pa-monitor-overview.json` — top banner Stat panel (id 105, y-shift +3).
- `grafana/alerting/auth-failure.yaml` — provisioned unified-alerting rule (new).

**`$PM` (darwin module):**

- `darwin/modules/pa-monitor/default.nix` — register `alertRuleFiles`.

**`$OBS` (observability module):**

- `darwin/modules/observability/alerting.nix` — new `alertRuleFiles` option + render.
- `darwin/modules/observability/ui.nix` — `provisioning/alerting/` mkdir + symlink.
- `darwin/modules/observability/default.nix` — import `alerting.nix`.

Tasks 1–7 are Go (TDD). Task 8 is the dashboard JSON. Task 9 is the alert YAML. Tasks 10–11 are nix wiring. Tasks are ordered so each leaves the tree building/green.

---

## Task 1: Add a red `Error` style to the Theme

**Files:**

- Modify: `internal/render/theme.go:11-21` (struct) and `:46-56` (`NewTheme` color branch)

- [ ] **Step 1: Add the struct field**

In `internal/render/theme.go`, add `Error` to the `Theme` struct (after `Awaiting`):

```go
type Theme struct {
	Working      lipgloss.Style
	Idle         lipgloss.Style
	Awaiting     lipgloss.Style
	Error        lipgloss.Style
	Dormant      lipgloss.Style
	Cursor       lipgloss.Style
	DirRow       lipgloss.Style
	Branch       lipgloss.Style
	Prompt       lipgloss.Style
	ActiveToggle lipgloss.Style
}
```

- [ ] **Step 2: Populate it in the color branch of `NewTheme`**

In the `return Theme{ ... }` at the end of `NewTheme` (the `hasColors` branch), add:

```go
		Error:        lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
```

(ANSI palette color `1` = red. The no-color branch leaves `Error` as the zero `lipgloss.Style`, which renders plain text — correct for non-color terminals.)

- [ ] **Step 3: Verify it builds**

Run (from `$PM/packages/pa-monitor`): `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
cd "$PM/packages/pa-monitor"
git add internal/render/theme.go
git commit -m "feat(pa-monitor): add red Error style to TUI Theme"
```

---

## Task 2: Per-session `⊘` auth glyph

**Files:**

- Modify: `internal/render/tree.go` (add `authFailed`; edit `sessionGlyph` at `:212-220`)
- Test: `internal/render/session_glyph_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/render/session_glyph_test.go`:

```go
// TestSessionGlyphAuthFailure verifies a terminal authentication_failed error
// shows the ⊘ glyph and not the generic ⚠/✗ error glyphs.
func TestSessionGlyphAuthFailure(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrAuthFailed,
		IsTerminal: true,
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, false, nil)

	glyph := sessionGlyph(sv, Theme{})
	if !strings.Contains(glyph, "⊘") {
		t.Errorf("auth failure: expected ⊘ glyph; got %q", glyph)
	}
	if strings.Contains(glyph, "⚠") || strings.Contains(glyph, "✗") {
		t.Errorf("auth failure: should not use generic error glyph; got %q", glyph)
	}
}

// TestSessionGlyphNonAuthNonRetryableStillX guards that other non-retryable
// terminal errors keep the ✗ glyph (no accidental ⊘ widening).
func TestSessionGlyphNonAuthNonRetryableStillX(t *testing.T) {
	le := &transcript.ErrorRecord{
		Kind:       transcript.ErrInvalidRequest,
		IsTerminal: true,
		At:         time.Now(),
	}
	sv := makeSessionView(session.Idle, le, false, nil)

	glyph := sessionGlyph(sv, Theme{})
	if strings.Contains(glyph, "⊘") {
		t.Errorf("invalid_request should not use ⊘; got %q", glyph)
	}
	if !strings.Contains(glyph, "✗") {
		t.Errorf("invalid_request: expected ✗; got %q", glyph)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/render/ -run 'TestSessionGlyphAuthFailure|TestSessionGlyphNonAuthNonRetryableStillX' -v`
Expected: FAIL — `TestSessionGlyphAuthFailure` reports `⊘` missing (it currently renders `✗`).

- [ ] **Step 3: Add the `authFailed` predicate**

In `internal/render/tree.go`, add near the top of the file's helpers (above `sessionGlyph`):

```go
// authFailed reports a terminal authentication failure (non-retryable; run /login).
func authFailed(le *transcript.ErrorRecord) bool {
	return le != nil && le.IsTerminal && le.Kind == transcript.ErrAuthFailed
}
```

Confirm `tree.go` already imports `"github.com/phillipgreenii/pa-monitor/internal/core/transcript"` (it uses `*transcript.ErrorRecord` elsewhere). If not, add it.

- [ ] **Step 4: Special-case auth in `sessionGlyph`**

In `internal/render/tree.go`, replace the existing error block (currently):

```go
	le := s.SessionEnrichment.LastError
	if le != nil && le.IsTerminal {
		if s.SessionEnrichment.LastErrorRetryable {
			primary = "⚠"
		} else {
			primary = "✗"
		}
	}
```

with:

```go
	le := s.SessionEnrichment.LastError
	if le != nil && le.IsTerminal {
		switch {
		case authFailed(le):
			primary = theme.Error.Render("⊘") // auth failure — run /login
		case s.SessionEnrichment.LastErrorRetryable:
			primary = "⚠"
		default:
			primary = "✗"
		}
	}
```

- [ ] **Step 5: Run to verify pass (and no regressions)**

Run: `go test ./internal/render/ -run 'TestSessionGlyph' -v`
Expected: PASS (all glyph tests, new and existing).

- [ ] **Step 6: Commit**

```bash
git add internal/render/tree.go internal/render/session_glyph_test.go
git commit -m "feat(pa-monitor): distinct red ⊘ glyph for auth failures in TUI tree"
```

---

## Task 3: `Tree.AuthFailedCount()` helper

**Files:**

- Modify: `internal/core/aggregate/tree.go` (add method after `TopupShouldDisplay`, ~`:101`)
- Test: `internal/core/aggregate/tree_test.go` (new file)

- [ ] **Step 1: Write the failing test**

Create `internal/core/aggregate/tree_test.go`:

```go
package aggregate

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func authSession(sid string, kind transcript.ErrorKind, terminal bool) *SessionView {
	return &SessionView{
		Session: &session.Session{SessionID: sid},
		SessionEnrichment: SessionEnrichment{
			LastError: &transcript.ErrorRecord{Kind: kind, IsTerminal: terminal},
		},
	}
}

func TestAuthFailedCount(t *testing.T) {
	tree := &Tree{Dirs: []*Directory{{Sessions: []*SessionView{
		authSession("a", transcript.ErrAuthFailed, true),  // counts
		authSession("b", transcript.ErrAuthFailed, false), // not terminal — skip
		authSession("c", transcript.ErrServerError, true), // wrong kind — skip
		{Session: &session.Session{SessionID: "d"}},        // no error — skip
		authSession("e", transcript.ErrAuthFailed, true),  // counts
	}}}}

	if got := tree.AuthFailedCount(); got != 2 {
		t.Errorf("AuthFailedCount() = %d, want 2", got)
	}
	if got := (*Tree)(nil).AuthFailedCount(); got != 0 {
		t.Errorf("nil tree AuthFailedCount() = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/core/aggregate/ -run TestAuthFailedCount -v`
Expected: FAIL — `tree.AuthFailedCount undefined`.

- [ ] **Step 3: Implement the method**

In `internal/core/aggregate/tree.go`, after `TopupShouldDisplay`:

```go
// AuthFailedCount returns the number of sessions whose most recent error is a
// terminal authentication failure (HTTP 401 → run /login). Because a 401 is
// account-wide, any positive count means the credentials are broken for the
// whole user. Safe on a nil tree.
func (t *Tree) AuthFailedCount() int {
	n := 0
	for _, s := range t.Sessions() {
		le := s.SessionEnrichment.LastError
		if le != nil && le.IsTerminal && le.Kind == transcript.ErrAuthFailed {
			n++
		}
	}
	return n
}
```

Confirm `tree.go` imports `"github.com/phillipgreenii/pa-monitor/internal/core/transcript"` (it types `LastError` as `*transcript.ErrorRecord`, so it does).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/core/aggregate/ -run TestAuthFailedCount -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/core/aggregate/tree.go internal/core/aggregate/tree_test.go
git commit -m "feat(pa-monitor): Tree.AuthFailedCount for account-wide auth-failure detection"
```

---

## Task 4: Global alert-bar auth banner (highest priority, red)

**Files:**

- Modify: `internal/render/alerts.go` (`AlertsOpts` struct `:13-21`, `Alerts` `:30`)
- Modify: `internal/tui/view.go:44-50` (pass `Theme`)
- Test: `internal/render/alerts_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/render/alerts_test.go`. Also add the two imports shown in Step 1b.

```go
func treeWithAuthFailures(n int) *aggregate.Tree {
	var sv []*aggregate.SessionView
	for i := 0; i < n; i++ {
		sv = append(sv, &aggregate.SessionView{
			Session: &session.Session{SessionID: string(rune('a' + i))},
			SessionEnrichment: aggregate.SessionEnrichment{
				LastError: &transcript.ErrorRecord{Kind: transcript.ErrAuthFailed, IsTerminal: true},
			},
		})
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: sv}}}
}

func TestAlertsAuthFailureShows(t *testing.T) {
	out := Alerts(treeWithAuthFailures(1), AlertsOpts{Now: time.Now(), Width: 200})
	if !strings.Contains(out, "⊘") || !strings.Contains(out, "AUTHENTICATION FAILURE") {
		t.Errorf("expected auth-failure banner, got: %q", out)
	}
	if !strings.Contains(out, "/login") {
		t.Errorf("expected /login remediation, got: %q", out)
	}
}

func TestAlertsAuthFailureAbsentWhenNone(t *testing.T) {
	out := Alerts(treeWithAuthFailures(0), AlertsOpts{Now: time.Now(), Width: 200})
	if strings.Contains(out, "AUTHENTICATION") {
		t.Errorf("expected no auth banner when none failing, got: %q", out)
	}
}

func TestAlertsAuthFailureSortsFirst(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := treeWithAuthFailures(1)
	tree.WindowResetsAt = now.Add(60 * time.Second)
	out := Alerts(tree, AlertsOpts{
		Now: now, Width: 200, AutoResume: true, WindowResetsAt: tree.WindowResetsAt,
	})
	authIdx := strings.Index(out, "⊘")
	resumeIdx := strings.Index(out, "⏸")
	if authIdx == -1 || resumeIdx == -1 || authIdx > resumeIdx {
		t.Errorf("auth banner must sort before resume; got: %q", out)
	}
}
```

**Step 1b:** add to the import block of `alerts_test.go`:

```go
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/render/ -run TestAlertsAuthFailure -v`
Expected: FAIL — banner text absent.

- [ ] **Step 3: Add `Theme` to `AlertsOpts`**

In `internal/render/alerts.go`, add `Theme` to `AlertsOpts`:

```go
type AlertsOpts struct {
	Now             time.Time
	Width           int
	Theme           Theme
	AutoResume      bool
	WindowResetsAt  time.Time
	AutoResumeDelay time.Duration
	TopupPoolUSD    float64
	TopupConsumed   float64
}
```

- [ ] **Step 4: Prepend the auth segment in `Alerts`**

In `internal/render/alerts.go`, immediately after `var segs []string` and **before** the auto-resume block, insert:

```go
	if n := tree.AuthFailedCount(); n > 0 {
		var seg string
		switch tier {
		case wrap.TierWide:
			seg = "⊘ AUTHENTICATION FAILURE — run /login"
		case wrap.TierNarrow:
			seg = "⊘ auth — run /login"
		default:
			seg = "⊘ /login"
		}
		segs = append(segs, opts.Theme.Error.Render(seg))
	}
```

(The zero `Theme{}` used in tests renders `seg` unstyled, so `strings.Contains` still matches. `tier` is already computed at the top of `Alerts`.)

- [ ] **Step 5: Thread the theme at the call site**

In `internal/tui/view.go`, in the `render.AlertsOpts{...}` literal (~`:44`), add the `Theme` field:

```go
	alerts := render.Alerts(m.tree, render.AlertsOpts{
		Now:             now,
		Width:           m.width,
		Theme:           m.theme,
		AutoResume:      m.autoResumeEnabled,
		WindowResetsAt:  m.tree.WindowResetsAt,
		AutoResumeDelay: m.autoResumeDelay,
	})
```

- [ ] **Step 6: Run to verify pass (and no regressions)**

Run: `go test ./internal/render/ ./internal/tui/ -v`
Expected: PASS (new auth tests + existing alerts tests).

- [ ] **Step 7: Commit**

```bash
git add internal/render/alerts.go internal/render/alerts_test.go internal/tui/view.go
git commit -m "feat(pa-monitor): highest-priority red auth-failure banner in TUI alert bar"
```

---

## Task 5: Document the error glyphs in the legend

**Files:**

- Modify: `internal/render/modals.go:182-191` (`legendRows`)
- Test: `internal/render/modals_test.go:68-75`

- [ ] **Step 1: Update the failing test**

In `internal/render/modals_test.go`, extend the symbol slice in `TestLegendModalContainsAllSymbols`:

```go
	for _, sym := range []string{"●", "○", "⏸", "?", "✕", "🤖", "🐚", "🌿", "⊘", "⚠", "✗"} {
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/render/ -run TestLegendModalContainsAllSymbols -v`
Expected: FAIL — `⊘` (and `⚠`/`✗`) missing from legend.

- [ ] **Step 3: Add the legend rows**

In `internal/render/modals.go`, add to `legendRows` (after the `✕` dormant row, before the emoji rows):

```go
	{Left: "⊘", Right: "auth       authentication failure — run /login"},
	{Left: "⚠", Right: "error      retryable error (auto-resuming)"},
	{Left: "✗", Right: "error      non-retryable error"},
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/render/ -run TestLegendModalContainsAllSymbols -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/render/modals.go internal/render/modals_test.go
git commit -m "docs(pa-monitor): document ⊘/⚠/✗ error glyphs in TUI legend"
```

---

## Task 6: CLI — auth banner, `auth` column, and info hint

**Files:**

- Modify: `cmd/pa-monitor/cli_format.go` (add helpers; edit `formatStatusSessions:69-71`, `formatSessionInfo:133-149`)
- Modify: `cmd/pa-monitor/cli.go:38-75` (`runStatus` — print banner)
- Test: `cmd/pa-monitor/cli_format_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/pa-monitor/cli_format_test.go`:

```go
func authDetail(sid string) *pb.SessionDetail {
	return &pb.SessionDetail{
		View:      &pb.SessionView{SessionId: sid, Status: "idle"},
		LastError: &pb.ApiError{Kind: "authentication_failed", IsTerminal: true, Text: "Please run /login · API Error: 401 Invalid authentication credentials"},
	}
}

func TestFormatAuthFailureBanner(t *testing.T) {
	if out := formatAuthFailureBanner(nil); out != "" {
		t.Errorf("no sessions: want empty, got %q", out)
	}
	one := formatAuthFailureBanner([]*pb.SessionDetail{authDetail("a")})
	if !strings.Contains(one, "⊘") || !strings.Contains(one, "/login") || !strings.Contains(one, "(1 session)") {
		t.Errorf("one auth failure banner wrong: %q", one)
	}
	two := formatAuthFailureBanner([]*pb.SessionDetail{authDetail("a"), authDetail("b")})
	if !strings.Contains(two, "(2 sessions)") {
		t.Errorf("two auth failures plural wrong: %q", two)
	}
}

func TestFormatStatusSessionsAuthColumn(t *testing.T) {
	out := formatStatusSessions([]*pb.SessionDetail{authDetail("sid-1")})
	if !strings.Contains(out, "auth") {
		t.Errorf("expected compact 'auth' in ERROR column, got:\n%s", out)
	}
	if strings.Contains(out, "authentication_failed") {
		t.Errorf("expected compact 'auth', not raw kind, got:\n%s", out)
	}
}

func TestFormatSessionInfoAuthHint(t *testing.T) {
	out := formatSessionInfo(authDetail("sid-1"))
	if !strings.Contains(out, "authentication_failed — run /login") {
		t.Errorf("expected run /login hint on last_error line, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/pa-monitor/ -run 'TestFormatAuthFailureBanner|TestFormatStatusSessionsAuthColumn|TestFormatSessionInfoAuthHint' -v`
Expected: FAIL — `formatAuthFailureBanner` undefined; column shows raw kind; info lacks hint.

- [ ] **Step 3: Add the predicate + banner helper**

In `cmd/pa-monitor/cli_format.go`, after `apiErrorIsEscalated` (~`:169`), add:

```go
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
```

- [ ] **Step 4: Use the compact `auth` label in the status table**

In `formatStatusSessions`, replace the **entire** terminal-error `if` block (currently `if le := sd.GetLastError(); le != nil && le.GetIsTerminal() { r.errKind = le.GetKind() }` at lines `:69-71`) — not just the inner line:

```go
		if le := sd.GetLastError(); le != nil && le.GetIsTerminal() {
			if apiErrorIsAuthFailure(le) {
				r.errKind = "auth"
			} else {
				r.errKind = le.GetKind()
			}
		}
```

- [ ] **Step 5: Add the `run /login` hint in `info`**

In `formatSessionInfo`, replace the `kindStr` construction (currently at `:134-137`):

```go
		kindStr := le.GetKind()
		if apiErrorIsAuthFailure(le) {
			kindStr += " — run /login"
		} else if apiErrorIsEscalated(le) {
			kindStr += "  (escalated)"
		}
```

- [ ] **Step 6: Run to verify the unit tests pass**

Run: `go test ./cmd/pa-monitor/ -run 'TestFormatAuthFailureBanner|TestFormatStatusSessionsAuthColumn|TestFormatSessionInfoAuthHint|TestFormatStatusSessions|TestFormatSessionInfo' -v`
Expected: PASS (new + existing format tests).

- [ ] **Step 7: Wire the banner into `runStatus`**

In `cmd/pa-monitor/cli.go`, `runStatus` today has **exactly one** detail-collection loop (~`:55-72`), located _after_ the cost/caffeinate/auto_resume prints and feeding `formatStatusSessions`. **Move that entire loop up** to just after the `sessions:` summary line, then add the banner print right after it. There must remain only **one** `var details []*pb.SessionDetail` in the function — leaving two is a `details redeclared` compile error. After this line:

```go
	fmt.Printf("sessions:      %d working, %d idle, %d dormant\n", working, idle, dormant)
```

insert detail collection + banner:

```go
	// Collect per-session details once (LastError + PendingNudge); used for the
	// auth banner here and the errors/nudges table below.
	var details []*pb.SessionDetail
	for _, d := range state.GetDirs() {
		for _, sv := range d.GetSessions() {
			sid := sv.GetSessionId()
			if sid == "" {
				continue
			}
			sel := &pb.Selector{Target: &pb.Selector_SessionId{SessionId: sid}}
			sd, err := client.C.GetSessionInfo(ctx, &pb.GetSessionInfoRequest{Selector: sel})
			if err != nil {
				continue
			}
			details = append(details, sd)
		}
	}
	if banner := formatAuthFailureBanner(details); banner != "" {
		fmt.Print(banner)
	}
```

Because you **moved** (not copied) the original loop, the bottom of the function now has no second `var details` collection block — only the final render call remains:

```go
	if annotation := formatStatusSessions(details); annotation != "" {
		fmt.Print(annotation)
	}
```

- [ ] **Step 8: Verify the package builds and all CLI tests pass**

Run: `go build ./... && go test ./cmd/pa-monitor/ -v`
Expected: build clean; tests PASS.

- [ ] **Step 9: Commit**

```bash
git add cmd/pa-monitor/cli_format.go cmd/pa-monitor/cli.go cmd/pa-monitor/cli_format_test.go
git commit -m "feat(pa-monitor): CLI auth-failure banner, compact 'auth' column, run /login hint"
```

---

## Task 7: Contract guard — auth is classified non-retryable

This locks the upstream behavior the no-retry path depends on (the nudger gate is `!transcript.Retryable(s.LastError)` at `internal/daemon/nudger/disrupt.go:71`). Note: `internal/daemon/nudger/disrupt_test.go:87 TestDisruptProducerCancelsOnNonRetryable` already exercises `transcript.ErrAuthFailed` end-to-end through the producer — this task adds a focused unit guard at the classification layer.

**Files:**

- Test: `internal/core/transcript/classifier_test.go` (append)

- [ ] **Step 1: Write the test**

Append to `internal/core/transcript/classifier_test.go`:

```go
func TestAuthFailedIsTerminalAndNonRetryable(t *testing.T) {
	rec := &ErrorRecord{
		Kind:       ErrAuthFailed,
		Text:       "Please run /login · API Error: 401 Invalid authentication credentials",
		IsTerminal: true,
	}
	if rec.RetryClass() != ClassTerminal {
		t.Errorf("auth failure RetryClass = %v, want ClassTerminal", rec.RetryClass())
	}
	if Retryable(rec) {
		t.Error("auth failure must be non-retryable (Retryable == false)")
	}
}
```

(`classifier_test.go` is in `package transcript`, so `ErrorRecord`, `ErrAuthFailed`, `ClassTerminal`, and `Retryable` are referenced unqualified — confirm against the existing tests in that file, which use the same unqualified names. If the file imports the shared types under an alias, match that.)

- [ ] **Step 2: Run to verify it passes (guard, not a change)**

Run: `go test ./internal/core/transcript/ -run TestAuthFailedIsTerminalAndNonRetryable -v`
Expected: PASS (the behavior already holds; this test guards against regression).

- [ ] **Step 3: Commit**

```bash
git add internal/core/transcript/classifier_test.go
git commit -m "test(pa-monitor): guard that auth_failed classifies terminal + non-retryable"
```

---

## Task 8: Grafana dashboard — top auth banner panel

**Files:**

- Modify: `grafana/pa-monitor-overview.json`

- [ ] **Step 1: Transform the JSON (shift all panels down 3, add banner id 105 at y:0)**

Run from `$PM/packages/pa-monitor`:

```bash
jq '
  .panels |= map(.gridPos.y += 3)
  | .panels += [{
      id: 105, type: "stat", title: "Authentication",
      datasource: {type: "prometheus", uid: "prometheus"},
      gridPos: {x: 0, y: 0, w: 24, h: 3},
      targets: [{
        expr: "sum(pa_monitor_sessions_errored{kind=\"authentication_failed\"}) or vector(0)",
        legendFormat: "", refId: "A"
      }],
      options: {colorMode: "background", graphMode: "none", textMode: "value"},
      fieldConfig: {defaults: {
        mappings: [
          {type: "value", options: {"0": {text: "✓ Auth OK", color: "green"}}},
          {type: "range", options: {from: 1, to: 1000000, result: {text: "⊘ AUTHENTICATION FAILURE — run /login", color: "red"}}}
        ],
        thresholds: {mode: "absolute", steps: [
          {value: null, color: "green"},
          {value: 1, color: "red"}
        ]}
      }}
    }]
' grafana/pa-monitor-overview.json > grafana/.overview.tmp && mv grafana/.overview.tmp grafana/pa-monitor-overview.json
```

- [ ] **Step 2: Validate structure**

Run:

```bash
jq -e '
  ([.panels[].id] | length) == ([.panels[].id] | unique | length)
  and ([.panels[] | select(.id == 105)] | length == 1)
  and ([.panels[] | select(.id == 105) | .gridPos.y] == [0])
  and ([.panels[] | select(.title == "Current status") | .gridPos.y] == [3])
' grafana/pa-monitor-overview.json
```

Expected: prints `true`, exit 0. If `false`/error, the transform is wrong — re-check Step 1.

- [ ] **Step 3: Commit**

```bash
git add grafana/pa-monitor-overview.json
git commit -m "feat(pa-monitor): always-on red auth-failure banner panel atop Grafana dashboard"
```

---

## Task 9: Grafana provisioned alert rule

**Files:**

- Create: `grafana/alerting/auth-failure.yaml`

- [ ] **Step 1: Create the rule file**

Create `$PM/packages/pa-monitor/grafana/alerting/auth-failure.yaml`:

```yaml
apiVersion: 1
groups:
  - orgId: 1
    name: pa-monitor
    folder: Claude Agents
    interval: 1m
    rules:
      - uid: pa-monitor-auth-failure
        title: pa-monitor authentication failure
        condition: C
        for: 0s # fire immediately — the error is already terminal
        noDataState: OK
        execErrState: Error
        labels:
          severity: critical
        annotations:
          summary: "Authentication failure — run /login"
          description: "One or more Claude Code sessions have a terminal authentication failure (HTTP 401). Run /login to re-authenticate."
        data:
          - refId: A
            relativeTimeRange:
              from: 600
              to: 0
            datasourceUid: prometheus
            model:
              refId: A
              editorMode: code
              instant: true
              expr: sum(pa_monitor_sessions_errored{kind="authentication_failed"}) or vector(0)
          - refId: B
            datasourceUid: __expr__
            model:
              refId: B
              type: reduce
              expression: A
              reducer: last
          - refId: C
            datasourceUid: __expr__
            model:
              refId: C
              type: threshold
              expression: B
              conditions:
                - evaluator:
                    type: gt
                    params: [0]
```

- [ ] **Step 2: Validate the YAML shape**

Run:

```bash
yq -e '.groups[0].rules[0].condition == "C"
  and (.groups[0].rules[0].data | length == 3)
  and (.groups[0].rules[0].data[0].datasourceUid == "prometheus")
  and (.groups[0].rules[0].for == "0s")' \
  grafana/alerting/auth-failure.yaml
```

Expected: prints `true`, exit 0.

- [ ] **Step 3: Commit**

```bash
git add grafana/alerting/auth-failure.yaml
git commit -m "feat(pa-monitor): provisioned Grafana alert rule for authentication failures"
```

---

## Task 10: Observability module — `alertRuleFiles` provisioning

Work in `$OBS` (`phillipgreenii-nix-support-apps`). Branch first.

**Files:**

- Create: `darwin/modules/observability/alerting.nix`
- Modify: `darwin/modules/observability/ui.nix` (provisioning dir + symlink)
- Modify: `darwin/modules/observability/default.nix` (import)

- [ ] **Step 1: Branch**

```bash
cd "$OBS"
git checkout -b pa-monitor-auth-failure
```

- [ ] **Step 2: Read the existing patterns first**

Read `darwin/modules/observability/dashboards.nix` (the `dashboardProviders` option + `catalog`/`runCommand` render) and `darwin/modules/observability/ui.nix` (the `provisioning/{datasources,dashboards}` mkdir + `ln -sfn` block, ~`:54-66`, and how `config` exposes `cfg.internal._renderedDashboards`). Match their style (option namespace `phillipgreenii.observability.*`, `lib.mkIf` guards, `runCommand` to assemble a directory). Run `rg -n "_renderedDashboards" darwin/modules/observability` to see how the internal handoff option is declared.

- [ ] **Step 3: Create `alerting.nix`**

Create `darwin/modules/observability/alerting.nix`:

```nix
{ config, lib, pkgs, ... }:
let
  cfg = config.phillipgreenii.observability;
  files = cfg.alertRuleFiles;
  # Assemble all contributed rule YAMLs into one directory Grafana can load
  # from provisioning/alerting. Unlike dashboards there is no provider catalog:
  # Grafana reads rule-definition YAML files directly.
  rulesDir = pkgs.runCommand "observability-alerting-rules" { } ''
    mkdir -p "$out"
    n=0
    ${lib.concatMapStringsSep "\n"
      (f: ''cp ${f} "$out/rule-$n.yaml"; n=$((n+1))'')
      files}
  '';
in
{
  options.phillipgreenii.observability.alertRuleFiles = lib.mkOption {
    type = lib.types.listOf lib.types.path;
    default = [ ];
    description = ''
      App-contributed Grafana unified-alerting provisioning YAML files. Each is
      copied into Grafana's provisioning/alerting directory. The alert's folder
      is named inside each rule's YAML (there is no provider catalog as there is
      for dashboards). Safe to set when observability is disabled (no-op).
    '';
  };

  config = lib.mkIf (cfg.enable && cfg.ui.enable && files != [ ]) {
    phillipgreenii.observability.internal._renderedAlertingDir = rulesDir;
  };
}
```

Note: `phillipgreenii.observability.internal` is declared as `lib.types.attrsOf lib.types.anything` (a free-form attrset), so assigning a fresh `_renderedAlertingDir` key needs no option declaration — the primary path above always works. (No fallback required.)

- [ ] **Step 4: Wire it into `ui.nix`**

In `darwin/modules/observability/ui.nix`:

1. Near `dashboardsCatalogPath`/`dashboardsDirPath` (~`:45-46`), add:

```nix
  alertingDirPath = cfg.internal._renderedAlertingDir or null;
```

2. In the provisioning-setup script, where it does `mkdir -p ".../provisioning/datasources" ".../provisioning/dashboards"`, add `"$svcDir/provisioning/alerting"` to that mkdir list.

3. After the dashboards `ln -sfn` block (~`:63-65`), add:

```nix
    ${lib.optionalString (alertingDirPath != null) ''
      for f in ${alertingDirPath}/*.yaml; do
        ln -sfn "$f" "$svcDir/provisioning/alerting/$(basename "$f")"
      done
    ''}
```

4. **Confirm unified alerting is on:** inspect the `grafanaIni` heredoc in `ui.nix`. Ensure it does **not** set `[unified_alerting] enabled = false` or enable legacy `[alerting]`. Grafana ~11.x (nixpkgs 26.05) defaults unified alerting on; if the ini is silent, that's correct — no change. If legacy alerting is enabled, add `[unified_alerting]` `enabled = true` and disable `[alerting]`.

- [ ] **Step 5: Import `alerting.nix`**

In `darwin/modules/observability/default.nix`, add `./alerting.nix` to the `imports` list alongside the other module files.

- [ ] **Step 6: Format + validate**

Run from `$OBS`:

```bash
nix fmt
nix flake check
```

Expected: `nix flake check` passes (the existing observability checks still build with the new option defaulting to `[]`).

- [ ] **Step 7: Commit**

```bash
git add darwin/modules/observability/alerting.nix darwin/modules/observability/ui.nix darwin/modules/observability/default.nix
git commit -m "feat(observability): provision app-contributed Grafana alert rules via alertRuleFiles"
```

---

## Task 11: Register pa-monitor's alert rule + full-repo check

**Files:**

- Modify: `$PM/darwin/modules/pa-monitor/default.nix:49-54`
- Modify: `$PM/packages/pa-monitor/README.md` (legend/symbols note)

- [ ] **Step 1: Register the rule file**

In `$PM/darwin/modules/pa-monitor/default.nix`, inside the same `lib.mkIf (obs.enable or false) { ... }` attrset (one element of the file's `lib.mkMerge`) that sets `phillipgreenii.observability.dashboardProviders.pa-monitor` (~`:50-53`), add a sibling assignment:

```nix
      phillipgreenii.observability.alertRuleFiles =
        [ ../../../packages/pa-monitor/grafana/alerting/auth-failure.yaml ];
```

(Same guard as the dashboard registration — no-op on machines without the observability stack. `alertRuleFiles` is a list; NixOS module merging concatenates lists, so multiple contributors compose safely.)

- [ ] **Step 2: Add a README note for the new symbol**

In `$PM/packages/pa-monitor/README.md`, find the symbols/legend section (`rg -n "⏸|legend|Symbols|glyph" README.md`) and add a line documenting `⊘ — authentication failure (run /login)`. If no symbols section exists, add a short "TUI symbols" subsection listing `● ○ ⏸ ? ✕ ⚠ ✗ ⊘` with one-line meanings.

- [ ] **Step 3: Format + validate both repos**

```bash
cd "$PM" && nix fmt && nix flake check
cd "$PM/packages/pa-monitor" && go test ./...
```

Expected: `nix flake check` passes; all Go tests PASS.

- [ ] **Step 4: Commit**

```bash
cd "$PM"
git add darwin/modules/pa-monitor/default.nix packages/pa-monitor/README.md
git commit -m "feat(pa-monitor): register auth-failure Grafana alert rule + document ⊘ symbol"
```

---

## Final verification (live, post-merge)

These require a running Grafana with the observability stack (cannot be unit-tested):

- [ ] Confirm `[unified_alerting] enabled` in the rendered `grafana.ini`; the rule appears under **Alerting → Alert rules** in a "Claude Agents" folder.
- [ ] With no auth failures, the dashboard banner shows green "✓ Auth OK" (never "No data"), and the alert state is **Normal**.
- [ ] Induce/simulate `pa_monitor_sessions_errored{kind="authentication_failed"} >= 1`: the dashboard banner turns red ("⊘ AUTHENTICATION FAILURE — run /login") and the alert transitions to **Firing**.
- [ ] In the live TUI, an auth-failed session row shows red `⊘` and the alert bar shows the banner; `pa-monitor status` prints the `⊘ authentication failure — run /login (N …)` line and `auth` in the ERROR column; `pa-monitor info <sid>` shows `authentication_failed — run /login`.

---

## Self-Review

**Spec coverage:** TUI glyph (T2); red style (T1); alert-bar banner + Theme threading (T4); `AuthFailedCount` (T3); legend (T5); CLI status banner + `auth` column + info hint + shared predicate (T6); classifier-contract + nudger no-retry (T7, plus existing `disrupt_test.go:87`); dashboard banner with `or vector(0)` + y-shift + unique id (T8); provisioned alert rule with valid `groups/rules/data[]` (T9); observability `alertRuleFiles` + `provisioning/alerting` + unified-alerting check (T10); pa-monitor registration + README (T11). Out-of-scope items (classifier/proto/OTel/no-retry code) correctly have no implementation task — only the T7 guard. No gaps.

**Placeholder scan:** no TBD/TODO; every code step has concrete code; the judgment steps (T10 Step 4 grafana.ini check, T11 Step 2 README location) give the exact `rg` to locate the target and the exact content to add.

**Type consistency:** `authFailed(le *transcript.ErrorRecord)` (T2) and `apiErrorIsAuthFailure(e *pb.ApiError)` (T6) are the two predicates named in the spec; `AuthFailedCount()` (T3) is used by `Alerts` (T4); `AlertsOpts.Theme` (T4) matches the `view.go` field passed; `formatAuthFailureBanner` (T6) is referenced only in T6; the metric `pa_monitor_sessions_errored{kind="authentication_failed"}` is identical across T8/T9. Consistent.
