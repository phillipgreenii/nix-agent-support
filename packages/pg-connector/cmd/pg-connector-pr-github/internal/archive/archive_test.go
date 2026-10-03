package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func sampleRecord() Record {
	return Record{
		Repo: "foo/bar", PR: 42, ReviewID: "PRR_1", DatabaseID: 77,
		URL: "https://example.invalid/review/77", CommitSHA: "h1", HeadSHA: "h2", DigestState: "verified",
		Body:     "summary\r\nline two <!-- marker -->",
		Comments: []Comment{{ID: "C1", Path: "a.go", Line: 4, Body: "finding", Marked: true}},
	}
}

func TestDirArchiverWritesKeyedAtomicRecord(t *testing.T) {
	dir := t.TempDir()
	fixed := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	path, err := DirArchiver{Dir: dir, Now: func() time.Time { return fixed }}.Write(sampleRecord())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if want := filepath.Join(dir, "foo", "bar", "pr-42", "review-77.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := sampleRecord()
	want.Version, want.ArchivedAt = RecordVersion, "2026-10-03T01:02:03Z"
	if got.Body != want.Body || got.ArchivedAt != want.ArchivedAt || got.Version != RecordVersion ||
		got.ReviewID != "PRR_1" || got.CommitSHA != "h1" || len(got.Comments) != 1 || got.Comments[0].Body != "finding" {
		t.Errorf("record = %+v", got)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestDirArchiverRewritesSameReview(t *testing.T) {
	dir := t.TempDir()
	a := DirArchiver{Dir: dir}
	if _, err := a.Write(sampleRecord()); err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord()
	rec.Body = "changed"
	path, err := a.Write(rec)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "changed") {
		t.Errorf("second write must replace the file: %s", raw)
	}
}

func TestDirArchiverFailures(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := func(mut func(*Record)) Record { r := sampleRecord(); mut(&r); return r }
	cases := map[string]struct {
		a   DirArchiver
		rec Record
	}{
		"no directory configured": {DirArchiver{}, sampleRecord()},
		"directory is a file":     {DirArchiver{Dir: notADir}, sampleRecord()},
		"traversal in repo":       {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.Repo = "../../etc/passwd" })},
		"dotdot owner":            {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.Repo = "../bar" })},
		"repo without slash":      {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.Repo = "nope" })},
		"extra path segment":      {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.Repo = "a/b/c" })},
		"zero review id":          {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.DatabaseID = 0 })},
		"zero pr":                 {DirArchiver{Dir: t.TempDir()}, bad(func(r *Record) { r.PR = 0 })},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if path, err := c.a.Write(c.rec); err == nil {
				t.Fatalf("expected an error, wrote %q", path)
			}
		})
	}
}

func TestDirFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"explicit override": {map[string]string{EnvDir: "/x/arch", "XDG_STATE_HOME": "/s", "HOME": "/h"}, "/x/arch"},
		"xdg state home":    {map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/pg-connector-pr-github/archive"},
		"home default":      {map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-pr-github/archive"},
		"nothing":           {map[string]string{}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DirFromEnv(env(c.env)); got != c.want {
				t.Errorf("DirFromEnv = %q, want %q", got, c.want)
			}
		})
	}
	// With nothing resolvable the production archiver exists but refuses.
	if _, err := FromEnv(env(nil)).Write(sampleRecord()); err == nil {
		t.Errorf("an unresolvable location must fail the write, not skip it")
	}
}
