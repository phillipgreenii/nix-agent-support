package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// oldSchemaRunStore points every `run` seam at a retired-schema (version 1)
// store, and returns it. INV-REINTERPRET-12: `run` refuses it before
// anything is written.
func oldSchemaRunStore(t *testing.T, cfg *config.Config, threadText string) (open func() *store.Store) {
	t.Helper()
	origConfigLoad, origStoreOpen, origThreadShow, origResolve := runConfigLoad, runStoreOpen, runThreadShow, runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad, runStoreOpen, runThreadShow, runResolveBeadPR = origConfigLoad, origStoreOpen, origThreadShow, origResolve
	})
	// A file-backed retired-schema store (version 1, no tables but meta): `run`
	// closes the handle it is given, so every open (the command's and the
	// test's own) is a fresh one, opened raw because store.Open refuses it.
	path := storeAtVersion(t, "old")
	open = func() *store.Store {
		s, err := store.OpenRaw(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	st := open() // version 1
	if err := st.RequireNewSchema(); !errors.Is(err, store.ErrOldSchema) {
		t.Fatalf("precondition: want a schema-1 store, got %v", err)
	}
	runConfigLoad = func(context.Context) (*config.Config, error) { return cfg, nil }
	runStoreOpen = func() (*store.Store, error) { return open(), nil }
	runThreadShow = func(context.Context, string) (threadShowResult, bool, error) {
		return threadShowResult{Text: threadText, AsOf: "2026-09-18T00:00:00Z"}, false, nil
	}
	runResolveBeadPR = func(context.Context, *config.Config, string) (string, string, error) {
		t.Error("run issue resolved a bead on a schema-1 store: the schema gate must come first")
		return "", "", errors.New("unreachable")
	}
	return open
}

func assertRefusedWritingNothing(t *testing.T, open func() *store.Store, err error) {
	t.Helper()
	st := open()
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "no longer migrates schema version 1") {
		t.Fatalf("err = %v, want the retired-schema refusal", err)
	}
	// Nothing was written: the file is still the version-1 store with its one
	// table, and no table of schema 2 appeared.
	if v, e := st.SchemaVersion(); e != nil || v != 1 {
		t.Fatalf("SchemaVersion = %d, %v, want 1 left alone", v, e)
	}
	meta, e := st.ListMetaPrefix("")
	if e != nil || len(meta) != 1 || meta["schema_version"] != "1" {
		t.Fatalf("meta = %v, %v, want only schema_version 1", meta, e)
	}
}

func TestRunThread_SchemaOneStoreIsRefusedBeforeAnyXrefIsWritten(t *testing.T) {
	open := oldSchemaRunStore(t, issueTestCfg(), "see https://github.com/acme/widgets/pull/7 for the context")
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	assertRefusedWritingNothing(t, open, cmd.RunE(cmd, []string{"thread", "T1", "--change", "changed"}))
}

func TestRunIssue_SchemaOneStoreIsRefusedForBothBackends(t *testing.T) {
	open := oldSchemaRunStore(t, jiraTestCfg(), "")
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PROJ-99", "anchor-1"} { // Jira key; bead id
		t.Run(id, func(t *testing.T) {
			assertRefusedWritingNothing(t, open, cmd.RunE(cmd, []string{"issue", id, "--change", "changed"}))
		})
	}
}
