package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// TestBanner_MutuallyExclusiveHeaderVsPaused is this packet's own
// acceptance bar: the header and the PAUSED banner are mutually exclusive;
// PAUSED wording is "dispatch halted · N in flight"; a quiescing screen
// never combines with the halted wording [design: Task 4.6 Step 3;
// Binding decisions 6].
func TestBanner_MutuallyExclusiveHeaderVsPaused(t *testing.T) {
	theme := render.NewTheme(false)

	t.Run("neither gated nor quiescing renders the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core: CoreInfo{State: "started", Version: "1.2.3", StartedAt: time.Now().Add(-2 * time.Hour)},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "pg-router") {
			t.Errorf("expected the header (contains %q); got:\n%s", "pg-router", got)
		}
		if strings.Contains(got, "routing halted") {
			t.Errorf("header must not carry the PAUSED wording; got:\n%s", got)
		}
	})

	t.Run("gated renders the PAUSED banner, never the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core:  CoreInfo{State: "started", Version: "1.2.3"},
				Gates: []Gate{{Type: core.GateSystemPause}},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "routing halted for blocked participants (SYSTEM_PAUSE) · 0 in flight") {
			t.Errorf("PAUSED banner wording wrong; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("PAUSED banner must not also render the header; got:\n%s", got)
		}
	})

	t.Run("gated AND quiescing still shows only the halted wording", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply: StatusReply{
				Gates: []Gate{{Type: "LOW_DISK_USAGE"}},
			},
			quiescing: true,
			width:     120,
			theme:     theme,
		})
		if !strings.Contains(got, "routing halted") {
			t.Errorf("expected the halted wording to win; got:\n%s", got)
		}
		if strings.Contains(got, "quiescing —") {
			t.Errorf("halted and quiescing wordings must never combine; got:\n%s", got)
		}
	})

	t.Run("quiescing with no gate set renders the quiescing line, not the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply:     StatusReply{},
			quiescing: true,
			width:     120,
			theme:     theme,
		})
		if !strings.Contains(got, "quiescing") {
			t.Errorf("expected the quiescing wording; got:\n%s", got)
		}
		if strings.Contains(got, "routing halted") {
			t.Errorf("quiescing (ungated) must not show the halted wording; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("quiescing must not also render the header; got:\n%s", got)
		}
	})

	t.Run("N in flight reflects the reply's deliveries count", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply: StatusReply{
				Gates:      []Gate{{Type: core.GateSystemPause}},
				Deliveries: []Delivery{{ID: "d1"}},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "1 in flight") {
			t.Errorf("expected N in flight to reflect len(Deliveries); got:\n%s", got)
		}
	})
}

// TestGatesSummary_ListsActiveGateTypes pins the header's compact gates
// line: "none" when clear, else the sorted TYPEs, capped with "+N more".
func TestGatesSummary_ListsActiveGateTypes(t *testing.T) {
	if got, want := gatesSummary(nil), "none"; got != want {
		t.Errorf("gatesSummary(nil) = %q, want %q", got, want)
	}
	got := gatesSummary([]Gate{{Type: "ZED"}, {Type: core.GateSystemPause}})
	if want := "SYSTEM_PAUSE, ZED"; got != want {
		t.Errorf("gatesSummary = %q, want %q (sorted)", got, want)
	}
	many := []Gate{{Type: "A"}, {Type: "B"}, {Type: "C"}, {Type: "D"}, {Type: "E"}}
	if got, want := gatesSummary(many), "A, B, C, +2 more"; got != want {
		t.Errorf("gatesSummary(5 gates) = %q, want %q", got, want)
	}
}

// TestAnyGateSet_AnyActiveGate checks the aggregate: the wire lists ACTIVE gates
// only, so any entry at all means something is gated, whatever its TYPE.
func TestAnyGateSet_AnyActiveGate(t *testing.T) {
	cases := []struct {
		name  string
		gates []Gate
		want  bool
	}{
		{"none", nil, false},
		{"system pause", []Gate{{Type: core.GateSystemPause}}, true},
		{"an arbitrary TYPE", []Gate{{Type: "LOW_DISK_USAGE"}}, true},
		{"several", []Gate{{Type: core.GateSystemPause}, {Type: "LOW_DISK_USAGE"}}, true},
	}
	for _, c := range cases {
		if got := anyGateSet(c.gates); got != c.want {
			t.Errorf("%s: anyGateSet = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBannerText_NamesGatesAndKeepsPausedForSystemPause: SYSTEM_PAUSE keeps the
// operator-facing "PAUSED" lead, any other gate reads "GATED", and the banner
// names every gate in force.
func TestBannerText_NamesGatesAndKeepsPausedForSystemPause(t *testing.T) {
	got := bannerText([]string{"LOW_DISK_USAGE"}, false, 2, Dispatch{})
	if !strings.HasPrefix(got, "GATED") || !strings.Contains(got, "LOW_DISK_USAGE") {
		t.Errorf("non-pause gate banner = %q, want a GATED lead naming the TYPE", got)
	}
	got = bannerText([]string{core.GateSystemPause, "LOW_DISK_USAGE"}, false, 0, Dispatch{})
	if !strings.HasPrefix(got, "PAUSED") || !strings.Contains(got, "LOW_DISK_USAGE") {
		t.Errorf("SYSTEM_PAUSE banner = %q, want a PAUSED lead naming every gate", got)
	}
	if got := bannerText(nil, false, 0, Dispatch{}); got != "" {
		t.Errorf("no gates, not quiescing: banner = %q, want empty (header renders)", got)
	}
}
