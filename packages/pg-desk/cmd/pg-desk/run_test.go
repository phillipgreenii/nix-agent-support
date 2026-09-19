package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestRunCmdRejectsUnsupportedEntityType proves an entirely unknown type is
// rejected rather than silently falling through to the pr pipeline (issue
// and thread are now both implemented — see the TestRunCmdIssue_* and
// TestRunCmdThread_* tests below).
func TestRunCmdRejectsUnsupportedEntityType(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"pull-request", "1"})
	if runErr == nil {
		t.Fatal("run pull-request: expected an error, got nil")
	}
	if !strings.Contains(runErr.Error(), "unknown entity type") {
		t.Fatalf("run pull-request: error = %q, want it to contain %q", runErr.Error(), "unknown entity type")
	}
}

// TestRunCmdRequiresTwoArgs proves the CLI wiring: `pg-desk run` takes
// exactly two positional args (<type> <id>), enforced by cobra's own Args
// validation before RunE ever runs.
func TestRunCmdRequiresTwoArgs(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if cmd.Args == nil {
		t.Fatal("run subcommand has no Args validator")
	}
	for _, args := range [][]string{{}, {"pr"}, {"pr", "1", "extra"}} {
		if err := cmd.Args(cmd, args); err == nil {
			t.Fatalf("Args(%v): expected an error for the wrong arg count, got nil", args)
		}
	}
	if err := cmd.Args(cmd, []string{"pr", "1"}); err != nil {
		t.Fatalf("Args([pr 1]): unexpected error: %v", err)
	}
}

// TestRunCmdDefaultChangeIsSweep proves "an absent --change means sweep"
// [design 7.2] at the flag-definition level.
func TestRunCmdDefaultChangeIsSweep(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	f := cmd.Flags().Lookup("change")
	if f == nil {
		t.Fatal("run subcommand has no --change flag")
	}
	if f.DefValue != "sweep" {
		t.Fatalf("--change default = %q, want %q", f.DefValue, "sweep")
	}
}

// TestRunCmdRequiresConfigAndStore proves the CLI wiring for the pr path:
// a config-load failure surfaces as a RunE error rather than a panic or
// silent success, and never reaches runStoreOpen (so this test never
// touches the real $XDG_STATE_HOME/pg-desk/store.db).
func TestRunCmdRequiresConfigAndStore(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
	})

	wantErr := errors.New("no config file found")
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return nil, wantErr }
	storeOpenCalled := false
	runStoreOpen = func() (*store.Store, error) {
		storeOpenCalled = true
		return nil, nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"pr", "1"})
	if runErr == nil {
		t.Fatal("run pr: expected an error when config loading fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "no config file found") {
		t.Fatalf("run pr: error = %q, want it to wrap the config-load failure", runErr.Error())
	}
	if storeOpenCalled {
		t.Fatal("run pr: runStoreOpen was called despite a config-load failure")
	}
}

// issueTestCfg is a minimal single-repo Config, matching Phase 9/10's
// "exactly one configured repository" scope — mirrors
// internal/pipeline/pipeline_test.go's own testCfg helper independently
// (this package does not import that test file).
func issueTestCfg() *config.Config {
	return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/widgets"}}}
}

// seedPRFacts writes a minimal, well-formed entity row for "pr"
// entityID, so a `run issue` re-interpretation has stored facts to
// re-read (Store.GetEntity) without ever calling gather. The pr_show
// payload is nested under Facts.PRShow exactly as internal/pipeline's own
// persist() marshals it — mirroring internal/pipeline/pipeline_test.go's
// own minimalFacts helper independently (this package does not import
// that test file).
func seedPRFacts(t *testing.T, st *store.Store, entityID string) {
	t.Helper()
	prShow, err := json.Marshal(map[string]any{
		"author": "me", "title": "fix: x", "repo": "acme/widgets", "number": 7, "state": "open",
	})
	if err != nil {
		t.Fatalf("marshal pr show fixture: %v", err)
	}
	factsJSON, err := json.Marshal(gather.Facts{PRShow: prShow, AsOf: "2026-09-17T00:00:00Z", HeadSHA: "deadbeef"})
	if err != nil {
		t.Fatalf("marshal facts fixture: %v", err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: "acme/widgets", EntityType: entityTypePR, EntityID: entityID,
		Facts: string(factsJSON), AsOf: "2026-09-17T00:00:00Z", ContentHash: "fixture",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
}

// TestRunCmdIssue_ResolvesAndReinterprets proves the "issue" case's own
// dispatch [design 7.2, 6.1]: it resolves the triggering bead to its
// linked PR via runResolveBeadPR (never through internal/gather, which is
// never invoked for entityType "issue"), then re-runs interpret for that
// PR, persisting a fresh interpretation row.
func TestRunCmdIssue_ResolvesAndReinterprets(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := issueTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }

	// runCmd's own RunE closes the store it opens (see run.go's `defer
	// func() { _ = st.Close() }()`), so this test opens a real on-disk
	// store (not store.OpenForTest's own instance, which it would then
	// find closed for its own post-RunE assertions) and reopens the same
	// file afterward to read back what was persisted.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	seedPRFacts(t, st, "acme/widgets#7")

	resolveCalls := 0
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalls++
		if beadID != "anchor-1" {
			t.Fatalf("runResolveBeadPR called with beadID=%q, want %q", beadID, "anchor-1")
		}
		return "acme/widgets", "acme/widgets#7", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"issue", "anchor-1", "--change", "changed"}); runErr != nil {
		t.Fatalf("run issue: unexpected error: %v", runErr)
	}
	if resolveCalls != 1 {
		t.Fatalf("runResolveBeadPR called %d times, want 1", resolveCalls)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdIssue_ResolveFailureReturnsClearError proves the acceptance
// criterion "a bead whose metadata and title both fail to match any known
// shape returns a clear, non-panicking error" at the CLI-wiring level:
// runResolveBeadPR's own error (internal/beadref's clear-error contract)
// surfaces as a wrapped RunE error, and runStoreOpen's store is never
// touched again after that (this test reaching its assertions at all is
// itself proof RunE did not panic).
func TestRunCmdIssue_ResolveFailureReturnsClearError(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := issueTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	st := store.OpenForTest(t)
	runStoreOpen = func() (*store.Store, error) { return st, nil }

	wantErr := errors.New("beadref: bead unresolvable-1 (title \"some other bead\") matches no known bead shape (anchor, feedback-cycle, review-request, or a title-adopted merge-request)")
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		return "", "", wantErr
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"issue", "unresolvable-1"})
	if runErr == nil {
		t.Fatal("run issue: expected an error for an unresolvable bead, got nil")
	}
	if !strings.Contains(runErr.Error(), "matches no known bead shape") {
		t.Fatalf("run issue: error = %q, want it to wrap the resolve failure", runErr.Error())
	}
}

// TestRunCmdIssue_ConfigFailureNeverResolves mirrors
// TestRunCmdRequiresConfigAndStore for the issue path: a config-load
// failure surfaces as a RunE error and never reaches runResolveBeadPR or
// runStoreOpen.
func TestRunCmdIssue_ConfigFailureNeverResolves(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	wantErr := errors.New("no config file found")
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return nil, wantErr }
	storeOpenCalled := false
	runStoreOpen = func() (*store.Store, error) {
		storeOpenCalled = true
		return nil, nil
	}
	resolveCalled := false
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalled = true
		return "", "", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"issue", "anchor-1"})
	if runErr == nil {
		t.Fatal("run issue: expected an error when config loading fails, got nil")
	}
	if !strings.Contains(runErr.Error(), "no config file found") {
		t.Fatalf("run issue: error = %q, want it to wrap the config-load failure", runErr.Error())
	}
	if storeOpenCalled {
		t.Fatal("run issue: runStoreOpen was called despite a config-load failure")
	}
	if resolveCalled {
		t.Fatal("run issue: runResolveBeadPR was called despite a config-load failure")
	}
}

// --- run issue: Jira half (docket pg2-2j5ac.40, Phase 13) ------------------

// jiraTestCfg mirrors issueTestCfg, additionally configuring TicketPatterns
// so ticketkey.MatchesShape can recognize a Jira ticket key at all (an
// unconfigured TicketPatterns list is itself covered by
// TestRunCmdIssue_ResolvesAndReinterprets above, which never sets it).
func jiraTestCfg() *config.Config {
	cfg := issueTestCfg()
	cfg.TicketPatterns = []string{`[A-Z]+-\d+`}
	return cfg
}

// seedXref writes one xref row directly through the real store — this
// packet's own Store.UpsertXref, never a fake, since run.go's Jira dispatch
// reads it back through the real Store.ListXrefsByTo.
func seedXref(t *testing.T, st *store.Store, fromID, toID string) {
	t.Helper()
	if err := st.UpsertXref(store.Xref{
		Repo: "acme/widgets", FromType: entityTypePR, FromID: fromID,
		ToType: "issue", ToID: toID, Evidence: "branch",
		FirstSeen: "2026-09-17T00:00:00Z", LastConfirmed: "2026-09-17T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed xref: %v", err)
	}
}

// TestRunCmdIssue_JiraTicketKey_ResolvesViaXrefAndReinterprets is this
// packet's own pinned acceptance criterion: `run issue <jira-ticket-key>
// --change changed` resolves via the xref table to its linked PR(s) and
// re-runs interpret WITHOUT a gather call (there is no real pg-connector on
// this test's $PATH, so an accidental gather call would fail this test
// loudly rather than silently) and without writing any bead (RunInterpretOnly
// never writes a bead by construction — there is no bead-writing code on
// this path at all to assert a call count against).
func TestRunCmdIssue_JiraTicketKey_ResolvesViaXrefAndReinterprets(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := jiraTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	seedPRFacts(t, st, "acme/widgets#7")
	seedXref(t, st, "acme/widgets#7", "PROJ-99")

	resolveCalled := false
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalled = true
		return "", "", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"issue", "PROJ-99", "--change", "changed"}); runErr != nil {
		t.Fatalf("run issue PROJ-99: unexpected error: %v", runErr)
	}
	if resolveCalled {
		t.Fatal("run issue PROJ-99: runResolveBeadPR (the beads path) was called for a Jira-shaped id")
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdIssue_JiraTicketKey_NoLinkedPR_IsNoop proves the no-op half:
// a Jira-shaped id with no xref row at all (nothing has ever linked a PR
// to it) exits cleanly rather than erroring.
func TestRunCmdIssue_JiraTicketKey_NoLinkedPR_IsNoop(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
	})

	cfg := jiraTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	st := store.OpenForTest(t)
	runStoreOpen = func() (*store.Store, error) { return st, nil }

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"issue", "PROJ-999"}); runErr != nil {
		t.Fatalf("run issue PROJ-999 (no linked PR): want a no-op (nil error), got %v", runErr)
	}
}

// TestRunCmdIssue_NonJiraShapedID_StillUsesBeadsPath proves the dispatch
// order is genuinely by the incoming id's own SHAPE, not merely "configured
// TicketPatterns => always the Jira path": a beads-shaped id, even with
// TicketPatterns configured, still resolves via runResolveBeadPR.
func TestRunCmdIssue_NonJiraShapedID_StillUsesBeadsPath(t *testing.T) {
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origResolve := runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runResolveBeadPR = origResolve
	})

	cfg := jiraTestCfg()
	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	st := store.OpenForTest(t)
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	seedPRFacts(t, st, "acme/widgets#7")

	resolveCalls := 0
	runResolveBeadPR = func(ctx context.Context, c *config.Config, beadID string) (string, string, error) {
		resolveCalls++
		return "acme/widgets", "acme/widgets#7", nil
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"issue", "anchor-1"}); runErr != nil {
		t.Fatalf("run issue anchor-1: unexpected error: %v", runErr)
	}
	if resolveCalls != 1 {
		t.Fatalf("runResolveBeadPR called %d times, want 1 (a beads-shaped id must still use the beads path)", resolveCalls)
	}
}

// --- run thread: Slack half (docket pg2-2j5ac.40, Phase 13) ---------------

// setupRunThreadTest wires runConfigLoad/runStoreOpen/runThreadShow for one
// `run thread` test and returns the opened store plus the store's own file
// path (so a caller can reopen it afterward for verification, mirroring
// TestRunCmdIssue_ResolvesAndReinterprets' own dbPath convention — runCmd's
// RunE closes the store it opens).
func setupRunThreadTest(t *testing.T, cfg *config.Config, show threadShowResult, notFound bool, showErr error) (st *store.Store, dbPath string) {
	t.Helper()
	origConfigLoad := runConfigLoad
	origStoreOpen := runStoreOpen
	origThreadShow := runThreadShow
	t.Cleanup(func() {
		runConfigLoad = origConfigLoad
		runStoreOpen = origStoreOpen
		runThreadShow = origThreadShow
	})

	runConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	runThreadShow = func(ctx context.Context, threadID string) (threadShowResult, bool, error) {
		return show, notFound, showErr
	}

	dbPath = filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	var err error
	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	runStoreOpen = func() (*store.Store, error) { return st, nil }
	return st, dbPath
}

// TestRunCmdThread_PermalinkMatch_WritesXrefAndReinterprets proves the
// permalink half of this packet's own acceptance criterion: a thread whose
// text contains a GitHub PR URL is cross-referenced to that PR
// (evidence="permalink"), and that PR is re-interpreted (stage 2 only, no
// gather — there is no real pg-connector on this test's $PATH, so an
// accidental gather call would fail this test loudly rather than silently
// — and no sync, and no bead write: RunInterpretOnly never writes a bead
// by construction, mirroring TestRunCmdIssue_JiraTicketKey_*'s own
// identical reasoning — there is no bead-writing code on this path at all
// to assert a call count against).
func TestRunCmdThread_PermalinkMatch_WritesXrefAndReinterprets(t *testing.T) {
	cfg := issueTestCfg()
	show := threadShowResult{
		Text: "see https://github.com/acme/widgets/pull/7 for the context",
		AsOf: "2026-09-18T00:00:00Z",
	}
	st, dbPath := setupRunThreadTest(t, cfg, show, false, nil)
	seedPRFacts(t, st, "acme/widgets#7")

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"thread", "T1", "--change", "changed"}); runErr != nil {
		t.Fatalf("run thread T1: unexpected error: %v", runErr)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	xref, found, err := verify.GetXref("acme/widgets", entityTypePR, "acme/widgets#7", "thread", "T1")
	if err != nil || !found {
		t.Fatalf("GetXref: found=%v err=%v", found, err)
	}
	if xref.Evidence != "permalink" {
		t.Fatalf("xref evidence = %q, want %q", xref.Evidence, "permalink")
	}

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdThread_TicketKeyMatch_ResolvesViaXrefAndReinterprets proves the
// ticket-key half: a thread whose text contains a Jira ticket key already
// cross-referenced to a PR (by internal/gather's own Jira scan, seeded here
// via seedXref) is cross-referenced to that SAME PR (evidence="ticket-key",
// resolved indirectly through the issue-side xref — never a second,
// independent PR guess), and that PR is re-interpreted.
func TestRunCmdThread_TicketKeyMatch_ResolvesViaXrefAndReinterprets(t *testing.T) {
	cfg := jiraTestCfg()
	show := threadShowResult{
		Text: "any update on PROJ-99?",
		AsOf: "2026-09-18T00:00:00Z",
	}
	st, dbPath := setupRunThreadTest(t, cfg, show, false, nil)
	seedPRFacts(t, st, "acme/widgets#7")
	seedXref(t, st, "acme/widgets#7", "PROJ-99")

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"thread", "T2", "--change", "changed"}); runErr != nil {
		t.Fatalf("run thread T2: unexpected error: %v", runErr)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	xref, found, err := verify.GetXref("acme/widgets", entityTypePR, "acme/widgets#7", "thread", "T2")
	if err != nil || !found {
		t.Fatalf("GetXref: found=%v err=%v", found, err)
	}
	if xref.Evidence != "ticket-key" {
		t.Fatalf("xref evidence = %q, want %q", xref.Evidence, "ticket-key")
	}

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdThread_NoMatch_WritesNoXrefAndNoReinterpret proves the negative
// half of this packet's own acceptance criterion: a thread whose text
// contains neither a PR permalink nor a recognized ticket key writes no
// xref row and triggers no re-interpret.
func TestRunCmdThread_NoMatch_WritesNoXrefAndNoReinterpret(t *testing.T) {
	cfg := jiraTestCfg()
	show := threadShowResult{Text: "nothing relevant in here", AsOf: "2026-09-18T00:00:00Z"}
	st, dbPath := setupRunThreadTest(t, cfg, show, false, nil)

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"thread", "T3", "--change", "changed"}); runErr != nil {
		t.Fatalf("run thread T3: unexpected error: %v", runErr)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	linked, err := verify.ListXrefsByTo("acme/widgets", "thread", "T3")
	if err != nil {
		t.Fatalf("ListXrefsByTo: %v", err)
	}
	if len(linked) != 0 {
		t.Fatalf("no-match thread wrote %d xref row(s), want 0: %+v", len(linked), linked)
	}
	_ = st // st itself is exercised only through runStoreOpen above
}

// TestRunCmdThread_ExistingLink_StaysLinkedWithoutRematch proves the
// "never just the matches this one scan found" rule [design 7.2]: a PR
// already xref'd to this thread from an EARLIER run stays linked (and gets
// re-interpreted) even when THIS run's own scan finds no new match at all.
func TestRunCmdThread_ExistingLink_StaysLinkedWithoutRematch(t *testing.T) {
	cfg := issueTestCfg()
	show := threadShowResult{Text: "just chatting, no links here", AsOf: "2026-09-18T00:00:00Z"}
	st, dbPath := setupRunThreadTest(t, cfg, show, false, nil)
	seedPRFacts(t, st, "acme/widgets#7")
	if err := st.UpsertXref(store.Xref{
		Repo: "acme/widgets", FromType: entityTypePR, FromID: "acme/widgets#7",
		ToType: "thread", ToID: "T4", Evidence: "permalink",
		FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed pre-existing thread xref: %v", err)
	}

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	if runErr := cmd.RunE(cmd, []string{"thread", "T4", "--change", "changed"}); runErr != nil {
		t.Fatalf("run thread T4: unexpected error: %v", runErr)
	}

	verify, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen test store for verification: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	interp, found, err := verify.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("GetInterpretation: found=%v err=%v (the pre-existing link should still have been re-interpreted)", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Fatalf("interpretation row looks unpopulated: %+v", interp)
	}
}

// TestRunCmdThread_FetchFailure_ReturnsClearError proves the freedom-
// boundary choice documented on runThread: a thread `pg-connector` cannot
// fetch at all (here, a not_found answer) is a hard, clear error — never a
// silent no-op or a panic.
func TestRunCmdThread_FetchFailure_ReturnsClearError(t *testing.T) {
	cfg := issueTestCfg()
	_, _ = setupRunThreadTest(t, cfg, threadShowResult{}, true, nil)

	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	runErr := cmd.RunE(cmd, []string{"thread", "missing-thread"})
	if runErr == nil {
		t.Fatal("run thread missing-thread: expected an error for a not_found thread, got nil")
	}
	if !strings.Contains(runErr.Error(), "not_found") {
		t.Fatalf("run thread missing-thread: error = %q, want it to mention not_found", runErr.Error())
	}
}
