package settle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// A file the recorder never ingested (scratchpad/ is outside the ingest
// allowlist) sits in the session directory. Removal works from the recorded
// files only: it removes the Conversation, leaves the unrecorded file
// untouched (no deletion, no hold), and keeps the session directory because
// it is not empty. The store, the copier and the removal path are the real ones.
func TestRemovalLeavesAnUnrecordedScratchpadFileAndItsDirectory(t *testing.T) {
	e := newRemoveEnv(t, false)
	rel := "projects/example-project/" + sess + "/scratchpad/claim.2.json"
	const body = `{"example":"scratch"}` + "\n"
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	e.oldSettled(5, removeAge+time.Hour)

	rep := e.remove(sess)
	if len(rep.Removed) != 1 || len(rep.Skipped) != 0 {
		t.Fatalf("the Conversation was held or skipped: %+v", rep)
	}
	if e.exists(e.transcript()) || e.stage() != store.StageRemoved {
		t.Fatalf("transcript exists=%v stage=%s", e.exists(e.transcript()), e.stage())
	}
	if got := e.readOrig(rel); got != body {
		t.Fatalf("the unrecorded scratchpad file changed: %q", got)
	}
	if !e.exists("projects/example-project/" + sess) {
		t.Fatal("the session directory went although it still holds the scratchpad file")
	}
	if files, _ := e.st.FilesByCurrentPath(p); len(files) != 0 {
		t.Fatalf("the scratchpad file was recorded: %+v", files)
	}
}

// A scratchpad file an older version recorded stays in the store but is never
// re-ingested. If it later changes on disk, verifying it would fail forever
// (the stored version never matches), so removal ignores recorded files the
// ingest allowlist rejects: the Conversation is removed, the stale file stays.
func TestRemovalIgnoresAStaleRecordedScratchpadFileThatChanged(t *testing.T) {
	e := newRemoveEnv(t, false)
	// Record a file at an accepted path, then move it into scratchpad/ and
	// repoint its path: this is the state an older version left behind.
	legacyRel := "projects/example-project/" + sess + "/subagents/legacy.json"
	scratchRel := "projects/example-project/" + sess + "/scratchpad/legacy.json"
	old := e.clk.Now().Add(-removeAge - 2*time.Hour)
	e.write(legacyRel, `{"v":1}`+"\n", old, false)
	res, err := e.cp.Ingest(e.path(legacyRel))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(e.path(scratchRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(e.path(legacyRel), e.path(scratchRel)); err != nil {
		t.Fatal(err)
	}
	now := e.clk.Now().UnixNano()
	if err := e.st.Commit(
		&store.Rec{T: store.TPath, At: now, File: res.File, Path: e.path(scratchRel)},
		&store.Rec{T: store.TPathClose, At: now, File: res.File, Path: e.path(legacyRel)},
	); err != nil {
		t.Fatal(err)
	}
	e.oldSettled(5, removeAge+time.Hour)
	// the stale file changes on disk after it was recorded
	e.write(scratchRel, `{"v":2,"changed":true}`+"\n", old, false)

	rep := e.remove(sess)
	if len(rep.Removed) != 1 || len(rep.Skipped) != 0 {
		t.Fatalf("a stale recorded scratchpad file held the Conversation: %+v", rep)
	}
	if got := e.readOrig(scratchRel); got != `{"v":2,"changed":true}`+"\n" {
		t.Fatalf("the stale scratchpad file was touched: %q", got)
	}
	if e.exists(e.transcript()) {
		t.Fatal("the transcript was not removed")
	}
}
