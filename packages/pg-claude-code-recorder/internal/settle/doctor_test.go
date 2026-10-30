package settle

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
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
		_, err := tx.Exec(`UPDATE files SET last_size=last_size+1`)
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
		found = found || m.Kind == MismatchJournal
	}
	if !found {
		t.Fatalf("no journal mismatch: %+v", res)
	}
}
