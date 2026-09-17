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
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "42",
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

func TestShowJSONIncludesWIP(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "42",
		Ownership: "mine", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	w := true
	if err := st.UpsertAnnotation(store.Annotation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "42",
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
		Repo: "o/r", EntityType: entityTypePR, EntityID: "42",
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
	if gotType != entityTypePR || gotID != "42" {
		t.Errorf("pipeline invoked with (%q,%q), want (%q,%q)", gotType, gotID, entityTypePR, "42")
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
