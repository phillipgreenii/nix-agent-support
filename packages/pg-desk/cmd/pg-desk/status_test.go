package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func runStatusCmd(t *testing.T) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"status"})
	if ferr != nil {
		t.Fatalf("rootCmd has no status subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, nil)
	return buf.String(), err
}

func TestStatusReportsCountsAgainstFixtureStore(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	if err := st.UpsertEntity(store.Entity{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "1",
		Facts: `{}`, AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "1",
		Degraded: true, SyncError: "boom", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	if err := st.SetMeta(store.MetaKeyLastHeartbeat, "2026-09-16T00:00:00Z"); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout, "entities: 1") {
		t.Errorf("stdout missing entity count: %s", stdout)
	}
	if !strings.Contains(stdout, "interpretations: 1") {
		t.Errorf("stdout missing interpretation count: %s", stdout)
	}
	if !strings.Contains(stdout, "degraded: 1") {
		t.Errorf("stdout missing degraded count: %s", stdout)
	}
	if !strings.Contains(stdout, "2026-09-16T00:00:00Z") {
		t.Errorf("stdout missing last_heartbeat: %s", stdout)
	}
	// The sync-era lines are gone (P9a-1): a store carrying a sync_error row
	// still prints none of the sync_error, anchor-check or ledger lines.
	for _, gone := range []string{"sync_errors", "oldest_anchor_check_age", "planned_sync_rows", "pg-connector ledger show"} {
		if strings.Contains(stdout, gone) {
			t.Errorf("stdout still carries the retired %q line: %s", gone, stdout)
		}
	}
}

// TestStatusReportsChangeFlowPerTypeAndConsumer pins the change-flow section
// of `status`: per type the active count, due backlog, records by kind and
// origin, hydration totals and repeated degraded entities; per consumer the
// cursor, lag and seen_at; and the sweep bound's inputs with poll_interval
// unknown and NO verdict (only `doctor --router-config` evaluates it).
func TestStatusReportsChangeFlowPerTypeAndConsumer(t *testing.T) {
	st, openFresh := openTestStore(t)
	if err := st.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	origNow := changesNow
	t.Cleanup(func() { changesNow = origNow })
	changesNow = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

	write := func(id string, expected int64, hydratedAt string, kinds []string, origin string) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: entityTypePR, EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := st.WriteEntityStateWithLog(e, expected, hydratedAt, true, kinds, origin, "2026-10-01T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	write("o/r#1", 0, "2026-10-01T11:00:00Z", []string{"created"}, "poll")
	write("o/r#2", 0, "2026-09-30T00:00:00Z", []string{"created"}, "poll") // due
	if err := st.RegisterConsumer("alpha", entityTypePR, time.Date(2026, 10, 1, 11, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := st.AdvanceCursor("alpha", entityTypePR, 1); err != nil {
		t.Fatal(err)
	}
	changes.RecordHydration(st, entityTypePR, "o/r#2", pipeline.EntityChangeResult{Degraded: "ci"}, nil)
	changes.RecordHydration(st, entityTypePR, "o/r#2", pipeline.EntityChangeResult{Retries: 3}, errors.New("boom"))

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{
		"change_flow:\n",
		"  type pr:\n",
		"    active: 2\n",
		"    due_backlog: 0\n", // o/r#2 is older than the max age, and a PR has no due backlog
		"    sweep_bound: active_count=2 max_per_poll=20 max_age=6h0m0s poll_interval=unknown\n",
		"    hydrations: 2\n",
		"    hydration_failures: 2\n",
		"    occ_retries: 3\n",
		"    repeated_degraded: 1\n",
		"      o/r#2 count=2 since=",
		"      kind=created origin=poll count=2\n",
		"      alpha cursor=1 lag=1 seen_at=2026-10-01T11:30:00Z\n",
		"  type issue:\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, stdout)
		}
	}
	// No verdict: status never says whether the bound holds.
	for _, banned := range []string{"violated", "holds", "ok", "OK"} {
		for _, line := range strings.Split(stdout, "\n") {
			if strings.Contains(line, "sweep_bound") && strings.Contains(line, banned) {
				t.Errorf("sweep_bound line carries a verdict (%q): %s", banned, line)
			}
		}
	}
}

// TestStatusChangeFlowDegradesOnAnOldSchemaStore pins that the new section
// reports "unmigrated" on an old-schema (and an uninitialized) store instead
// of refusing or crashing, alongside
// TestStatusAndDoctorDoNotCrashOnOldSchemaStore.
func TestStatusChangeFlowDegradesOnAnOldSchemaStore(t *testing.T) {
	for _, kind := range []string{"old", "empty"} {
		t.Run(kind, func(t *testing.T) {
			withRawStoreAt(t, storeAtVersion(t, kind))

			stdout, err := runStatusCmd(t)
			if err != nil {
				t.Fatalf("status on a %s store: %v", kind, err)
			}
			if !strings.Contains(stdout, "change_flow:\n  unmigrated: run pg-desk migrate --cutover\n") {
				t.Errorf("status lacks the unmigrated change_flow note:\n%s", stdout)
			}
			if strings.Contains(stdout, "  type pr:") {
				t.Errorf("status printed per-type change-flow data for a %s store:\n%s", kind, stdout)
			}
		})
	}
}
