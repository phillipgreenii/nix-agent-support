package claudetranscript

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverTranscripts_FixtureTreeListsSessionsAndSubagentsNotSidecars(t *testing.T) {
	files, err := DiscoverTranscripts(sessionsFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []TranscriptFile
	for _, f := range files {
		rel, err := filepath.Rel(sessionsFixtureDir, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, TranscriptFile{Path: filepath.ToSlash(rel), Subagent: f.Subagent})
	}
	want := []TranscriptFile{
		{Path: "-slug-a/aaaa.jsonl"},
		{Path: "-slug-a/aaaa/subagents/agent-1.jsonl", Subagent: true},
		{Path: "-slug-b/orig.jsonl"},
		{Path: "-slug-b/resumed.jsonl"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDiscoverTranscripts_MissingProjectsDirIsAnError(t *testing.T) {
	files, err := DiscoverTranscripts(filepath.Join(t.TempDir(), "absent"))
	if err == nil || files != nil {
		t.Fatalf("got (%v, %v), want (nil, error)", files, err)
	}
}

func TestDiscoverTranscripts_IgnoresStrayFilesAndWrongDepths(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("top-level.jsonl")                    // not inside a slug
	mk("slug/real.jsonl")                    // transcript
	mk("slug/real.status.jsonl")             // sidecar
	mk("slug/real.txt")                      // not jsonl
	mk("slug/real/subagents/a.jsonl")        // subagent
	mk("slug/real/subagents/a.status.jsonl") // subagent sidecar
	mk("slug/real/other/b.jsonl")            // not a subagents dir
	mk("slug/deeper/x.jsonl")                // depth 3
	files, err := DiscoverTranscripts(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []TranscriptFile{
		{Path: filepath.Join(root, "slug", "real.jsonl")},
		{Path: filepath.Join(root, "slug", "real", "subagents", "a.jsonl"), Subagent: true},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %+v, want %+v", files, want)
	}
}

func TestDiscoverTranscripts_UnreadableSlugDirIsReportedButOthersListed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	root := t.TempDir()
	for _, rel := range []string{"ok/a.jsonl", "locked/b.jsonl"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	files, err := DiscoverTranscripts(root)
	if err == nil {
		t.Fatal("want an error for the unreadable slug directory")
	}
	if len(files) != 1 || files[0].Path != filepath.Join(root, "ok", "a.jsonl") {
		t.Fatalf("got %+v, want only ok/a.jsonl", files)
	}
}
