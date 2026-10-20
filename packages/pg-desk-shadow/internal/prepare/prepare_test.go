package prepare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

// stubTool installs a stand-in for name the way the nix packages do: a wrapper
// script plus its unwrapped .<name>-wrapped sibling (what scratch.Resolve pins),
// which answers --version and then whatever extra says.
func stubTool(t *testing.T, dir, name, extra string) {
	t.Helper()
	wrapper := fmt.Sprintf("#!/bin/sh\nexec \"$(dirname \"$0\")/.%s-wrapped\" \"$@\"\n", name)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in --version) echo '%s 1.2.3'; echo second-line; exit 0;; esac\n%s", name, extra)
	if err := os.WriteFile(filepath.Join(dir, "."+name+"-wrapped"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The full Prepare path over stand-in tools, a version-2 store copy and the
// real sandbox: every step runs, and the manifest it saves wires the daemon cap,
// every pinned tool (the daemon included) and every build line. The stand-ins
// replace only the tools; the sandbox self-test, the store backup and integrity
// check, the warm-up seeding and the manifest are the real code.
func TestPrepareHappyPathWiresTheManifestCapToolsAndBuilds(t *testing.T) {
	testutil.RequireSandbox(t)
	testutil.RequireSQLite(t)

	tools := t.TempDir()
	for _, name := range []string{"pg-desk", "pg-connector-pr-github", "pg-connector-issue-beads", "pg-connector-github", "pg-router-source-pg-desk", "gh", "bd"} {
		stubTool(t, tools, name, "exit 0\n")
	}
	// The warm-up lists the "mine" query through the scratch pg-connector.
	stubTool(t, tools, "pg-connector", fakeConnector)

	home := t.TempDir()
	live := filepath.Join(t.TempDir(), "store.db")
	db := testutil.FakeStore(t, live)
	var sb strings.Builder
	sb.WriteString("PRAGMA user_version=2;\n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&sb, "INSERT INTO entity (repo, entity_type, entity_id, facts, as_of) VALUES ('acme/api','pr','acme/api#%d','{}','2026-01-05T09:00:00Z');\n", i)
	}
	if err := db.Exec(context.Background(), sb.String()); err != nil {
		t.Fatal(err)
	}
	cfgDir := t.TempDir()
	deskCfg := filepath.Join(cfgDir, "pg-desk.yaml")
	prCfg := filepath.Join(cfgDir, "pg-pr.yaml")
	if err := os.WriteFile(deskCfg, []byte("self_login: tester\nrepos:\n  - remote: acme/api\nsync: {mode: apply}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prCfg, []byte("connector:\n  pr: [pg-connector-pr-github]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := shortRoot(t)
	root = filepath.Join(root, "s")
	const cap = 2500
	var log strings.Builder
	o := Options{
		Root: root, Phase: "B", Home: home, Self: "/bin/true", Queries: []string{"mine"},
		LiveStore: live, DeskConfig: deskCfg, PRConfig: prCfg, ToolDirs: []string{tools},
		DaemonQueriesFile: writeQueries(t, `{"mine":"is:pr is:open author:@me"}`), DaemonPointsPerHour: cap,
		Log: &log, LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	l, m, err := Prepare(context.Background(), o)
	if err != nil {
		t.Fatalf("Prepare: %v\n%s", err, log.String())
	}

	// The manifest on disk is the manifest returned.
	saved, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	returned := m
	returned.Schema = scratch.ManifestSchema // Save stamps the schema on its own copy
	if a, b := mustJSON(t, saved), mustJSON(t, returned); a != b {
		t.Errorf("saved manifest differs from the returned one:\n%s\nvs\n%s", a, b)
	}

	// The cap the daemon runs under is recorded, and the rendered daemon config carries it.
	if m.DaemonGraphQLPointsPerHour != cap {
		t.Errorf("manifest cap = %d, want %d", m.DaemonGraphQLPointsPerHour, cap)
	}
	rawCfg, err := os.ReadFile(l.DaemonConfig())
	if err != nil {
		t.Fatal(err)
	}
	var daemonCfg struct {
		Points int `json:"graphql_points_per_hour"`
	}
	if err := json.Unmarshal(rawCfg, &daemonCfg); err != nil || daemonCfg.Points != cap {
		t.Errorf("daemon config cap = %d (%v), want %d", daemonCfg.Points, err, cap)
	}

	// Every tool is pinned to a scratch bin link (the daemon too), the shims are
	// written, and each build line reports the unwrapped stand-in's version.
	for _, name := range []string{"pg-desk", "pg-connector", "pg-connector-pr-github", "pg-connector-issue-beads", scratch.DaemonBinary, "pg-router-source-pg-desk"} {
		want := filepath.Join(l.BinDir(), name)
		if m.Tools[name] != want {
			t.Errorf("Tools[%s] = %q, want %q", name, m.Tools[name], want)
		}
		target, err := os.Readlink(want)
		if err != nil || !strings.HasSuffix(target, "."+name+"-wrapped") {
			t.Errorf("%s links to %q (%v), want the unwrapped sibling", name, target, err)
		}
		if m.Reals[name] != target {
			t.Errorf("Reals[%s] = %q, want %q", name, m.Reals[name], target)
		}
	}
	for _, name := range []string{"gh", "bd"} {
		if m.Tools[name] != filepath.Join(l.BinDir(), name) {
			t.Errorf("Tools[%s] = %q", name, m.Tools[name])
		}
		if body, err := os.ReadFile(m.Tools[name]); err != nil || !strings.Contains(string(body), "shim --tool "+name) {
			t.Errorf("%s shim = %q (%v)", name, body, err)
		}
		if m.Reals[name] != filepath.Join(tools, name) {
			t.Errorf("Reals[%s] = %q, want the machine tool (never unwrapped)", name, m.Reals[name])
		}
	}
	for build, want := range map[string]string{
		"pg-desk": "pg-desk 1.2.3", "pg-connector": "pg-connector 1.2.3", scratch.DaemonBinary: scratch.DaemonBinary + " 1.2.3", "live-pg-desk": "pg-desk 1.2.3",
	} {
		if m.Builds[build] != want {
			t.Errorf("Builds[%s] = %q, want %q", build, m.Builds[build], want)
		}
	}

	// The rest of the record: phase, queries, bd mode, seeding and the store copy.
	if m.Phase != "B" || m.BDMode != "hermetic" || strings.Join(m.Queries, ",") != "mine" || m.Consumer != "shadow-compare" || m.LiveStore != live || m.Home != home {
		t.Errorf("manifest = %+v", m)
	}
	if !m.Seeded || m.SeededAt == "" {
		t.Errorf("Seeded = %v at %q, want the warm-up recorded", m.Seeded, m.SeededAt)
	}
	copyDB := sqlite.DB{Path: l.StoreDB()}
	if n, err := copyDB.Int(context.Background(), "SELECT count(*) FROM entity WHERE entity_type='pr' AND active=1"); err != nil || n != 2 {
		t.Errorf("active rows in the store copy = %d (%v), want the 2 the warm-up listed", n, err)
	}
	if live, err := (sqlite.DB{Path: live}).Int(context.Background(), "SELECT count(*) FROM entity WHERE active=1"); err != nil || live != 5 {
		t.Errorf("the live store was modified: %d active rows (%v), want 5", live, err)
	}
	if !strings.Contains(log.String(), "sandbox self-test passed") {
		t.Errorf("log lacks the self-test line:\n%s", log.String())
	}

	// A prepared directory is refused a second time.
	if _, _, err := Prepare(context.Background(), o); err == nil || !strings.Contains(err.Error(), "already prepared") {
		t.Errorf("second Prepare = %v, want an already-prepared refusal", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
