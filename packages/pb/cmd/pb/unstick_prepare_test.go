package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/gate"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

func normalizeWorkdir(s, w string) string { return strings.ReplaceAll(s, w, "<WORKDIR>") }

func prepareEnv(t *testing.T) (unstickEnv, *run.FakeRunner, string) {
	t.Helper()
	w := filepath.Join(t.TempDir(), "w")
	f := run.NewFakeRunner()
	bdScript(f, w, unstickReadyJSON)
	return testEnv(&exportRunner{FakeRunner: f, content: unstickExport}), f, w
}

func TestPrepare_orchestration(t *testing.T) {
	env, f, w := prepareEnv(t)
	if _, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("want exactly 2 bd calls (export, ready), got %d: %+v", len(calls), calls)
	}
	wantArgs := [][]string{
		{"-C", unstickRoot, "export", "-o", filepath.Join(w, "export.jsonl")},
		{"-C", unstickRoot, "ready", "-n", "0", "--json"},
	}
	wantTimeouts := []time.Duration{bd.ExportTimeout, bd.ReadyTimeout}
	for i, c := range calls {
		if c.Name != "bd" || !reflect.DeepEqual(c.Args, wantArgs[i]) {
			t.Errorf("call %d = %s %v, want bd %v", i, c.Name, c.Args, wantArgs[i])
		}
		if c.Opts.Timeout != wantTimeouts[i] {
			t.Errorf("call %d timeout = %s, want %s", i, c.Opts.Timeout, wantTimeouts[i])
		}
		if !containsStr(c.Opts.Env, "BD_JSON_ENVELOPE=1") {
			t.Errorf("call %d must pin BD_JSON_ENVELOPE=1", i)
		}
	}

	for _, p := range []string{
		"export.jsonl", "ready.json", "prepare.json", "progress.txt", "followups.txt",
		"triage-targets.txt", "triage-live.txt", "triage-marker.txt", "triage-review.txt",
		"triage-inprog.txt", "triage-assigned_open.txt", "triage-drain.txt",
		"batches/B01", "facts/B01.json", "probes/gate-check.json",
	} {
		if _, err := os.Stat(filepath.Join(w, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	wantLists := map[string]string{
		"triage-targets.txt":       "a-2\na-3\na-4\na-5\na-6\n",
		"triage-live.txt":          "a-2\n",
		"triage-marker.txt":        "a-5\n",
		"triage-review.txt":        "a-3\na-4\na-6\n",
		"triage-inprog.txt":        "a-7\n",
		"triage-assigned_open.txt": "a-9\n",
		"triage-drain.txt":         "a-1\n",
		"batches/B01":              "a-3\na-4\na-6\n",
	}
	for p, want := range wantLists {
		if got := readFile(t, filepath.Join(w, p)); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	if got := readFile(t, filepath.Join(w, "ready.json")); got != unstickReadyJSON {
		t.Errorf("ready.json must be bd's raw stdout, got %q", got)
	}

	st, err := unstick.ReadPrepare(filepath.Join(w, "prepare.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Start != unstickNow || !reflect.DeepEqual(st.Review, []string{"a-3", "a-4", "a-6"}) ||
		!reflect.DeepEqual(st.ReadyIDs, []string{"a-1"}) || !reflect.DeepEqual(st.Closed, []string{"a-8"}) {
		t.Errorf("prepare.json = %+v", st)
	}
	if st.Pre["a-7"] != "in_progress" || st.Pre["a-3"] != "deferred" || st.Pre["a-8"] != "" || len(st.Pre) != 8 {
		t.Errorf("prepare.json pre map = %v (want 8 non-closed ids)", st.Pre)
	}
	if st.Counts["targets"] != 5 || st.Counts["review"] != 3 || st.Counts["ready"] != 1 {
		t.Errorf("prepare.json counts = %v", st.Counts)
	}
	if got := strings.Split(readFile(t, filepath.Join(w, "progress.txt")), "\n")[0]; got != unstickNow {
		t.Errorf("progress.txt line 1 = %q", got)
	}
}

func TestPrepare_summaryGoldens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		golden string
	}{
		{"json", []string{"--json"}, "prepare.summary.golden.txt"},
		{"human", nil, "prepare.human.golden.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, w := prepareEnv(t)
			args := append([]string{"prepare", "--root", unstickRoot, "--workdir", w}, tc.args...)
			out, _, err := execUnstick(env, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			checkCmdGolden(t, tc.golden, []byte(normalizeWorkdir(out, w)))
		})
	}
}

func TestPrepare_summaryJSONShape(t *testing.T) {
	env, _, w := prepareEnv(t)
	out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"workdir", "start", "counts", "partition", "batches", "claim_candidates", "malformed_markers", "gate_check"} {
		if _, ok := m[k]; !ok {
			t.Errorf("summary lacks %q", k)
		}
	}
	// The lease/heartbeat enrichment was dropped: export rows do not carry it.
	for _, c := range m["claim_candidates"].([]any) {
		for _, banned := range []string{"heartbeat_at", "lease_expires_at"} {
			if _, ok := c.(map[string]any)[banned]; ok {
				t.Errorf("claim candidate carries dropped field %s", banned)
			}
		}
	}
	mm := m["malformed_markers"].(map[string]any)
	if mm["count"].(float64) != 1 || mm["ids"].([]any)[0] != "a-6" {
		t.Errorf("malformed_markers = %v", mm)
	}
}

func TestPrepare_narrowingAndFullFlags(t *testing.T) {
	// --label narrows TARGETS only after LIVE is computed; --full sends the
	// marker-skipped a-5 to REVIEW.
	env, _, w := prepareEnv(t)
	out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--label", "beta", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"partition": "targets 1 = live 0 + marker 0 + review 1"`) {
		t.Errorf("--label beta summary:\n%s", out)
	}
	env, _, w = prepareEnv(t)
	out, _, err = execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--full", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"partition": "targets 5 = live 1 + marker 0 + review 4"`) {
		t.Errorf("--full summary:\n%s", out)
	}
	env, _, w = prepareEnv(t)
	out, _, err = execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--id-prefix", "zz-", "--json")
	if err != nil || !strings.Contains(out, `"partition": "targets 0 = live 0 + marker 0 + review 0"`) {
		t.Errorf("--id-prefix zz- = %v\n%s", err, out)
	}
}

func TestPrepare_gateCheckStatuses(t *testing.T) {
	cases := []struct {
		name      string
		gc        GateCheckFunc
		want      string
		wantProbe bool
		wantWarn  string
	}{
		{"ok", func(context.Context, string, time.Time) (gate.CheckResult, error) { return gate.CheckResult{}, nil }, "ok", true, ""},
		{"skipped is partial", func(context.Context, string, time.Time) (gate.CheckResult, error) {
			return gate.CheckResult{Skipped: []gate.Skip{{GateID: "g-1", Repo: "r", Reason: "dirty"}}}, nil
		}, "partial", true, "gate check partial: 1 gate(s)"},
		{"hard failure is unavailable", func(context.Context, string, time.Time) (gate.CheckResult, error) {
			return gate.CheckResult{}, errors.New("pn missing")
		}, "unavailable", false, "gate check unavailable: pn missing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, _, w := prepareEnv(t)
			env.GateCheck = c.gc
			out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--json")
			if err != nil {
				t.Fatalf("gate check outcome must never be fatal: %v", err)
			}
			if !strings.Contains(out, `"gate_check": "`+c.want+`"`) {
				t.Errorf("want gate_check %q:\n%s", c.want, out)
			}
			if c.wantWarn != "" && !strings.Contains(out, c.wantWarn) {
				t.Errorf("want warning %q:\n%s", c.wantWarn, out)
			}
			_, statErr := os.Stat(filepath.Join(w, "probes", "gate-check.json"))
			if (statErr == nil) != c.wantProbe {
				t.Errorf("probes/gate-check.json present = %v, want %v", statErr == nil, c.wantProbe)
			}
		})
	}
}

func TestPrepare_defaultGateCheckRunsInProcessAndNeverFatal(t *testing.T) {
	// No GateCheck override: gate.Check runs over the scripted runner, whose
	// `pn workspace info` is unscripted -> hard failure -> "unavailable".
	env, f, w := prepareEnv(t)
	env.GateCheck = nil
	out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"gate_check": "unavailable"`) {
		t.Errorf("want unavailable:\n%s", out)
	}
	var sawPN bool
	for _, c := range f.Calls() {
		if c.Name == "pn" {
			sawPN = true
			if !reflect.DeepEqual(c.Args, []string{"workspace", "info", "--json"}) || c.Opts.Dir != unstickRoot {
				t.Errorf("pn call = %v dir=%q", c.Args, c.Opts.Dir)
			}
		}
		if c.Name == "pb" {
			t.Error("gate check must run in-process, not shell out to pb")
		}
	}
	if !sawPN {
		t.Error("expected the in-process gate check to query pn")
	}
}

func TestPrepare_autoAllocatesFreshWorkdirUnderTmpBase(t *testing.T) {
	base := t.TempDir()
	var dirs []string
	for i := 0; i < 2; i++ {
		f := run.NewFakeRunner()
		w := filepath.Join(base, "bead-unstick-2026-10-10")
		if i == 1 {
			w += "-2"
		}
		bdScript(f, w, unstickReadyJSON)
		env := testEnv(&exportRunner{FakeRunner: f, content: unstickExport})
		env.TmpBase = base
		out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--json")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !strings.Contains(out, `"workdir": "`+w+`"`) {
			t.Errorf("run %d: want workdir %s:\n%s", i, w, out)
		}
		dirs = append(dirs, w)
	}
	if dirs[0] == dirs[1] {
		t.Error("workdirs must never be reused")
	}
}

func TestPrepare_exitCodes(t *testing.T) {
	boom := errors.New("exit status 1")
	t.Run("bd export failure is exit 2", func(t *testing.T) {
		w := filepath.Join(t.TempDir(), "w")
		f := run.NewFakeRunner()
		f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, "export.jsonl")}, run.Result{ExitCode: 1, Stderr: "dolt down"}, boom)
		_, _, err := execUnstick(testEnv(f), "", "prepare", "--root", unstickRoot, "--workdir", w)
		if exitCodeFor(err) != 2 || !strings.Contains(err.Error(), "bd export") {
			t.Errorf("exit = %d, err = %v", exitCodeFor(err), err)
		}
	})
	t.Run("bd ready failure is exit 2", func(t *testing.T) {
		w := filepath.Join(t.TempDir(), "w")
		f := run.NewFakeRunner()
		f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, "export.jsonl")}, run.Result{}, nil)
		f.AddResponse("bd", []string{"-C", unstickRoot, "ready", "-n", "0", "--json"}, run.Result{ExitCode: 1}, boom)
		_, _, err := execUnstick(testEnv(&exportRunner{FakeRunner: f, content: unstickExport}), "", "prepare", "--root", unstickRoot, "--workdir", w)
		if exitCodeFor(err) != 2 {
			t.Errorf("exit = %d, err = %v", exitCodeFor(err), err)
		}
	})
	t.Run("ready without data envelope is exit 2", func(t *testing.T) {
		w := filepath.Join(t.TempDir(), "w")
		f := run.NewFakeRunner()
		bdScript(f, w, `{"schema_version":1}`)
		_, _, err := execUnstick(testEnv(&exportRunner{FakeRunner: f, content: unstickExport}), "", "prepare", "--root", unstickRoot, "--workdir", w)
		if exitCodeFor(err) != 2 {
			t.Errorf("exit = %d, err = %v", exitCodeFor(err), err)
		}
	})
	t.Run("usage errors are exit 1 and call no bd", func(t *testing.T) {
		existing := t.TempDir()
		for _, args := range [][]string{
			{"prepare", "--root", "rel", "--workdir", "/x"},
			{"prepare", "--root", unstickRoot, "--workdir", "rel/w"},
			{"prepare", "--root", unstickRoot, "--workdir", existing}, // exists: never reused
			{"prepare", "--root", unstickRoot, "--now", "yesterday"},
			{"prepare", "--bogus"},
			{"prepare", "stray-arg"},
		} {
			f := run.NewFakeRunner()
			_, _, err := execUnstick(testEnv(f), "", args...)
			if err == nil || exitCodeFor(err) != 1 {
				t.Errorf("%v: exit = %d, err = %v", args, exitCodeFor(err), err)
			}
			if n := len(f.Calls()); n != 0 {
				t.Errorf("%v: made %d bd call(s)", args, n)
			}
		}
	})
	t.Run("unresolvable root is exit 1", func(t *testing.T) {
		_, _, err := execUnstick(testEnv(run.NewFakeRunner()), "", "prepare", "--workdir", filepath.Join(t.TempDir(), "w"))
		if err == nil || exitCodeFor(err) != 1 {
			t.Errorf("exit = %d, err = %v", exitCodeFor(err), err)
		}
	})
}

func TestCheckBatchCoverage(t *testing.T) {
	if err := checkBatchCoverage([]string{"a", "b"}, [][]string{{"a"}, {"b"}}); err != nil {
		t.Errorf("valid coverage rejected: %v", err)
	}
	for name, batches := range map[string][][]string{
		"missing":   {{"a"}},
		"duplicate": {{"a", "b"}, {"b"}},
		"extra":     {{"a", "b", "c"}},
	} {
		if err := checkBatchCoverage([]string{"a", "b"}, batches); err == nil {
			t.Errorf("%s: coverage violation not detected", name)
		}
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
