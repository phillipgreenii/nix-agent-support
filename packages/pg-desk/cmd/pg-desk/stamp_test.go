package main

import (
	"io"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// INV-REINTERPRET-4: every command that builds a Pipeline gives its store
// handle the running stamp (build version plus the config digest), so every
// interpretation row it writes carries it. The table builds the pipeline the way
// each command does. The heartbeat catch-up joins this table when it lands.
func TestEveryCommandThatWritesInterpretationsStamps(t *testing.T) {
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/widgets"}}}
	want := interpret.Stamp(Version, cfg)
	if want == "" {
		t.Fatal("empty running stamp")
	}
	builders := map[string]func(st *store.Store){
		"changes": func(st *store.Store) { newChangesPipeline(cfg, st, io.Discard) },
		"run":     func(st *store.Store) { newRunPipeline(cfg, st, io.Discard, nil) },
		"refresh": func(st *store.Store) { _ = newRefreshHydrator(cfg, st) },
		"pr reinterpret": func(st *store.Store) {
			newReinterpreter(&cobra.Command{}, cfg, st, "acme/widgets", false, false)
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			if st.InterpStamp() != "" {
				t.Fatal("precondition: fresh handle has a stamp")
			}
			build(st)
			if got := st.InterpStamp(); got != want {
				t.Fatalf("stamp = %q, want %q", got, want)
			}
			// A write through the handle carries it.
			st.SeedEntityForTest(t, store.Entity{Repo: "acme/widgets", EntityType: "pr", EntityID: "1", Facts: "{}", AsOf: "a", ContentHash: "h"})
			if _, err := st.WriteInterpretation(store.Interpretation{
				Repo: "acme/widgets", EntityType: "pr", EntityID: "1", Enrichment: "{}", Urgency: "{}",
				Dispositions: "{}", Approvals: "{}", MatchReasons: "[]", AsOf: "a",
			}, store.InterpretationWriteOptions{ExpectedHash: "h", RequireStamp: true}); err != nil {
				t.Fatal(err)
			}
			got, _, _ := st.GetInterpretation("acme/widgets", "pr", "1")
			if got.InterpVersion != want {
				t.Fatalf("row stamp = %q, want %q", got.InterpVersion, want)
			}
		})
	}
}
