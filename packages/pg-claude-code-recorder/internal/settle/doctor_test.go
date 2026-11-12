package settle

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func TestDoctorCleanWritesResultFileAndGauges(t *testing.T) {
	e := newEnv(t, false)
	e.seed(5, time.Hour)
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 || res.FilesChecked != 1 || res.MismatchFiles != 0 {
		t.Fatalf("%+v", res)
	}
	got, ok := LoadDoctor(e.state)
	if !ok || got.AtNS != e.clk.Now().UnixNano() {
		t.Fatalf("result file: %v %+v", ok, got)
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorLastRun, nil); v != float64(e.clk.Now().UnixNano())/1e9 {
		t.Fatalf("doctor_last_run_timestamp %v", v)
	}
	if v, ok := e.obs.Reg.Gauge(obs.MDoctorMismatchFiles, nil); !ok || v != 0 {
		t.Fatalf("doctor_mismatch_files %v %v", v, ok)
	}
}

func TestDoctorFindsACorruptCopyAndStartsANewGeneration(t *testing.T) {
	e := newEnv(t, false)
	content := e.seed(5, time.Hour)
	file := e.fileID()
	f, _ := e.st.File(file)
	cp := e.st.CopyPath(file, f.CurGen)
	b, _ := os.ReadFile(cp)
	b[10] ^= 0xff // same size: only a full-hash comparison sees it
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 || res.Mismatches[0].Kind != MismatchCopy || !res.Mismatches[0].Repaired || res.MismatchFiles != 1 {
		t.Fatalf("%+v", res)
	}
	after, _ := e.st.File(file)
	if after.CurGen != f.CurGen+1 {
		t.Fatalf("generation %d, want %d", after.CurGen, f.CurGen+1)
	}
	e.ingest(e.transcript()) // the copier fills the new generation from byte 0
	if got := e.raw(file, len(content)); got != content {
		t.Fatal("the new generation does not hold the original's bytes")
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorMismatchFiles, nil); v != 1 {
		t.Fatalf("doctor_mismatch_files %v", v)
	}
	// The new generation is clean on the next run.
	res, _ = e.sett.Doctor()
	if len(res.Mismatches) != 0 {
		t.Fatalf("second run: %+v", res)
	}
}

func TestDoctorFindsAnArchiveThatDisagreesWithItsManifest(t *testing.T) {
	e := newEnv(t, false)
	e.oldSettled(5, 48*time.Hour)
	res, err := e.sett.Doctor()
	if err != nil || len(res.Mismatches) != 0 || res.ArchivesCheck != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	mp := filepath.Join(e.st.SegDir(sess), "manifest.json")
	if err := os.WriteFile(mp, []byte(`{"session":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ = e.sett.Doctor()
	if len(res.Mismatches) != 1 || res.Mismatches[0].Kind != MismatchManifest {
		t.Fatalf("%+v", res)
	}
}

func TestDoctorSeesAJournalThatDisagreesWithTheIndex(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, time.Hour)
	e.skipInv = true // the test makes the index disagree on purpose
	if err := e.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE files SET last_size=last_size+1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range res.Mismatches {
		if m.Kind == MismatchJournal {
			found = true
			// The finding names the table and the row that differ.
			if !strings.Contains(m.Detail, "files: ") || !strings.Contains(m.Detail, "replay has ") {
				t.Fatalf("detail does not name what differs: %q", m.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("no journal mismatch: %+v", res)
	}
}

// pg2-dt89wi: a commit writes the journal first and the index second. A doctor
// run that lands between the two must not report journal_differs_from_index on
// a live daemon: it waits for the commit and compares a frozen view.
func TestDoctorIgnoresACommitInFlight(t *testing.T) {
	e := newEnv(t, false)
	e.seed(3, time.Hour)
	inJournal := make(chan struct{})
	release := make(chan struct{})
	disarm := faults.Arm("store.after-journal", func() {
		close(inJournal)
		<-release
	})
	defer disarm()
	committed := make(chan error, 1)
	go func() {
		committed <- e.st.Commit(&store.Rec{T: store.TState, At: e.clk.Now().UnixNano(), File: 1, State: "copy_mismatch"})
	}()
	<-inJournal // the journal has the record, the index does not
	type answer struct {
		res DoctorResult
		err error
	}
	done := make(chan answer, 1)
	go func() {
		r, err := e.sett.Doctor()
		done <- answer{r, err}
	}()
	var a answer
	select {
	case a = <-done: // an unfrozen doctor finishes while the commit is stuck
	case <-time.After(300 * time.Millisecond):
		close(release)
		a = <-done
		release = nil
	}
	if release != nil {
		close(release)
	}
	if a.err != nil {
		t.Fatal(a.err)
	}
	for _, m := range a.res.Mismatches {
		if m.Kind == MismatchJournal {
			t.Fatalf("false journal mismatch during a commit: %+v", m)
		}
	}
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
}

// snapshotSettled seeds a settled Conversation holding one in-scope snapshot
// file and returns its relative path, its file id and its stored-bytes path.
func (e *env) snapshotSettled(content string) (rel string, file int64, stored string) {
	e.t.Helper()
	rel = "projects/example-project/" + sess + "/subagents/agent-1.meta.json"
	age := e.clk.Now().Add(-48 * time.Hour)
	e.write(rel, content, age, false)
	e.ingest(rel)
	e.oldSettled(3, 48*time.Hour)
	fl, err := e.st.FilesByCurrentPath(e.path(rel))
	if err != nil || len(fl) == 0 {
		e.t.Fatalf("no snapshot file: %v", err)
	}
	stored = e.st.SnapshotPath(fl[0].ID, 1)
	// Reproduce the live damage: no plain copy and no archive range.
	if err := e.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `DELETE FROM ranges WHERE file_id=?`, fl[0].ID)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Remove(stored); err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	return rel, fl[0].ID, stored
}

func noticeKinds(res DoctorResult) []string {
	var k []string
	for _, n := range res.Notices {
		k = append(k, n.Kind)
	}
	return k
}

func TestDoctorRestoresAMissingSnapshotCopyFromAMatchingSource(t *testing.T) {
	e := newEnv(t, false)
	content := `{"agentType":"example"}`
	rel, _, stored := e.snapshotSettled(content)
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 || res.MismatchFiles != 0 {
		t.Fatalf("mismatches after repair: %+v", res)
	}
	if len(res.Notices) != 1 || res.Notices[0].Kind != NoticeRestored || !res.Notices[0].Repaired {
		t.Fatalf("notices: %+v", res.Notices)
	}
	if b, err := os.ReadFile(stored); err != nil || string(b) != content {
		t.Fatalf("stored bytes %q %v", b, err)
	}
	_ = rel
	again, _ := e.sett.Doctor()
	if len(again.Mismatches) != 0 || len(again.Notices) != 0 {
		t.Fatalf("second run: %+v", again)
	}
}

func TestDoctorReportsAnUnrecoverableMissingCopyAsANonCriticalNotice(t *testing.T) {
	for name, damage := range map[string]func(abs string) error{
		"source gone":    os.Remove,
		"source changed": func(abs string) error { return os.WriteFile(abs, []byte(`{"agentType":"changed"}`), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, false)
			rel, _, stored := e.snapshotSettled(`{"agentType":"example"}`)
			if err := damage(e.path(rel)); err != nil {
				t.Fatal(err)
			}
			res, err := e.sett.Doctor()
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Mismatches) != 0 || res.MismatchFiles != 0 {
				t.Fatalf("mismatches: %+v", res)
			}
			if k := noticeKinds(res); len(k) != 1 || k[0] != NoticeUnrecoverable {
				t.Fatalf("notices: %+v", res.Notices)
			}
			if _, err := os.Stat(stored); err == nil {
				t.Fatal("an unrecoverable entry was stored")
			}
			if v, ok := e.obs.Reg.Gauge(obs.MDoctorMismatchFiles, nil); !ok || v != 0 {
				t.Fatalf("doctor_mismatch_files %v %v", v, ok)
			}
		})
	}
}

// A file the current allowlist rejects was removed on purpose: doctor must not
// store it again, even though its source still exists and matches.
func TestDoctorDoesNotRestoreAnOutOfScopeFile(t *testing.T) {
	e := newEnv(t, false)
	content := `{"agentType":"example"}`
	rel, file, stored := e.snapshotSettled(content)
	// Point the manifest at a path the allowlist rejects (a scratchpad file).
	scratch := "projects/example-project/" + sess + "/scratchpad/note.md"
	e.write(scratch, content, e.clk.Now().Add(-48*time.Hour), false)
	st, ok, err := e.st.SettledOf(sess)
	if err != nil || !ok {
		t.Fatal(err)
	}
	var m store.Manifest
	if err := json.Unmarshal(st.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	for i := range m.Files {
		if m.Files[i].File == file {
			m.Files[i].Path = scratch
		}
	}
	b, _ := json.Marshal(m)
	if err := e.st.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE settled SET manifest=? WHERE session=?`, b, sess)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.st.SegDir(sess), "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = rel
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 || res.MismatchFiles != 0 {
		t.Fatalf("mismatches: %+v", res)
	}
	if k := noticeKinds(res); len(k) != 1 || k[0] != NoticeOutOfScope {
		t.Fatalf("notices: %+v", res.Notices)
	}
	if _, err := os.Stat(stored); err == nil {
		t.Fatal("an out-of-scope file was stored again")
	}
}

// snapshotSettledFile seeds a settled Conversation holding one snapshot file
// and returns its row and the content its stored version holds.
func (e *env) snapshotSettledFile() (store.File, string) {
	e.t.Helper()
	content := `{"agentType":"example"}`
	rel := "projects/example-project/" + sess + "/subagents/agent-1.meta.json"
	e.write(rel, content, e.clk.Now().Add(-48*time.Hour), false)
	e.ingest(rel)
	e.oldSettled(3, 48*time.Hour)
	fl, err := e.st.FilesByCurrentPath(e.path(rel))
	if err != nil || len(fl) == 0 {
		e.t.Fatalf("no snapshot file: %v", err)
	}
	return fl[0], content
}

func (e *env) settledManifest() store.Manifest {
	e.t.Helper()
	st, ok, err := e.st.SettledOf(sess)
	if err != nil || !ok {
		e.t.Fatalf("not settled: %v", err)
	}
	var m store.Manifest
	if err := json.Unmarshal(st.Manifest, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// A snapshot file whose row last recorded more bytes than its stored version
// holds (the row follows the source path, the version holds what was stored)
// must settle with the stored version's size next to its hash, so the archive
// agrees with its own manifest.
func TestSettleManifestRecordsTheStoredSizeOfASnapshotWhoseRowRecordsMore(t *testing.T) {
	e := newEnv(t, false)
	f, content := e.snapshotSettledFile()
	f.LastSize = 1022927
	c := &conv{session: sess, files: []store.File{f}, rel: map[int64]string{f.ID: "projects/x.json"}}
	m, err := e.sett.manifest(c, "clone", e.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	mf := m.Files[0]
	if mf.Size != int64(len(content)) || mf.StoredSize() != int64(len(content)) {
		t.Fatalf("size %d stored %d, want %d", mf.Size, mf.StoredSize(), len(content))
	}
	if mf.LastSize == nil || *mf.LastSize != 1022927 || mf.ChangeSize() != 1022927 {
		t.Fatalf("last_size %v", mf.LastSize)
	}
	// The change check still sees the row's size, so the settle is not repeated.
	b, _ := json.Marshal(m)
	if changedAgainst(store.Settled{Manifest: b}, c, func(store.File) (int64, bool) { return 0, false }) {
		t.Fatal("an unchanged snapshot file reads as changed")
	}
}

// A manifest written before last_size existed holds the row's size in size
// while the stored version is shorter: doctor must verify the stored bytes
// against the hash and report nothing, and extract must restore them.
func TestDoctorAndExtractAcceptALegacyManifestWithAShortStoredGeneration(t *testing.T) {
	e := newEnv(t, false)
	f, content := e.snapshotSettledFile()
	m := e.settledManifest()
	for i := range m.Files {
		if m.Files[i].File == f.ID {
			m.Files[i].Size, m.Files[i].LastSize = 1022927, nil // the shape that failed the doctor
		}
	}
	mb, _ := json.Marshal(m)
	val, _ := json.Marshal(store.SettleRec{Manifest: mb})
	if err := e.st.Commit(&store.Rec{T: store.TSettle, At: e.clk.Now().UnixNano(), Session: sess, Method: "clone", Val: string(val)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.st.SegDir(sess), "manifest.json"), mb, 0o600); err != nil {
		t.Fatal(err)
	}
	var mf store.ManifestFile
	for _, x := range e.settledManifest().Files {
		if x.File == f.ID {
			mf = x
		}
	}
	if mf.Size != 1022927 || mf.StoredSize() != int64(len(content)) {
		t.Fatalf("legacy shape not reproduced: size %d stored %d", mf.Size, mf.StoredSize())
	}
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 || res.MismatchFiles != 0 {
		t.Fatalf("mismatches: %+v", res.Mismatches)
	}
	tmp := filepath.Join(t.TempDir(), "restored")
	if err := e.sett.writeRestored(mf, tmp, e.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tmp); string(b) != content {
		t.Fatalf("restored %q", b)
	}
}
