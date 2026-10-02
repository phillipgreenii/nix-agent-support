// runlog_e2e_test.go: the run directory's lifecycle and the run log, checked
// through the real wrapper.
package runner_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/app"
	"github.com/phillipgreenii/pg-rescue/internal/rundir"
)

func (e *e2e) stateRoot() string { return filepath.Join(e.env["XDG_STATE_HOME"], "pg-rescue") }

// runLog reads every line of runs.jsonl, failing the test on an unparseable one.
func (e *e2e) runLog() []map[string]any {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.stateRoot(), "runs.jsonl"))
	if err != nil {
		e.t.Fatalf("no run log: %v", err)
	}
	var rows []map[string]any
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			e.t.Fatalf("run log line %d is not parseable: %v\n%.300s", i+1, err, l)
		}
		rows = append(rows, m)
	}
	return rows
}

// onlyRow is the run log's single line.
func (e *e2e) onlyRow() map[string]any {
	e.t.Helper()
	rows := e.runLog()
	if len(rows) != 1 {
		e.t.Fatalf("run log has %d lines; want 1", len(rows))
	}
	return rows[0]
}

func TestRunLogResultValues(t *testing.T) {
	resolves := result("h", "resolved", "summary=fixed")
	declines := result("h", "declined")
	defers := result("h", "deferred")

	cases := []struct {
		name string
		hs   []hd
		cmd  []string
		opts []string
		want map[string]any
	}{
		{
			"success",
			[]hd{declines},
			helperArgv("exit", "code=0"), nil,
			map[string]any{"result": "success", "exit": 0.0, "final_exit": 0.0, "resolved_by": nil},
		},
		{
			"resolved",
			[]hd{resolves},
			helperArgv("exit", "code=1"),
			[]string{"--verify", "true"},
			map[string]any{"result": "resolved", "exit": 1.0, "final_exit": 0.0, "resolved_by": "h"},
		},
		{
			"deferred",
			[]hd{defers},
			helperArgv("exit", "code=1"), nil,
			map[string]any{"result": "deferred", "exit": 1.0, "final_exit": 75.0, "deferred_by": "h"},
		},
		{
			"unhandled",
			[]hd{declines},
			helperArgv("exit", "code=4"), nil,
			map[string]any{"result": "unhandled", "exit": 4.0, "final_exit": 4.0},
		},
		{
			"error: the command cannot be spawned",
			[]hd{declines},
			[]string{"no-such-command-xyz"},
			nil,
			map[string]any{"result": "error", "exit": nil, "final_exit": 70.0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", c.hs, chainOf("h"))
			e.wrap("h", c.cmd, c.opts...)
			row := e.onlyRow()
			for k, v := range c.want {
				if row[k] != v {
					t.Errorf("%s = %#v; want %#v", k, row[k], v)
				}
			}
		})
	}
}

func TestRunLogInterruptedNamesTheHandler(t *testing.T) {
	for _, phase := range []string{"handler", "verify", "command"} {
		t.Run(phase, func(t *testing.T) {
			started := filepath.Join(t.TempDir(), "started")
			first := hd{name: "first", argv: helperArgv("sleep", "started="+started)}
			cmd := helperArgv("exit", "code=1")
			var opts []string
			switch phase {
			case "verify":
				first = result("first", "resolved")
				opts = []string{"--verify", helperShell("sleep", "started="+started)}
			case "command":
				first = result("first", "declined")
				cmd = helperArgv("sleep", "started="+started)
			}
			e := newE2E(t, "", []hd{first}, chainOf("first"))
			code, _, _ := e.signalDuring(started, syscall.SIGTERM, "first", cmd, opts...)
			row := e.onlyRow()
			wantHandler := any("first")
			if phase == "command" {
				wantHandler = nil
			}
			if code != 143 || row["result"] != "interrupted" || row["interrupted_during"] != wantHandler ||
				row["signal"] != "SIGTERM" || row["final_exit"] != 143.0 {
				t.Errorf("exit=%d row=%v", code, row)
			}
		})
	}
}

func TestRunLogFieldsAndAttempts(t *testing.T) {
	hs := []hd{
		{name: "det", argv: helperArgv("result", "outcome=declined", "summary=nope", `meta={"why":"x"}`), tags: []string{"deterministic"}},
		{name: "ag", argv: helperArgv("result", "outcome=resolved", `meta={"total_cost_usd":0.5}`), tags: []string{"agent"}},
	}
	e := newE2E(t, "", hs, map[string][]string{"c": {"det", "ag"}})
	code, _, _ := e.run("--config", e.cfgPath, "--chain", "c", "-C", e.cwd, "--context", "why", "--verify", "true", "--", "sh", "-c", "exit 1")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	row := e.onlyRow()
	rep := e.result().Report
	for k, want := range map[string]any{
		"run_id": rep.RunID, "parent_run_id": nil, "depth": 1.0, "ts": "2026-10-02T14:03:11Z",
		"pg_rescue_version": "test", "host": "testhost", "mode": "argv", "chain": "c",
		"cmd": "sh -c 'exit 1'", "cwd": e.cwd, "context": "why", "fingerprint": rep.Fingerprint,
		"exit": 1.0, "result": "resolved", "resolved_by": "ag", "final_exit": 0.0,
	} {
		if row[k] != want {
			t.Errorf("%s = %#v; want %#v", k, row[k], want)
		}
	}
	if d, _ := row["duration_ms"].(float64); d < 0 {
		t.Errorf("duration_ms = %v", row["duration_ms"])
	}
	atts := row["attempts"].([]any)
	if len(atts) != 2 {
		t.Fatalf("attempts = %v", atts)
	}
	a1, a2 := atts[0].(map[string]any), atts[1].(map[string]any)
	if a1["handler"] != "det" || a1["outcome"] != "declined" || a1["exit"] != 2.0 || a1["position"] != 1.0 ||
		a1["tags"].([]any)[0] != "deterministic" || a1["meta"].(map[string]any)["why"] != "x" {
		t.Errorf("attempt 1 = %v", a1)
	}
	if !filepath.IsAbs(a1["binary"].(string)) {
		t.Errorf("binary = %v; want the resolved absolute path", a1["binary"])
	}
	if a2["outcome"] != "resolved" || a2["reason"] != "verify passed" || a2["verify_ms"] == nil ||
		a2["meta"].(map[string]any)["total_cost_usd"] != 0.5 {
		t.Errorf("attempt 2 = %v", a2)
	}
}

func TestRunLogHandlersSelectorAndStdinMode(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	e.rt.Stdin = strings.NewReader("boom\n")
	e.wrap("h", nil, "--stdin", "--verify", "true", "--context", "ctx")
	row := e.onlyRow()
	if row["mode"] != "stdin" || row["chain"] != nil || row["cmd"] != nil || row["exit"] != nil ||
		row["result"] != "unhandled" || row["final_exit"] != 1.0 || row["context"] != "ctx" {
		t.Errorf("stdin row = %v", row)
	}
	if hs := row["handlers"].([]any); len(hs) != 1 || hs[0] != "h" {
		t.Errorf("handlers = %v", hs)
	}
}

func TestRunLogRedactsCmdAndContext(t *testing.T) {
	e := newE2E(t, "redact = ['s3cr3t']", []hd{result("h", "declined")}, chainOf("h"))
	e.wrap("h", helperArgv("exit", "code=0", "out=s3cr3t"), "--context", "token s3cr3t")
	raw, _ := os.ReadFile(filepath.Join(e.stateRoot(), "runs.jsonl"))
	if strings.Contains(string(raw), "s3cr3t") {
		t.Errorf("a secret reached the run log:\n%s", raw)
	}
	row := e.onlyRow()
	if row["context"] != "token [REDACTED]" || !strings.Contains(row["cmd"].(string), "out=[REDACTED]") {
		t.Errorf("cmd=%v context=%v", row["cmd"], row["context"])
	}
}

func TestRunLogNestedRunNamesItsParent(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	e.env["PG_RESCUE_RUN_ID"] = "20261002T140000Z-0a0b0c0d"
	e.env["PG_RESCUE_DEPTH"] = "2"
	e.wrap("h", helperArgv("exit", "code=0"))
	row := e.onlyRow()
	if row["parent_run_id"] != "20261002T140000Z-0a0b0c0d" || row["depth"] != 3.0 {
		t.Errorf("parent=%v depth=%v", row["parent_run_id"], row["depth"])
	}
}

func TestTwentyParallelHappyPathRunsProduceTwentyParseableLines(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt := *e.rt
			rt.Rand = &counterRand{n: byte(i * 7)}
			rt.Executor = &app.ChainExecutor{}
			var out, errb strings.Builder
			// Pad the command line so each log line is large.
			args := append([]string{"--config", e.cfgPath, "--handlers", "h", "--context", strings.Repeat("c", 20000), "--"}, helperArgv("exit", "code=0")...)
			code := app.Main(&rt, args, &out, &errb)
			if code != 0 {
				t.Errorf("run %d: exit %d: %s", i, code, errb.String())
			}
		}()
	}
	wg.Wait()
	rows := e.runLog()
	if len(rows) != n {
		t.Fatalf("lines = %d; want %d", len(rows), n)
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r["run_id"].(string)] = true
		if r["result"] != "success" {
			t.Errorf("row = %v", r)
		}
	}
	if len(ids) != n {
		t.Errorf("distinct run ids = %d; want %d", len(ids), n)
	}
	if dirs := e.runDirs(); len(dirs) != 0 {
		t.Errorf("happy-path run directories were left behind: %v", dirs)
	}
}

func TestAFailedRunLogWriteNeverChangesTheExitCode(t *testing.T) {
	for _, c := range []struct {
		name string
		cmd  []string
		opts []string
		want int
	}{
		{"happy path", helperArgv("exit", "code=0"), nil, 0},
		{"unhandled", helperArgv("exit", "code=3"), nil, 3},
		{"resolved", helperArgv("exit", "code=3"), []string{"--verify", "true"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", []hd{result("h", "resolved")}, chainOf("h"))
			if c.name == "unhandled" {
				e = newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
			}
			// A directory squatting on the log's name makes every append fail.
			if err := os.MkdirAll(filepath.Join(e.stateRoot(), "runs.jsonl"), 0o700); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := e.wrap("h", c.cmd, c.opts...)
			if code != c.want {
				t.Errorf("exit = %d; want %d", code, c.want)
			}
			if strings.Contains(stderr, "run log") {
				t.Errorf("the default level must not mention a run log failure:\n%s", stderr)
			}
			_, _, stderr = e.wrap("h", c.cmd, append([]string{"-v"}, c.opts...)...)
			if !strings.Contains(stderr, "pg-rescue: warning: cannot write the run log: ") {
				t.Errorf("-v must warn about the run log:\n%s", stderr)
			}
			_, _, stderr = e.wrap("h", c.cmd, append([]string{"-q"}, c.opts...)...)
			if strings.Contains(stderr, "run log") {
				t.Errorf("-q must stay silent:\n%s", stderr)
			}
		})
	}
}

func TestResultFile(t *testing.T) {
	t.Run("holds the run log line, and tells reserved exit codes apart", func(t *testing.T) {
		// The command's own 75 and a deferral's 75 are the same exit code; the
		// result file says which it was.
		e := newE2E(t, "", []hd{result("d", "deferred"), result("n", "declined")}, map[string][]string{"def": {"d"}, "dec": {"n"}})
		f1 := filepath.Join(e.root, "own75.json")
		f2 := filepath.Join(e.root, "deferred75.json")
		c1, _, _ := e.run(append([]string{"--config", e.cfgPath, "--chain", "dec", "-C", e.cwd, "--result-file", f1, "--"}, helperArgv("exit", "code=75")...)...)
		c2, _, _ := e.run(append([]string{"--config", e.cfgPath, "--chain", "def", "-C", e.cwd, "--result-file", f2, "--"}, helperArgv("exit", "code=1")...)...)
		if c1 != 75 || c2 != 75 {
			t.Fatalf("exits %d %d; want 75 75", c1, c2)
		}
		read := func(p string) map[string]any {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(strings.TrimRight(string(b), "\n"), "\n") != 0 {
				t.Errorf("a result file is one JSON object: %q", b)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
		r1, r2 := read(f1), read(f2)
		if r1["result"] != "unhandled" || r1["final_exit"] != 75.0 || r2["result"] != "deferred" || r2["final_exit"] != 75.0 {
			t.Errorf("own 75: %v / deferred 75: %v", r1, r2)
		}
		// It is the run log line, byte for byte.
		rows := e.runLog()
		if len(rows) != 2 {
			t.Fatalf("run log lines = %d", len(rows))
		}
		for k, v := range r1 {
			if rows[0][k] == nil && v != nil || (v != nil && rows[0][k] != nil && jsonOf(rows[0][k]) != jsonOf(v)) {
				t.Errorf("result file field %s = %v differs from the run log's %v", k, v, rows[0][k])
			}
		}
		if fi, _ := os.Stat(f1); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %04o", fi.Mode().Perm())
		}
	})
	t.Run("is written on the happy path too", func(t *testing.T) {
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		f := filepath.Join(e.root, "ok.json")
		if code, _, _ := e.wrap("h", helperArgv("exit", "code=0"), "--result-file", f); code != 0 {
			t.Fatalf("exit %d", code)
		}
		b, _ := os.ReadFile(f)
		if !strings.Contains(string(b), `"result":"success"`) {
			t.Errorf("result file = %s", b)
		}
	})
	t.Run("a write failure warns and leaves the exit code alone", func(t *testing.T) {
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		bad := filepath.Join(e.root, "missing-dir", "r.json")
		code, _, stderr := e.wrap("h", helperArgv("exit", "code=4"), "--result-file", bad)
		if code != 4 || !strings.Contains(stderr, "pg-rescue: warning: cannot write --result-file "+bad+": ") {
			t.Errorf("exit=%d stderr=%s", code, stderr)
		}
	})
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestRunDirectoryCreationFailureExitsSeventyBeforeTheCommandRuns(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	blocker := filepath.Join(e.root, "state-is-a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e.env["XDG_STATE_HOME"] = blocker
	marker := filepath.Join(e.root, "ran")
	code, stdout, stderr := e.wrap("h", helperArgv("ran", "file="+marker, "code=0"))
	if code != 70 {
		t.Errorf("exit = %d; want 70", code)
	}
	if exists(marker) {
		t.Error("the command ran although the run directory could not be created")
	}
	if stdout != "" || !strings.HasPrefix(stderr, "pg-rescue: cannot create run directory") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestFilesAndDirectoriesArePrivateUnderAPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	e := newE2E(t, "", []hd{result("h", "resolved", "summary=s", "details=d", `meta={"a":1}`, "err=oops\n")}, chainOf("h"))
	code, _, stderr := e.wrap("h", helperArgv("exit", "code=1", "out=o\n"), "-vv", "--verify", "echo v", "--result-file", filepath.Join(e.root, "rf.json"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	root := e.stateRoot()
	var files, dirs int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs++
			if fi.Mode().Perm() != 0o700 {
				t.Errorf("directory %s mode = %04o; want 0700", p, fi.Mode().Perm())
			}
		} else {
			files++
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("file %s mode = %04o; want 0600", p, fi.Mode().Perm())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// state root, runs/, the run dir; runs.jsonl plus the run dir's files.
	for _, name := range []string{"report.json", "output.log", "display.log", ".lock", "attempt-1.stderr.log", "attempt-1.verify.log"} {
		if !exists(filepath.Join(e.rdir(), name)) {
			t.Errorf("run dir lacks %s", name)
		}
	}
	if dirs != 3 || files < 7 {
		t.Errorf("walked %d dirs and %d files", dirs, files)
	}
	if fi, _ := os.Stat(filepath.Join(e.root, "rf.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("result file mode = %04o", fi.Mode().Perm())
	}
}

func TestRunDirectoryLifecycle(t *testing.T) {
	t.Run("removed on the happy path", func(t *testing.T) {
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		e.wrap("h", helperArgv("exit", "code=0"))
		if dirs := e.runDirs(); len(dirs) != 0 {
			t.Errorf("run directories left: %v", dirs)
		}
	})
	t.Run("removed after a wrapper error", func(t *testing.T) {
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		e.wrap("h", []string{"no-such-command-xyz"})
		if dirs := e.runDirs(); len(dirs) != 0 {
			t.Errorf("run directories left: %v", dirs)
		}
	})
	t.Run("kept when a handler ran", func(t *testing.T) {
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		e.wrap("h", helperArgv("exit", "code=1"))
		if dirs := e.runDirs(); len(dirs) != 1 {
			t.Errorf("run directories = %v; want the one this run made", dirs)
		}
	})
	t.Run("kept when the run was interrupted", func(t *testing.T) {
		started := filepath.Join(t.TempDir(), "started")
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		e.signalDuring(started, syscall.SIGINT, "h", helperArgv("sleep", "started="+started))
		if dirs := e.runDirs(); len(dirs) != 1 {
			t.Errorf("run directories = %v; want the one this run made", dirs)
		}
	})
	t.Run("the running wrapper holds a lock on .lock", func(t *testing.T) {
		started := filepath.Join(t.TempDir(), "started")
		e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
		e.exec.Signals = make(chan os.Signal, 1)
		done := make(chan struct{})
		go func() { e.wrap("h", helperArgv("sleep", "started="+started)); close(done) }()
		waitForFile(t, started)
		dirs := e.runDirs()
		if len(dirs) != 1 {
			t.Fatalf("run directories = %v", dirs)
		}
		f, err := os.Open(filepath.Join(e.stateRoot(), "runs", dirs[0], ".lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
			t.Errorf("flock while the wrapper runs = %v; want EWOULDBLOCK", err)
		}
		e.exec.Signals <- syscall.SIGTERM
		<-done
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Errorf("flock after the wrapper ended = %v; want it released", err)
		}
	})
}

func TestEachRunPrunesOldUnlockedRunDirectoriesOnly(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	runs := filepath.Join(e.stateRoot(), "runs")
	mk := func(name string) string {
		d := filepath.Join(runs, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	old := mk("20260901T000000Z-aaaaaaaa")      // 31 days before the fake clock
	recent := mk("20260930T000000Z-bbbbbbbb")   // 2 days
	locked := mk("20260801T000000Z-cccccccc")   // old but held
	misnamed := mk("20260801T000000Z-NOTHEX00") // old but not a run id
	outside := t.TempDir()
	link := filepath.Join(runs, "20260802T000000Z-dddddddd")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	lk, err := rundir.Acquire(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()

	e.wrap("h", helperArgv("exit", "code=0"))
	if exists(old) {
		t.Error("an old, unlocked run directory should have been pruned")
	}
	for name, p := range map[string]string{"recent": recent, "locked": locked, "misnamed": misnamed, "symlink": link, "symlink target": outside} {
		if !exists(p) {
			t.Errorf("%s was deleted", name)
		}
	}
}
