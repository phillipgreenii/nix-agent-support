package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

// pausedDisk is a role blocked by disk at TIGHT.
func pausedDisk(role string) PausedRole {
	return PausedRole{Role: role, Failing: []FailingClause{{Clause: "disk >= OK", Resource: "disk", Level: "TIGHT", Kind: "level"}}}
}

// TestBanner_MutuallyExclusiveHeaderVsPaused is this packet's own
// acceptance bar: the header and the paused-roles banner are mutually
// exclusive; the paused wording is "N roles paused (disk TIGHT) · N in
// flight"; a quiescing screen never combines with the paused wording.
func TestBanner_MutuallyExclusiveHeaderVsPaused(t *testing.T) {
	theme := render.NewTheme(false)

	t.Run("no role paused and not quiescing renders the header", func(t *testing.T) {
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
		if strings.Contains(got, "paused (") {
			t.Errorf("header must not carry the paused wording; got:\n%s", got)
		}
		if !strings.Contains(got, "levels:") {
			t.Errorf("header must carry the levels summary; got:\n%s", got)
		}
	})

	t.Run("a paused role renders the banner, never the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core:        CoreInfo{State: "started", Version: "1.2.3"},
				PausedRoles: []PausedRole{pausedDisk("worker")},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "1 role paused (disk TIGHT) · 0 in flight") {
			t.Errorf("paused banner wording wrong; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("paused banner must not also render the header; got:\n%s", got)
		}
	})

	t.Run("paused AND quiescing still shows only the paused wording", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply:     StatusReply{PausedRoles: []PausedRole{pausedDisk("worker")}},
			quiescing: true,
			width:     120,
			theme:     theme,
		})
		if !strings.Contains(got, "role paused") {
			t.Errorf("expected the paused wording to win; got:\n%s", got)
		}
		if strings.Contains(got, "quiescing —") {
			t.Errorf("paused and quiescing wordings must never combine; got:\n%s", got)
		}
	})

	t.Run("quiescing with no role paused renders the quiescing line, not the header", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply:     StatusReply{},
			quiescing: true,
			width:     120,
			theme:     theme,
		})
		if !strings.Contains(got, "quiescing") {
			t.Errorf("expected the quiescing wording; got:\n%s", got)
		}
		if strings.Contains(got, "role paused") || strings.Contains(got, "roles paused") {
			t.Errorf("quiescing (nothing paused) must not show the paused wording; got:\n%s", got)
		}
		if strings.Contains(got, "config:") {
			t.Errorf("quiescing must not also render the header; got:\n%s", got)
		}
	})

	t.Run("N in flight reflects the reply's deliveries count", func(t *testing.T) {
		got := renderTopZone(topZoneData{
			reply: StatusReply{
				PausedRoles: []PausedRole{pausedDisk("worker")},
				Deliveries:  []Delivery{{ID: "d1"}},
			},
			width: 120,
			theme: theme,
		})
		if !strings.Contains(got, "1 in flight") {
			t.Errorf("expected N in flight to reflect len(Deliveries); got:\n%s", got)
		}
	})
}

// TestBannerText_OrdersResourcesByRolesBlockedThenName: the banner names every
// blocking resource with its level, ordered by the number of roles each blocks
// (descending), ties by resource name; a role counts once per resource; a silent
// resource reads "no reading"; with no paused role and no quiescing it is empty.
func TestBannerText_OrdersResourcesByRolesBlockedThenName(t *testing.T) {
	two := PausedRole{Role: "b", Failing: []FailingClause{
		{Resource: "git_auth", Level: "UNAVAILABLE", Kind: "level"},
		{Resource: "disk", Level: "TIGHT", Kind: "level"},
		{Resource: "disk", Level: "TIGHT", Kind: "level"},
	}}
	paused := []PausedRole{
		two,
		{Role: "c", Failing: []FailingClause{{Resource: "git_auth", Level: "UNAVAILABLE", Kind: "level"}}},
		{Role: "d", Failing: []FailingClause{{Resource: "claude_5h", Level: "EXHAUSTED", Kind: "level"}}},
		{Role: "e", Failing: []FailingClause{{Resource: "net", Level: "DOWN", Kind: "unknown"}}},
	}
	got := bannerText(paused, false, 2, Dispatch{})
	want := "4 roles paused (git_auth UNAVAILABLE, claude_5h EXHAUSTED, disk TIGHT, net no reading) · 2 in flight"
	if !strings.HasPrefix(got, want) {
		t.Errorf("banner = %q, want prefix %q", got, want)
	}
	if got := bannerText(nil, false, 0, Dispatch{}); got != "" {
		t.Errorf("no paused role, not quiescing: banner = %q, want empty (header renders)", got)
	}
}

// TestAnyRolePaused: any paused role at all.
func TestAnyRolePaused(t *testing.T) {
	if anyRolePaused(nil) || !anyRolePaused([]PausedRole{pausedDisk("w")}) {
		t.Error("anyRolePaused must be true exactly when a role is paused")
	}
}

// The header carries the durable queue log's size (bead pg2-8e0m6) in every
// tier that shows the levels line.
func TestBanner_HeaderShowsQueueLogSize(t *testing.T) {
	theme := render.NewTheme(false)
	for _, width := range []int{120, 40} {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core:     CoreInfo{State: "started", Version: "1.2.3", StartedAt: time.Now().Add(-time.Hour)},
				QueueLog: QueueLog{Bytes: 33291620, Compactions: 1},
			},
			width: width,
			theme: theme,
		})
		if !strings.Contains(got, "log: 31.7 MiB") {
			t.Errorf("width %d: header lacks the queue log size; got:\n%s", width, got)
		}
	}
}

func TestQueueLogSummary(t *testing.T) {
	if got := queueLogSummary(QueueLog{}); got != "log: 0 B" {
		t.Errorf("zero value renders %q", got)
	}
}
