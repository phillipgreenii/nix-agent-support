package posted

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestStateHomeFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"override wins", map[string]string{EnvStateDir: "/o", "XDG_STATE_HOME": "/x", "HOME": "/h"}, "/o"},
		{"xdg", map[string]string{"XDG_STATE_HOME": "/x", "HOME": "/h"}, "/x/pg-connector-pr-github"},
		{"home", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-pr-github"},
		{"none", map[string]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateHomeFromEnv(env(tc.env)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestStoreFromEnv_PostedPath(t *testing.T) {
	s := StoreFromEnv(env(map[string]string{"XDG_STATE_HOME": "/x"}))
	p, err := s.Path("owner", "repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/x/pg-connector-pr-github/posted/owner__repo__7.json"; p != want {
		t.Fatalf("got %q want %q", p, want)
	}
	s = StoreFromEnv(env(map[string]string{EnvStateDir: "/o"}))
	if p, _ := s.Path("owner", "repo", 7); p != "/o/posted/owner__repo__7.json" {
		t.Fatalf("override path = %q", p)
	}
	if _, err := StoreFromEnv(env(nil)).Path("owner", "repo", 7); err == nil {
		t.Fatal("no home must be an error")
	}
}

func TestStore_PathRejectsUnsafeParts(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, c := range []struct {
		owner, repo string
		pr          int
	}{
		{"..", "repo", 1},
		{"owner", "..", 1},
		{"a/b", "repo", 1},
		{"owner", "re/po", 1},
		{"", "repo", 1},
		{"owner", "", 1},
		{"owner", "repo", 0},
		{"owner", "repo", -2},
	} {
		if _, err := s.Path(c.owner, c.repo, c.pr); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

func TestStore_LoadMissingIsEmpty(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "posted")}
	st, err := s.Load("owner", "repo", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Fingerprints) != 0 || st.LastAppend != nil {
		t.Fatalf("state = %+v", st)
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "posted")}
	var st State
	st.AddFingerprints("aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaa")
	st.AddBodyHead("abc1234")
	st.AddBodyHead("abc1234")
	st.LastAppend = &LastAppend{At: "2026-10-06T00:00:00Z", Added: 2, Head: "abc1234"}
	if err := s.Save("owner", "repo", 9, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("owner", "repo", 9)
	if err != nil {
		t.Fatal(err)
	}
	st.Version = StateVersion
	if !reflect.DeepEqual(got, st) {
		t.Fatalf("got %+v want %+v", got, st)
	}
	if !reflect.DeepEqual(got.Fingerprints, []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}) {
		t.Fatalf("fingerprints not de-duplicated: %v", got.Fingerprints)
	}
	other, err := s.Load("owner", "repo", 10)
	if err != nil || len(other.Fingerprints) != 0 {
		t.Fatalf("PRs must not share state: %+v %v", other, err)
	}
}

func TestStore_SaveIsAtomicTempThenRename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "posted")
	s := Store{Dir: dir}
	if err := s.Save("owner", "repo", 1, State{Fingerprints: []string{"aaaaaaaaaaaaaaaa"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("owner", "repo", 1, State{Fingerprints: []string{"bbbbbbbbbbbbbbbb"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "owner__repo__1.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only the final file, got %v", names)
	}
	fi, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestStore_FailedRenameIsAnErrorAndLeavesNoTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "posted")
	// A directory squatting on the final name makes the rename fail after the
	// temporary file was fully written.
	if err := os.MkdirAll(filepath.Join(dir, "owner__repo__1.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: dir}
	if err := s.Save("owner", "repo", 1, State{Fingerprints: []string{"aaaaaaaaaaaaaaaa"}}); err == nil {
		t.Fatal("a failed sidecar write must be an error")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestStore_SaveFailsWhenDirIsAFile(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "posted")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Dir: blocker}).Save("owner", "repo", 1, State{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestStore_CorruptFileIsReportedAndKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "posted")
	s := Store{Dir: dir}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "owner__repo__1.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("owner", "repo", 1)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if b, rerr := os.ReadFile(path); rerr != nil || string(b) != "{not json" {
		t.Fatalf("corrupt file must be left in place: %q %v", b, rerr)
	}
	// Deleting it (the operator's repost lever) restores an empty state.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Load("owner", "repo", 1); err != nil || len(st.Fingerprints) != 0 {
		t.Fatalf("after delete: %+v %v", st, err)
	}
}
