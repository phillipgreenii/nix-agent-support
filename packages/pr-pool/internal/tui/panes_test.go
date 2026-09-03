package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/tui/render"
)

// TestPanes_DerivedHealthTwoAxes is this packet's own acceptance bar:
// derived health for listeners ranks disabled > excluded > cooling > ok;
// for sources disabled > excluded > failing > stale > idle > ok; for the
// pool, the no-core/paused checks happen before
// core-tick-wedged/degraded/ok [design: Task 4.6 Binding decisions].
func TestPanes_DerivedHealthTwoAxes(t *testing.T) {
	theme := render.NewTheme(false) // mono: plain text tokens, no ANSI noise

	t.Run("pool", func(t *testing.T) {
		cases := []struct {
			name                          string
			hasCore, gated, wedged, degra bool
			want                          string
		}{
			{"no-core wins over everything", false, true, true, true, "no-core"},
			{"paused wins over wedged/degraded", true, true, true, true, "paused"},
			{"tick-wedged wins over degraded", true, false, true, true, "core-tick-wedged"},
			{"degraded when nothing higher applies", true, false, false, true, "degraded"},
			{"ok when nothing applies", true, false, false, false, "ok"},
		}
		for _, c := range cases {
			if got := poolHealthText(c.hasCore, c.gated, c.wedged, c.degra, theme); got != c.want {
				t.Errorf("%s: poolHealthText = %q, want %q", c.name, got, c.want)
			}
		}
	})

	t.Run("listener", func(t *testing.T) {
		cooling := &Backoff{NextEligible: time.Now().Add(42 * time.Second)}
		cases := []struct {
			name string
			l    Listener
			want string
		}{
			{"disabled wins over excluded+cooling", Listener{Enabled: false, Excluded: true, Backoff: cooling}, "disabled"},
			{"excluded wins over cooling", Listener{Enabled: true, Excluded: true, Backoff: cooling}, "excluded"},
			{"cooling when enabled+included", Listener{Enabled: true, Backoff: cooling}, "cooling"},
			{"ok otherwise", Listener{Enabled: true}, "ok"},
		}
		for _, c := range cases {
			got := listenerHealthText(c.l, theme)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: listenerHealthText = %q, want it to contain %q", c.name, got, c.want)
			}
		}
	})

	t.Run("source", func(t *testing.T) {
		now := time.Now()
		failing := &Failure{Count: 2}
		cases := []struct {
			name string
			s    Source
			want string
		}{
			{"disabled wins over excluded+failing", Source{Enabled: false, Excluded: true, Failure: failing}, "disabled"},
			{"excluded wins over failing", Source{Enabled: true, Excluded: true, Failure: failing}, "excluded"},
			{"failing wins over stale", Source{Enabled: true, Failure: failing, LastTick: now.Add(-1 * time.Hour)}, "failing"},
			{"stale when ticked long ago", Source{Enabled: true, LastTick: now.Add(-1 * time.Hour)}, "stale"},
			{"idle when never ticked", Source{Enabled: true}, "idle"},
			{"ok when ticked recently", Source{Enabled: true, LastTick: now}, "ok"},
		}
		for _, c := range cases {
			got := sourceHealthText(c.s, 1000, now, theme)
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: sourceHealthText = %q, want it to contain %q", c.name, got, c.want)
			}
		}
	})
}

// TestPanes_ThreeTierMockups is this packet's own acceptance bar: Wide/
// Narrow/Tiny tier renders show the correct column SET and pane SET per
// the design's own mockups (§4.3) -- column/pane SET comparisons, never
// golden-string matches (those are reserved for header/banner strings
// only) [design: Task 4.6 Validation].
func TestPanes_ThreeTierMockups(t *testing.T) {
	reply := StatusReply{
		Core:      CoreInfo{State: "started"},
		Listeners: []Listener{{Role: "reviewer", Binds: []string{"pr.new"}, Enabled: true, Delivered: 14}},
		Sources:   []Source{{Name: "gh-prs", Enabled: true, LastTick: time.Now()}},
		Queues:    []Queue{{Type: "pr.new", Depth: 2}},
		Registry:  []Registration{{ID: "h1", Kind: "handler", State: "started", Self: "healthy"}},
		Activity:  []ActivityEntry{{Seq: 1, StartedAt: time.Now(), Type: "dispatch", Outcome: "Closed"}},
	}

	t.Run("Wide (>=120 cols) shows every pane and the BINDS column", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 120, 30
		m.screen = screenMain
		m.reply = reply
		got := m.View()

		for _, want := range []string{"Listeners", "Queues", "Sources", "Registry", "Activity", "ROLE", "BINDS", "HEALTH", "DLVD", "DECL"} {
			if !strings.Contains(got, want) {
				t.Errorf("Wide: missing %q; got:\n%s", want, got)
			}
		}
	})

	t.Run("Narrow (80-119 cols) drops the BINDS column but keeps DECL", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 90, 30
		m.screen = screenMain
		m.reply = reply
		got := m.View()

		if strings.Contains(got, "BINDS") {
			t.Errorf("Narrow: BINDS column should be dropped; got:\n%s", got)
		}
		for _, want := range []string{"ROLE", "HEALTH", "DLVD", "DECL"} {
			if !strings.Contains(got, want) {
				t.Errorf("Narrow: missing %q; got:\n%s", want, got)
			}
		}
	})

	t.Run("Tiny (<80 cols) under height pressure shows only the focused pane, without BINDS or DECL", func(t *testing.T) {
		m := newTestModel(nil)
		m.width, m.height = 60, 6 // short terminal: forces the drop-order search
		m.screen = screenMain
		m.reply = reply
		// m.focusedPane defaults to paneListeners.
		got := m.View()

		if !strings.Contains(got, "Listeners") {
			t.Fatalf("Tiny: focused pane (Listeners) must survive; got:\n%s", got)
		}
		if strings.Contains(got, "BINDS") {
			t.Errorf("Tiny: BINDS column should be dropped; got:\n%s", got)
		}
		if strings.Contains(got, "DECL") {
			t.Errorf("Tiny: DECL column should be dropped; got:\n%s", got)
		}
		for _, dropped := range []string{"Queues", "Sources", "Registry"} {
			if strings.Contains(got, dropped) {
				t.Errorf("Tiny: unfocused pane %q should have dropped under height pressure; got:\n%s", dropped, got)
			}
		}
	})
}

// TestRenderRegistryPane_OmittedEntirelyWhenEmpty pins v1's own carried
// decision (§3, restated at §4.3 for Narrow): the Registry pane is
// omitted entirely -- not shown as an empty box -- when the registry has
// no entries, unless it is the focused pane.
func TestRenderRegistryPane_OmittedEntirelyWhenEmpty(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 30
	m.screen = screenMain
	m.reply = StatusReply{Core: CoreInfo{State: "started"}}

	got := m.View()
	if strings.Contains(got, "Registry") {
		t.Errorf("empty Registry should be omitted entirely; got:\n%s", got)
	}
}
