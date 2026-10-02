package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/rundir"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// recorders declares n handlers that record what they see to dir/rec-<pos>
// and decline, so the whole chain runs.
func recorders(dir string, n int) []hd {
	var hs []hd
	for i := 1; i <= n; i++ {
		hs = append(hs, hd{name: "r" + strconv.Itoa(i), argv: helperArgv("record", "dest="+dir+"/rec-{pos}", "code=2")})
	}
	return hs
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEveryPGRescueVariableReachesTheHandler(t *testing.T) {
	dir := t.TempDir()
	hs := recorders(dir, 2)
	e := newE2E(t, "", hs, map[string][]string{"sync": {"r1", "r2"}})
	work := t.TempDir()
	argv := helperArgv("exit", "code=3", "it's", "a b", "$HOME")
	code, _, stderr := e.run(append([]string{"--config", e.cfgPath, "--chain", "sync", "-C", work, "--context", "why: this chore"}, append([]string{"--"}, argv...)...)...)
	if code != 3 {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}
	res := e.result()
	rdir := e.rdir()
	for pos := 1; pos <= 2; pos++ {
		got := readRecord(t, filepath.Join(dir, "rec-"+strconv.Itoa(pos))).Env
		want := map[string]string{
			"PG_RESCUE_REPORT":      filepath.Join(rdir, "report.json"),
			"PG_RESCUE_CMD":         report.QuoteArgv(argv),
			"PG_RESCUE_EXIT":        "3",
			"PG_RESCUE_OUTPUT_FILE": filepath.Join(rdir, "output.log"),
			"PG_RESCUE_CONTEXT":     "why: this chore",
			"PG_RESCUE_FINGERPRINT": report.Fingerprint(report.ModeArgv, argv, work, ""),
			"PG_RESCUE_RUN_ID":      res.Report.RunID,
			"PG_RESCUE_RUN_DIR":     rdir,
			"PG_RESCUE_RUN_LOG":     filepath.Join(e.env["XDG_STATE_HOME"], "pg-rescue", "runs.jsonl"),
			"PG_RESCUE_HANDLER":     "r" + strconv.Itoa(pos),
			"PG_RESCUE_POSITION":    strconv.Itoa(pos),
			"PG_RESCUE_DEPTH":       "1",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("handler %d: %s = %q; want %q", pos, k, got[k], v)
			}
		}
		if _, ok := got["PG_RESCUE_PARENT_RUN_ID"]; ok {
			t.Errorf("handler %d: PG_RESCUE_PARENT_RUN_ID is set for a top-level run", pos)
		}
		if !rundir.IDPattern.MatchString(got["PG_RESCUE_RUN_ID"]) {
			t.Errorf("run id %q", got["PG_RESCUE_RUN_ID"])
		}
		if !filepath.IsAbs(got["PG_RESCUE_REPORT"]) || !filepath.IsAbs(got["PG_RESCUE_OUTPUT_FILE"]) {
			t.Error("paths must be absolute")
		}
	}
}

func TestStdinModeLeavesCmdAndExitEmpty(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	in, _ := os.CreateTemp(e.root, "in")
	in.WriteString("saved failure\n")
	in.Seek(0, 0)
	e.rt.Stdin = in
	if code, _, stderr := e.wrap("r1", nil, "--stdin", "--verify", "true", "--context", "c"); code != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	env := readRecord(t, filepath.Join(dir, "rec-1")).Env
	for _, k := range []string{"PG_RESCUE_CMD", "PG_RESCUE_EXIT"} {
		if v, ok := env[k]; !ok || v != "" {
			t.Errorf("%s = %q (set: %v); want set and empty", k, v, ok)
		}
	}
	if want := report.Fingerprint(report.ModeStdin, nil, e.cwd, "c"); env["PG_RESCUE_FINGERPRINT"] != want {
		t.Errorf("fingerprint = %q; want %q", env["PG_RESCUE_FINGERPRINT"], want)
	}
	rep := e.onDisk()
	if rep.Mode != report.ModeStdin || rep.Command != nil {
		t.Errorf("mode=%s command=%+v", rep.Mode, rep.Command)
	}
	if b, _ := os.ReadFile(rep.OutputFile); string(b) != "saved failure\n" {
		t.Errorf("output.log = %q", b)
	}
	if !strings.Contains(rep.OutputTail, "saved failure") {
		t.Errorf("tail = %q", rep.OutputTail)
	}
}

func TestNestedRunsSeeDepthAndParent(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	e.env["PG_RESCUE_RUN_ID"] = "20260101T000000Z-aaaaaaaa"
	e.env["PG_RESCUE_DEPTH"] = "2"
	e.wrap("r1", helperArgv("exit", "code=1"))
	env := readRecord(t, filepath.Join(dir, "rec-1")).Env
	if env["PG_RESCUE_DEPTH"] != "3" || env["PG_RESCUE_PARENT_RUN_ID"] != "20260101T000000Z-aaaaaaaa" {
		t.Errorf("depth=%q parent=%q", env["PG_RESCUE_DEPTH"], env["PG_RESCUE_PARENT_RUN_ID"])
	}
	rep := e.onDisk()
	if rep.Depth != 3 || rep.ParentRunID == nil || *rep.ParentRunID != "20260101T000000Z-aaaaaaaa" {
		t.Errorf("report depth=%d parent=%v", rep.Depth, rep.ParentRunID)
	}
	if rep.RunID == "20260101T000000Z-aaaaaaaa" || env["PG_RESCUE_RUN_ID"] != rep.RunID {
		t.Errorf("the child's own run id must replace the inherited one: %q", env["PG_RESCUE_RUN_ID"])
	}

	// A stale parent id with no run id is not passed on.
	e2 := newE2E(t, "", recorders(dir+"2", 1), chainOf("r1"))
	e2.env["PG_RESCUE_PARENT_RUN_ID"] = "stale"
	e2.wrap("r1", helperArgv("exit", "code=1"))
	if _, ok := readRecord(t, filepath.Join(dir+"2", "rec-1")).Env["PG_RESCUE_PARENT_RUN_ID"]; ok {
		t.Error("a stale PG_RESCUE_PARENT_RUN_ID leaked into a top-level run")
	}
}

func TestEvalOfPGRescueCmdReproducesTheArgv(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	argv := helperArgv("exit", "code=1", "it's", "a b", "$HOME", ";", "", "x\ny", "`id`", `a"b`)
	e.wrap("r1", argv)
	cmd := readRecord(t, filepath.Join(dir, "rec-1")).Env["PG_RESCUE_CMD"]
	c := exec.Command("sh", "-c", `eval set -- "$PG_RESCUE_CMD"; printf '%s\0' "$@"`)
	c.Env = append(os.Environ(), "PG_RESCUE_CMD="+cmd)
	out, err := c.Output()
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, argv) {
		t.Errorf("eval reproduced %q; want %q", got, argv)
	}
}

func TestArgvRunsWithoutAShell(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined")}, chainOf("h"))
	file := filepath.Join(e.root, "argv.json")
	e.wrap("h", helperArgv("argv", "file="+file, "$HOME", ";", "a b", "", "*", "$(id)", "x;y"))
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := `["$HOME",";","a b","","*","$(id)","x;y"]`; string(b) != want {
		t.Errorf("argv = %s; want %s", b, want)
	}
}

func TestDirFlagAppliesToCommandHandlerAndVerify(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	hs := recorders(dir, 1)
	hs = append(hs, result("fixer", "resolved"))
	e := newE2E(t, "", hs, chainOf("r1", "fixer"))
	cwdFile := filepath.Join(dir, "verify-rec")
	e.wrap("r1,fixer", helperArgv("record", "dest="+filepath.Join(dir, "cmd-rec"), "code=1"), "-C", work,
		"--verify", helperShell("record", "dest="+cwdFile))
	for _, f := range []string{"cmd-rec", "rec-1", "verify-rec"} {
		if got := readRecord(t, filepath.Join(dir, f)).Cwd; realPath(t, got) != realPath(t, work) {
			t.Errorf("%s ran in %q; want %q", f, got, work)
		}
	}
	if c := e.onDisk().Command; c == nil || c.Cwd != work {
		t.Errorf("command.cwd = %+v; want %q", c, work)
	}
}

func TestHandlerCwdStdinAndProcessGroup(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	in, _ := os.CreateTemp(e.root, "in")
	in.WriteString("the wrapper's stdin, which a handler must not see")
	in.Seek(0, 0)
	e.rt.Stdin = in
	e.wrap("r1", helperArgv("exit", "code=1"))
	r := readRecord(t, filepath.Join(dir, "rec-1"))
	if !r.StdinIsDevNull || r.StdinLen != 0 {
		t.Errorf("handler stdin: devnull=%v len=%d", r.StdinIsDevNull, r.StdinLen)
	}
	if r.Pgid != r.Pid {
		t.Errorf("the handler must lead its own process group: pid=%d pgid=%d", r.Pid, r.Pgid)
	}
	if r.Pgid == r.ParentPgid {
		t.Error("the handler shares the wrapper's process group")
	}
}

func TestSameInstanceListedTwice(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	code, _, _ := e.wrap("r1,r1", helperArgv("exit", "code=1"))
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	rep := e.onDisk()
	if len(rep.Attempts) != 2 || rep.Attempts[0].Position != 1 || rep.Attempts[1].Position != 2 ||
		rep.Attempts[0].Handler != "r1" || rep.Attempts[1].Handler != "r1" {
		t.Errorf("attempts = %+v", rep.Attempts)
	}
	if !reflect.DeepEqual(rep.Handlers, []string{"r1", "r1"}) || rep.Chain != nil {
		t.Errorf("handlers=%v chain=%v", rep.Handlers, rep.Chain)
	}
	if rep.Attempts[0].StderrFile == rep.Attempts[1].StderrFile {
		t.Error("each attempt needs its own files")
	}
}

func TestSpawnFailureIsAFailedAttemptNotAWrapperError(t *testing.T) {
	dir := t.TempDir()
	hs := append([]hd{{name: "ghost", argv: []string{"pg-rescue-no-such-handler-xyz"}}, {name: "ghost2", argv: []string{"/no/such/handler"}}}, recorders(dir, 1)...)
	e := newE2E(t, "", hs, chainOf("ghost", "ghost2", "r1"))
	code, _, stderr := e.wrap("ghost,ghost2,r1", helperArgv("exit", "code=4"))
	if code != 4 {
		t.Fatalf("exit = %d; want the command's 4 (stderr %s)", code, stderr)
	}
	atts := e.onDisk().Attempts
	if len(atts) != 3 {
		t.Fatalf("attempts = %+v", atts)
	}
	if atts[0].Outcome != contract.Failed || atts[0].Reason != "not found on PATH" || atts[0].Exit != -1 {
		t.Errorf("attempt 1 = %+v", atts[0])
	}
	if atts[1].Outcome != contract.Failed || !strings.HasPrefix(atts[1].Reason, "not found: ") {
		t.Errorf("attempt 2 = %+v", atts[1])
	}
	if atts[2].Outcome != contract.Declined {
		t.Errorf("the chain must continue after a spawn failure: %+v", atts[2])
	}
	if exists(atts[0].StderrFile) == false {
		t.Error("even a failed spawn gets its stderr file")
	}
}

func TestHandlerKilledBySignalIsFailed(t *testing.T) {
	e := newE2E(t, "", []hd{{name: "k", argv: helperArgv("selfkill", "sig=9")}, result("next", "declined")}, chainOf("k", "next"))
	code, _, _ := e.wrap("k,next", helperArgv("exit", "code=1"))
	atts := e.onDisk().Attempts
	if code != 1 || len(atts) != 2 {
		t.Fatalf("exit=%d attempts=%+v", code, atts)
	}
	if a := atts[0]; a.Outcome != contract.Failed || a.Reason != "killed by signal SIGKILL" || a.Exit != -1 {
		t.Errorf("attempt = %+v", a)
	}
}

func TestHandlerTimeoutIsTermThenKill(t *testing.T) {
	for _, c := range []struct {
		name  string
		argv  []string
		limit time.Duration
	}{
		{"term is enough", helperArgv("sleep", "secs=60", "started={pid}"), 5 * time.Second},
		{"ignores term: killed after the grace", helperArgv("ignore-term", "secs=60", "started={pid}"), 5 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pid")
			argv := make([]string, len(c.argv))
			for i, a := range c.argv {
				argv[i] = strings.ReplaceAll(a, "{pid}", pidFile)
			}
			e := newE2E(t, "", []hd{{name: "slow", argv: argv, timeout: "300ms"}, result("next", "declined")}, chainOf("slow", "next"))
			start := time.Now()
			code, _, _ := e.wrap("slow,next", helperArgv("exit", "code=1"))
			if time.Since(start) > c.limit {
				t.Errorf("took %v", time.Since(start))
			}
			atts := e.onDisk().Attempts
			if code != 1 || len(atts) != 2 {
				t.Fatalf("exit=%d attempts=%+v", code, atts)
			}
			if a := atts[0]; a.Outcome != contract.Failed || a.Reason != "timed out after 300ms" || a.Exit != -1 {
				t.Errorf("attempt = %+v", a)
			}
			pid, _ := strconv.Atoi(waitForFile(t, pidFile))
			if !eventuallyDead(pid) {
				t.Errorf("the timed-out handler %d is still alive", pid)
			}
		})
	}
}

func TestGrandchildrenAreKilledWhenTheHandlerExits(t *testing.T) {
	for _, hold := range []string{"0", "1"} {
		t.Run("hold="+hold, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "gc-pid")
			e := newE2E(t, "", []hd{
				{name: "leaves-child", argv: helperArgv("grandchild", "pid="+pidFile, "hold="+hold, "code=2")},
				result("next", "declined"),
			}, chainOf("leaves-child", "next"))
			done := make(chan int, 1)
			go func() { c, _, _ := e.wrap("leaves-child,next", helperArgv("exit", "code=1")); done <- c }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("a grandchild holding the pipes wedged the wrapper")
			}
			pid, err := strconv.Atoi(waitForFile(t, pidFile))
			if err != nil {
				t.Fatal(err)
			}
			// kill -0 probe: the handler's sleeping child must be gone before
			// the next step.
			if !eventuallyDead(pid) {
				t.Errorf("grandchild %d survived the handler", pid)
			}
			if a := e.onDisk().Attempts; len(a) != 2 || a[0].Outcome != contract.Declined {
				t.Errorf("attempts = %+v", a)
			}
		})
	}
}

func TestGrandchildIsGoneBeforeTheNextHandlerStarts(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "gc-pid")
	probe := filepath.Join(dir, "probe")
	// The second handler checks, as its first act, whether the first
	// handler's grandchild is still alive.
	e := newE2E(t, "", []hd{
		{name: "leaves-child", argv: helperArgv("grandchild", "pid="+pidFile, "code=2")},
		{name: "probe", argv: helperArgv("probe-pid", "pidfile="+pidFile, "dest="+probe, "code=2")},
	}, chainOf("leaves-child", "probe"))
	e.wrap("leaves-child,probe", helperArgv("exit", "code=1"))
	if got := waitForFile(t, probe); got != "dead" {
		t.Errorf("the grandchild was %s when the next handler started", got)
	}
}

func TestResultStdoutSizeBoundaries(t *testing.T) {
	const mib = 1 << 20
	for _, c := range []struct {
		name    string
		size    int
		outcome contract.Outcome
	}{
		{"exactly 1 MiB", mib, contract.Declined},
		{"1 MiB + 1", mib + 1, contract.Failed},
		{"5 MiB, far past the cap", 5 * mib, contract.Failed},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newE2E(t, "", []hd{{name: "big", argv: helperArgv("bigout", "size="+strconv.Itoa(c.size), "code=2"), timeout: "20s"}}, chainOf("big"))
			start := time.Now()
			e.wrap("big", helperArgv("exit", "code=1"))
			if time.Since(start) > 15*time.Second {
				t.Errorf("took %v: the handler blocked on a full pipe", time.Since(start))
			}
			a := e.onDisk().Attempts[0]
			if a.Outcome != c.outcome {
				t.Errorf("outcome = %s (%s); want %s", a.Outcome, a.Reason, c.outcome)
			}
			if c.outcome == contract.Declined && a.Reported.Summary != "big" {
				t.Errorf("the 1 MiB result was not read in full: %+v", a.Reported)
			}
			if c.outcome == contract.Failed && !strings.Contains(a.Reason, "larger than 1048576 bytes") {
				t.Errorf("reason = %q", a.Reason)
			}
		})
	}
}

func TestHandlerResultMapping(t *testing.T) {
	cases := []struct {
		name    string
		h       hd
		outcome contract.Outcome
		reason  string
		summary string
	}{
		{"bare exit 2", hd{argv: helperArgv("exit", "code=2")}, contract.Declined, "exit 2", ""},
		{"bare exit 3", hd{argv: helperArgv("exit", "code=3")}, contract.Deferred, "exit 3", ""},
		{"bare exit 1", hd{argv: helperArgv("exit", "code=1")}, contract.Failed, "exit 1", ""},
		{"json agrees", result("", "declined", "summary=nope"), contract.Declined, "exit 2", "nope"},
		{"json outcome mismatch", hd{argv: helperArgv("result", "outcome=resolved", "code=2")}, contract.Failed, `stdout outcome "resolved" disagrees with exit code 2 (declined)`, ""},
		{"stdout is not json", hd{argv: helperArgv("result", "raw=Successfully rebased", "code=2")}, contract.Failed, "handlers must log to stderr", ""},
		{"json on an exit outside the table keeps the claims", hd{argv: helperArgv("result", "summary=hint", `meta={"a":1}`, "code=5")}, contract.Failed, "exit 5", "hint"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.h.name = "h"
			e := newE2E(t, "", []hd{c.h}, chainOf("h"))
			e.wrap("h", helperArgv("exit", "code=1"))
			a := e.onDisk().Attempts[0]
			if a.Outcome != c.outcome || !strings.Contains(a.Reason, c.reason) || a.Reported.Summary != c.summary {
				t.Errorf("attempt = %+v", a)
			}
		})
	}
}

func TestMetaIsCopiedVerbatim(t *testing.T) {
	e := newE2E(t, "", []hd{result("h", "declined", `meta={"session_id":"abc","total_cost_usd":0.04,"nested":{"k":[1,2]}}`)}, chainOf("h"))
	e.wrap("h", helperArgv("exit", "code=1"))
	got := string(e.result().Report.Attempts[0].Reported.Meta)
	if want := `{"session_id":"abc","total_cost_usd":0.04,"nested":{"k":[1,2]}}`; got != want {
		t.Errorf("meta = %s; want %s", got, want)
	}
}

func TestAttemptsGrowByExactlyOnePerHandler(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 3), chainOf("r1", "r2", "r3"))
	e.wrap("r1,r2,r3", helperArgv("exit", "code=1"))
	for n := 1; n <= 3; n++ {
		r := readRecord(t, filepath.Join(dir, "rec-"+strconv.Itoa(n)))
		var seen report.Report
		if err := jsonUnmarshal(r.Report, &seen); err != nil {
			t.Fatalf("handler %d saw an invalid report: %v\n%s", n, err, r.Report)
		}
		if len(seen.Attempts) != n-1 {
			t.Errorf("handler %d saw %d attempts; want %d", n, len(seen.Attempts), n-1)
		}
		for i, a := range seen.Attempts {
			if a.Position != i+1 || a.Handler != "r"+strconv.Itoa(i+1) {
				t.Errorf("handler %d saw attempt %d = %+v", n, i, a)
			}
		}
	}
	if got := len(e.onDisk().Attempts); got != 3 {
		t.Errorf("final report has %d attempts", got)
	}
}

func TestAHandlerOverwritingReportJSONDoesNotChangeWhatTheNextSees(t *testing.T) {
	dir := t.TempDir()
	hs := append([]hd{{name: "vandal", argv: helperArgv("overwrite-report", "code=2")}}, recorders(dir, 1)...)
	e := newE2E(t, "", hs, chainOf("vandal", "r1"))
	e.wrap("vandal,r1", helperArgv("exit", "code=1"))
	r := readRecord(t, filepath.Join(dir, "rec-2"))
	var seen report.Report
	if err := jsonUnmarshal(r.Report, &seen); err != nil || len(seen.Attempts) != 1 || seen.Attempts[0].Handler != "vandal" {
		t.Errorf("the next handler saw %q (err %v)", r.Report, err)
	}
	if rep := e.onDisk(); len(rep.Attempts) != 2 {
		t.Errorf("final report = %+v", rep)
	}
}

func TestReportJSONSymlinkIsReplacedNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	hs := append([]hd{{name: "linker", argv: helperArgv("symlink-report", "target="+target, "code=2")}}, recorders(dir, 1)...)
	e := newE2E(t, "", hs, chainOf("linker", "r1"))
	e.wrap("linker,r1", helperArgv("exit", "code=1"))
	if b, _ := os.ReadFile(target); string(b) != "sentinel" {
		t.Errorf("the write went through the symlink: %q", b)
	}
	r := readRecord(t, filepath.Join(dir, "rec-2"))
	var seen report.Report
	if err := jsonUnmarshal(r.Report, &seen); err != nil || len(seen.Attempts) != 1 {
		t.Errorf("the next handler saw %q (err %v)", r.Report, err)
	}
	fi, err := os.Lstat(filepath.Join(e.rdir(), "report.json"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Errorf("report.json is %v (%v); want a regular file", fi, err)
	}
}

func TestFilesAreOwnerOnly(t *testing.T) {
	old := setUmask(0o022)
	defer setUmask(old)
	hs := []hd{result("d", "declined", "details=some details")}
	e := newE2E(t, "", hs, chainOf("d"))
	e.wrap("d", helperArgv("exit", "code=1", "err=oops"), "--verify", "true")
	rdir := e.rdir()
	entries, err := os.ReadDir(rdir)
	if err != nil || len(entries) < 4 {
		t.Fatalf("run dir: %v %v", entries, err)
	}
	for _, en := range entries {
		fi, _ := en.Info()
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", en.Name(), fi.Mode().Perm())
		}
	}
}

func TestDurationsUseTheInjectedClock(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	var n int64
	base := time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC)
	e.rt.Now = func() time.Time { n++; return base.Add(time.Duration(n) * 250 * time.Millisecond) }
	e.wrap("d", helperArgv("exit", "code=1"))
	a := e.onDisk().Attempts[0]
	if a.DurationMS <= 0 || a.DurationMS%250 != 0 {
		t.Errorf("duration_ms = %d; want a positive multiple of the 250ms clock step", a.DurationMS)
	}
	if e.result().Duration <= 0 {
		t.Error("run duration not measured with the injected clock")
	}
}

func TestEnvPrecedenceAndEnvFileErrors(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "h.env")
	if err := os.WriteFile(envFile, []byte("XT_B=file\nXT_C=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := hd{
		name: "h", argv: helperArgv("record", "dest="+dir+"/rec", "code=2"),
		env:     map[string]string{"XT_A": "env", "XT_B": "env", "PG_RESCUE_HANDLER": "evil"},
		envFile: envFile,
	}
	missing := hd{name: "missing", argv: helperArgv("exit", "code=2"), envFile: filepath.Join(dir, "absent.env")}
	e := newE2E(t, "", []hd{h, missing}, chainOf("h", "missing"))
	e.env["XT_A"] = "wrapper"
	e.env["XT_D"] = "wrapper"
	e.wrap("h,missing", helperArgv("exit", "code=1"))
	env := readRecord(t, filepath.Join(dir, "rec")).Env
	for k, want := range map[string]string{"XT_A": "env", "XT_B": "file", "XT_C": "file", "XT_D": "wrapper", "PG_RESCUE_HANDLER": "h"} {
		if env[k] != want {
			t.Errorf("%s = %q; want %q", k, env[k], want)
		}
	}
	a := e.onDisk().Attempts[1]
	if a.Outcome != contract.Failed || !strings.Contains(a.Reason, `[handler.missing] key "env_file"`) {
		t.Errorf("a missing env_file must fail the attempt: %+v", a)
	}
}

func TestHappyPathRemovesTheRunDirectoryAndRunsNoHandler(t *testing.T) {
	dir := t.TempDir()
	e := newE2E(t, "", recorders(dir, 1), chainOf("r1"))
	code, stdout, stderr := e.wrap("r1", helperArgv("exit", "code=0", "out=fine\n", "err=warn\n"))
	if code != 0 || stdout != "fine\n" || stderr != "warn\n" {
		t.Errorf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if exists(filepath.Join(dir, "rec-1")) {
		t.Error("a handler ran on the happy path")
	}
	if dirs := e.runDirs(); len(dirs) != 0 {
		t.Errorf("run directory left behind: %v", dirs)
	}
	if r := e.result(); r.Kept || r.Kind != runner.KindSuccess || r.Report != nil {
		t.Errorf("result = %+v", r)
	}
}

func TestRunDirectoryIsKeptWhenAHandlerRan(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	e.wrap("d", helperArgv("exit", "code=1"))
	if !e.result().Kept || len(e.runDirs()) != 1 {
		t.Errorf("kept=%v dirs=%v", e.result().Kept, e.runDirs())
	}
}

func TestFingerprintIsComputedOnceInBothModes(t *testing.T) {
	e := newE2E(t, "", []hd{result("d", "declined")}, chainOf("d"))
	argv := helperArgv("exit", "code=1", "x")
	e.wrap("d", argv, "-C", e.root, "--context", "ignored in argv mode")
	if got, want := e.onDisk().Fingerprint, report.Fingerprint(report.ModeArgv, argv, e.root, ""); got != want {
		t.Errorf("argv fingerprint = %s; want %s", got, want)
	}
	in, _ := os.CreateTemp(e.root, "in")
	in.WriteString("x")
	in.Seek(0, 0)
	e.rt.Stdin = in
	e.wrap("d", nil, "--stdin", "--verify", "true", "-C", e.root, "--context", "ctx")
	if got, want := e.onDisk().Fingerprint, report.Fingerprint(report.ModeStdin, nil, e.root, "ctx"); got != want {
		t.Errorf("stdin fingerprint = %s; want %s", got, want)
	}
}

func jsonUnmarshal(s string, v any) error { return jsonDecode([]byte(s), v) }
