package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func runShowCmd(t *testing.T, ref string, f showCmdFlags) (stdout string, err error) {
	t.Helper()
	origFlags := showFlags
	t.Cleanup(func() { showFlags = origFlags })
	showFlags = f

	c, _, ferr := rootCmd.Find([]string{"show"})
	if ferr != nil {
		t.Fatalf("rootCmd has no show subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, []string{ref})
	return buf.String(), err
}

func TestShowPrintsStoredInterpretation(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	// Seeded with the qualified "o/r#42" entity id — the real convention
	// gather/pg-connector write (pg2-276sg) — not the bare "42" a pre-fix
	// resolvePRRef would have queried with.
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", Category: "bugfix", Panel: panelMineActNow,
		AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(stdout, "mine") || !strings.Contains(stdout, "bugfix") {
		t.Errorf("stdout missing stored fields: %s", stdout)
	}
}

// TestShowPrintsPlannedSyncRowsInPlanMode is docket pg2-2j5ac.34.1's own
// acceptance criterion: `pg-desk show <pr>` prints that PR's planned sync
// writes when sync.mode is "plan" (design section 7.5, 7.7).
func TestShowPrintsPlannedSyncRowsInPlanMode(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Sync.Mode = "plan"
	withOpenSeams(t, cfg, openFresh)

	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", Category: "bugfix", Panel: panelMineActNow,
		AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42", Kind: "anchor",
		BeadID: "", LastSyncedContentHash: "deadbeefhash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed planned ledger row: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(stdout, "planned_sync_rows:") {
		t.Fatalf("stdout missing planned_sync_rows section: %s", stdout)
	}
	if !strings.Contains(stdout, "anchor") || !strings.Contains(stdout, "deadbeefhash") {
		t.Errorf("stdout missing the planned anchor row's kind/content_hash: %s", stdout)
	}

	jsonOut, err := runShowCmd(t, "42", showCmdFlags{jsonOut: true})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var payload showPayload
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("decode show --json: %v", err)
	}
	if len(payload.PlannedSyncRows) != 1 || payload.PlannedSyncRows[0].Kind != "anchor" {
		t.Fatalf("payload.PlannedSyncRows = %+v, want one anchor row", payload.PlannedSyncRows)
	}
}

// TestShowOmitsPlannedSyncRowsOutsidePlanMode proves the section/field is
// absent in off/apply mode even when a ledger row happens to exist.
func TestShowOmitsPlannedSyncRowsOutsidePlanMode(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}} // Sync.Mode unset => off
	withOpenSeams(t, cfg, openFresh)

	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	if err := st.UpsertLedger(store.LedgerEntry{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42", Kind: "anchor",
		BeadID: "", LastSyncedContentHash: "deadbeefhash", LastSyncedAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed planned ledger row: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(stdout, "planned_sync_rows:") {
		t.Fatalf("off mode printed a planned_sync_rows section: %s", stdout)
	}
}

func TestShowJSONIncludesWIP(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	w := true
	if err := st.UpsertAnnotation(store.Annotation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		WIP: &w, SetBy: "operator", SetAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{jsonOut: true})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("show --json output is not valid JSON: %v\n%s", err, stdout)
	}
	wip, present := payload["wip"]
	if !present {
		t.Fatalf("show --json payload missing \"wip\": %+v", payload)
	}
	if wip != true {
		t.Errorf(`payload["wip"] = %v, want true`, wip)
	}
}

// TestShowPlainTextEntityIDNotDoubled is pg2-5mnbo's regression test: the
// plain-text formatter must print entity_id exactly once, matching the
// --json form, rather than prepending "repo#" onto an EntityID that already
// carries the full qualified "owner/repo#N" form (resolvePRRef in desk.go
// always rebuilds it that way). The existing show tests above all use the
// single-slash "o/r" remote and never assert on the exact plain-text
// prefix, so they passed even with the doubling bug present.
func TestShowPlainTextEntityIDNotDoubled(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "owner/repo"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "owner/repo", EntityType: entityTypePR, EntityID: "owner/repo#105204",
		Ownership: "mine", Category: "bugfix", Panel: panelMineActNow,
		AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runShowCmd(t, "105204", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(stdout, "owner/repo#owner/repo#105204") {
		t.Fatalf("plain-text output double-prints the repo prefix: %s", stdout)
	}
	if !strings.HasPrefix(stdout, "owner/repo#105204\t") {
		t.Fatalf("plain-text output does not start with the qualified entity_id exactly once: %s", stdout)
	}

	jsonOut, err := runShowCmd(t, "105204", showCmdFlags{jsonOut: true})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var payload showPayload
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("decode show --json: %v", err)
	}
	if !strings.HasPrefix(stdout, payload.EntityID+"\t") {
		t.Errorf("plain-text entity_id prefix %q does not match --json entity_id %q", stdout, payload.EntityID)
	}
}

func TestShowUnresolvedPRFails(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)

	_, err := runShowCmd(t, "999", showCmdFlags{})
	if err == nil {
		t.Fatal("show unresolved PR: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error %q does not say the PR does not resolve", err)
	}
}

// TestShowRefreshInvokesThePipeline proves --refresh calls the injected
// showRefresh seam (packet 6's pipeline.Run) with the resolved (type, id)
// BEFORE reading the store — this packet's own Acceptance Criteria:
// "show --refresh invokes the pipeline."
func TestShowRefreshInvokesThePipeline(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	origRefresh := showRefresh
	t.Cleanup(func() { showRefresh = origRefresh })
	var gotType, gotID string
	var gotChange gather.ChangeKind
	called := false
	showRefresh = func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
		called = true
		gotType, gotID, gotChange = entityType, entityID, change
		return nil
	}

	_, err := runShowCmd(t, "42", showCmdFlags{refresh: true})
	if err != nil {
		t.Fatalf("show --refresh: %v", err)
	}
	if !called {
		t.Fatal("show --refresh did not invoke the pipeline seam")
	}
	// The pipeline must be invoked with the SAME qualified id the store is
	// later read with (o/r#42), not the bare "42" — otherwise --refresh
	// would gather/write under one id while show reads back another.
	if gotType != entityTypePR || gotID != "o/r#42" {
		t.Errorf("pipeline invoked with (%q,%q), want (%q,%q)", gotType, gotID, entityTypePR, "o/r#42")
	}
	if gotChange != gather.ChangeSweep {
		t.Errorf("pipeline invoked with change=%q, want %q", gotChange, gather.ChangeSweep)
	}
}

// TestShowRefreshPropagatesPipelineError proves a hard pipeline failure
// surfaces as show's own error (exit 1) rather than being swallowed.
func TestShowRefreshPropagatesPipelineError(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)

	origRefresh := showRefresh
	t.Cleanup(func() { showRefresh = origRefresh })
	wantErr := errors.New("gather failed")
	showRefresh = func(ctx context.Context, entityType, entityID string, change gather.ChangeKind) error {
		return wantErr
	}

	_, err := runShowCmd(t, "42", showCmdFlags{refresh: true})
	if err == nil {
		t.Fatal("show --refresh: error = nil, want the pipeline failure propagated")
	}
	if !strings.Contains(err.Error(), "gather failed") {
		t.Errorf("error %q does not wrap the pipeline failure", err)
	}
}
