package pipeline

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func gateIssueRows(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	rows, err := st.ListChangesAfter("issue", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.EntityID == id {
			n++
		}
	}
	return n
}

// InstallIssueGate gates every issue entity whatever the configuration holds:
// a nil config, an empty one and one naming an owning instance all install the
// same scope, so an issue of any backend (or none) appends no row until it is
// watched.
func TestInstallIssueGateGatesEveryIssueWhateverTheConfig(t *testing.T) {
	owning := &config.Config{}
	owning.Watch.Issue.Backends = map[string]config.WatchBackend{"open": {Instance: "owning-demo", OwnsChanges: true}}
	for name, cfg := range map[string]*config.Config{"nil": nil, "empty": {}, "owning": owning} {
		t.Run(name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			InstallIssueGate(st, cfg)
			const repo, id = "example/repo", "bd-1"
			if _, err := st.WriteEntityStateWithLogOpts(
				store.Entity{Repo: repo, EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "2026-10-20T00:00:00Z"},
				0, "2026-10-20T00:00:00Z", true, nil, []string{"added"}, "pg-desk", "2026-10-20T00:00:00Z", store.WriteOptions{},
			); err != nil {
				t.Fatal(err)
			}
			if got := gateIssueRows(t, st, id); got != 0 {
				t.Fatalf("unwatched issue appended %d rows, want 0", got)
			}
			on := true
			if err := st.SetEntityColumns(repo, "issue", id, nil, &on); err != nil {
				t.Fatal(err)
			}
			if seq, err := st.AppendEntityChange(repo, "issue", id, []string{"link_changed"}, "pg-desk", "2026-10-20T00:00:00Z"); err != nil || seq == 0 {
				t.Errorf("watched append = (%d, %v), want a sequence", seq, err)
			}
		})
	}
}
