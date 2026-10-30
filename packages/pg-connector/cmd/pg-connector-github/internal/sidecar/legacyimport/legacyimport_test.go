package legacyimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.sqlite"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// legacyHome seeds <home>/posted with the given file name -> content.
func legacyHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "posted")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func stateOf(t *testing.T, st *store.Store, owner, repo string, n int) (string, bool) {
	t.Helper()
	var text string
	err := st.DB().QueryRow(`SELECT state FROM posted_review WHERE owner=? AND repo=? AND number=?`, owner, repo, n).Scan(&text)
	if err != nil {
		return "", false
	}
	return text, true
}

func marker(t *testing.T, st *store.Store) bool {
	t.Helper()
	_, ok, err := st.GetMeta(context.Background(), MarkerKey)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestHomeFromEnvOrder(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	all := map[string]string{EnvStateDir: "/e/state", "XDG_STATE_HOME": "/e/xdg", "HOME": "/e/home"}
	if got := HomeFromEnv(env(all)); got != "/e/state" {
		t.Errorf("override = %q", got)
	}
	delete(all, EnvStateDir)
	if got := HomeFromEnv(env(all)); got != "/e/xdg/pg-connector-pr-github" {
		t.Errorf("xdg = %q", got)
	}
	delete(all, "XDG_STATE_HOME")
	if got := HomeFromEnv(env(all)); got != "/e/home/.local/state/pg-connector-pr-github" {
		t.Errorf("home = %q", got)
	}
	if got := HomeFromEnv(env(nil)); got != "" {
		t.Errorf("empty env = %q", got)
	}
	if got := HomeFromEnv(nil); got != "" {
		t.Errorf("nil getenv = %q", got)
	}
}

func TestParseName(t *testing.T) {
	for name, want := range map[string]struct {
		owner, repo string
		n           int
		ok          bool
	}{
		"foo__bar__42.json":      {"foo", "bar", 42, true},
		"my-org__a_b__c__7.json": {"my-org", "a_b__c", 7, true},
		"foo__bar.json":          {ok: false},
		"foo__bar__0.json":       {ok: false},
		"foo__bar__x.json":       {ok: false},
		".sidecar-123.tmp":       {ok: false},
		"foo__bar__1.txt":        {ok: false},
		"__bar__1.json":          {ok: false},
	} {
		owner, repo, n, ok := ParseName(name)
		if ok != want.ok || (ok && (owner != want.owner || repo != want.repo || n != want.n)) {
			t.Errorf("%s -> %q %q %d %v, want %+v", name, owner, repo, n, ok, want)
		}
	}
}

func TestImportBringsLegacyFilesInOnceAndLeavesThemUntouched(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	bodyA := "{\n  \"version\": 1,\n  \"fingerprints\": [\"0123456789abcdef\"],\n  \"body_heads\": []\n}\n"
	home := legacyHome(t, map[string]string{
		"foo__bar__1.json":  bodyA,
		"foo__bar__2.json":  "{not json", // imported as-is; Load reports it corrupt
		".sidecar-9.tmp":    "x",
		"README":            "x",
		"foo__bar__2.json~": "x",
	})
	res, err := Import(ctx, st, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || res.Existing != 0 || res.Skipped != 3 || res.AlreadyDone {
		t.Fatalf("result = %+v", res)
	}
	if got, ok := stateOf(t, st, "foo", "bar", 1); !ok || got != bodyA {
		t.Fatalf("record 1 = %q %v, want the file's bytes", got, ok)
	}
	if got, ok := stateOf(t, st, "foo", "bar", 2); !ok || got != "{not json" {
		t.Fatalf("record 2 = %q %v", got, ok)
	}
	if !marker(t, st) {
		t.Fatal("the marker must be written")
	}
	if b, err := os.ReadFile(filepath.Join(home, "posted", "foo__bar__1.json")); err != nil || string(b) != bodyA {
		t.Fatalf("legacy file changed: %q %v", b, err)
	}
	if e, _ := os.ReadDir(filepath.Join(home, "posted")); len(e) != 5 {
		t.Fatalf("legacy directory has %d entries, want 5 untouched", len(e))
	}

	// A second start does not re-import: a new file and a deleted row stay so.
	if err := os.WriteFile(filepath.Join(home, "posted", "foo__bar__3.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM posted_review WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	res, err = Import(ctx, st, home)
	if err != nil || !res.AlreadyDone || res.Imported != 0 {
		t.Fatalf("second import = %+v %v", res, err)
	}
	if _, ok := stateOf(t, st, "foo", "bar", 3); ok {
		t.Error("a second start must not import a new file")
	}
	if _, ok := stateOf(t, st, "foo", "bar", 1); ok {
		t.Error("a second start must not restore a hand-deleted record")
	}
}

func TestImportStoreWinsOverLegacy(t *testing.T) {
	st := openStore(t)
	if _, err := st.DB().Exec(
		`INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES ('foo', 'bar', 1, '{"from":"store"}', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	home := legacyHome(t, map[string]string{"foo__bar__1.json": `{"from":"legacy"}`, "foo__bar__2.json": `{"from":"legacy"}`})
	res, err := Import(context.Background(), st, home)
	if err != nil || res.Imported != 1 || res.Existing != 1 {
		t.Fatalf("result = %+v %v", res, err)
	}
	if got, _ := stateOf(t, st, "foo", "bar", 1); got != `{"from":"store"}` {
		t.Errorf("the store's record was overwritten: %q", got)
	}
	if got, _ := stateOf(t, st, "foo", "bar", 2); got != `{"from":"legacy"}` {
		t.Errorf("record 2 = %q", got)
	}
}

func TestImportOfAMissingLegacyDirectoryWritesTheMarker(t *testing.T) {
	st := openStore(t)
	res, err := Import(context.Background(), st, filepath.Join(t.TempDir(), "absent"))
	if err != nil || res.Imported != 0 || !marker(t, st) {
		t.Fatalf("result = %+v %v marker=%v", res, err, marker(t, st))
	}
}

func TestImportErrorWritesNoMarkerAndNoRowsAndRetries(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	home := legacyHome(t, map[string]string{"foo__bar__1.json": "{}", "foo__bar__2.json": "{}"})
	// A file that cannot be read: a directory by a sidecar's name is skipped, so
	// make the second one unreadable by removing its permission.
	bad := filepath.Join(home, "posted", "foo__bar__2.json")
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(bad); err == nil {
		_ = f.Close()
		t.Skip("running with privileges that ignore file modes")
	}
	if _, err := Import(ctx, st, home); err == nil {
		t.Fatal("an unreadable file must be an error")
	}
	if marker(t, st) {
		t.Fatal("a failed import must write no marker")
	}
	if _, ok := stateOf(t, st, "foo", "bar", 1); ok {
		t.Fatal("a failed import must roll back every row")
	}
	// The next start retries and succeeds once the file is readable.
	if err := os.Chmod(bad, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Import(ctx, st, home)
	if err != nil || res.Imported != 2 || !marker(t, st) {
		t.Fatalf("retry = %+v %v", res, err)
	}
}

func TestImportWithoutAHomeIsAnErrorAndWritesNoMarker(t *testing.T) {
	st := openStore(t)
	if _, err := Import(context.Background(), st, ""); err == nil {
		t.Fatal("no home must be an error")
	}
	if marker(t, st) {
		t.Fatal("no marker without an import")
	}
	if _, err := Import(context.Background(), nil, "/x"); err == nil {
		t.Fatal("a nil store must be an error")
	}
}
