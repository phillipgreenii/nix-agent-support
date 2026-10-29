package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// PR lane refactor, packet P9a-3: pg-desk run issue and pg-desk run thread are
// KEPT (with the interpret-only path they use, which does no sync, hydration
// or link derivation and never reads a PR watch key). These tests pin that
// they still run on a CUT-OVER (schema-2) store with a configuration that has
// no watch.pr.queries, and that neither goes through `run pr`.
//
// Each test runs the real command against a real schema-2 store; only the
// pg-connector thread read is replaced (a fake closure), as in the run tests.

// newSchema2RunStore opens a store at a temp path, cuts it over to schema 2,
// points `run` at it, and returns a reopen func for inspection.
func newSchema2RunStore(t *testing.T, cfg *config.Config) (st *store.Store, reopen func() *store.Store) {
	t.Helper()
	origConfigLoad, origStoreOpen, origChange := runConfigLoad, runStoreOpen, runF.change
	t.Cleanup(func() { runConfigLoad, runStoreOpen, runF.change = origConfigLoad, origStoreOpen, origChange })
	runConfigLoad = func(context.Context) (*config.Config, error) { return cfg, nil }

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	if v, err := st.SchemaVersion(); err != nil || v < store.NewSchemaVersion {
		t.Fatalf("schema version = %d, %v; want a cut-over store", v, err)
	}
	runStoreOpen = func() (*store.Store, error) { return store.Open(dbPath) }
	return st, func() *store.Store {
		t.Helper()
		v, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen test store: %v", err)
		}
		t.Cleanup(func() { _ = v.Close() })
		return v
	}
}

func TestRunIssueRunsOnASchema2StoreWithoutRunPR(t *testing.T) {
	cfg := jiraTestCfg()
	if got := cfg.WatchQueries("pr"); got != nil {
		t.Fatalf("fixture has watch.pr.queries %v", got)
	}
	st, reopen := newSchema2RunStore(t, cfg)
	seedPRFacts(t, st, "acme/widgets#7")
	seedXref(t, st, "acme/widgets#7", "PROJ-99")

	runF.change = "changed"
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"issue", "PROJ-99"}); err != nil {
		t.Fatalf("run issue PROJ-99 on a schema-2 store: %v", err)
	}

	interp, found, err := reopen().GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7")
	if err != nil || !found {
		t.Fatalf("the linked PR was not re-interpreted: found=%v err=%v", found, err)
	}
	if interp.Category == "" && interp.GateState == "" && interp.Ownership == "" {
		t.Errorf("interpretation row looks unpopulated: %+v", interp)
	}
}

func TestRunThreadRunsOnASchema2StoreWithoutRunPR(t *testing.T) {
	cfg := issueTestCfg()
	if got := cfg.WatchQueries("pr"); got != nil {
		t.Fatalf("fixture has watch.pr.queries %v", got)
	}
	st, reopen := newSchema2RunStore(t, cfg)
	seedPRFacts(t, st, "acme/widgets#7")
	origThreadShow := runThreadShow
	t.Cleanup(func() { runThreadShow = origThreadShow })
	runThreadShow = func(context.Context, string) (threadShowResult, bool, error) {
		return threadShowResult{Text: "see https://github.com/acme/widgets/pull/7 for the context", AsOf: "2026-09-18T00:00:00Z"}, false, nil
	}

	runF.change = "changed"
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"thread", "T1"}); err != nil {
		t.Fatalf("run thread T1 on a schema-2 store: %v", err)
	}

	v := reopen()
	xref, found, err := v.GetXref("acme/widgets", entityTypePR, "acme/widgets#7", "thread", "T1")
	if err != nil || !found || xref.Evidence != "permalink" {
		t.Fatalf("thread xref: found=%v evidence=%q err=%v", found, xref.Evidence, err)
	}
	if _, found, err := v.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7"); err != nil || !found {
		t.Fatalf("the linked PR was not re-interpreted: found=%v err=%v", found, err)
	}
}
