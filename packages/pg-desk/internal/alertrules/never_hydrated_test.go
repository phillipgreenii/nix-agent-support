package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// neverHydratedExpr MUST equal the expr of rule pg-desk-pr-never-hydrated.
const neverHydratedExpr = `((pg_desk_pr_unhydrated > 0) or (pg_desk_liveness == 1 unless on() pg_desk_pr_unhydrated)) unless on() (pg_router_gates_active{type="SYSTEM_PAUSE"} == 1)`

func neverHydratedBlock(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: pg-desk-pr-never-hydrated")
	if i < 0 {
		t.Fatal("rule pg-desk-pr-never-hydrated not found")
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// evalNeverHydrated models neverHydratedExpr at one instant.
//
//	(unhydrated > 0)                       the gauge's series above 0 (a filter)
//	or (liveness == 1 unless on() gauge)   the liveness series, only while the gauge has NO series
//	unless on() (gates == 1)               every surviving series dropped while a SYSTEM_PAUSE gate reads 1
//
// `unless on()` matches on zero labels, so any right-hand element drops every
// left-hand one whatever their labels. `or` adds the right-hand elements whose
// label sets the left side does not already hold; the two branches are never
// both populated (the second needs the gauge to have no series), so the union
// is plain.
func evalNeverHydrated(unhydrated, liveness, systemPause []series) []series {
	var out []series
	for _, s := range unhydrated {
		if s.v > 0 {
			out = append(out, s)
		}
	}
	if len(unhydrated) == 0 {
		for _, s := range liveness {
			if s.v == 1 {
				out = append(out, s)
			}
		}
	}
	for _, g := range systemPause {
		if g.v == 1 {
			return nil
		}
	}
	return out
}

// firesAbove0 models the rule's reduce(last) + threshold(gt 0): an empty
// result is NoData, which noDataState: OK maps to Normal.
func firesAbove0(res []series) bool {
	for _, s := range res {
		if s.v > 0 {
			return true
		}
	}
	return false
}

func TestNeverHydratedRuleMatchesModel(t *testing.T) {
	rest := neverHydratedBlock(t)
	m := regexp.MustCompile(`(?m)^\s*expr: '?(.+?)'?$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != neverHydratedExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], neverHydratedExpr)
	}
	for _, need := range []string{"for: 30m", "noDataState: OK", "execErrState: Error", "severity: warning", "type: gt", "params: [0]", "reducer: last", "SYSTEM_PAUSE"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

var (
	up   = []series{{"pg-desk", 1}}
	down = []series{{"pg-desk", 0}}
)

func TestNeverHydratedFiresAboveThreshold(t *testing.T) {
	for name, gauge := range map[string][]series{
		"495 rows unhydrated (the 2026-10-19 state)": {{"pg-desk", 495}},
		"one row": {{"pg-desk", 1}},
	} {
		if !firesAbove0(evalNeverHydrated(gauge, up, nil)) {
			t.Errorf("%s: did not fire", name)
		}
	}
}

func TestNeverHydratedFiresWhenTheGaugeIsAbsentWhileLivenessIsUp(t *testing.T) {
	if !firesAbove0(evalNeverHydrated(nil, up, nil)) {
		t.Error("an absent gauge with liveness up must fire: the store cannot answer")
	}
	if !firesAbove0(evalNeverHydrated(nil, up, []series{{"pg-router", 0}})) {
		t.Error("a SYSTEM_PAUSE series reading 0 is not a pause: it must still fire")
	}
}

func TestNeverHydratedIsQuietWhenHealthy(t *testing.T) {
	if firesAbove0(evalNeverHydrated([]series{{"pg-desk", 0}}, up, nil)) {
		t.Error("a gauge reading 0 (emitted when healthy) must not fire")
	}
}

func TestNeverHydratedIsQuietWhenPgDeskIsDown(t *testing.T) {
	// No gauge and no liveness series: nothing to report here (the liveness
	// rule speaks), and a liveness series reading 0 does not count as up.
	if firesAbove0(evalNeverHydrated(nil, nil, nil)) {
		t.Error("no gauge and no liveness series must not fire")
	}
	if firesAbove0(evalNeverHydrated(nil, down, nil)) {
		t.Error("an absent gauge with liveness 0 must not fire")
	}
}

func TestNeverHydratedIsSuppressedUnderSystemPause(t *testing.T) {
	pause := []series{{"pg-router", 1}}
	if firesAbove0(evalNeverHydrated([]series{{"pg-desk", 495}}, up, pause)) {
		t.Error("the gauge above 0 fired under SYSTEM_PAUSE")
	}
	if firesAbove0(evalNeverHydrated(nil, up, pause)) {
		t.Error("the absent gauge fired under SYSTEM_PAUSE")
	}
}
