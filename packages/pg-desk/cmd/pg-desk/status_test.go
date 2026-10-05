package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

	if err := st.UpsertEntity(store.Entity{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "1",
		Facts: `{}`, AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "1",
		Degraded: true, AsOf: "2026-09-16T00:00:00Z",
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
	if !strings.Contains(stdout, "pg-connector ledger show") {
		t.Errorf("stdout missing the pg-connector ledger show section: %s", stdout)
	}
}

// TestStatusCountsSyncErrorsByRetryState: status splits the sync_error rows
// into retrying / exhausted / non-transient (bead pg2-xb6fs); a row with no
// recorded retry state yet counts as retrying.
func TestStatusCountsSyncErrorsByRetryState(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

	states := map[string]string{
		"o/r#1": store.SyncRetryRetrying,
		"o/r#2": "", // no recorded state
		"o/r#3": store.SyncRetryExhausted,
		"o/r#4": store.SyncRetryNonTransient,
	}
	for id, state := range states {
		if err := st.UpsertInterpretation(store.Interpretation{
			Repo: "o/r", EntityType: entityTypePR, EntityID: id, SyncError: "boom", AsOf: "2026-09-30T00:00:00Z",
		}); err != nil {
			t.Fatalf("seed interpretation %s: %v", id, err)
		}
		if state != "" {
			if err := st.SetSyncRetry(id, store.SyncRetry{Attempts: 1, State: state}); err != nil {
				t.Fatalf("seed retry state %s: %v", id, err)
			}
		}
	}

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{
		"sync_errors: 4\n",
		"sync_errors_retrying: 2\n",
		"sync_errors_exhausted: 1\n",
		"sync_errors_non_transient: 1\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// TestStatusPrintsPlannedSyncRowsInPlanMode is docket pg2-2j5ac.34.1's own
// acceptance criterion: `pg-desk status` prints planned sync rows by kind
// when sync.mode is "plan" (design section 7.5, 7.7), and stays silent on
// that section otherwise.
func TestStatusPrintsPlannedSyncRowsInPlanMode(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	cfg.Sync.Mode = "plan"
	withOpenSeams(t, cfg, openFresh)

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#1", Kind: "anchor",
		BeadID: "", LastSyncedContentHash: "somehash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed planned ledger row: %v", err)
	}
	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#2", Kind: "review-request",
		BeadID: "bd-real-1", LastSyncedContentHash: "otherhash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed applied ledger row: %v", err)
	}

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout, "planned_sync_rows:") {
		t.Fatalf("stdout missing planned_sync_rows section: %s", stdout)
	}
	if !strings.Contains(stdout, "anchor: 1") {
		t.Errorf("stdout does not count the planned (empty bead_id) anchor row: %s", stdout)
	}
	if !strings.Contains(stdout, "review-request: 0") {
		t.Errorf("stdout counted an APPLIED (real bead_id) row as planned: %s", stdout)
	}
}

// TestStatusPrintsOldestAnchorCheckAge covers bead pg2-u4c1s: status prints
// oldest_anchor_check_age_seconds, 0 with no applied anchor and the stalest
// open applied anchor's age otherwise (closed and planned rows excluded).
func TestStatusPrintsOldestAnchorCheckAge(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout, "oldest_anchor_check_age_seconds: 0\n") {
		t.Fatalf("empty store should print age 0: %s", stdout)
	}

	old := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	for _, l := range []store.LedgerEntry{
		{Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#1", Kind: "anchor", BeadID: "bd-1", LastSyncedContentHash: "h", LastSyncedAt: old},
		{Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#2", Kind: "anchor", BeadID: "bd-2", LastSyncedContentHash: "closed", LastSyncedAt: "2026-01-01T00:00:00Z"},
		{Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#3", Kind: "anchor", BeadID: "", LastSyncedContentHash: "h", LastSyncedAt: "2026-01-01T00:00:00Z"},
	} {
		if err := st.UpsertLedger(l); err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
	}
	stdout, err = runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got int
	for _, line := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(line, "oldest_anchor_check_age_seconds: "); ok {
			if _, err := fmt.Sscanf(v, "%d", &got); err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
		}
	}
	if got < 3*3600 || got > 3*3600+300 {
		t.Fatalf("oldest_anchor_check_age_seconds = %d, want about %d: %s", got, 3*3600, stdout)
	}
}

// TestStatusOmitsPlannedSyncRowsOutsidePlanMode proves the section is
// absent in off/apply mode.
func TestStatusOmitsPlannedSyncRowsOutsidePlanMode(t *testing.T) {
	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh) // Sync.Mode unset => off

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(stdout, "planned_sync_rows:") {
		t.Fatalf("off mode printed a planned_sync_rows section: %s", stdout)
	}
}

func TestStatusSurvivesPgConnectorUnavailable(t *testing.T) {
	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) {
		return "", errors.New("exec: \"pg-connector\": executable file not found in $PATH")
	}

	stdout, err := runStatusCmd(t)
	if err != nil {
		t.Fatalf("status: %v, want status to still succeed with pg-connector unavailable", err)
	}
	if !strings.Contains(stdout, "unavailable") {
		t.Errorf("stdout does not report the ledger-show failure: %s", stdout)
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

	origLedger := statusRunPgConnectorLedgerShow
	t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
	statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

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
		"    due_backlog: 1\n",
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
			origLedger := statusRunPgConnectorLedgerShow
			t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
			statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

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
