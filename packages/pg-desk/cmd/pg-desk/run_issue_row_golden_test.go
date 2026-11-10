package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/corpus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The three `run issue` cases in which hydrateJiraIssue reaches
// pipeline.RunGenericEntity write an interpretation row; each is pinned
// byte-exact over the same issue, so the three cases and the corpus path agree
// today and only a task that names them may change a byte.
func TestRunIssueHydrationRowGoldens(t *testing.T) {
	const id = "PROJ-31"
	body := fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":"t","owner":"me","assignee":"x","issue_type":"task","state":"open","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`, id)
	cases := []struct {
		name   string
		golden string
		setup  func(f *backendFixture)
		// oldSchema opens the seeded store at the pre-cutover schema.
		oldSchema bool
	}{
		{"no owning backend", "run_issue_no_owning", func(f *backendFixture) {
			f.cfg.Watch.Issue.Backends = nil
		}, false},
		{"old schema with an owning backend", "run_issue_old_schema", func(*backendFixture) {}, true},
		{"non-owning backend", "run_issue_non_owning", func(f *backendFixture) { f.setBackend(id, "beads-a") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBackendFixture(t, id)
			f.write("show-"+id+".json", body)
			tc.setup(f)
			open := func() (*store.Store, error) { return deskStoreOpen() }
			if tc.oldSchema {
				path := filepath.Join(t.TempDir(), "old.db")
				store.SetSynchronousForTests("OFF")
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				_ = s.Close()
				open = func() (*store.Store, error) { return store.Open(path) }
			}
			st, err := open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			installIssueGate(st, f.cfg)
			p := pipeline.New(f.cfg, st, pipeline.WithClock(corpus.Clock()), pipeline.WithLogWriter(io.Discard))
			if err := hydrateJiraIssue(context.Background(), p, f.cfg, st, id, gather.ChangeAdded); err != nil {
				t.Fatalf("hydrateJiraIssue: %v", err)
			}
			row, found, err := st.GetInterpretation("o/r", "issue", id)
			if err != nil || !found {
				t.Fatalf("interpretation found=%v err=%v", found, err)
			}
			corpus.AssertBytes(t, filepath.Join("testdata", "golden", tc.golden+".row.txt"), corpus.RenderRow(row))
		})
	}
	_ = config.Config{}
	_ = os.Stdout
}
