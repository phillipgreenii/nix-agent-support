package settle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

const removeAge = 30 * 24 * time.Hour

// reconfig reopens the environment with a changed settle configuration.
func (e *env) reconfig(f func(*Config)) {
	e.t.Helper()
	_ = e.st.Close()
	e.extra = f
	e.open()
}

// oldSettled seeds a Conversation whose files are age old and settles it.
func (e *env) oldSettled(n int, age time.Duration) string {
	e.t.Helper()
	content := e.seed(n, age)
	e.tickSettleOnly()
	if got := e.stage(); got != StageSettled {
		e.t.Fatalf("stage = %s, want settled", got)
	}
	return content
}

// tickSettleOnly runs the settle pass without the automatic removal pass, so a
// test controls when removal runs.
func (e *env) tickSettleOnly() {
	e.t.Helper()
	was := e.sett.cfg.RemoveEnable
	e.sett.cfg.RemoveEnable = false
	defer func() { e.sett.cfg.RemoveEnable = was }()
	e.tick()
}

func (e *env) remove(sessions ...string) RemoveReport {
	e.t.Helper()
	rep, err := e.sett.Remove(RemoveOptions{Sessions: sessions})
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

func (e *env) skipReasons(rep RemoveReport, session string) []string {
	for _, s := range rep.Skipped {
		if s.Session == session {
			return s.Reasons
		}
	}
	return nil
}

func (e *env) exists(rel string) bool {
	_, err := os.Lstat(e.path(rel))
	return err == nil
}

func (e *env) readOrig(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) counter(name string, labels map[string]string) float64 {
	v, _ := e.obs.Reg.Counter(name, labels)
	return v
}

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

func TestRemovalOffByDefaultAndTickNeverRemoves(t *testing.T) {
	e := newEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	if _, err := e.sett.Remove(RemoveOptions{Sessions: []string{sess}}); !errors.Is(err, ErrRemovalDisabled) {
		t.Fatalf("Remove with removal disabled: %v", err)
	}
	e.tick()
	if !e.exists(e.transcript()) || e.stage() != StageSettled {
		t.Fatal("a disabled removal touched the Claude directory")
	}
}

func TestRemovalThirtyDayBoundaryBothSides(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge-time.Minute)
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockQuietAge) {
		t.Fatalf("29d23h59m: %+v", rep)
	}
	if !e.exists(e.transcript()) {
		t.Fatal("the file went at 29d23h59m")
	}
	e.clk.Advance(time.Minute)
	rep = e.remove(sess)
	if len(rep.Removed) != 1 {
		t.Fatalf("30d: %+v", rep)
	}
	if e.exists(e.transcript()) || e.stage() != store.StageRemoved {
		t.Fatalf("after removal: exists=%v stage=%s", e.exists(e.transcript()), e.stage())
	}
}

// remove_ turns remove.enable on (the helper keeps tests readable).
func (e *env) remove_(on bool) {
	e.t.Helper()
	e.removeOn = on
	_ = e.st.Close()
	e.open()
}

func TestRemovalKeepsTheDocumentedOrderAndTheBytes(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(6, removeAge+time.Hour)
	file := e.fileID()
	e.rec.Reset()
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("%+v", rep)
	}
	dir := e.path("projects/example-project")
	steps := fsx.RemovalOrder(dir, scope.QuarantineDir, "000001.seg")
	if err := fsx.AssertOrder(e.rec.Events(), steps...); err != nil {
		t.Fatal(err)
	}
	if got := e.raw(file, len(content)); got != content {
		t.Fatal("archived bytes differ after removal")
	}
	// The empty project directory is tidied after the last file goes.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the empty project directory is left: %v", err)
	}
	row, _, _ := e.st.Stage(sess)
	if row.Stage != store.StageRemoved {
		t.Fatalf("stage %s", row.Stage)
	}
	if v, _ := e.obs.Reg.Counter(obs.MRemovedTotal, nil); v != 1 {
		t.Fatalf("removed counter %v", v)
	}
	if paths, _ := e.st.CurrentPaths(file); len(paths) != 0 {
		t.Fatalf("paths stay open after removal: %v", paths)
	}
}

func TestCloneModeRemovalWritesAndVerifiesTheArchiveFromTheClone(t *testing.T) {
	e := newRemoveEnv(t, true)
	content := e.oldSettled(5, removeAge+time.Hour)
	file := e.fileID()
	f, _ := e.st.File(file)
	if _, err := os.Stat(e.st.CopyPath(file, f.CurGen)); err != nil {
		t.Fatalf("clone mode keeps the clone until removal: %v", err)
	}
	if rs, _ := e.st.Ranges(file, f.CurGen); len(rs) != 0 {
		t.Fatalf("clone mode settled with an archive already: %+v", rs)
	}
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("%+v", rep)
	}
	if rs, _ := e.st.Ranges(file, f.CurGen); len(rs) == 0 {
		t.Fatal("removal did not write an archive from the clone")
	}
	if got := e.raw(file, len(content)); got != content {
		t.Fatal("archived bytes differ")
	}
}

func TestNewWritesAfterPendingReturnToQuiet(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	extra := line(9)
	e.hooks.BeforeQuarantine = func(_, p string) {
		f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		_, _ = f.WriteString(extra)
		_ = f.Close()
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || e.stage() != StageQuiet {
		t.Fatalf("removed=%v stage=%s", rep.Removed, e.stage())
	}
	if e.readOrig(e.transcript()) != content+extra {
		t.Fatal("the file is not intact")
	}
	e.hooks.BeforeQuarantine = nil
	e.ingest(e.transcript())
	if got := e.raw(e.fileID(), len(content+extra)); got != content+extra {
		t.Fatal("the copy did not continue")
	}
}

func TestQuarantineWriteThroughAHeldDescriptorRestores(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	fd, err := os.OpenFile(e.path(e.transcript()), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fd.Close() }()
	extra := line(9)
	e.hooks.AfterQuarantine = func(_, _ string) { _, _ = fd.WriteString(extra) }
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), "write_after_rename") {
		t.Fatalf("%+v", rep)
	}
	if e.stage() != StageQuiet {
		t.Fatalf("stage %s", e.stage())
	}
	if got := e.readOrig(e.transcript()); got != content+extra {
		t.Fatalf("restored bytes are not the original plus the write: %d bytes", len(got))
	}
	if e.counter(obs.MQuarantineMismatch, nil) != 1 {
		t.Fatal("removal_quarantine_mismatch_total not counted")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.path("projects/example-project"), scope.QuarantineDir)); len(ents) != 0 {
		t.Fatal("a stray quarantine file")
	}
}

func TestWriteBetweenRehashAndUnlinkIsACountedRace(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	fd, _ := os.OpenFile(e.path(e.transcript()), os.O_WRONLY|os.O_APPEND, 0)
	defer func() { _ = fd.Close() }()
	extra := line(9)
	e.hooks.BeforeUnlink = func(_, _ string) { _, _ = fd.WriteString(extra) }
	rep := e.remove(sess)
	if len(rep.Removed) != 0 {
		t.Fatalf("removed despite a write before the unlink: %+v", rep)
	}
	if v, _ := e.obs.Reg.Counter(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonRemovalRace}); v != float64(len(extra)) {
		t.Fatalf("removal race counted %v, want %d", v, len(extra))
	}
	if e.readOrig(e.transcript()) != content+extra {
		t.Fatal("the file was lost")
	}
}

func TestHolderFoundRestoresTheFile(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	e.holders = func(string) ([]int, error) { return []int{os.Getpid() + 1}, nil }
	_ = e.st.Close()
	e.open()
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), "holder_found") {
		t.Fatalf("%+v", rep)
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the file is not back")
	}
}

func TestCrashAtEveryRemovalPointConverges(t *testing.T) {
	for _, point := range []string{"remove.after-pending", "remove.after-rename", "remove.after-quarantined", "remove.after-unlink", "remove.before-removed", "remove.after-removed"} {
		t.Run(point, func(t *testing.T) {
			e := newRemoveEnv(t, false)
			content := e.oldSettled(6, removeAge+time.Hour)
			file := e.fileID()
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { _, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}}) })
			disarm()
			if !crashed {
				t.Fatalf("%s not reached", point)
			}
			e.crash()
			if got := e.stage(); got != store.StageRemoved {
				t.Fatalf("stage %s after recovery", got)
			}
			if e.exists(e.transcript()) {
				t.Fatal("the file is back or was never removed")
			}
			if got := e.raw(file, len(content)); got != content {
				t.Fatal("bytes lost")
			}
		})
	}
}

func TestCrashAfterQuarantineWithANewProcessRestores(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(6, removeAge+time.Hour)
	disarm := faults.ArmPanic("remove.after-quarantined")
	crashed := faults.Recover(func() { _, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}}) })
	disarm()
	if !crashed {
		t.Fatal("not reached")
	}
	e.live[sess] = true // a Process came back while the recorder was down
	e.crash()
	if e.stage() == store.StageRemoved {
		t.Fatal("removed under a live Process")
	}
	if e.readOrig(e.transcript()) != content {
		t.Fatal("the quarantined file was not restored")
	}
	if e.stage() == store.StageRemovalPending {
		t.Fatal("the pending removal was left")
	}
}

func TestOrphanInTheQuarantineIsLoggedAndKept(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.seed(3, time.Hour)
	q := filepath.Join(e.path("projects/example-project"), scope.QuarantineDir)
	if err := os.MkdirAll(q, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(q, "999-unlisted.jsonl")
	if err := os.WriteFile(orphan, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.allowQuarantine = true
	e.crash()
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("the orphan was not left in place")
	}
	if len(e.obs.EventsNamed(obs.EvQuarantineOrphan)) != 1 {
		t.Fatalf("events %+v", e.obs.Events())
	}
}

func TestFileCreatedAtTheOriginalPathIsNeverOverwritten(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	e.allowQuarantine = true
	e.holders = func(string) ([]int, error) { return []int{os.Getpid() + 1}, nil }
	_ = e.st.Close()
	e.open()
	fresh := "a new file\n"
	e.hooks.AfterQuarantine = func(_, p string) {
		if err := os.WriteFile(p, []byte(fresh), 0o600); err != nil {
			t.Error(err)
		}
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 {
		t.Fatalf("%+v", rep)
	}
	if e.readOrig(e.transcript()) != fresh {
		t.Fatal("the new file was overwritten")
	}
	if e.stage() != StageHeld || e.heldReason() != ReasonRestoreConflict {
		t.Fatalf("stage %s", e.stage())
	}
	q := filepath.Join(e.path("projects/example-project"), scope.QuarantineDir)
	ents, _ := os.ReadDir(q)
	if len(ents) != 1 {
		t.Fatalf("the quarantined original is not kept: %v", ents)
	}
	if b, _ := os.ReadFile(filepath.Join(q, ents[0].Name())); string(b) != content {
		t.Fatal("the kept original's bytes differ")
	}
	e.tick()
	if v, _ := e.obs.Reg.Gauge(obs.MQuarantineFiles, nil); v != 1 {
		t.Fatalf("quarantine_files = %v", v)
	}
}

// exdevFS fails every rename with EXDEV, as a quarantine on another device would.
type exdevFS struct{ fsx.FS }

func (x exdevFS) Rename(a, b string) error {
	return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV}
}

func TestExdevRefusesRemovalAndLeavesTheFileUntouched(t *testing.T) {
	e := newEnvWith(t, false, func(e *env) {
		e.removeOn = true
		e.claudeFS = func(in fsx.FS) fsx.FS { return exdevFS{in} }
	})
	content := e.oldSettled(5, removeAge+time.Hour)
	before, _ := lstat(e.path(e.transcript()))
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockUnavailable) {
		t.Fatalf("%+v", rep)
	}
	after, _ := lstat(e.path(e.transcript()))
	if before.Ino != after.Ino || before.MtimeNS != after.MtimeNS || e.readOrig(e.transcript()) != content {
		t.Fatal("the file was touched")
	}
	if e.counter(obs.MSkippedFiles, map[string]string{"reason": obs.ReasonQuarantineUnavl}) < 1 {
		t.Fatal("skipped_files_total{reason=quarantine_unavailable} not counted")
	}
}

func TestHardLinkedFileIsSkippedAndTheOtherLinkUntouched(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	other := filepath.Join(t.TempDir(), "other-link")
	if err := os.Link(e.path(e.transcript()), other); err != nil {
		t.Fatal(err)
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockUnavailable) {
		t.Fatalf("%+v", rep)
	}
	if b, _ := os.ReadFile(other); string(b) != content || e.readOrig(e.transcript()) != content {
		t.Fatal("a link was touched")
	}
}

func TestWriteThroughAHardLinkMadeAfterTheRenameIsDetected(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(5, removeAge+time.Hour)
	other := filepath.Join(t.TempDir(), "other-link")
	extra := line(9)
	e.hooks.AfterQuarantine = func(_, p string) {
		q := filepath.Join(filepath.Dir(p), scope.QuarantineDir)
		ents, _ := os.ReadDir(q)
		if len(ents) != 1 {
			t.Error("no quarantined file")
			return
		}
		if err := os.Link(filepath.Join(q, ents[0].Name()), other); err != nil {
			t.Error(err)
			return
		}
		f, _ := os.OpenFile(other, os.O_WRONLY|os.O_APPEND, 0)
		_, _ = f.WriteString(extra)
		_ = f.Close()
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || e.counter(obs.MQuarantineMismatch, nil) != 1 {
		t.Fatalf("%+v", rep)
	}
	if e.readOrig(e.transcript()) != content+extra {
		t.Fatal("the restored file lacks the write made through the other link")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("the other link was removed")
	}
}

func TestRegistryNamingTheSessionBlocksRemoval(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.seed(3, removeAge+time.Hour)
	reg := `{"pid":4242,"sessionId":"` + sess + `","cwd":"/example/work","procStart":"Wed Oct 14 07:51:50 2026","version":"2.1.0","status":"idle"}`
	e.write("sessions/4242.json", reg, e.clk.Now().Add(-removeAge-time.Hour), false)
	e.ingest("sessions/4242.json")
	e.tickSettleOnly()
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockRegistry) {
		t.Fatalf("%+v", rep)
	}
	w, err := e.sett.Where(sess)
	if err != nil {
		t.Fatal(err)
	}
	if w.ClaudeCodeVersion != "2.1.0" || w.ParserVersion == 0 {
		t.Fatalf("where lacks the versions: %+v", w)
	}
	if !e.exists(e.transcript()) {
		t.Fatal("removed")
	}
}

func TestLiveProcessBlocksRemoval(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	e.live[sess] = true
	rep := e.remove(sess)
	if !hasReason(e.skipReasons(rep, sess), BlockLive) || !e.exists(e.transcript()) {
		t.Fatalf("%+v", rep)
	}
}

func TestFormatHoldKeepsFilesWhenTheParseLooksWrong(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.write(e.transcript(), line(1)+"{broken\n{broken\n{broken\n", e.clk.Now().Add(-removeAge-time.Hour), false)
	e.ingest(e.transcript())
	e.parseAll()
	e.tickSettleOnly()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s (a bad line counts as parsed)", e.stage())
	}
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), BlockFormat) {
		t.Fatalf("%+v", rep)
	}
	e.reconfig(func(c *Config) { c.HoldRate = 0.9 })
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("a high holdRate should let it go: %+v", rep)
	}
}

// seedAs seeds a second Conversation (a different session id).
func (e *env) seedAs(id string, age time.Duration) {
	e.t.Helper()
	var s string
	for i := 1; i <= 3; i++ {
		s += strings.ReplaceAll(line(i), sess, id)
	}
	rel := "projects/example-project/" + id + ".jsonl"
	e.write(rel, s, e.clk.Now().Add(-age), false)
	e.ingest(rel)
	e.parseAll()
}

func TestMaxPerRunAndMaxPerHour(t *testing.T) {
	ids := []string{"0a1b2c3d-0000-4000-8000-000000000002", "0a1b2c3d-0000-4000-8000-000000000003"}
	e := newRemoveEnv(t, false)
	e.reconfig(func(c *Config) { c.MaxPerRun, c.MaxPerHour = 1, 1 })
	e.seed(3, removeAge+time.Hour)
	for _, id := range ids {
		e.seedAs(id, removeAge+time.Hour)
	}
	e.tickSettleOnly()
	rep, err := e.sett.Remove(RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.SkipCounts[BlockRunCap] != 2 {
		t.Fatalf("maxPerRun: %+v", rep)
	}
	rep, _ = e.sett.Remove(RemoveOptions{})
	if len(rep.Removed) != 0 || rep.SkipCounts[BlockRunCap]+rep.SkipCounts[BlockHourCap] == 0 {
		t.Fatalf("maxPerHour within the hour: %+v", rep)
	}
	if rep.SkipCounts[BlockHourCap] == 0 {
		t.Fatalf("the hourly cap did not apply: %+v", rep)
	}
	e.clk.Advance(61 * time.Minute)
	rep, _ = e.sett.Remove(RemoveOptions{})
	if len(rep.Removed) != 1 {
		t.Fatalf("after the hour: %+v", rep)
	}
}

func TestDryRunWorksWhileDisabledAndChangesNothing(t *testing.T) {
	e := newEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	before := snap(t, e.claude)
	stateBefore := snap(t, e.state)
	rep, err := e.sett.Remove(RemoveOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Enabled || !rep.DryRun || len(rep.Candidates) != 1 || rep.Bytes == 0 {
		t.Fatalf("%+v", rep)
	}
	if d := before.Diff(snap(t, e.claude)); d != "" {
		t.Fatalf("a dry run changed the Claude directory: %s", d)
	}
	if d := stateBefore.Diff(snap(t, e.state)); d != "" {
		t.Fatalf("a dry run changed the state directory: %s", d)
	}
	rep, _ = e.sett.Remove(RemoveOptions{DryRun: true, Sessions: []string{sess}})
	if len(rep.Candidates) != 1 {
		t.Fatalf("%+v", rep)
	}
}

func TestDryRunReportsSkipsWithCountsByReason(t *testing.T) {
	e := newEnv(t, false)
	e.oldSettled(5, 40*24*time.Hour) // settled but not 30 days old
	e.clk.Advance(-20 * 24 * time.Hour)
	rep, err := e.sett.Remove(RemoveOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.SkipCounts[BlockQuietAge] != 1 || len(rep.Candidates) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestRemovedToQuietWhenAPathReappears(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("%+v", rep)
	}
	e.write(e.transcript(), line(1), e.clk.Now(), false)
	e.ingest(e.transcript())
	e.tick()
	if got := e.stage(); got != StageQuiet {
		t.Fatalf("stage after a path reappeared = %s", got)
	}
}

func TestWhereBlockedByAndClear(t *testing.T) {
	e := newEnv(t, false)
	e.oldSettled(5, 40*24*time.Hour)
	e.clk.Advance(-20 * 24 * time.Hour)
	w, err := e.sett.Where(sess)
	if err != nil {
		t.Fatal(err)
	}
	if w.Stage != StageSettled || len(w.BlockedBy) < 2 || w.BlockedBy[0] != BlockRemovalDisabled || !hasReason(w.BlockedBy, BlockQuietAge) {
		t.Fatalf("%+v", w)
	}
	if w.NextEligible == nil || w.Clear == "" || w.ArchivePath == "" || len(w.Manifest) == 0 {
		t.Fatalf("%+v", w)
	}
	if _, err := e.sett.Where("0a1b2c3d-0000-4000-8000-0000000000ff"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}

func TestHeldListsStableColumnsAndRemedy(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, 48*time.Hour)
	// A held Conversation: an unparsed file of a parsed kind.
	e.write(e.transcript(), line(1)+line(2)+line(3)+line(4), e.clk.Now().Add(-48*time.Hour), false)
	e.ingest(e.transcript())
	e.tick()
	// The check passes after a parse, so force a held row through the store.
	if err := e.st.Commit(&store.Rec{
		T: store.TStage, At: e.clk.Now().UnixNano(), Session: sess, State: StageHeld, Reason: ReasonCopyTail,
		Val: `[{"file":1,"path":"projects/example-project/x.jsonl","reason":"copy_tail_mismatch","detail":"tail hash differs"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.sett.Held()
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %+v", err, rows)
	}
	if rows[0].Reason != ReasonCopyTail || rows[0].Remedy == "" || rows[0].Files != 1 {
		t.Fatalf("%+v", rows[0])
	}
	for _, r := range Reasons {
		if Remedies[r] == "" {
			t.Errorf("reason %s has no remedy line", r)
		}
	}
	if fmt.Sprint(rows[0].Since) == "" {
		t.Fatal("no since")
	}
	_ = fs.ErrNotExist
}

// A quarantined file creates no new path record and no rename: the copier skips
// anything under the quarantine directory (the copier's lock is held for the
// whole removal, so the watcher's events are answered after it), and a vanished
// original path is no tombstone while its Conversation is being removed.
func TestQuarantinedFileCreatesNoPathRecordOrRename(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.oldSettled(5, removeAge+time.Hour)
	file := e.fileID()
	before, _ := e.st.PathHistory(file)
	q := filepath.Join(e.path("projects/example-project"), scope.QuarantineDir)
	var seenQ bool
	e.hooks.AfterQuarantine = func(_, _ string) {
		ents, _ := os.ReadDir(q)
		seenQ = len(ents) == 1
	}
	if rep := e.remove(sess); len(rep.Removed) != 1 || !seenQ {
		t.Fatalf("%+v quarantined=%v", rep, seenQ)
	}
	res, err := e.cp.Ingest(filepath.Join(q, "1-"+sess+".jsonl"))
	if err != nil || res.Action != "skipped" || res.Reason != "quarantine" {
		t.Fatalf("ingest of a quarantine path: %+v %v", res, err)
	}
	if res, err := e.cp.Ingest(e.path(e.transcript())); err != nil || res.Action == "tombstone" {
		t.Fatalf("the removed original became a tombstone: %+v %v", res, err)
	}
	after, _ := e.st.PathHistory(file)
	if len(after) != len(before) {
		t.Fatalf("path records %v -> %v", before, after)
	}
}
