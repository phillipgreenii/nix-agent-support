package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

type okFiler struct{}

func (okFiler) Find(_ context.Context, _, _ string) (string, bool, error) { return "", false, nil }
func (okFiler) Create(_ context.Context, _ ledger.BeadRequest) (string, error) {
	return "b-1", nil
}

func stepOf(res MaintainResult, name string) *StepResult {
	for i := range res.Steps {
		if res.Steps[i].Name == name {
			return &res.Steps[i]
		}
	}
	return nil
}

func TestMaintainRunsEveryStepAndWritesHeartbeat(t *testing.T) {
	e := setup(t)
	res := Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	for _, n := range []string{"outbox", "auto-defer", "promote-waiting", "metrics", "export", "backup", "evidence-ttl", "reaper", "heartbeat"} {
		s := stepOf(res, n)
		if s == nil || s.Err != nil {
			t.Errorf("step %s: %+v", n, s)
		}
	}
	// auto-defer precedes promote-waiting
	var ad, pw int
	for i, s := range res.Steps {
		switch s.Name {
		case "auto-defer":
			ad = i
		case "promote-waiting":
			pw = i
		}
	}
	if ad > pw {
		t.Error("PromoteWaiting must follow auto-defer")
	}
	hb, err := e.l.Heartbeat(bg)
	if err != nil || !hb.Equal(t0) {
		t.Errorf("heartbeat %v %v", hb, err)
	}
	sd := e.d.Cfg.Env.StateHome
	if _, err := os.Stat(filepath.Join(sd, "pg-review", "export.jsonl")); err != nil {
		t.Errorf("export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sd, "pg-review", "backups", "2026-03-10.db")); err != nil {
		t.Errorf("backup: %v", err)
	}
}

func TestMaintainFailingStepDoesNotStopOthers(t *testing.T) {
	e := setup(t)
	// A regular file where the backups directory belongs fails the backup step.
	if err := os.MkdirAll(filepath.Join(e.d.Cfg.Env.StateHome, "pg-review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.d.Cfg.Env.StateHome, "pg-review", "backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Maintain(bg, e.d, MaintainOptions{}) // no filer: outbox fails too
	failed := map[string]bool{}
	for _, s := range res.Failed() {
		failed[s.Name] = true
	}
	if !failed["backup"] || !failed["outbox"] || len(failed) != 2 {
		t.Errorf("failed steps: %v", failed)
	}
	if _, err := e.l.Heartbeat(bg); err != nil {
		t.Errorf("heartbeat not written after failures: %v", err)
	}
	if s := stepOf(res, "reaper"); s == nil || s.Err != nil {
		t.Errorf("later step did not run: %+v", s)
	}
}

func TestMaintainBackupOncePerUTCDay(t *testing.T) {
	e := setup(t)
	Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	res := Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	if s := stepOf(res, "backup"); s.Err != nil || s.Detail != "already written today" {
		t.Errorf("%+v", s)
	}
	*e.now = e.now.Add(24 * time.Hour)
	Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	if _, err := os.Stat(filepath.Join(e.d.Cfg.Env.StateHome, "pg-review", "backups", "2026-03-11.db")); err != nil {
		t.Errorf("next day backup: %v", err)
	}
}

func TestMaintainPromotesWaitingAfterAutoDefer(t *testing.T) {
	e := setup(t)
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		_, err := tx.CreateTopic(bg, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "w", Kind: "stale", Lens: "agent-workflow", Waiting: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	var state string
	if err := e.l.DB().QueryRow(`SELECT state FROM topic`).Scan(&state); err != nil || state != "Pending" {
		t.Errorf("state %q %v", state, err)
	}
}

func TestMaintainReapsDeadRun(t *testing.T) {
	e := setup(t)
	var run ledger.Run
	if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
		var err error
		run, err = tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "demo", PID: 999, StartedAt: t0.Add(-time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	got, _ := e.l.GetRun(bg, run.ID)
	if got.State != ledger.RunFailed {
		t.Errorf("state %s", got.State)
	}
}

func TestEvidenceTTLSweep(t *testing.T) {
	e := setup(t)
	root := filepath.Join(e.d.Cfg.Env.CacheHome, "pg-review", "evidence")
	mk := func(name string, ended time.Time) ledger.Run {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rows.jsonl"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		var r ledger.Run
		if err := e.l.WithTx(bg, func(tx *ledger.Tx) error {
			var err error
			r, err = tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: name, State: ledger.RunFiled, EvidenceDir: dir, StagingPath: filepath.Join(dir, "raw.txt"), EndedAt: ended})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return r
	}
	ttl := e.d.Cfg.Limits.EvidenceTTLDays
	old := mk("old", t0.AddDate(0, 0, -ttl-1))
	fresh := mk("fresh", t0.AddDate(0, 0, -ttl+1))
	// A path outside the evidence root is never deleted.
	outside := t.TempDir()
	var out ledger.Run
	_ = e.l.WithTx(bg, func(tx *ledger.Tx) error {
		var err error
		out, err = tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "out", State: ledger.RunFiled, EvidenceDir: outside, EndedAt: t0.AddDate(0, 0, -ttl-5)})
		return err
	})
	res := Maintain(bg, e.d, MaintainOptions{Filer: okFiler{}})
	if s := stepOf(res, "evidence-ttl"); s.Err != nil {
		t.Fatal(s.Err)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Error("old evidence kept")
	}
	if _, err := os.Stat(filepath.Join(root, "fresh", "rows.jsonl")); err != nil {
		t.Error("fresh evidence deleted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("path outside the evidence root deleted")
	}
	if r, _ := e.l.GetRun(bg, old.ID); r.EvidenceDir != "" {
		t.Errorf("old run keeps pointer %q", r.EvidenceDir)
	}
	if r, _ := e.l.GetRun(bg, fresh.ID); r.EvidenceDir == "" {
		t.Error("fresh run pointer cleared")
	}
	if r, _ := e.l.GetRun(bg, out.ID); r.EvidenceDir != outside {
		t.Error("outside run pointer changed")
	}
}
