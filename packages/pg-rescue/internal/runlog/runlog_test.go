package runlog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

func ptr[T any](v T) *T { return &v }

var started = time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)

func baseInput(res *runner.Result) Input {
	return Input{
		Result:    res,
		Options:   &cli.Options{Argv: []string{"git", "pull", "--rebase"}, Context: "sync-projects: rebase repo onto origin"},
		Chain:     "sync",
		Handlers:  []string{"a", "b"},
		Cwd:       "/abs/repo",
		RunID:     "20261002T140311Z-7f3a9c2e",
		Host:      "host1",
		Version:   "0.1.0",
		StartedAt: started,
	}
}

// decode round-trips an entry through its JSON line.
func decode(t *testing.T, e Entry) map[string]any {
	t.Helper()
	line, err := e.Line()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("a run log entry is exactly one line: %q", line)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, line)
	}
	return m
}

func TestSuccessLine(t *testing.T) {
	res := &runner.Result{Kind: runner.KindSuccess, CommandExit: 0, Depth: 1, Fingerprint: "sha256:x", Duration: 1500 * time.Millisecond}
	m := decode(t, Build(baseInput(res)))
	want := map[string]any{
		"run_id": "20261002T140311Z-7f3a9c2e", "parent_run_id": nil, "depth": float64(1),
		"ts": "2026-10-02T14:03:11Z", "pg_rescue_version": "0.1.0", "host": "host1", "mode": "argv",
		"chain": "sync", "cmd": "git pull --rebase", "cwd": "/abs/repo",
		"context": "sync-projects: rebase repo onto origin", "fingerprint": "sha256:x",
		"exit": float64(0), "result": "success", "resolved_by": nil, "deferred_by": nil,
		"interrupted_during": nil, "signal": nil, "final_exit": float64(0), "duration_ms": float64(1500),
	}
	for k, v := range want {
		if got, ok := m[k]; !ok || got != v {
			t.Errorf("%s = %#v (present %v); want %#v", k, got, ok, v)
		}
	}
	if a, ok := m["attempts"].([]any); !ok || len(a) != 0 {
		t.Errorf("attempts = %#v; want []", m["attempts"])
	}
	if hs, _ := m["handlers"].([]any); len(hs) != 2 {
		t.Errorf("handlers = %#v", m["handlers"])
	}
}

func attemptsReport() (*report.Report, []string) {
	return &report.Report{Attempts: []report.Attempt{
		{Handler: "a", Position: 1, Tags: []string{"deterministic"}, Outcome: contract.Declined, Reason: "exit 2", Exit: 2, DurationMS: 310},
		{
			Handler: "b", Position: 2, Tags: []string{"agent"}, Outcome: contract.Failed, Reason: "resolved → verify failed (exit 128)",
			Exit: 0, DurationMS: 41200, VerifyMS: ptr(int64(900)),
			Reported: contract.Reported{Meta: json.RawMessage(`{ "session_id": "s", "total_cost_usd": 0.04 }`)},
		},
	}}, []string{"/nix/store/x/bin/a", "/nix/store/x/bin/b"}
}

func TestEveryResultValue(t *testing.T) {
	rep, bins := attemptsReport()
	for _, c := range []struct {
		name string
		res  runner.Result
		want map[string]any
	}{
		{"success", runner.Result{Kind: runner.KindSuccess}, map[string]any{"result": "success", "final_exit": float64(0)}},
		{
			"resolved",
			runner.Result{Kind: runner.KindResolved, ResolvedBy: "b", Report: rep, Binaries: bins, CommandExit: 1},
			map[string]any{"result": "resolved", "resolved_by": "b", "exit": float64(1)},
		},
		{
			"deferred",
			runner.Result{Kind: runner.KindDeferred, DeferredBy: "b", ExitCode: 75, Report: rep, Binaries: bins, CommandExit: 1},
			map[string]any{"result": "deferred", "deferred_by": "b", "final_exit": float64(75)},
		},
		{
			"unhandled",
			runner.Result{Kind: runner.KindUnhandled, ExitCode: 1, Report: rep, Binaries: bins, CommandExit: 1},
			map[string]any{"result": "unhandled", "final_exit": float64(1)},
		},
		{
			"error",
			runner.Result{Kind: runner.KindError, ExitCode: 70, CommandExit: -1},
			map[string]any{"result": "error", "final_exit": float64(70), "exit": nil},
		},
		{
			"interrupted in a handler",
			runner.Result{
				Kind: runner.KindInterrupted, ExitCode: 130, Report: rep, Binaries: bins, CommandExit: 1,
				Interrupted: &runner.Interruption{Signal: syscall.SIGINT, Phase: runner.PhaseHandler, Handler: "b", Position: 2},
			},
			map[string]any{"result": "interrupted", "interrupted_during": "b", "signal": "SIGINT", "final_exit": float64(130)},
		},
		{
			"interrupted during the command",
			runner.Result{
				Kind: runner.KindInterrupted, ExitCode: 143, CommandExit: 143,
				Interrupted: &runner.Interruption{Signal: syscall.SIGTERM, Phase: runner.PhaseCommand},
			},
			map[string]any{"result": "interrupted", "interrupted_during": nil, "signal": "SIGTERM", "final_exit": float64(143)},
		},
		{
			"interrupted by SIGHUP",
			runner.Result{
				Kind: runner.KindInterrupted, ExitCode: 129, CommandExit: -1,
				Interrupted: &runner.Interruption{Signal: syscall.SIGHUP, Phase: runner.PhaseBetween},
			},
			map[string]any{"signal": "SIGHUP", "exit": nil},
		},
	} {
		m := decode(t, Build(baseInput(&c.res)))
		for k, v := range c.want {
			if got := m[k]; got != v {
				t.Errorf("%s: %s = %#v; want %#v", c.name, k, got, v)
			}
		}
	}
}

func TestAttemptsCarryTagsBinaryAndMeta(t *testing.T) {
	rep, bins := attemptsReport()
	m := decode(t, Build(baseInput(&runner.Result{Kind: runner.KindUnhandled, Report: rep, Binaries: bins, CommandExit: 1})))
	atts := m["attempts"].([]any)
	if len(atts) != 2 {
		t.Fatalf("attempts = %v", atts)
	}
	a1 := atts[0].(map[string]any)
	if a1["handler"] != "a" || a1["position"] != float64(1) || a1["outcome"] != "declined" || a1["reason"] != "exit 2" ||
		a1["exit"] != float64(2) || a1["duration_ms"] != float64(310) || a1["binary"] != "/nix/store/x/bin/a" || a1["meta"] != nil {
		t.Errorf("attempt 1 = %v", a1)
	}
	if _, has := a1["verify_ms"]; has {
		t.Error("verify_ms is omitted when verify did not run")
	}
	if tags := a1["tags"].([]any); len(tags) != 1 || tags[0] != "deterministic" {
		t.Errorf("tags = %v", tags)
	}
	a2 := atts[1].(map[string]any)
	if a2["verify_ms"] != float64(900) {
		t.Errorf("verify_ms = %v", a2["verify_ms"])
	}
	meta, _ := a2["meta"].(map[string]any)
	if meta["session_id"] != "s" || meta["total_cost_usd"] != 0.04 {
		t.Errorf("meta = %v", a2["meta"])
	}
}

func TestAttemptWithoutAResolvedBinaryStillHasTheField(t *testing.T) {
	rep := &report.Report{Attempts: []report.Attempt{{Handler: "a", Position: 1, Outcome: contract.Failed, Reason: "not found on PATH", Exit: -1}}}
	m := decode(t, Build(baseInput(&runner.Result{Kind: runner.KindUnhandled, Report: rep, Binaries: []string{""}})))
	a := m["attempts"].([]any)[0].(map[string]any)
	if b, ok := a["binary"]; !ok || b != "" {
		t.Errorf("binary = %#v (present %v)", b, ok)
	}
	// A report with more attempts than recorded binaries must not panic.
	Build(baseInput(&runner.Result{Kind: runner.KindUnhandled, Report: rep}))
}

func TestStdinModeAndHandlersSelector(t *testing.T) {
	in := baseInput(&runner.Result{Kind: runner.KindUnhandled, ExitCode: 1, CommandExit: -1})
	in.Chain = ""
	in.Options = &cli.Options{Stdin: true, Context: "ctx"}
	m := decode(t, Build(in))
	if m["mode"] != "stdin" || m["cmd"] != nil || m["exit"] != nil || m["chain"] != nil {
		t.Errorf("stdin line: mode=%v cmd=%v exit=%v chain=%v", m["mode"], m["cmd"], m["exit"], m["chain"])
	}
}

func TestCmdAndContextAreRedacted(t *testing.T) {
	in := baseInput(&runner.Result{Kind: runner.KindSuccess})
	in.Options = &cli.Options{Argv: []string{"curl", "-H", "Authorization: Bearer s3cr3t"}, Context: "token s3cr3t here"}
	in.Redact = runner.RedactWith([]*regexp.Regexp{regexp.MustCompile(`s3cr3t`)})
	line, _ := Build(in).Line()
	if bytes.Contains(line, []byte("s3cr3t")) {
		t.Errorf("a secret reached the run log: %s", line)
	}
	m := decode(t, Build(in))
	if m["cmd"] != `curl -H 'Authorization: Bearer [REDACTED]'` || m["context"] != "token [REDACTED] here" {
		t.Errorf("cmd=%v context=%v", m["cmd"], m["context"])
	}
}

func TestLineDoesNotEscapeHTMLCharacters(t *testing.T) {
	in := baseInput(&runner.Result{Kind: runner.KindSuccess})
	in.Options = &cli.Options{Argv: []string{"sh", "-c", "a && b < c > d"}}
	line, _ := Build(in).Line()
	if !bytes.Contains(line, []byte("a && b < c > d")) {
		t.Errorf("html characters were escaped: %s", line)
	}
}

func TestNestedRunCarriesParentAndDepth(t *testing.T) {
	m := decode(t, Build(baseInput(&runner.Result{Kind: runner.KindSuccess, Depth: 3, ParentRunID: ptr("20261002T140000Z-00000000")})))
	if m["parent_run_id"] != "20261002T140000Z-00000000" || m["depth"] != float64(3) {
		t.Errorf("parent=%v depth=%v", m["parent_run_id"], m["depth"])
	}
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for i, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %d is not parseable JSON: %v\n%.200s", i+1, err, l)
		}
		out = append(out, m)
	}
	return out
}

func TestAppendCreatesTheFilePrivately(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	root := filepath.Join(t.TempDir(), "state", "pg-rescue")
	if err := Append(root, []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("runs.jsonl: %v %v; want mode 0600", fi, err)
	}
	if fi, _ := os.Stat(root); fi.Mode().Perm() != 0o700 {
		t.Errorf("state root mode = %04o; want 0700", fi.Mode().Perm())
	}
}

func TestConcurrentAppendsNeverInterleave(t *testing.T) {
	root := t.TempDir()
	const writers, each = 20, 10
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				// 40 KiB lines (8 MiB in all, under the rotation size): far past PIPE_BUF, so only a single O_APPEND
				// write keeps them whole.
				pad := strings.Repeat("x", 40<<10)
				line, _ := json.Marshal(map[string]any{"w": w, "i": i, "pad": pad})
				if err := Append(root, append(line, '\n')); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	lines := readLines(t, filepath.Join(root, FileName))
	if len(lines) != writers*each {
		t.Fatalf("lines = %d; want %d", len(lines), writers*each)
	}
	seen := map[[2]float64]bool{}
	for _, m := range lines {
		seen[[2]float64{m["w"].(float64), m["i"].(float64)}] = true
	}
	if len(seen) != writers*each {
		t.Errorf("distinct lines = %d; want %d", len(seen), writers*each)
	}
}

func TestRotationAtTheSizeLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	big := append(bytes.Repeat([]byte("o"), MaxBytes), '\n')
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, []byte("{\"new\":1}\n")); err != nil {
		t.Fatal(err)
	}
	rotated, err := os.ReadFile(path + ".1")
	if err != nil || !bytes.Equal(rotated, big) {
		t.Errorf("runs.jsonl.1 should hold the old log (err %v, len %d)", err, len(rotated))
	}
	if cur, _ := os.ReadFile(path); string(cur) != "{\"new\":1}\n" {
		t.Errorf("runs.jsonl = %.40q; want only the new line", cur)
	}
	// A second rotation replaces the previous rotated copy.
	if err := os.WriteFile(path, append(bytes.Repeat([]byte("p"), MaxBytes), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, []byte("{\"newer\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path + ".1"); b[0] != 'p' {
		t.Error("the rotated copy was not replaced")
	}
}

func TestNoRotationAtOrBelowTheLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("o"), MaxBytes), 0o600); err != nil { // exactly 10 MiB
		t.Fatal(err)
	}
	if err := Append(root, []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("a log of exactly 10 MiB must not rotate")
	}
}

func TestRacingRotationsKeepTheFreshLog(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	big := append(bytes.Repeat([]byte("o"), 4096), '\n')
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := AppendMax(root, []byte("{\"i\":"+string(rune('a'+i))+"}\n"), 1024); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// The old log was rotated exactly once: every new line is in the fresh
	// file, none was thrown away by a second rename.
	cur, _ := os.ReadFile(path)
	if n := bytes.Count(cur, []byte("\n")); n != 16 {
		t.Errorf("fresh log has %d lines; want 16", n)
	}
	if b, _ := os.ReadFile(path + ".1"); !bytes.Equal(b, big) {
		t.Error("the rotated copy should be the original log")
	}
}

func TestAppendFailureIsReported(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, FileName), 0o700); err != nil { // a directory squats on the name
		t.Fatal(err)
	}
	if err := Append(root, []byte("x\n")); err == nil {
		t.Error("expected an error")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Append(filepath.Join(blocker, "sub"), []byte("x\n")); err == nil {
		t.Error("expected an error when the state root cannot be created")
	}
}

func TestAppendRefusesASymlinkedLog(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Symlink(target, filepath.Join(root, FileName)); err != nil {
		t.Fatal(err)
	}
	if err := Append(root, []byte("x\n")); err == nil {
		t.Error("Append must not write through a symlink")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("the symlink target was created")
	}
}

func TestWriteResultFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	if err := WriteResultFile(p, []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "{\"a\":1}\n" {
		t.Errorf("content = %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o", fi.Mode().Perm())
	}
	if err := WriteResultFile(filepath.Join(t.TempDir(), "no", "such", "dir", "r.json"), nil); err == nil {
		t.Error("expected an error")
	}
}
