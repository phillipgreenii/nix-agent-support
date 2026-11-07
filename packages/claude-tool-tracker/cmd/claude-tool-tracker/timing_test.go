package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// timingCall appends one tracker call: ran from running to end, with Claude
// Code's duration_ms (dur < 0: none), optionally a ceta.command_class, and an
// outcome (ok, error, interrupted).
func (a *app) timingCall(session, tu, cwd, command, class, outcome string, dur int, start, running, end int64, agent string) {
	a.t.Helper()
	extra := []string{}
	if agent != "" {
		extra = append(extra, "--agent-id", agent)
	}
	n := func(v int64) string { return strconv.FormatInt(v, 10) }
	cmdJSON, _ := json.Marshal(command)
	a.append(session, "tracker", "tracker.call_start", tu, n(start), tu+"-1",
		`{"tool":"Bash","input":{"command":`+string(cmdJSON)+`},"cwd":"`+cwd+`"}`, extra...)
	if running > 0 {
		a.append(session, "tracker", "tracker.running", tu, n(running), tu+"-2", `{"tool":"Bash"}`, extra...)
	}
	if dur >= 0 {
		hook := "PostToolUse"
		isErr := "false"
		if outcome == "error" {
			hook, isErr = "PostToolUseFailure", "true"
		}
		a.append(session, "tracker", "tracker.post_tool", tu, n(end-1), tu+"-3",
			`{"hook_event":"`+hook+`","tool_name":"Bash","duration_ms":`+strconv.Itoa(dur)+`,"is_error":`+isErr+`}`, extra...)
	}
	a.append(session, "tracker", "tracker.call_end", tu, n(end), tu+"-4", `{"outcome":"`+outcome+`"}`, extra...)
	if class != "" {
		a.append(session, "ceta", "ceta.command_class", tu, n(start), tu+"-5", `{"class":"`+class+`"}`, extra...)
	}
}

func gitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

type timingJSON struct {
	Calls, Used int
	Excluded    struct {
		Failed, Interrupted, Refused int
		NoTime                       int `json:"no_usable_time"`
	}
	ByTotal []timingGroup `json:"by_total"`
	ByP90   []timingGroup `json:"by_p90"`
}

type timingGroup struct {
	Tool, Class, Repo string
	Runs              int
	TotalMS           int64   `json:"total_ms"`
	MedianMS          float64 `json:"median_ms"`
	P90MS             int64   `json:"p90_ms"`
	MaxMS             int64   `json:"max_ms"`
}

func (a *app) timing(args ...string) (string, int) {
	a.t.Helper()
	out, errOut, code := run(a.t, a.state, "", append([]string{"timing"}, args...)...)
	if code != 0 {
		a.t.Logf("stderr: %s", errOut)
	}
	return out, code
}

func (a *app) timingJSON(args ...string) timingJSON {
	a.t.Helper()
	out, code := a.timing(append([]string{"--format", "json"}, args...)...)
	if code != 0 {
		a.t.Fatalf("timing exit %d", code)
	}
	var r timingJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		a.t.Fatalf("json: %v\n%s", err, out)
	}
	return r
}

// timingFixture is the documented fixture of the README heading "Timing".
func timingFixture(t *testing.T) *app {
	a := newApp(t)
	repoA, repoB := gitRepo(t, "alpha"), gitRepo(t, "beta")
	base := int64(1700000000000)
	// go test in alpha, CETA class "go test": durations 100, 200, 300, 400 (unasked: running present).
	for i, d := range []int{100, 200, 300, 400} {
		tu := "gt" + strconv.Itoa(i)
		s := base + int64(i)*10000
		a.timingCall("s1", tu, repoA, "go test ./...", "go test", "ok", d, s, s+5, s+5+int64(d)+22, "")
	}
	// go test in beta: one run of 50 ms.
	a.timingCall("s1", "gtb", repoB, "go test ./x", "go test", "ok", 50, base+60000, base+60005, base+60077, "")
	// A rare slow command: one 5000 ms run in alpha, with no CETA class (first word "deploy").
	a.timingCall("s1", "dep", repoA, "deploy --prod", "", "ok", 5000, base+70000, base+70005, base+75027, "")
	// An approved-after-ask call: a person took ~1300 s, the tool ran 160 ms; no running event.
	a.timingCall("s1", "ask", repoA, "go test ./y", "go test", "ok", 160, base+80000, 0, base+80000+1300000, "")
	// An unasked call without duration_ms: running to call_end (450 ms).
	a.timingCall("s1", "nodur", repoA, "go test ./z", "go test", "ok", -1, base+90000, base+90010, base+90460, "")
	// A subagent's call.
	a.timingCall("s1", "sub", repoA, "go test ./w", "go test", "ok", 1000, base+100000, base+100005, base+100030, "agent-1")
	// An errored call (700 ms), an interrupted call (800 ms), a call with no usable time.
	a.timingCall("s1", "err", repoA, "go test ./e", "go test", "error", 700, base+110000, base+110005, base+110720, "")
	a.timingCall("s1", "int", repoA, "go test ./i", "go test", "interrupted", 800, base+120000, base+120005, base+120830, "")
	a.timingCall("s1", "none", repoA, "go test ./n", "go test", "ok", -1, base+130000, 0, base+130500, "")
	a.ingest()
	return a
}

func findGroup(t *testing.T, gs []timingGroup, tool, class, repo string) timingGroup {
	t.Helper()
	for _, g := range gs {
		if g.Tool == tool && g.Class == class && g.Repo == repo {
			return g
		}
	}
	t.Fatalf("no group %s/%s/%s in %+v", tool, class, repo, gs)
	return timingGroup{}
}

func TestTimingGroupsStatsAndExclusions(t *testing.T) {
	a := timingFixture(t)
	r := a.timingJSON()
	if r.Calls != 12 || r.Used != 9 || r.Excluded.Failed != 1 || r.Excluded.Interrupted != 1 || r.Excluded.NoTime != 1 || r.Excluded.Refused != 0 {
		t.Fatalf("counts: %+v", r)
	}
	// go test in alpha: 100,200,300,400, ask 160, nodur 450 (fallback), sub 1000 => sorted 100,160,200,300,400,450,1000.
	g := findGroup(t, r.ByTotal, "Bash", "go test", "alpha")
	want := timingGroup{Tool: "Bash", Class: "go test", Repo: "alpha", Runs: 7, TotalMS: 2610, MedianMS: 300, P90MS: 1000, MaxMS: 1000}
	if g != want {
		t.Errorf("alpha go test = %+v, want %+v", g, want)
	}
	if g := findGroup(t, r.ByTotal, "Bash", "go test", "beta"); g.Runs != 1 || g.TotalMS != 50 || g.MedianMS != 50 || g.P90MS != 50 || g.MaxMS != 50 {
		t.Errorf("beta go test = %+v", g)
	}
	// ceta.command_class fallback: no CETA class, first word of the command.
	if g := findGroup(t, r.ByTotal, "Bash", "deploy", "alpha"); g.TotalMS != 5000 {
		t.Errorf("deploy = %+v", g)
	}
}

func TestTimingRankings(t *testing.T) {
	a := timingFixture(t)
	r := a.timingJSON()
	// Total: deploy 5000 first? alpha go test is 2610, so deploy leads total too; the rare slow
	// command must also lead p90, while go test alpha leads neither.
	if r.ByTotal[0].Class != "deploy" || r.ByTotal[1].Repo != "alpha" || r.ByP90[0].Class != "deploy" {
		t.Errorf("rankings: total %+v p90 %+v", r.ByTotal, r.ByP90)
	}
	if r.ByP90[1].P90MS != 1000 || r.ByP90[2].P90MS != 50 {
		t.Errorf("p90 order: %+v", r.ByP90)
	}
}

func TestTimingIncludeFailed(t *testing.T) {
	a := timingFixture(t)
	r := a.timingJSON("--include-failed")
	if r.Used != 11 || r.Excluded.Failed != 0 || r.Excluded.Interrupted != 0 || r.Excluded.NoTime != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if g := findGroup(t, r.ByTotal, "Bash", "go test", "alpha"); g.Runs != 9 || g.TotalMS != 4110 || g.MaxMS != 1000 {
		t.Errorf("alpha go test = %+v", g)
	}
}

func TestTimingTable(t *testing.T) {
	a := timingFixture(t)
	out, code := a.timing("--top", "2")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"calls=12 used=9 excluded: failed=1 interrupted=1 refused=0 no_usable_time=1", "by total time", "by p90 (median beside it)", "TOOL", "MEDIAN_MS"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "Bash") != 4 { // 2 rows per ranking
		t.Errorf("--top 2 should print 2 rows per ranking:\n%s", out)
	}
}

func TestTimingUsageAndMissingDB(t *testing.T) {
	a := newApp(t)
	if _, code := a.timing("--format", "xml"); code != 2 {
		t.Errorf("bad format exit %d, want 2", code)
	}
	if _, code := a.timing(); code != 1 {
		t.Errorf("missing database exit %d, want 1", code)
	}
}
