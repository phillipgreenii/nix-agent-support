package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// hermeticQuiet is hermetic with the seeded run ended, so the reaper has no
// run to fail and no staging file to delete from the checked-in testdata.
func hermeticQuiet(t *testing.T) *ledger.Ledger {
	t.Helper()
	_, l := hermetic(t)
	if _, err := l.DB().Exec(`UPDATE run SET state='Filed', ended_at=created_at`); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestMaintainRetriesPendingBeadAndWritesHeartbeat(t *testing.T) {
	l := hermeticQuiet(t)
	writeConfig(t, "schema_version = 1\n[tracker]\ndefault_label = \"x\"\n")
	useFiler(t, &fakeFiler{fail: true})
	ids := seedFindings(t, l, syn("a", "Should a?", "E-a"))
	id := fmt.Sprint(ids[0])
	run(t, "human", "show", id)
	run(t, "human", "rule", id, "adopted") // tracker down: stays pending
	good := &fakeFiler{}
	useFiler(t, good)
	code, out, errs := run(t, "job", "maintain")
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	if len(good.reqs) != 1 {
		t.Fatalf("beads filed: %d", len(good.reqs))
	}
	if _, err := l.Heartbeat(context.Background()); err != nil {
		t.Errorf("heartbeat: %v", err)
	}
}

func TestMaintainFailingStepExitsOneAndReports(t *testing.T) {
	hermeticQuiet(t)
	useFiler(t, &fakeFiler{})
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pg-review")
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(t, "job", "maintain")
	if code != exitcode.Failure || !strings.Contains(errs, "maintain step backup failed") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
	if !strings.Contains(out, "heartbeat: ok") {
		t.Errorf("later steps did not run: %q", out)
	}
}

func TestNoJobExportOrOutboxDrainCommand(t *testing.T) {
	hermetic(t)
	for _, c := range []string{"export", "drain", "outbox"} {
		if code, _, _ := run(t, "job", c); code != exitcode.Usage {
			t.Errorf("job %s: exit %d", c, code)
		}
	}
}

func TestStatusJSONShapeAndShort(t *testing.T) {
	_, l := hermetic(t)
	writeConfig(t, "[[scope]]\nname=\"w\"\nprefixes=[\"/a\"]\n")
	code, out, errs := run(t, "job", "status", "--json")
	if code != exitcode.OK {
		t.Fatalf("exit %d %s", code, errs)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	for _, k := range []string{"last_runs", "pending", "waiting", "outbox_backlog", "outbox_failed", "paused_lenses", "stale_lenses", "scopes", "skipped_runs_24h", "maintain_stale", "maintain"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s in %s", k, out)
		}
	}
	if m["maintain"] != "never ran" || m["maintain_stale"] != true {
		t.Errorf("heartbeat: %v", m)
	}
	if sc, _ := m["scopes"].([]any); len(sc) != 1 || sc[0] != "w" {
		t.Errorf("scopes %v", m["scopes"])
	}
	// heartbeat stale after twice the period, fresh within it
	old := l.Now().Add(-11 * time.Minute)
	_ = l.WithTx(context.Background(), func(tx *ledger.Tx) error { return tx.SetHeartbeat(context.Background(), old) })
	_, out, _ = run(t, "job", "status", "--short")
	if !strings.Contains(out, "maintain: stale, last ran 11m ago") {
		t.Errorf("short: %q", out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("short form is not one line: %q", out)
	}
	fresh := l.Now().Add(-9 * time.Minute)
	_ = l.WithTx(context.Background(), func(tx *ledger.Tx) error { return tx.SetHeartbeat(context.Background(), fresh) })
	_, out, _ = run(t, "job", "status", "--short")
	if strings.Contains(out, "maintain:") {
		t.Errorf("fresh heartbeat shown stale: %q", out)
	}
	_, out, _ = run(t, "job", "status")
	if !strings.Contains(out, "Paused lenses: none") || !strings.Contains(out, "Scopes: w") {
		t.Errorf("text: %q", out)
	}
}

func TestStatusShowsStaleLensAndSkipped(t *testing.T) {
	_, l := hermetic(t)
	bg := context.Background()
	now := l.Now()
	if err := l.WithTx(bg, func(tx *ledger.Tx) error {
		if _, err := tx.InsertRun(bg, ledger.Run{
			Lens: "agent-workflow", Scope: "w", State: ledger.RunFiled, ScheduleID: "daily",
			WindowFrom: now.Add(-72 * time.Hour).UTC().Format(time.RFC3339), WindowTo: now.Add(-48 * time.Hour).UTC().Format(time.RFC3339), EndedAt: now.Add(-48 * time.Hour),
		}); err != nil {
			return err
		}
		_, err := tx.InsertRun(bg, ledger.Run{Lens: "config-health", Scope: "w", State: ledger.RunSkipped, SkipReason: "cap"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, out, _ := run(t, "job", "status", "--json")
	var s Status
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.StaleLenses) != 1 || s.StaleLenses[0] != "agent-workflow" || s.SkippedRuns24h != 1 || len(s.LastRuns) != 3 {
		t.Errorf("%+v", s)
	}
}

func TestLensPausesAfterThreeAllRejectedRuns(t *testing.T) {
	_, l := hermetic(t)
	bg := context.Background()
	for i := 0; i < 3; i++ {
		var tp ledger.Topic
		if err := l.WithTx(bg, func(tx *ledger.Tx) error {
			r, err := tx.InsertRun(bg, ledger.Run{Lens: "agent-workflow", Scope: "w", State: ledger.RunFiled, PID: 1, StartedAt: time.Now()})
			if err != nil {
				return err
			}
			if tp, err = tx.CreateTopic(bg, ledger.NewTopic{DefinedIn: "a.md", SubjectKey: "p" + strconv.Itoa(i), Kind: "stale", Lens: "agent-workflow"}); err != nil {
				return err
			}
			return tx.AddOccurrence(bg, tp.ID, r.ID, "w", "agent-workflow", "", "")
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Rule(bg, ledger.Ruling{TopicID: tp.ID, Ruling: ledger.RulingRejected}); err != nil {
			t.Fatal(err)
		}
	}
	_, out, _ := run(t, "job", "status", "--json")
	var s Status
	_ = json.Unmarshal([]byte(out), &s)
	if len(s.PausedLenses) != 1 || s.PausedLenses[0] != "agent-workflow" {
		t.Fatalf("paused: %+v", s)
	}
	if code, _, errs := run(t, "job", "resume", "agent-workflow"); code != exitcode.OK {
		t.Fatalf("resume exit %d %s", code, errs)
	}
	_, out, _ = run(t, "job", "status", "--json")
	s = Status{}
	_ = json.Unmarshal([]byte(out), &s)
	if len(s.PausedLenses) != 0 {
		t.Errorf("still paused: %+v", s)
	}
}

func TestResumeUnknownLensExitsFour(t *testing.T) {
	hermetic(t)
	if code, _, errs := run(t, "job", "resume", "no-such-lens"); code != exitcode.ValidationRejected || !strings.Contains(errs, "unknown lens") {
		t.Errorf("exit %d %q", code, errs)
	}
}
