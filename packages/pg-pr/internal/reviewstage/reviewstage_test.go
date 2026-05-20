package reviewstage

import (
	"errors"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestSaveLoadClear_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	in := &Draft{Repo: "foo/bar", PR: 42, Body: "hi",
		Comments: []api.Comment{{Path: "main.go", Line: 1, Body: "x"}}}
	path, err := Save(dir, in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	got, err := Load(dir, "foo/bar", 42)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Repo != "foo/bar" || got.PR != 42 || len(got.Comments) != 1 {
		t.Fatalf("roundtrip: %+v", got)
	}

	if err := Clear(dir, "foo/bar", 42); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := Load(dir, "foo/bar", 42); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist after Clear, got %v", err)
	}
	// Clear of missing draft is a no-op.
	if err := Clear(dir, "foo/bar", 99); err != nil {
		t.Fatalf("Clear missing: %v", err)
	}
}

func TestDedup_RemovesByPathLineBodyPrefix(t *testing.T) {
	existing := []api.Comment{
		{Path: "main.go", Line: 10, Body: "rename foo"},
		{Path: "main.go", Line: 11, Body: "different"},
	}
	incoming := []api.Comment{
		{Path: "main.go", Line: 10, Body: "rename foo"},         // duplicate
		{Path: "main.go", Line: 10, Body: "rename foo and bar"}, // not a dup (different body prefix beyond 100)
		{Path: "main.go", Line: 12, Body: "different"},          // different line
	}
	unique, skipped := Dedup(incoming, existing)
	if skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", skipped)
	}
	if len(unique) != 2 {
		t.Fatalf("expected 2 unique, got %d: %+v", len(unique), unique)
	}
}

func TestPathFor_SlashesReplaced(t *testing.T) {
	got := PathFor("/d", "foo/bar", 42)
	want := "/d/foo-bar-42.json"
	if got != want {
		t.Fatalf("PathFor: got %q want %q", got, want)
	}
}
