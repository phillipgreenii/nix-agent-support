package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

// postExport: a-3 was closed by the sweep (listed in results/B01.md); a-4 got
// a worker marker; a-1 was closed by someone else (a peer); a-10 is new.
const unstickPostExport = `{"_type":"issue","id":"a-1","title":"Ready task","status":"closed","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-10T12:30:00Z","closed_at":"2026-10-10T12:30:00Z","close_reason":"peer finished"}
{"_type":"issue","id":"a-2","title":"Waits on a-1","status":"open","priority":2,"issue_type":"task","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-10T12:30:00Z","dependencies":[{"issue_id":"a-2","depends_on_id":"a-1","type":"blocks"}]}
{"_type":"issue","id":"a-3","title":"Deferred long ago","status":"closed","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-10-10T12:20:00Z","closed_at":"2026-10-10T12:20:00Z","close_reason":"stale"}
{"_type":"issue","id":"a-4","title":"Waits on a-3","status":"open","priority":3,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-10-10T12:21:00Z","notes":"[unstick 2026-10-10T12:21:00Z] retargeted: blocker closed; recheck-when: on-change","dependencies":[{"issue_id":"a-4","depends_on_id":"a-3","type":"blocks"}]}
{"_type":"issue","id":"a-10","title":"Brand new","status":"open","priority":3,"issue_type":"task","created_at":"2026-10-10T12:40:00Z","updated_at":"2026-10-10T12:40:00Z"}
`

const unstickPostReady = `{"data":[{"id":"a-2","status":"open","issue_type":"task"},{"id":"a-4","status":"open","issue_type":"task"},{"id":"a-10","status":"open","issue_type":"task"}],"schema_version":1}`

const reportNow = "2026-10-10T13:00:00Z"

func reportEnv(w string, ready string, readyErr error) (unstickEnv, *run.FakeRunner) {
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, unstick.ExportPostFile)}, run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", unstickRoot, "ready", "-n", "0", "--json"}, run.Result{Stdout: ready}, readyErr)
	return testEnv(&exportRunner{FakeRunner: f, content: unstickPostExport}), f
}

func seedResults(t *testing.T, w string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(w, "results", "B01.md"), []byte("closed a-3: stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, "followups.txt"), []byte("OPERATOR: decide on a-6\nFOLLOWUP: revisit a-9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReport_orchestrationAndJSON(t *testing.T) {
	w := preparedWorkdir(t)
	seedResults(t, w)
	env, f := reportEnv(w, unstickPostReady, nil)
	out, _, err := execUnstick(env, "", "report", "--workdir", w, "--root", unstickRoot, "--json", "--now", reportNow)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	wantArgs := [][]string{
		{"-C", unstickRoot, "export", "-o", filepath.Join(w, "export.post.jsonl")},
		{"-C", unstickRoot, "ready", "-n", "0", "--json"},
	}
	wantTimeouts := []time.Duration{bd.ExportTimeout, bd.ReadyTimeout}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	for i, c := range calls {
		if c.Name != "bd" || !reflect.DeepEqual(c.Args, wantArgs[i]) || c.Opts.Timeout != wantTimeouts[i] {
			t.Errorf("call %d = %s %v timeout %s", i, c.Name, c.Args, c.Opts.Timeout)
		}
	}
	if got := readFile(t, filepath.Join(w, "ready.post.json")); got != unstickPostReady {
		t.Errorf("ready.post.json = %q", got)
	}

	var rep unstick.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Start != unstickNow || rep.Now != reportNow {
		t.Errorf("start/now = %s/%s", rep.Start, rep.Now)
	}
	attrib := map[string]string{}
	for _, c := range rep.Closed {
		attrib[c.ID] = c.Attribution
	}
	if attrib["a-3"] != unstick.AttribSweep || attrib["a-1"] != unstick.AttribPeer || len(attrib) != 2 {
		t.Errorf("closed attribution = %v", attrib)
	}
	if !reflect.DeepEqual(rep.NewBeads, []string{"a-10"}) {
		t.Errorf("new beads = %v", rep.NewBeads)
	}
	if rep.OpenNotReady == nil {
		t.Error("with a post ready list the open-not-ready split must be present")
	}
	if !reflect.DeepEqual(rep.Operator, []string{"decide on a-6"}) || !reflect.DeepEqual(rep.Followup, []string{"revisit a-9"}) {
		t.Errorf("followups = %v / %v", rep.Operator, rep.Followup)
	}
	if len(rep.MarkersByOutcome) != 1 || rep.MarkersByOutcome[0].Outcome != "retargeted" {
		t.Errorf("markers = %+v", rep.MarkersByOutcome)
	}
}

func TestReport_humanOutput(t *testing.T) {
	w := preparedWorkdir(t)
	seedResults(t, w)
	env, _ := reportEnv(w, unstickPostReady, nil)
	out, _, err := execUnstick(env, "", "report", "--workdir", w, "--root", unstickRoot, "--now", reportNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Sweep report (start "+unstickNow+", report "+reportNow+")") || !strings.Contains(out, "HEURISTIC") {
		t.Errorf("human output:\n%s", out)
	}
	if json.Valid([]byte(out)) {
		t.Error("human mode must not emit JSON")
	}
}

func TestReport_postReadyFailureDegrades(t *testing.T) {
	w := preparedWorkdir(t)
	env, _ := reportEnv(w, "", errors.New("exit status 1"))
	out, errOut, err := execUnstick(env, "", "report", "--workdir", w, "--root", unstickRoot, "--json", "--now", reportNow)
	if err != nil {
		t.Fatalf("ready failure must not be fatal: %v", err)
	}
	if !strings.Contains(errOut, "post-sweep ready list unavailable") {
		t.Errorf("stderr = %q", errOut)
	}
	var rep unstick.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.OpenNotReady != nil {
		t.Errorf("without ready the split must be omitted: %v %+v", err, rep.OpenNotReady)
	}
	if _, err := os.Stat(filepath.Join(w, "ready.post.json")); err == nil {
		t.Error("ready.post.json must not be written when the fetch failed")
	}
}

func TestReport_exitCodes(t *testing.T) {
	t.Run("export failure is exit 2", func(t *testing.T) {
		w := preparedWorkdir(t)
		f := run.NewFakeRunner()
		f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, "export.post.jsonl")}, run.Result{ExitCode: 1}, errors.New("exit status 1"))
		_, _, err := execUnstick(testEnv(f), "", "report", "--workdir", w, "--root", unstickRoot)
		if exitCodeFor(err) != 2 {
			t.Errorf("exit = %d, err = %v", exitCodeFor(err), err)
		}
	})
	t.Run("usage errors are exit 1 and call no bd", func(t *testing.T) {
		w := preparedWorkdir(t)
		bare := t.TempDir() // a directory without prepare.json
		for name, args := range map[string][]string{
			"no workdir":       {"report", "--root", unstickRoot},
			"relative workdir": {"report", "--workdir", "rel", "--root", unstickRoot},
			"not prepared":     {"report", "--workdir", bare, "--root", unstickRoot},
			"relative root":    {"report", "--workdir", w, "--root", "rel"},
			"bad now":          {"report", "--workdir", w, "--root", unstickRoot, "--now", "x"},
		} {
			f := run.NewFakeRunner()
			_, _, err := execUnstick(testEnv(f), "", args...)
			if err == nil || exitCodeFor(err) != 1 {
				t.Errorf("%s: exit %d, err %v", name, exitCodeFor(err), err)
			}
			if len(f.Calls()) != 0 {
				t.Errorf("%s: made bd calls", name)
			}
		}
	})
}
