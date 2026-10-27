package changes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// resolveCfg configures three watched issue queries: two on a non-owning
// backend instance pair and one owning instance, plus a ticket pattern.
func resolveCfg() *config.Config {
	cfg := &config.Config{
		Repos:          []config.RepoConfig{{Remote: "o/r"}},
		TicketPatterns: []string{`[A-Z]+-\d+`},
	}
	cfg.Watch.Issue.Queries = []string{"mine", "team", "tracked"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"mine":    {Instance: "tracker-a"},
		"team":    {Instance: "tracker-b"},
		"tracked": {Instance: "owner-a", OwnsChanges: true},
	}
	return cfg
}

func seedIssue(t *testing.T, st *store.Store, id string, backend *string) {
	t.Helper()
	if _, err := st.WriteEntityWithLog(
		store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
		0, []string{"created"}, "sync", "2026-09-29T10:00:00Z",
	); err != nil {
		t.Fatal(err)
	}
	if backend != nil {
		if err := st.SetEntityColumns("o/r", "issue", id, backend, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveIssueBackendReadsTheColumn(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := resolveCfg()
	owner, tracker := "owner-a", "tracker-a"
	seedIssue(t, st, "PROJ-1", &owner)
	seedIssue(t, st, "bd-1", &tracker)
	// A watch set on another instance must not override a stored column.
	if err := saveWatchSet(st, "issue", "team", map[string]bool{"bd-1": true}); err != nil {
		t.Fatal(err)
	}

	if inst, owning, err := ResolveIssueBackend(cfg, st, "PROJ-1"); err != nil || inst != "owner-a" || !owning {
		t.Errorf("owning column: %q %v %v, want owner-a true nil", inst, owning, err)
	}
	if inst, owning, err := ResolveIssueBackend(cfg, st, "bd-1"); err != nil || inst != "tracker-a" || owning {
		t.Errorf("non-owning column: %q %v %v, want tracker-a false nil", inst, owning, err)
	}
}

func TestResolveIssueBackendNullColumnUsesTheNonOwningWatchSet(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := resolveCfg()
	seedIssue(t, st, "bd-2", nil)
	if err := saveWatchSet(st, "issue", "mine", map[string]bool{"bd-2": true, "bd-3": true}); err != nil {
		t.Fatal(err)
	}
	// An id the store has no row for resolves the same way.
	if inst, owning, err := ResolveIssueBackend(cfg, st, "bd-2"); err != nil || inst != "tracker-a" || owning {
		t.Errorf("NULL column: %q %v %v, want tracker-a false nil", inst, owning, err)
	}
	if inst, owning, err := ResolveIssueBackend(cfg, st, "bd-3"); err != nil || inst != "tracker-a" || owning {
		t.Errorf("no row: %q %v %v, want tracker-a false nil", inst, owning, err)
	}
}

func TestResolveIssueBackendAnIdInTwoBackendsIsAmbiguous(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := resolveCfg()
	for _, q := range []string{"mine", "team"} {
		if err := saveWatchSet(st, "issue", q, map[string]bool{"bd-9": true}); err != nil {
			t.Fatal(err)
		}
	}
	inst, owning, err := ResolveIssueBackend(cfg, st, "bd-9")
	if !errors.Is(err, ErrAmbiguousBackend) || inst != "" || owning {
		t.Fatalf("got %q %v %v, want ErrAmbiguousBackend", inst, owning, err)
	}
	if !strings.Contains(err.Error(), "bd-9") {
		t.Errorf("error %q must name the id", err)
	}
	// Two queries of the SAME instance are not ambiguous.
	cfg.Watch.Issue.Backends["team"] = config.WatchBackend{Instance: "tracker-a"}
	if inst, _, err := ResolveIssueBackend(cfg, st, "bd-9"); err != nil || inst != "tracker-a" {
		t.Errorf("same instance twice: %q %v, want tracker-a nil", inst, err)
	}
}

func TestResolveIssueBackendTicketShapeFallsBackToTheSingleOwningInstance(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	cfg := resolveCfg()
	// In no watch set, NULL column, ticket-shaped: the owning instance, no probe.
	seedIssue(t, st, "PROJ-5", nil)
	if inst, owning, err := ResolveIssueBackend(cfg, st, "PROJ-5"); err != nil || inst != "owner-a" || !owning {
		t.Errorf("ticket shape: %q %v %v, want owner-a true nil", inst, owning, err)
	}
	// Not ticket-shaped and in no set: unresolved, no error.
	if inst, owning, err := ResolveIssueBackend(cfg, st, "bd-77"); err != nil || inst != "" || owning {
		t.Errorf("unresolved: %q %v %v, want empty false nil", inst, owning, err)
	}
	// Two owning instances make the ticket shape insufficient.
	cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, "tracked2")
	cfg.Watch.Issue.Backends["tracked2"] = config.WatchBackend{Instance: "owner-b", OwnsChanges: true}
	if inst, _, err := ResolveIssueBackend(cfg, st, "PROJ-5"); err != nil || inst != "" {
		t.Errorf("two owning instances: %q %v, want empty nil", inst, err)
	}
}

// installProbeConnector puts a fake pg-connector on PATH that logs every call
// to the returned file and answers `issue show <id> --fresh --backend <b>`:
// owner-m holds the id, owner-f-down answers unavailable, everything else
// answers not found.
func installProbeConnector(t *testing.T) (callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$*" >> ` + callLog + `
backend=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--backend" ]; then backend="$a"; fi
  prev="$a"
done
case "$backend" in
  owner-m) echo '{"protocolVersion":1,"schemaVersion":4,"result":{"id":"x"}}'; exit 0 ;;
  owner-f-down) echo '{"error":{"code":"unavailable","message":"daemon down"}}'; exit 1 ;;
  *) echo '{"error":{"code":"not_found","message":"no"}}'; exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "pg-connector"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

func probeCfg(instances ...string) *config.Config {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{}
	for _, in := range instances {
		cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, "q-"+in)
		cfg.Watch.Issue.Backends["q-"+in] = config.WatchBackend{Instance: in, OwnsChanges: true}
	}
	return cfg
}

func TestResolveProbeOwningReturnsTheFirstInstanceThatHoldsTheID(t *testing.T) {
	callLog := installProbeConnector(t)
	cfg := probeCfg("owner-a", "owner-m", "owner-z")
	inst, held, err := ProbeOwning(context.Background(), gather.NewGatherer(cfg, nil), cfg, "PROJ-1")
	if err != nil || !held || inst != "owner-m" {
		t.Fatalf("got %q %v %v, want owner-m true nil", inst, held, err)
	}
	log, _ := os.ReadFile(callLog)
	calls := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(calls) != 2 {
		t.Fatalf("calls %q, want owner-a then owner-m and no more", calls)
	}
	for i, want := range []string{"owner-a", "owner-m"} {
		if !strings.Contains(calls[i], "--backend "+want) || !strings.Contains(calls[i], "issue show PROJ-1") {
			t.Errorf("call %d = %q, want a pinned show against %s", i, calls[i], want)
		}
	}
}

func TestResolveProbeOwningHeldByNoInstance(t *testing.T) {
	installProbeConnector(t)
	cfg := probeCfg("owner-a", "owner-c")
	inst, held, err := ProbeOwning(context.Background(), gather.NewGatherer(cfg, nil), cfg, "PROJ-1")
	if err != nil || held || inst != "" {
		t.Fatalf("got %q %v %v, want empty false nil", inst, held, err)
	}
}

func TestResolveProbeOwningStopsAtTheFirstUnavailable(t *testing.T) {
	callLog := installProbeConnector(t)
	cfg := probeCfg("owner-a", "owner-f-down", "owner-m")
	inst, held, err := ProbeOwning(context.Background(), gather.NewGatherer(cfg, nil), cfg, "PROJ-1")
	if !errors.Is(err, gather.ErrUnavailable) || held || inst != "" {
		t.Fatalf("got %q %v %v, want an ErrUnavailable error", inst, held, err)
	}
	log, _ := os.ReadFile(callLog)
	if strings.Contains(string(log), "owner-m") {
		t.Errorf("probe continued past the unavailable instance: %q", log)
	}
}
