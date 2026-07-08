package reviewstage

import (
	"errors"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestSaveLoadClear_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	in := &Draft{
		Repo: "foo/bar", PR: 42, Body: "hi",
		Comments: []api.Comment{{Path: "main.go", Line: 1, Body: "x"}},
	}
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

// The visible attribution banner is a long, constant prefix on every stamped
// body. Dedup must key on the underlying content, not the banner — otherwise
// two distinct findings on the same line share the banner within the key window
// and the second gets wrongly dropped.
func TestDedup_StampedBodiesDoNotCollideOnBanner(t *testing.T) {
	existing := []api.Comment{
		{Path: "main.go", Line: 10, Body: marker.Stamp("rename foo")},
	}
	incoming := []api.Comment{
		{Path: "main.go", Line: 10, Body: marker.Stamp("rename foo")},          // true duplicate
		{Path: "main.go", Line: 10, Body: marker.Stamp("extract this method")}, // distinct finding, same line
	}
	unique, skipped := Dedup(incoming, existing)
	if skipped != 1 {
		t.Fatalf("expected 1 skipped (only the true duplicate), got %d", skipped)
	}
	if len(unique) != 1 || marker.Strip(unique[0].Body) != "extract this method" {
		t.Fatalf("expected the distinct finding to survive dedup, got %+v", unique)
	}
}

// A comment posted before the banner existed carries only the invisible marker;
// a re-post now carries the banner too. Same content ⇒ still a duplicate.
func TestDedup_MatchesAcrossOldAndNewMarkerFormat(t *testing.T) {
	existing := []api.Comment{
		{Path: "main.go", Line: 5, Body: marker.HTMLMarker + "\nrename foo"},
	}
	incoming := []api.Comment{
		{Path: "main.go", Line: 5, Body: marker.Stamp("rename foo")},
	}
	if _, skipped := Dedup(incoming, existing); skipped != 1 {
		t.Fatalf("expected cross-format dedup (1 skipped), got %d", skipped)
	}
}

func TestPathFor_SlashesReplaced(t *testing.T) {
	got := PathFor("/d", "foo/bar", 42)
	want := "/d/foo-bar-42.json"
	if got != want {
		t.Fatalf("PathFor: got %q want %q", got, want)
	}
}
