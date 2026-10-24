package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func execLaneRecord(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	c := newLaneRecordCmd()
	var out, errb bytes.Buffer
	c.SetIn(strings.NewReader(stdin))
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err := c.ExecuteContext(context.Background())
	return out.String(), err
}

func withLaneClock(t *testing.T, now time.Time) {
	t.Helper()
	orig := laneNow
	t.Cleanup(func() { laneNow = orig })
	laneNow = func() time.Time { return now }
}

func TestLaneRecordWritesStateForTheConfiguredModeThroughTheRealStore(t *testing.T) {
	open, seed := seedLinkStore(t)
	cfg := openTestConfig("o/r")
	cfg.PRLane.Mode = "apply"
	withOpenSeams(t, cfg, open)
	withLaneClock(t, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC))

	// "1" is a bare number: it resolves to the canonical id.
	out, err := execLaneRecord(t, `{"pr":"1","mode":"apply","residual":2,"last_outcome":"applied",
		"events":[{"stage":"hydrate","class":"transient"}],
		"decisions":[{"rule":"all.closed","action":"close","outcome":"applied"}]}`)
	if err != nil {
		t.Fatalf("lane-record: %v", err)
	}
	if !strings.Contains(out, "recorded pr o/r#1") {
		t.Errorf("stdout %q", out)
	}
	snap, err := lanestate.Read(seed, time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snap.LaneMode != "apply" || snap.FlipAt != "2026-10-02T09:00:00Z" || snap.ResidualPRs != 1 ||
		snap.Runs[lanestate.RunKey{Stage: "hydrate", Class: "transient"}] != 1 ||
		snap.Decisions[lanestate.Decision{Rule: "all.closed", Action: "close", Outcome: "applied"}] != 1 {
		t.Errorf("snapshot after the record: %+v", snap)
	}
	if v, found, _ := seed.GetMeta(lanestate.KeyPRPrefix + "o/r#1"); !found || !strings.Contains(v, `"residual":2`) {
		t.Errorf("per-PR key value %q found=%v", v, found)
	}
}

func TestLaneRecordRejectsABadDocumentAndWritesNothing(t *testing.T) {
	open, seed := seedLinkStore(t)
	cfg := openTestConfig("o/r")
	cfg.PRLane.Mode = "plan"
	withOpenSeams(t, cfg, open)
	for name, doc := range map[string]string{
		"not json":      `nope`,
		"unknown field": `{"pr":"1","mode":"plan","extra":1}`,
		"bad class":     `{"pr":"1","mode":"plan","events":[{"stage":"hydrate","class":"retry_exhausted"}]}`,
		"empty":         ``,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := execLaneRecord(t, doc); err == nil {
				t.Fatal("lane-record accepted a bad document")
			}
			rows, err := seed.ListMetaPrefix(lanestate.KeyPrefix)
			if err != nil || len(rows) != 0 {
				t.Errorf("wrote %v (err %v), want nothing", rows, err)
			}
		})
	}
}

func TestLaneRecordRefusesAnOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(path) })
	if _, err := execLaneRecord(t, `{"pr":"1","mode":"plan"}`); err == nil {
		t.Error("lane-record on a schema-1 store must refuse")
	}
}

func TestMarkInactiveVerbWorksInPlanModeAndCountsOnce(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#1"})
	cfg := openTestConfig("o/r")
	cfg.PRLane.Mode = "plan"
	withOpenSeams(t, cfg, open)

	run := func() string {
		c := newMarkInactiveCmd()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs([]string{"o/r#1"})
		c.SilenceUsage, c.SilenceErrors = true, true
		if err := c.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("mark-inactive: %v", err)
		}
		return out.String()
	}
	if out := run(); !strings.Contains(out, "marked pr o/r#1 inactive") {
		t.Errorf("first run stdout %q", out)
	}
	if out := run(); !strings.Contains(out, "nothing to do") {
		t.Errorf("second run stdout %q", out)
	}
	e, found, err := seed.GetEntity("o/r", "pr", "o/r#1")
	if err != nil || !found || !e.Inactive {
		t.Errorf("entity %+v found=%v err=%v, want inactive", e, found, err)
	}
	snap, err := lanestate.Read(seed, time.Now())
	if err != nil || snap.MarkedInactive != 1 {
		t.Errorf("marked-inactive counter %d (err %v), want 1", snap.MarkedInactive, err)
	}
}
