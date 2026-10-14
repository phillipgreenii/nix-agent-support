package corpus

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestWalkCorpus_ClassifiesAndWindows(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dir := projectDir(t, home, "/tmp/proj")
	writeTranscript(t, dir, "s1.jsonl", now, assistantLine("m", 1, 1))                     // in-window transcript
	writeTranscript(t, dir, "old.jsonl", now.Add(-48*time.Hour), assistantLine("m", 1, 1)) // stale (out of window)
	// status sibling — ungated even though its mtime is old.
	writeStatus(t, filepath.Join(dir, "s1.status.jsonl"),
		`{"ts":100,"five_hour_pct":5,"five_hour_resets_at":200}`+"\n", now.Add(-48*time.Hour))

	files, err := walkCorpus(home, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	var transcripts, statuses int
	sawOld := false
	for _, f := range files {
		switch f.class {
		case Transcript:
			transcripts++
			if filepath.Base(f.path) == "old.jsonl" {
				sawOld = true
			}
		case StatusSibling:
			statuses++
		}
	}
	if transcripts != 1 {
		t.Errorf("transcripts = %d, want 1 (in-window only)", transcripts)
	}
	if sawOld {
		t.Errorf("stale transcript (out of window) was included in the walk")
	}
	if statuses != 1 {
		t.Errorf("statuses = %d, want 1 (status siblings are ungated)", statuses)
	}
}

// TestWalkCorpus_RecursesIntoSubagents proves the walk descends into
// <slug>/<id>/subagents/, so subagent transcripts (billable token usage the old
// recursive pricer counted) are surfaced for pricing.
func TestWalkCorpus_RecursesIntoSubagents(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dir := projectDir(t, home, "/tmp/proj")
	main := writeTranscript(t, dir, "s1.jsonl", now, assistantLine("m", 1, 1))
	sub := subagentsDirFor(t, main)
	writeTranscript(t, sub, "agent-1.jsonl", now, assistantLine("m", 2, 2))

	files, err := walkCorpus(home, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	foundSub := false
	for _, f := range files {
		if f.class == Transcript && filepath.Base(f.path) == "agent-1.jsonl" {
			foundSub = true
		}
	}
	if !foundSub {
		t.Fatalf("walk did not recurse into subagents/ — subagent transcript missing (pricing would undercount)")
	}
}

func TestWalkCorpus_MissingProjectsDir(t *testing.T) {
	files, err := walkCorpus(t.TempDir(), time.Hour, time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("err = %v, want nil for a missing projects dir", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %d, want 0", len(files))
	}
}

// --- incremental walker (pg2-uxtdf) ---
//
// The producer re-walks the whole projects tree on every assemble; profiling the
// live daemon showed that re-listing every directory dominated its CPU. The
// walker re-lists a directory only when its mtime moved, and must still notice
// every change a full re-walk would.

// settleDirs backdates the mtime of every directory under root so the walker's
// racy-timestamp guard trusts a listing taken right after.
func settleDirs(t *testing.T, root string, mtime time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chtimes(p, mtime, mtime)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func walkPaths(files []walkFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.path)
	}
	return out
}

func mustWalk(t *testing.T, w *corpusWalker, home string, window time.Duration, now time.Time) []walkFile {
	t.Helper()
	files, err := w.walk(home, window, now)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCorpusWalker_UnchangedTreeIsNotRelisted(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dir := projectDir(t, home, "/tmp/proj")
	main := writeTranscript(t, dir, "s1.jsonl", now, assistantLine("m", 1, 1))
	writeTranscript(t, subagentsDirFor(t, main), "agent-1.jsonl", now, assistantLine("m", 2, 2))
	writeStatus(t, filepath.Join(dir, "s1.status.jsonl"), `{"ts":100}`+"\n", now)
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	w := newCorpusWalker()
	first := mustWalk(t, w, home, 24*time.Hour, now)
	if w.readDirs == 0 {
		t.Fatal("the cold walk listed no directories")
	}
	cold := w.readDirs

	second := mustWalk(t, w, home, 24*time.Hour, now)
	if got := w.readDirs - cold; got != 0 {
		t.Errorf("an unchanged tree re-listed %d directories, want 0", got)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("cached walk differs from the cold walk:\n cold=%v\n warm=%v", walkPaths(first), walkPaths(second))
	}
	if want := mustWalk(t, newCorpusWalker(), home, 24*time.Hour, now); !reflect.DeepEqual(want, second) {
		t.Errorf("cached walk differs from a fresh walk:\n fresh=%v\n warm=%v", walkPaths(want), walkPaths(second))
	}
}

func TestCorpusWalker_NoticesChangesWithoutRelistingUntouchedDirs(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dirA := projectDir(t, home, "/tmp/a")
	dirB := projectDir(t, home, "/tmp/b")
	writeTranscript(t, dirA, "live.jsonl", now, assistantLine("m", 1, 1))
	old := writeTranscript(t, dirA, "old.jsonl", now.Add(-48*time.Hour), assistantLine("m", 1, 1))
	gone := writeTranscript(t, dirB, "gone.jsonl", now, assistantLine("m", 1, 1))
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	w := newCorpusWalker()
	if got := len(mustWalk(t, w, home, 24*time.Hour, now)); got != 2 {
		t.Fatalf("cold walk = %d files, want 2 (live + gone; old is out of window)", got)
	}

	// 1. An out-of-window file is modified: its directory's mtime does NOT move, so
	// only the per-file lstat can notice it entering the window.
	if err := os.Chtimes(old, now, now); err != nil {
		t.Fatal(err)
	}
	// 2. A file is deleted: also invisible to the directory mtime we trust below.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour)) // dir mtimes back to the cached values
	before := w.readDirs
	files := mustWalk(t, w, home, 24*time.Hour, now)
	if got := w.readDirs - before; got != 0 {
		t.Errorf("re-listed %d directories although no directory mtime moved", got)
	}
	got := walkPaths(files)
	want := []string{filepath.Join(dirA, "live.jsonl"), old}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("walk = %v, want %v (old entered the window, gone was deleted)", got, want)
	}

	// 3. A new file appears and its directory's mtime moves: only that directory is re-listed.
	added := writeTranscript(t, dirB, "new.jsonl", now, assistantLine("m", 1, 1))
	if err := os.Chtimes(dirB, now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	before = w.readDirs
	files = mustWalk(t, w, home, 24*time.Hour, now)
	if got := w.readDirs - before; got != 1 {
		t.Errorf("re-listed %d directories after one directory changed, want 1", got)
	}
	found := false
	for _, f := range files {
		found = found || f.path == added
	}
	if !found {
		t.Errorf("new file %s missing from %v", added, walkPaths(files))
	}
}

func TestCorpusWalker_ReportsCurrentSizeAndMtimeOfCachedFiles(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dir := projectDir(t, home, "/tmp/proj")
	path := writeTranscript(t, dir, "s1.jsonl", now.Add(-time.Minute), assistantLine("m", 1, 1))
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	w := newCorpusWalker()
	first := mustWalk(t, w, home, 24*time.Hour, now)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(assistantLine("m", 2, 2) + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	second := mustWalk(t, w, home, 24*time.Hour, now)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("walks = %d / %d files, want 1 / 1", len(first), len(second))
	}
	if second[0].size <= first[0].size || !second[0].mtime.Equal(now) {
		t.Errorf("stale metadata after an append: first=%+v second=%+v", first[0], second[0])
	}
}

func TestCorpusWalker_DoesNotTrustAListingTakenInsideTheRacyWindow(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	dir := projectDir(t, home, "/tmp/proj")
	writeTranscript(t, dir, "s1.jsonl", now, assistantLine("m", 1, 1))
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	// The listing is recorded 100ms after the directory's own mtime: a second change
	// in the same timestamp tick would be invisible, so it must be re-read.
	w := newCorpusWalker()
	w.clock = func() time.Time { return now.Add(-time.Hour).Add(100 * time.Millisecond) }
	mustWalk(t, w, home, 24*time.Hour, now)
	before := w.readDirs
	mustWalk(t, w, home, 24*time.Hour, now)
	if got := w.readDirs - before; got == 0 {
		t.Error("a listing recorded within the racy window was trusted")
	}
}

func TestCorpusWalker_ForgetsVanishedDirectories(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	keep := projectDir(t, home, "/tmp/keep")
	drop := projectDir(t, home, "/tmp/drop")
	writeTranscript(t, keep, "k.jsonl", now, assistantLine("m", 1, 1))
	writeTranscript(t, drop, "d.jsonl", now, assistantLine("m", 1, 1))
	settleDirs(t, filepath.Join(home, "projects"), now.Add(-time.Hour))

	w := newCorpusWalker()
	mustWalk(t, w, home, 24*time.Hour, now)
	cached := len(w.dirs)
	if err := os.RemoveAll(drop); err != nil {
		t.Fatal(err)
	}
	mustWalk(t, w, home, 24*time.Hour, now)
	if len(w.dirs) != cached-1 {
		t.Errorf("cache holds %d directories after one vanished, want %d", len(w.dirs), cached-1)
	}
	if err := os.RemoveAll(filepath.Join(home, "projects")); err != nil {
		t.Fatal(err)
	}
	mustWalk(t, w, home, 24*time.Hour, now)
	if len(w.dirs) != 0 {
		t.Errorf("cache holds %d directories after the projects tree vanished, want 0", len(w.dirs))
	}
}
