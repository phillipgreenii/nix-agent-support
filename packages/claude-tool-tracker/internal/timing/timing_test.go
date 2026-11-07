package timing

import (
	"database/sql"
	"testing"
)

func ms(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func TestPercentileAndMedian(t *testing.T) {
	s := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := Percentile(s, 90); got != 9 { // ceil(0.9*10) = rank 9
		t.Errorf("p90 of 1..10 = %d, want 9", got)
	}
	if got := Percentile([]int64{5}, 90); got != 5 {
		t.Errorf("p90 of one value = %d", got)
	}
	if got := Percentile([]int64{10, 20, 30}, 90); got != 30 { // ceil(2.7) = 3
		t.Errorf("p90 of 3 values = %d", got)
	}
	if Median(s) != 5.5 || Median([]int64{1, 2, 9}) != 2 || Median(nil) != 0 {
		t.Error("median wrong")
	}
}

func TestRunTimeSource(t *testing.T) {
	// An approved-after-ask call: duration_ms is the run only, the wait is not counted.
	if got, ok := RunTime(Call{DurationMS: ms(163), RunningTS: ms(1000), CallEndTS: ms(1300000)}); !ok || got != 163 {
		t.Errorf("duration_ms must win: %d %v", got, ok)
	}
	// An unasked call without duration_ms falls back to running to call_end.
	if got, ok := RunTime(Call{RunningTS: ms(1000), CallEndTS: ms(1450)}); !ok || got != 450 {
		t.Errorf("fallback: %d %v", got, ok)
	}
	if _, ok := RunTime(Call{CallEndTS: ms(5)}); ok {
		t.Error("no time source must be unusable")
	}
	if _, ok := RunTime(Call{RunningTS: ms(9), CallEndTS: ms(5)}); ok {
		t.Error("negative span must be unusable")
	}
}

func TestKeyOf(t *testing.T) {
	cases := []struct {
		c    Call
		want Key
	}{
		{Call{Tool: "Bash", Command: "go test ./...", Class: "go test", Repo: "r"}, Key{"Bash", "go test", "r"}},
		{Call{Tool: "Bash", Command: "  go test ./...", Repo: "r"}, Key{"Bash", "go", "r"}},
		{Call{Tool: "Read", Command: "ignored", Class: "ignored"}, Key{"Read", "", "(none)"}},
		{Call{}, Key{"(unknown)", "", "(none)"}},
	}
	for _, tc := range cases {
		if got := KeyOf(tc.c); got != tc.want {
			t.Errorf("KeyOf(%+v) = %+v, want %+v", tc.c, got, tc.want)
		}
	}
}

func call(tool, class, repo string, dur int64) Call {
	return Call{Tool: tool, Class: class, Repo: repo, DurationMS: ms(dur), Result: "ok"}
}

func TestAnalyzeStatsAndRankings(t *testing.T) {
	var calls []Call
	// "go test" in repo a: 10 runs of 100 ms: total 1000, p90 100.
	for i := 0; i < 10; i++ {
		calls = append(calls, call("Bash", "go test", "a", 100))
	}
	// A rare slow command: 2 runs, total 1800, so below go test's total... make it clearly lower.
	calls = append(calls, call("Bash", "deploy", "a", 900), call("Bash", "deploy", "a", 700))
	// Same command class in another repo is its own group.
	calls = append(calls, call("Bash", "go test", "b", 40), call("Bash", "go test", "b", 60))
	r := Analyze(calls, Options{})
	if r.Used != 14 || r.Calls != 14 {
		t.Fatalf("used %d calls %d", r.Used, r.Calls)
	}
	want := map[Key]Group{
		{"Bash", "go test", "a"}: {Runs: 10, TotalMS: 1000, MedianMS: 100, P90MS: 100, MaxMS: 100},
		{"Bash", "deploy", "a"}:  {Runs: 2, TotalMS: 1600, MedianMS: 800, P90MS: 900, MaxMS: 900},
		{"Bash", "go test", "b"}: {Runs: 2, TotalMS: 100, MedianMS: 50, P90MS: 60, MaxMS: 60},
	}
	if len(r.ByTotal) != 3 || len(r.ByP90) != 3 {
		t.Fatalf("groups: %+v", r.ByTotal)
	}
	for _, g := range r.ByTotal {
		w := want[g.Key]
		w.Key = g.Key
		if g != w {
			t.Errorf("group %+v, want %+v", g, w)
		}
	}
	// Total ranking: deploy (1600) above go test (1000). p90: deploy (900) first, go test a (100) second.
	if r.ByTotal[0].Class != "deploy" || r.ByTotal[1].Repo != "a" || r.ByTotal[2].Repo != "b" {
		t.Errorf("total order: %+v", r.ByTotal)
	}
	if r.ByP90[0].Class != "deploy" || r.ByP90[1].P90MS != 100 || r.ByP90[2].P90MS != 60 {
		t.Errorf("p90 order: %+v", r.ByP90)
	}
}

func TestRareSlowCommandAppearsInP90NotTotalTop(t *testing.T) {
	var calls []Call
	for i := 0; i < 50; i++ {
		calls = append(calls, call("Bash", "ls", "a", 100)) // total 5000
	}
	calls = append(calls, call("Bash", "migrate", "a", 3000)) // total 3000, p90 3000
	r := Analyze(calls, Options{})
	if r.ByTotal[0].Class != "ls" || r.ByP90[0].Class != "migrate" {
		t.Errorf("total top %q p90 top %q", r.ByTotal[0].Class, r.ByP90[0].Class)
	}
}

func TestAnalyzeExclusions(t *testing.T) {
	failed := call("Bash", "x", "a", 10)
	failed.FailedTiming, failed.Result = ms(1), "error"
	intr := call("Bash", "x", "a", 20)
	intr.Result = "interrupted"
	refused := call("Bash", "x", "a", 30)
	refused.Result = "refused"
	noTime := Call{Tool: "Bash", Class: "x", Repo: "a", Result: "ok"}
	good := call("Bash", "x", "a", 40)
	calls := []Call{failed, intr, refused, noTime, good}

	r := Analyze(calls, Options{})
	if r.Used != 1 || r.Excluded != (Excluded{Failed: 1, Interrupted: 1, Refused: 1, NoTime: 1}) {
		t.Errorf("default: used %d excluded %+v", r.Used, r.Excluded)
	}
	r = Analyze(calls, Options{IncludeFailed: true})
	if r.Used != 3 || r.Excluded != (Excluded{Refused: 1, NoTime: 1}) || r.ByTotal[0].TotalMS != 70 {
		t.Errorf("include failed: used %d excluded %+v group %+v", r.Used, r.Excluded, r.ByTotal)
	}
}

func TestSubagentCallsIncluded(t *testing.T) {
	// Load reads every row of the view, so a subagent's call is just a call;
	// Analyze has no agent filter.
	r := Analyze([]Call{call("Edit", "", "a", 5), call("Edit", "", "a", 7)}, Options{})
	if r.Used != 2 {
		t.Errorf("used %d", r.Used)
	}
}
