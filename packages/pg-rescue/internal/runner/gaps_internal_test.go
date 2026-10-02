package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForegroundOfTerminalIsFalseWithoutAControllingTerminal(t *testing.T) {
	f, err := os.Open("/dev/tty")
	if err == nil {
		f.Close()
		t.Skip("this run has a controlling terminal")
	}
	if foregroundOfTerminal() {
		t.Error("no terminal, so not the foreground group")
	}
}

func TestWriteFileAtomicSkipsATakenTemporaryName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "report.json")
	next := tmpSeq.Load() + 1
	taken := filepath.Join(dir, fmt.Sprintf(".report.json.tmp-%d-%d", os.Getpid(), next))
	if err := os.WriteFile(taken, []byte("squatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "data" {
		t.Errorf("report = %q", b)
	}
	if b, _ := os.ReadFile(taken); string(b) != "squatter" {
		t.Errorf("the squatter's file was touched: %q", b)
	}
}

func TestWriteFileAtomicCleansUpWhenTheRenameFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "report.json")
	if err := os.MkdirAll(filepath.Join(p, "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("data")); err == nil {
		t.Fatal("renaming over a non-empty directory must fail")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("a temporary file was left behind: %v", ents)
	}
}

func TestWriteAttemptFileReportsAMissingDirectory(t *testing.T) {
	if err := writeAttemptFile(filepath.Join(t.TempDir(), "no", "such", "f"), nil); err == nil {
		t.Error("expected an error")
	}
}

func TestSweepGroupWithNoMembersIsANoOp(t *testing.T) {
	r := &run{p: Params{KillGrace: time.Hour}}
	start := time.Now()
	r.sweepGroup(1 << 22) // no such group
	if time.Since(start) > time.Second {
		t.Error("sweeping an empty group must return at once")
	}
	if !waitGroupGone(1<<22, time.Hour) {
		t.Error("an absent group is gone immediately")
	}
}
