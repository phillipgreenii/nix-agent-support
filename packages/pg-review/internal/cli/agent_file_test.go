package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

const goldenDir = "../../testdata/review-item"

// hermetic points the tool at throwaway XDG directories and returns a run
// over the synthetic golden evidence.
func hermetic(t *testing.T) (runID int64, l *ledger.Ledger) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv(RunEnv, "")
	l, err := ledger.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	abs, _ := filepath.Abs(goldenDir)
	var id int64
	err = l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(context.Background(), ledger.Run{
			Lens: "agent-workflow", Scope: "demo-scope", PID: 1,
			StartedAt: time.Now(), EvidenceDir: abs, StagingPath: filepath.Join(abs, "raw.txt"),
		})
		id = r.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, l
}

func count(t *testing.T, l *ledger.Ledger, table string) int {
	t.Helper()
	var n int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func findingsPath() string { return filepath.Join(goldenDir, "findings.json") }

func TestAgentFileDryRunStoresNothing(t *testing.T) {
	id, l := hermetic(t)
	code, out, stderr := run(t, "agent", "file", findingsPath(), "--dry-run", "--run", strconv.FormatInt(id, 10))
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	for _, tb := range []string{"topic", "occurrence", "revision", "subject"} {
		if n := count(t, l, tb); n != 0 {
			t.Errorf("%s has %d rows after --dry-run", tb, n)
		}
	}
	if r, _ := l.GetRun(context.Background(), id); r.State != ledger.RunRunning {
		t.Errorf("run state %s", r.State)
	}
}

func TestAgentFileFilesAndReplays(t *testing.T) {
	id, l := hermetic(t)
	rid := strconv.FormatInt(id, 10)
	code, out, stderr := run(t, "agent", "file", findingsPath(), "--run", rid)
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	var res ledger.FileResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Filed != 1 {
		t.Fatalf("%q %v", out, err)
	}
	if count(t, l, "topic") != 1 {
		t.Fatal("no topic")
	}
	// The same batch again: exit 0, nothing new.
	if code, _, stderr := run(t, "agent", "file", findingsPath(), "--run", rid); code != exitcode.OK {
		t.Fatalf("replay exit %d: %s", code, stderr)
	}
	if count(t, l, "occurrence") != 1 {
		t.Error("replay stored a second occurrence")
	}
	// A different batch for the same run: exit 4.
	b, _ := os.ReadFile(findingsPath())
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["unverified"] = []string{"something else"}
	alt, _ := json.Marshal(doc)
	p := filepath.Join(t.TempDir(), "alt.json")
	_ = os.WriteFile(p, alt, 0o600)
	if code, _, _ := run(t, "agent", "file", p, "--run", rid); code != exitcode.ValidationRejected {
		t.Errorf("different second batch: exit %d, want 4", code)
	}
}

func TestAgentFileRejectsAndFailsRunWithoutStoring(t *testing.T) {
	id, l := hermetic(t)
	b, _ := os.ReadFile(findingsPath())
	bad := strings.Replace(string(b), `"wasted-retries"`, `"no-such-kind"`, 1)
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte(bad), 0o600)
	code, _, stderr := run(t, "agent", "file", p, "--run", strconv.FormatInt(id, 10))
	if code != exitcode.ValidationRejected || !strings.Contains(stderr, "unknown-kind") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if count(t, l, "topic") != 0 {
		t.Error("an invalid batch stored a topic")
	}
	r, _ := l.GetRun(context.Background(), id)
	if r.State != ledger.RunFailed || r.RepairCount != 1 {
		t.Errorf("run %+v", r)
	}
	// The repaired batch files on the same run.
	if code, _, stderr := run(t, "agent", "file", findingsPath(), "--run", strconv.FormatInt(id, 10)); code != exitcode.OK {
		t.Fatalf("repair exit %d: %s", code, stderr)
	}
}

func TestAgentFileRunResolution(t *testing.T) {
	id, _ := hermetic(t)
	if code, _, _ := run(t, "agent", "file", findingsPath()); code != exitcode.Usage {
		t.Errorf("no run: exit %d, want 2", code)
	}
	t.Setenv(RunEnv, strconv.FormatInt(id, 10))
	if code, _, stderr := run(t, "agent", "file", findingsPath(), "--dry-run"); code != exitcode.OK {
		t.Errorf("env run: exit %d %s", code, stderr)
	}
	if code, _, _ := run(t, "agent", "file", findingsPath(), "--run", "9999"); code != exitcode.ValidationRejected {
		t.Errorf("unknown run: exit %d, want 4", code)
	}
}

func TestAgentFileReadsStdin(t *testing.T) {
	id, _ := hermetic(t)
	b, _ := os.ReadFile(findingsPath())
	var out, errb bytes.Buffer
	root := NewRoot("test")
	root.SetArgs([]string{"agent", "file", "-", "--dry-run", "--run", strconv.FormatInt(id, 10)})
	root.SetIn(bytes.NewReader(b))
	root.SetOut(&out)
	root.SetErr(&errb)
	if err := root.Execute(); err != nil {
		t.Fatal(err, errb.String())
	}
}

func TestAgentFileRefusesOnIntegrityFailure(t *testing.T) {
	id, l := hermetic(t)
	path := l.Path()
	_ = l.Close()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := bytes.Repeat([]byte{0xAB}, 4096)
	if _, err := f.WriteAt(junk, 4096); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	code, _, _ := run(t, "agent", "file", findingsPath(), "--run", strconv.FormatInt(id, 10))
	if code != exitcode.Ledger && code != exitcode.Failure {
		t.Fatalf("exit %d", code)
	}
}

func TestAgentContextPrintsTopicsAndPaths(t *testing.T) {
	id, _ := hermetic(t)
	rid := strconv.FormatInt(id, 10)
	if code, _, stderr := run(t, "agent", "file", findingsPath(), "--run", rid); code != exitcode.OK {
		t.Fatalf("file: %d %s", code, stderr)
	}
	code, out, stderr := run(t, "agent", "context", "--run", rid)
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var c Context
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err, out)
	}
	if c.Lens != "agent-workflow" || len(c.Topics) != 1 || c.Topics[0].State != "Pending" {
		t.Errorf("%+v", c)
	}
	for _, p := range []string{c.EvidencePath, c.QueryLogPath, c.QueryResultsDir, c.StagingPath} {
		if p == "" || !filepath.IsAbs(p) {
			t.Errorf("path %q", p)
		}
	}
	if !strings.HasSuffix(c.EvidencePath, "evidence.json") || !strings.HasSuffix(c.QueryLogPath, "query-log.jsonl") {
		t.Errorf("%+v", c)
	}
}

// interactiveRun seeds an AwaitingAnalyst interactive run holding its lock,
// with a staging directory holding the synthetic raw text.
func interactiveRun(t *testing.T) (id int64, l *ledger.Ledger, staging string) {
	t.Helper()
	_, l = hermetic(t)
	raw, err := os.ReadFile(filepath.Join(goldenDir, "raw.txt"))
	if err != nil {
		t.Fatal(err)
	}
	staging = filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "raw.txt"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(goldenDir)
	err = l.WithTx(context.Background(), func(tx *ledger.Tx) error {
		r, err := tx.InsertRun(context.Background(), ledger.Run{
			Lens: "agent-workflow", Scope: "other-scope", State: ledger.RunAwaitingAnalyst, Analyst: "interactive",
			EvidenceDir: abs, StagingPath: staging,
		})
		if err != nil {
			return err
		}
		id = r.ID
		return tx.AcquireLock(context.Background(), r.Lens, r.Scope, r.ID, 0, time.Time{})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, l, staging
}

func TestAgentFileInteractiveRunFilesAndDeletesStaging(t *testing.T) {
	id, l, staging := interactiveRun(t)
	code, out, stderr := run(t, "agent", "file", findingsPath(), "--run", strconv.FormatInt(id, 10))
	if code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	r, err := l.GetRun(context.Background(), id)
	if err != nil || r.State != ledger.RunFiled || r.StagingPath != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Error("staging kept")
	}
	if _, err := l.GetLock(context.Background(), "agent-workflow", "other-scope"); err == nil {
		t.Error("lock kept")
	}
	// An identical repeat is idempotent.
	if code, out, stderr := run(t, "agent", "file", findingsPath(), "--run", strconv.FormatInt(id, 10)); code != exitcode.OK {
		t.Errorf("repeat: exit %d: %s %s", code, out, stderr)
	}
}

func TestAgentFileInteractiveRunNeedsRunFlag(t *testing.T) {
	interactiveRun(t)
	code, _, stderr := run(t, "agent", "file", findingsPath())
	if code != exitcode.Usage || !strings.Contains(stderr, "--run") {
		t.Errorf("exit %d: %s", code, stderr)
	}
}

func TestAgentFileDryRunKeepsInteractiveStaging(t *testing.T) {
	id, l, staging := interactiveRun(t)
	if code, out, stderr := run(t, "agent", "file", findingsPath(), "--dry-run", "--run", strconv.FormatInt(id, 10)); code != exitcode.OK {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Error("staging deleted by a dry run")
	}
	if r, _ := l.GetRun(context.Background(), id); r.State != ledger.RunAwaitingAnalyst {
		t.Errorf("state %s", r.State)
	}
}
