package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// oldSchemaRunStore points every `run` seam at a schema-1 store seeded with a
// linked PR, and returns it. INV-REINTERPRET-12: `run` refuses it before
// anything is written.
func oldSchemaRunStore(t *testing.T, cfg *config.Config, threadText string) (open func() *store.Store) {
	t.Helper()
	origConfigLoad, origStoreOpen, origThreadShow, origResolve := runConfigLoad, runStoreOpen, runThreadShow, runResolveBeadPR
	t.Cleanup(func() {
		runConfigLoad, runStoreOpen, runThreadShow, runResolveBeadPR = origConfigLoad, origStoreOpen, origThreadShow, origResolve
	})
	// A file-backed version-1 store: `run` closes the handle it is given, so
	// every open (the command's and the test's own) is a fresh one.
	path := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	open = func() *store.Store {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	st := open() // version 1, not cut over
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
	seedPRFacts(t, st, "acme/widgets#7")
	return open
}

func assertRefusedWritingNothing(t *testing.T, open func() *store.Store, err error) {
	t.Helper()
	st := open()
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Fatalf("err = %v, want the cutover refusal", err)
	}
	if _, found, e := st.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7"); e != nil || found {
		t.Fatalf("GetInterpretation found=%v err=%v, want nothing written", found, e)
	}
	if _, found, e := st.GetXref("acme/widgets", entityTypePR, "acme/widgets#7", "thread", "T1"); e != nil || found {
		t.Fatalf("GetXref found=%v err=%v, want no xref written", found, e)
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
	seedXref(t, open(), "acme/widgets#7", "PROJ-99")
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PROJ-99", "anchor-1"} { // Jira key; bead id
		t.Run(id, func(t *testing.T) {
			assertRefusedWritingNothing(t, open, cmd.RunE(cmd, []string{"issue", id, "--change", "changed"}))
			if _, found, e := open().GetEntity("acme/widgets", "issue", id); e != nil || found {
				t.Fatalf("GetEntity issue %s found=%v err=%v, want nothing written", id, found, e)
			}
		})
	}
}
