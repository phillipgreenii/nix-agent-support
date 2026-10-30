package settle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// removedConversation seeds, settles and removes the Conversation; it returns
// the original bytes and the original file's id.
func (e *env) removedConversation() (string, int64) {
	e.t.Helper()
	e.remove_(true)
	content := e.oldSettled(6, removeAge+time.Hour)
	file := e.fileID()
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		e.t.Fatalf("%+v", rep)
	}
	return content, file
}

func TestExtractRestoresByteExactAndRegistersSoTheCopierAttaches(t *testing.T) {
	e := newEnv(t, false)
	content, file := e.removedConversation()
	res, err := e.sett.Extract(sess, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) != 1 || res.Resume != "claude --resume "+sess {
		t.Fatalf("%+v", res)
	}
	if got := e.readOrig(e.transcript()); got != content {
		t.Fatal("not byte-exact")
	}
	fi, _ := os.Stat(e.path(e.transcript()))
	if !fi.ModTime().Equal(e.clk.Now()) {
		t.Fatalf("modification time %v, want now %v", fi.ModTime(), e.clk.Now())
	}
	if e.stage() != StageQuiet {
		t.Fatalf("stage %s", e.stage())
	}
	// The copier attaches to the existing copy: the same file id, nothing copied again.
	files, _ := e.st.AllFiles()
	r, err := e.cp.Ingest(e.path(e.transcript()))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.AllFiles()
	if r.File != file || len(after) != len(files) || r.Action != "unchanged" || r.Bytes != 0 {
		t.Fatalf("copier did not attach: %+v files %d -> %d", r, len(files), len(after))
	}
}

func TestExtractResetsTheQuietAge(t *testing.T) {
	e := newEnv(t, false)
	e.removedConversation()
	if _, err := e.sett.Extract(sess, ""); err != nil {
		t.Fatal(err)
	}
	e.ingest(e.transcript())
	e.tick()
	if e.stage() != StageQuiet {
		t.Fatalf("settled again at once: %s", e.stage())
	}
	e.clk.Advance(25 * time.Hour)
	e.tick()
	if e.stage() != StageSettled {
		t.Fatalf("stage after a quiet day = %s", e.stage())
	}
	// And it is not removable until a full remove.after passes from the extract.
	e.reconfig(func(c *Config) {})
	rep, _ := e.sett.Remove(RemoveOptions{DryRun: true, Sessions: []string{sess}})
	if len(rep.Candidates) != 0 || !hasReason(e.skipReasons(rep, sess), BlockQuietAge) {
		t.Fatalf("%+v", rep)
	}
}

func TestExtractRefusals(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	if _, err := e.sett.Extract(sess, ""); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("session id exists under a project directory: %v", err)
	}
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("%+v", rep)
	}
	e.live[sess] = true
	if _, err := e.sett.Extract(sess, ""); !errors.Is(err, ErrHeldByLive) {
		t.Fatalf("live Process: %v", err)
	}
	e.live[sess] = false
	if _, err := e.sett.Extract("0a1b2c3d-0000-4000-8000-0000000000ff", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	// Some other project directory holds the id.
	other := e.path("projects/other-project")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, sess+".jsonl"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sett.Extract(sess, ""); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("id under another project: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, sess+".jsonl")); string(b) != "x\n" {
		t.Fatal("the refusal touched the other file")
	}
}

func TestExtractToDirectoryNeverTouchesTheClaudeDirectory(t *testing.T) {
	e := newEnv(t, false)
	content, _ := e.removedConversation()
	before := snap(t, e.claude)
	to := filepath.Join(t.TempDir(), "out")
	res, err := e.sett.Extract(sess, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) != 1 || res.Resume != "" {
		t.Fatalf("%+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(to, e.transcript())); string(b) != content {
		t.Fatal("bytes differ")
	}
	if d := before.Diff(snap(t, e.claude)); d != "" {
		t.Fatalf("--to changed the Claude directory: %s", d)
	}
	if e.stage() != "removed" {
		t.Fatalf("--to changed the stage: %s", e.stage())
	}
	if _, err := e.sett.Extract(sess, e.path("projects/inside")); !errors.Is(err, ErrBadTarget) {
		t.Fatalf("--to inside the Claude directory: %v", err)
	}
	if _, err := e.sett.Extract(sess, to); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("--to target exists: %v", err)
	}
}

func TestExtractCrashPointsConverge(t *testing.T) {
	for _, point := range []string{"extract.after-register", "extract.after-mtime", "extract.after-path", "extract.after-partial"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t, false)
			content, _ := e.removedConversation()
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.sett.Extract(sess, "") })
			disarm()
			if !crashed {
				t.Fatalf("%s not reached", point)
			}
			e.crash()
			if _, err := e.sett.Extract(sess, ""); err != nil {
				t.Fatalf("running the extract again: %v", err)
			}
			if got := e.readOrig(e.transcript()); got != content {
				t.Fatal("bytes differ after the retried extract")
			}
			if e.stage() != StageQuiet {
				t.Fatalf("stage %s", e.stage())
			}
			matches, _ := filepath.Glob(e.path("projects/example-project/*.tmp.pgrec"))
			if len(matches) != 0 {
				t.Fatalf("a temporary file is left: %v", matches)
			}
		})
	}
}

func TestExtractRestoresSubFilesAndSnapshotFiles(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.seed(3, removeAge+time.Hour)
	meta := `{"agentType":"example"}`
	rel := "projects/example-project/" + sess + "/subagents/agent-1.meta.json"
	e.write(rel, meta, e.clk.Now().Add(-removeAge-time.Hour), false)
	e.ingest(rel)
	e.tickSettleOnly()
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("%+v", rep)
	}
	if e.exists(rel) {
		t.Fatal("the sub-file stayed")
	}
	if _, err := e.sett.Extract(sess, ""); err != nil {
		t.Fatal(err)
	}
	if e.readOrig(rel) != meta || !strings.HasPrefix(e.readOrig(e.transcript()), "{") {
		t.Fatal("not restored")
	}
}
