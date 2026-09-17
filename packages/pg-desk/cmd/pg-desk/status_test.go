package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
