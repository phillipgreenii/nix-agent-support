package corpus

import (
	"path/filepath"
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

func TestWalkCorpus_MissingProjectsDir(t *testing.T) {
	files, err := walkCorpus(t.TempDir(), time.Hour, time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("err = %v, want nil for a missing projects dir", err)
	}
	if len(files) != 0 {
		t.Fatalf("files = %d, want 0", len(files))
	}
}
