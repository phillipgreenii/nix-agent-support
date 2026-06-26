package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBeads(t *testing.T, dir, host, port, database, projectID string) {
	t.Helper()
	bd := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(bd, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"dolt_server_host":"` + host + `","dolt_database":"` + database + `","project_id":"` + projectID + `"}`
	if err := os.WriteFile(filepath.Join(bd, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	if port != "" {
		if err := os.WriteFile(filepath.Join(bd, "dolt-server.port"), []byte(port+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindBeadsDir_walksUpButStopsAtRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo-a", "sub")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBeads(t, root, "127.0.0.1", "25252", "pg2", "proj-1") // .beads at root only
	got, ok := FindBeadsDir(repo, root)
	if !ok || got != root {
		t.Fatalf("FindBeadsDir = %q, %v; want %q, true", got, ok, root)
	}
}

func TestFindBeadsDir_findsAtRepoLevel(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo-a")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBeads(t, repo, "127.0.0.1", "25252", "pg2", "proj-1")
	got, ok := FindBeadsDir(repo, root)
	if !ok || got != repo {
		t.Fatalf("FindBeadsDir = %q, %v; want %q, true", got, ok, repo)
	}
}

func TestFindBeadsDir_notFoundAtOrBelowRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo-a")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// .beads exists ABOVE root — must NOT be discovered.
	parent := filepath.Dir(root)
	writeBeads(t, parent, "127.0.0.1", "25252", "pg2", "proj-1")
	defer func() { _ = os.RemoveAll(filepath.Join(parent, ".beads")) }()
	if _, ok := FindBeadsDir(repo, root); ok {
		t.Fatal("must not ascend above root")
	}
}

func TestDistinctDBs_dedupesByIdentityNotPath(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "repo-a")
	b := filepath.Join(root, "repo-b")
	c := filepath.Join(root, "repo-c")
	for _, d := range []string{a, b, c} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// a and b share host:port|db|project (same project, different .beads dirs);
	// c is a genuinely distinct project.
	writeBeads(t, a, "127.0.0.1", "25252", "pg2", "proj-1")
	writeBeads(t, b, "127.0.0.1", "25252", "pg2", "proj-1")
	writeBeads(t, c, "127.0.0.1", "25252", "pg2", "proj-2")
	dbs, err := DistinctDBs([]string{a, b, c}, root)
	if err != nil {
		t.Fatalf("DistinctDBs: %v", err)
	}
	if len(dbs) != 2 {
		t.Fatalf("distinct = %d, want 2: %+v", len(dbs), dbs)
	}
}

func TestDistinctDBs_skipsPathsWithoutBeads(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "repo-a")
	nob := filepath.Join(root, "no-beads")
	for _, d := range []string{a, nob} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBeads(t, a, "127.0.0.1", "25252", "pg2", "proj-1")
	dbs, err := DistinctDBs([]string{a, nob}, root)
	if err != nil {
		t.Fatalf("DistinctDBs: %v", err)
	}
	if len(dbs) != 1 || dbs[0].Dir != a {
		t.Fatalf("dbs = %+v, want one at %q", dbs, a)
	}
}
