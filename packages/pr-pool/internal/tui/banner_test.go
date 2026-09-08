package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/tui/render"
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
		if !strings.Contains(got, "pr-pool") {
			t.Errorf("expected the header (contains %q); got:\n%s", "pr-pool", got)
		}
		if strings.Contains(got, "dispatch halted") {
			t.Errorf("header must not carry the PAUSED wording; got:\n%s", got)
		}
	})

	t.Run("gated renders the PAUSED banner, never the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core:  CoreInfo{State: "started", Version: "1.2.3"},
				Gates: []Gate{{Name: core.GateQuotaPaused, Set: true}},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "dispatch halted · 0 in flight") {
			t.Errorf("PAUSED banner wording wrong; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("PAUSED banner must not also render the header; got:\n%s", got)
		}
	})

	t.Run("gated AND quiescing still shows only the halted wording", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply: StatusReply{
				Gates: []Gate{{Name: core.GateCICDDown, Set: true}},
			},
			quiescing: true,
			width:     120,
			theme:     theme,
		})
		if !strings.Contains(got, "dispatch halted") {
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
		if strings.Contains(got, "dispatch halted") {
			t.Errorf("quiescing (ungated) must not show the halted wording; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("quiescing must not also render the header; got:\n%s", got)
		}
	})

	t.Run("N in flight reflects the reply's deliveries count", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply: StatusReply{
				Gates:      []Gate{{Name: core.GateQuotaPaused, Set: true}},
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

// TestGatesSummary_ChecksboxReflectsSetState pins the compact "quota[.]
// cicd[.]" grammar the header/mockups use, and its "X" transition when a
// gate is set.
func TestGatesSummary_ChecksboxReflectsSetState(t *testing.T) {
	if got, want := gatesSummary(nil), "quota[.] cicd[.]"; got != want {
		t.Errorf("gatesSummary(nil) = %q, want %q", got, want)
	}
	got := gatesSummary([]Gate{{Name: core.GateQuotaPaused, Set: true}, {Name: core.GateCICDDown, Set: false}})
	if want := "quota[X] cicd[.]"; got != want {
		t.Errorf("gatesSummary = %q, want %q", got, want)
	}
}

// TestAnyGateSet_ORsBothNamedGates checks the effective-aggregate
// semantics (ux-7): either gate being set is enough.
func TestAnyGateSet_ORsBothNamedGates(t *testing.T) {
	cases := []struct {
		name  string
		gates []Gate
		want  bool
	}{
		{"none", nil, false},
		{"quota only", []Gate{{Name: core.GateQuotaPaused, Set: true}}, true},
		{"cicd only", []Gate{{Name: core.GateCICDDown, Set: true}}, true},
		{"both clear", []Gate{{Name: core.GateQuotaPaused}, {Name: core.GateCICDDown}}, false},
	}
	for _, c := range cases {
		if got := anyGateSet(c.gates); got != c.want {
			t.Errorf("%s: anyGateSet = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRenderHeader_TinyTierDropsVersionAndConfig matches the design's own
// Tiny mockup: the version pair and config path are dropped at the Tiny
// tier (<80 cols).
func TestRenderHeader_TinyTierDropsVersionAndConfig(t *testing.T) {
	theme := render.NewTheme(false)
	d := topZoneData{
		clientVersion: "9.9.9",
		reply: StatusReply{
			Core: CoreInfo{State: "started", Version: "9.9.9", ConfigPath: "/etc/pr-pool/config.toml"},
		},
		width: 60,
		theme: theme,
	}
	got := renderHeader(d)
	if strings.Contains(got, "config:") {
		t.Errorf("Tiny header should drop the config path; got:\n%s", got)
	}
	if strings.Contains(got, "client v9.9.9") {
		t.Errorf("Tiny header should drop the version pair; got:\n%s", got)
	}
	if !strings.Contains(got, "pr-pool") || !strings.Contains(got, "gates:") {
		t.Errorf("Tiny header dropped too much; got:\n%s", got)
	}
}

// TestRenderHeader_DefaultTierLeadsWithHealthAndGroupsFields pins pg2-xp415's
// layout fix: at Narrow/Wide, the header spreads the SAME fields (health,
// identity/state/uptime, the client/core version pair, gates, config path)
// across three lines instead of one dense line -- health leads on line 1
// (the "is the core healthy" signal an operator needs at a glance), the
// version pair is its own secondary line, and gates/config trail on a
// tertiary line. This is a layout-only change: no field present before is
// missing now, and vice versa.
func TestRenderHeader_DefaultTierLeadsWithHealthAndGroupsFields(t *testing.T) {
	theme := render.NewTheme(false)
	d := topZoneData{
		clientVersion: "dev",
		reply: StatusReply{
			Core: CoreInfo{
				State:      "started",
				Version:    "0.0.0-8a00aeb8",
				ConfigPath: "/Volumes/ziprecruiter/pristine/.pr-pool/config.toml",
				StartedAt:  time.Now().Add(-4 * time.Minute),
			},
		},
		width: 120,
		theme: theme,
	}
	got := renderHeader(d)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected a 3-line header at the default tier; got %d lines:\n%s", len(lines), got)
	}

	// Line 1: health leads, then identity/state/uptime -- no version pair,
	// no gates, no config path competing for attention here.
	if !strings.HasPrefix(strings.TrimLeft(lines[0], " "), "[ok] pr-pool") {
		t.Errorf("line 1 must lead with the health signal; got %q", lines[0])
	}
	if !strings.Contains(lines[0], "core: started") || !strings.Contains(lines[0], "up 0h04m") {
		t.Errorf("line 1 must still carry core state and uptime; got %q", lines[0])
	}
	if strings.Contains(lines[0], "config:") || strings.Contains(lines[0], "gates:") {
		t.Errorf("line 1 must not also carry gates/config; got %q", lines[0])
	}

	// Line 2: the version pair, moved off line 1 and unambiguously labeled
	// (previously "core: started vdev · core v..." conflated the client
	// version with the core's own state/version).
	if !strings.Contains(lines[1], "client vdev") || !strings.Contains(lines[1], "core v0.0.0-8a00aeb8") {
		t.Errorf("line 2 must carry the client/core version pair; got %q", lines[1])
	}

	// Line 3: gates + config path, both still present (same data, just
	// relocated/de-emphasized -- not dropped).
	if !strings.Contains(lines[2], "gates: quota[.] cicd[.]") {
		t.Errorf("line 3 must carry the gates summary; got %q", lines[2])
	}
	if !strings.Contains(lines[2], "config: ") || !strings.Contains(lines[2], "/Volumes/ziprecruiter/pristine/.pr-pool/config.toml") {
		t.Errorf("line 3 must still carry the full config path; got %q", lines[2])
	}
}
