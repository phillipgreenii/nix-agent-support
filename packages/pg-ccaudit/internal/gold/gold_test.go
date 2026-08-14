package gold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsAMalformedLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gold.jsonl")
	if err := os.WriteFile(p, []byte(`{"id":"a","class":"user-correction"}`+"\n"+`{not json`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unlike the transcript corpus, where per-line tolerance keeps coverage provable,
	// a gold set is small and hand-maintained: silently dropping a line it could not
	// parse would inflate every score computed over the entries that remained.
	if _, err := Load(p); err == nil {
		t.Fatal("a malformed gold line must fail the load, not be skipped")
	}
}

func TestLoadRejectsADuplicateID(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gold.jsonl")
	body := `{"id":"a","class":"user-correction"}` + "\n" + `{"id":"a","class":"not-a-mistake"}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		// Two labels for one candidate means one of them is silently ignored, and which
		// one depends on file order.
		t.Fatalf("a duplicate id must be rejected, got %v", err)
	}
}

func TestPartitionsAreDisjoint(t *testing.T) {
	s := Set{Entries: []Entry{
		{ID: "typed-turn:a#1", Source: SourceHandLabelled, Class: "user-correction"},
		{ID: "typed-turn:a#2", Source: SourceHandLabelled, Class: ""},
		{ID: "file:/x/FEEDBACK.md", Source: SourceFileChannel, Class: "user-correction"},
	}}
	if len(s.Labelled()) != 1 || len(s.Unlabelled()) != 1 || len(s.FileChannel()) != 1 {
		t.Errorf("labelled=%d unlabelled=%d file-channel=%d, want 1/1/1",
			len(s.Labelled()), len(s.Unlabelled()), len(s.FileChannel()))
	}
	// A file-channel entry has no candidate to classify, so it must never enter the
	// scored set — counting it there would credit the classifier for a correction it
	// structurally cannot see.
	for _, e := range s.Labelled() {
		if e.Source == SourceFileChannel {
			t.Error("a file-channel entry leaked into the scored set")
		}
	}
}

func TestMergePreservesExistingLabels(t *testing.T) {
	existing := Set{Entries: []Entry{
		{ID: "a", Source: SourceHandLabelled, Class: "user-correction", Labeller: "operator", Excerpt: "old"},
		{ID: "b", Source: SourceHandLabelled, Class: "", Excerpt: "b"},
	}}
	incoming := Set{Entries: []Entry{
		{ID: "a", Source: SourceHandLabelled, Class: "", Excerpt: "new"},
		{ID: "b", Source: SourceHandLabelled, Class: "not-a-mistake", Labeller: "agent"},
		{ID: "c", Source: SourceHandLabelled, Class: ""},
	}}
	got := Merge(existing, incoming)

	if len(got.Entries) != 3 {
		t.Fatalf("merged %d entries, want 3", len(got.Entries))
	}
	// Re-seeding or re-sampling must NEVER discard hand labelling, or the set silently
	// resets to unlabelled while the evaluation it feeds keeps printing numbers.
	if got.Entries[0].Class != "user-correction" || got.Entries[0].Labeller != "operator" {
		t.Errorf("an existing label was overwritten: %+v", got.Entries[0])
	}
	if got.Entries[0].Excerpt != "old" {
		t.Errorf("a non-empty excerpt was overwritten: %q", got.Entries[0].Excerpt)
	}
	// An UNLABELLED existing entry may take an incoming label — that is how labelling
	// progresses.
	if got.Entries[1].Class != "not-a-mistake" || got.Entries[1].Labeller != "agent" {
		t.Errorf("an unlabelled entry did not take the incoming label: %+v", got.Entries[1])
	}
	if got.Entries[2].ID != "c" {
		t.Errorf("a new entry was not appended: %+v", got.Entries[2])
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "gold.jsonl")
	in := Set{Entries: []Entry{
		{
			ID: "a", Source: SourceHandLabelled, Signal: "typed-turn", Path: "/x.jsonl", Seq: 7,
			Class: "user-correction", Labeller: "operator", Excerpt: "e", Note: "n",
		},
	}}
	if err := Save(p, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out.Entries) != 1 || out.Entries[0] != in.Entries[0] {
		t.Errorf("round trip lost data:\n got %+v\nwant %+v", out.Entries, in.Entries)
	}
}

func TestDiscoverFeedbackFindsBothChannels(t *testing.T) {
	root := t.TempDir()
	mem := filepath.Join(root, "-Users-someone-repo", "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mem, "feedback_no_background_commits.md"),
		[]byte("# Never background a commit\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A memory file that is NOT a feedback_ file must not be swept in.
	if err := os.WriteFile(filepath.Join(mem, "reference_something.md"), []byte("# ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, "FEEDBACK.md")
	if err := os.WriteFile(extra, []byte("## raw critique\nline\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := DiscoverFeedback(root, []string{extra, filepath.Join(root, "absent.md")})
	if err != nil {
		t.Fatalf("DiscoverFeedback: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("found %d feedback files, want 2:\n%+v", len(files), files)
	}
	if files[0].Title != "Never background a commit" {
		t.Errorf("title = %q, want the first heading", files[0].Title)
	}
	if files[1].Path != extra {
		t.Errorf("the extra source was not included: %+v", files[1])
	}

	set := FromFeedback(files)
	for _, e := range set.Entries {
		if e.Source != SourceFileChannel {
			t.Errorf("entry %s has source %s, want %s", e.ID, e.Source, SourceFileChannel)
		}
		// The ground truth IS the file's existence: the operator wrote each one
		// specifically to correct agent behaviour, so it arrives labelled rather than
		// waiting for a labeller.
		if e.Class != "user-correction" {
			t.Errorf("entry %s class=%q, want user-correction", e.ID, e.Class)
		}
		if !strings.HasPrefix(e.ID, "file:") {
			t.Errorf("entry id %q must be prefixed file: so it can never collide with a candidate key", e.ID)
		}
		if !strings.Contains(e.Note, "not into a session") {
			t.Errorf("entry %s note must record that this correction bypassed the transcript: %q", e.ID, e.Note)
		}
	}
}

func TestDefaultPathHonoursTheEnvOverride(t *testing.T) {
	t.Setenv(EnvPath, "/tmp/somewhere/gold.jsonl")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/somewhere/gold.jsonl" {
		t.Errorf("DefaultPath()=%q, want the override", p)
	}
}

func TestDefaultPathLivesOutsideTheRepository(t *testing.T) {
	t.Setenv(EnvPath, "")
	t.Setenv("XDG_DATA_HOME", "/data")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	// Every entry quotes a real transcript or the operator's own critique, so the
	// default location is beside the index, never in git.
	if p != "/data/pg-ccaudit/goldset.jsonl" {
		t.Errorf("DefaultPath()=%q, want /data/pg-ccaudit/goldset.jsonl", p)
	}
}
