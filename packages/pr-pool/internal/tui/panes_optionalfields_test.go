// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.9) carries the per-listener/source optional-field absence test:
// a Listener with Backoff == nil renders no backoff/cooling indicator (not
// a zero-value artifact like "cooling 0s"), and a Source with Failure ==
// nil renders no failure indicator, across all three width tiers [design:
// Task 4.9 Files (panes_optionalfields_test.go); Step 5].
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestPanes_OptionalFieldAbsenceRendersCleanly [design: Task 4.9 Step 5].
// listenerHealthText/sourceHealthText (panes.go) both rank on the POINTER
// being nil vs non-nil ("case l.Backoff != nil:" / "case s.Failure != nil
// && s.Failure.Count > 0:"), never on the pointed-to struct's own zero
// value -- so there is no code path that could dereference a nil pointer
// into a "cooling 0s"/"failing ×0" artifact in the first place. This test
// pins that discovered fact as a regression guard, at all three tiers
// (neither health function's own switch depends on tier, only the pane's
// COLUMN SET does -- panes_test.go's own TestPanes_ThreeTierMockups already
// covers that axis).
func TestPanes_OptionalFieldAbsenceRendersCleanly(t *testing.T) {
	theme := render.NewTheme(false)

	t.Run("listener with Backoff == nil never renders cooling, at every tier", func(t *testing.T) {
		listeners := []Listener{{Role: "reviewer", Enabled: true, Backoff: nil}}
		for _, tier := range []int{render.TierWide, render.TierNarrow, render.TierTiny} {
			got := renderListenersPane(listeners, tier, theme, "(no listeners configured)", "Listeners")
			if strings.Contains(got, "cooling") {
				t.Errorf("tier=%d: nil Backoff still rendered a cooling indicator; got:\n%s", tier, got)
			}
			if !strings.Contains(got, "ok") {
				t.Errorf("tier=%d: nil Backoff (enabled, not excluded) should render \"ok\"; got:\n%s", tier, got)
			}
		}
	})

	t.Run("source with Failure == nil never renders failing", func(t *testing.T) {
		now := time.Now()
		sources := []Source{{Name: "gh-prs", Enabled: true, LastTick: now, Failure: nil}}
		got := renderSourcesPane(sources, 1000, now, theme, "(no sources configured)", "Sources")
		if strings.Contains(got, "failing") {
			t.Errorf("nil Failure still rendered a failing indicator; got:\n%s", got)
		}
		if !strings.Contains(got, "ok") {
			t.Errorf("nil Failure with a fresh LastTick should render \"ok\"; got:\n%s", got)
		}
	})

	t.Run("control case: a SET Backoff/Failure still renders its own indicator", func(t *testing.T) {
		cooling := &Backoff{NextEligible: time.Now().Add(30 * time.Second)}
		got := renderListenersPane([]Listener{{Role: "triager", Enabled: true, Backoff: cooling}}, render.TierWide, theme, "", "Listeners")
		if !strings.Contains(got, "cooling") {
			t.Errorf("a set Backoff should still render the cooling indicator (control case); got:\n%s", got)
		}

		now := time.Now()
		failing := &Failure{Count: 3}
		got2 := renderSourcesPane([]Source{{Name: "flaky", Enabled: true, LastTick: now, Failure: failing}}, 1000, now, theme, "", "Sources")
		if !strings.Contains(got2, "failing") {
			t.Errorf("a set Failure should still render the failing indicator (control case); got:\n%s", got2)
		}
	})

	t.Run("holds through the full View() pipeline at every width tier", func(t *testing.T) {
		reply := StatusReply{
			Core:      CoreInfo{State: coreStateStarted},
			Listeners: []Listener{{Role: "reviewer", Enabled: true, Backoff: nil}},
			Sources:   []Source{{Name: "gh-prs", Enabled: true, LastTick: time.Now(), Failure: nil}},
		}
		for _, w := range []int{60, 90, 120} { // Tiny, Narrow, Wide
			m := newTestModel(nil)
			m.width, m.height = w, 30
			m.screen = screenMain
			m.reply = reply
			out := m.View()
			if strings.Contains(out, "cooling") || strings.Contains(out, "failing") {
				t.Errorf("width=%d: nil Backoff/Failure rendered a cooling/failing indicator; got:\n%s", w, out)
			}
		}
	})
}
