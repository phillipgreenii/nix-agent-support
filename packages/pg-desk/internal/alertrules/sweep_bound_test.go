package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
)

// wantSweepBoundExpr MUST equal the expr of rule pg-desk-sweep-bound-violated.
// No PromQL engine is a dependency of this module, so the semantics are
// modelled below and the YAML is pinned to this exact text (the same approach
// as sync_stale_test.go).
const wantSweepBoundExpr = `max by (type, tier) (pg_desk_sweep_bound_violated)`

// boundSeries is one sample of pg_desk_sweep_bound_violated: its labels (job
// differs between the pg-desk and pg-pr scrapes of the same process) and value.
type boundSeries struct {
	job, typ, tier string
	v              float64
}

// evalSweepBoundRule models `max by (type, tier) (...)`: one output element
// per (type, tier), valued at the max over the series sharing those labels.
func evalSweepBoundRule(in []boundSeries) map[[2]string]float64 {
	out := map[[2]string]float64{}
	for _, s := range in {
		k := [2]string{s.typ, s.tier}
		if cur, ok := out[k]; !ok || s.v > cur {
			out[k] = s.v
		}
	}
	return out
}

// firingKeys models reduce(last) + threshold(gte 1): each (type, tier) at >= 1
// is an alert instance. An empty result is NoData, which noDataState: OK maps
// to Normal.
func firingKeys(res map[[2]string]float64) [][2]string {
	var out [][2]string
	for k, v := range res {
		if v >= 1 {
			out = append(out, k)
		}
	}
	return out
}

func sweepBoundRuleBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-desk-sweep-bound-violated")
	if i < 0 {
		t.Fatal("rule pg-desk-sweep-bound-violated not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestSweepBoundRuleExprMatchesModel(t *testing.T) {
	rest := sweepBoundRuleBlock(t)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantSweepBoundExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantSweepBoundExpr)
	}
	for _, need := range []string{"for: 15m", "noDataState: OK", "execErrState: Error", "severity: warning", "type: gte", "params: [1]"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

func TestSweepBoundViolationFiresPerTypeAndTier(t *testing.T) {
	// Both the job=pg-desk series and its job=pg-pr duplicate read 1 for
	// pr/local: ONE alert instance. pr/remote holds (0) and issue is fine.
	res := evalSweepBoundRule([]boundSeries{
		{"pg-desk", "pr", "local", 1},
		{"pg-pr", "pr", "local", 1},
		{"pg-desk", "pr", "remote", 0},
		{"pg-pr", "pr", "remote", 0},
		{"pg-desk", "issue", "local", 0},
		{"pg-desk", "issue", "remote", 0},
	})
	got := firingKeys(res)
	if len(got) != 1 || got[0] != [2]string{"pr", "local"} {
		t.Errorf("firing = %v, want exactly [pr local]", got)
	}
}

func TestSweepBoundHoldingOrUnknownNeverFires(t *testing.T) {
	// Holding: every series reads 0. Unknown poll interval: the metric has no
	// series at all (NoData -> Normal under noDataState: OK).
	for name, in := range map[string][]boundSeries{
		"bound holds for every type and tier": {{"pg-desk", "pr", "local", 0}, {"pg-desk", "pr", "remote", 0}},
		"poll interval unknown (no series)":   nil,
	} {
		if got := firingKeys(evalSweepBoundRule(in)); len(got) != 0 {
			t.Errorf("%s: fired %v", name, got)
		}
	}
}

// TestSweepBoundRuleAgreesWithTheEvaluation drives the model from the SAME
// changes.EvaluateSweepBound that pg-desk doctor and the metric use: with the
// live 88 active PRs the remote bound is 88 / 20 x 1m = 4.4m, far below 6h, so
// it does not fire; a lowered local age does.
func TestSweepBoundRuleAgreesWithTheEvaluation(t *testing.T) {
	poll := time.Minute
	value := func(in changes.SweepInputs) float64 {
		if changes.EvaluateSweepBound(in, poll, true) == changes.BoundViolated {
			return 1
		}
		return 0
	}
	remote := changes.SweepInputs{ActiveCount: 88, MaxPerPoll: 20, MaxAge: 6 * time.Hour}
	local := changes.SweepInputs{ActiveCount: 88, MaxPerPoll: 20, MaxAge: 4 * time.Minute} // 4.4m > 4m
	res := evalSweepBoundRule([]boundSeries{
		{"pg-desk", "pr", "remote", value(remote)},
		{"pg-desk", "pr", "local", value(local)},
	})
	got := firingKeys(res)
	if len(got) != 1 || got[0] != [2]string{"pr", "local"} {
		t.Errorf("firing = %v, want exactly [pr local]", got)
	}
}
