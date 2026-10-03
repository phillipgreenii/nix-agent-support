// Package alertrules guards the Grafana alert rules in grafana/alerting/alerts.yaml.
//
// No PromQL engine is a dependency of this module, so the semantics of the
// rule's `unless on()` suppression are modelled here and the test pins the YAML
// expression to the exact text the model implements (the same approach as
// packages/pg-router/internal/alertrules). The expression itself was also
// checked against a live Prometheus over the 2026-10-02 SYSTEM_PAUSE incident
// window (pg2-9awkw).
package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// wantExpr MUST equal the expr of rule pg-desk-sync-stale.
const wantExpr = `pg_desk_dashboard_stale unless on() (pg_router_gates_active{type="SYSTEM_PAUSE"} == 1)`

// series is one sample of an instant vector: the job label (only used to show
// that label sets differing between the two sides do not matter) and its value.
type series struct {
	job string
	v   float64
}

// evalStaleRule models wantExpr at one instant: `stale unless on()
// (gates == 1)`. The right-hand `== 1` is a filter, so gate series reading
// anything else (0) or absent entirely leave no right-hand element; `on()`
// matches on zero labels, so ANY surviving right-hand element drops EVERY
// left-hand element regardless of label sets. The result is the surviving
// left-hand series; the threshold (>= 1) is then applied by the rule's C step.
func evalStaleRule(stale, systemPause []series) []series {
	paused := false
	for _, g := range systemPause {
		if g.v == 1 {
			paused = true
		}
	}
	if paused {
		return nil
	}
	return stale
}

// firing models the rule's reduce(last) + threshold(gte 1): any surviving
// series at >= 1 fires; an empty result is NoData, which noDataState: OK maps
// to Normal.
func firing(res []series) bool {
	for _, s := range res {
		if s.v >= 1 {
			return true
		}
	}
	return false
}

func ruleBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-desk-sync-stale")
	if i < 0 {
		t.Fatal("rule pg-desk-sync-stale not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestSyncStaleRuleExprMatchesModel(t *testing.T) {
	rest := ruleBlock(t)
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantExpr)
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning", "type: gte", "params: [1]", "SYSTEM_PAUSE"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

// Both the job=pg-desk series and its job=pg-pr duplicate are stale.
var staleBoth = []series{{"pg-desk", 1}, {"pg-pr", 1}}

func TestStaleWithoutPauseFires(t *testing.T) {
	cases := map[string][]series{
		"gate series absent (the normal state)": nil,
		"gate series reads 0":                   {{"pg-router", 0}},
	}
	for name, gate := range cases {
		if !firing(evalStaleRule(staleBoth, gate)) {
			t.Errorf("%s: stale snapshot did not fire", name)
		}
	}
}

func TestStaleDuringPauseDoesNotFire(t *testing.T) {
	// The gate series carries job=pg-router while the stale series carry
	// job=pg-desk/pg-pr: on() must still match across the differing labels.
	if firing(evalStaleRule(staleBoth, []series{{"pg-router", 1}})) {
		t.Fatal("stale snapshot fired during a SYSTEM_PAUSE gate")
	}
}

func TestFreshNeverFires(t *testing.T) {
	fresh := []series{{"pg-desk", 0}, {"pg-pr", 0}}
	for _, gate := range [][]series{nil, {{"pg-router", 1}}} {
		if firing(evalStaleRule(fresh, gate)) {
			t.Fatalf("fresh snapshot fired (gate=%v)", gate)
		}
	}
}
