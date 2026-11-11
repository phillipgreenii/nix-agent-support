package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

func testResources() ResourceSet {
	return ResourceSet{
		{Name: "disk", Scale: []string{"PLENTY", "OK", "TIGHT", "EXHAUSTED"}, Ignored: IgnoredAny, Sensor: "host", GraceFactor: 2, Unknown: UnknownBest},
		{Name: "git_auth_x", Scale: []string{"AVAILABLE", "UNAVAILABLE"}, Ignored: IgnoredAny, Sensor: "git", GraceFactor: 2, Unknown: UnknownWorst},
		{Name: "router", Scale: []string{"RUNNING", "PAUSED"}, Ignored: "== RUNNING", Sensor: RouterSensor, GraceFactor: 2, Unknown: UnknownBest},
	}
}

// bruteHolds is the reference: the operator applied to scale positions, with
// "greater" meaning better (a smaller index), written without AllowedLevels.
func bruteHolds(scale []string, held, op, level string) bool {
	h, l := slices.Index(scale, held), slices.Index(scale, level)
	switch op {
	case "==":
		return h == l
	case "!=":
		return h != l
	case "<":
		return h > l
	case "<=":
		return h >= l
	case ">":
		return h < l
	case ">=":
		return h <= l
	}
	panic("op")
}

// Every operator, every level of every scale, every held level and an absent
// one, for both unknown values: Decide over the compiled clause equals the
// brute-force filter, with an absent level replaced by the unknown value
// BEFORE any operator runs (including !=).
func TestClause_exhaustiveAgainstBruteForce(t *testing.T) {
	scales := [][]string{
		{"PLENTY", "OK", "TIGHT", "EXHAUSTED"},
		{"UP", "DOWN"},
		{"A", "B", "C"},
	}
	n := 0
	for _, scale := range scales {
		for _, unknown := range []string{UnknownBest, UnknownWorst} {
			rs := ResourceSet{{Name: "r", Scale: scale, Ignored: IgnoredAny, GraceFactor: 2, Unknown: unknown}}
			unknownLevel := scale[0]
			if unknown == UnknownWorst {
				unknownLevel = scale[len(scale)-1]
			}
			for _, op := range []string{"==", "!=", "<", "<=", ">", ">="} {
				for _, lvl := range scale {
					text := fmt.Sprintf("r %s %s", op, lvl)
					comp, errs := CompileRequires(rs, "role \"x\"", []string{text})
					if len(errs) > 0 {
						// A clause that allows nothing (e.g. "> best") is a validation error.
						if !strings.Contains(errs[0].Error(), "allows no level") || bruteAny(scale, op, lvl) {
							t.Fatalf("%s: unexpected errors %v", text, errs)
						}
						continue
					}
					helds := append(slices.Clone(scale), "") // "" = absent
					for _, held := range helds {
						snap := levels.Snapshot{Now: time.Unix(1000, 0), Levels: map[string]levels.Level{}}
						eff := unknownLevel
						if held != "" {
							snap.Levels["r"] = levels.Level{Value: held}
							eff = held
						}
						got := levels.Decide(snap, rs.Registry(), comp.Requirement).Admit
						if want := bruteHolds(scale, eff, op, lvl); got != want {
							t.Errorf("scale %v unknown=%s %q held=%q: admit=%t, want %t", scale, unknown, text, held, got, want)
						}
						n++
					}
				}
			}
		}
	}
	if n < 200 {
		t.Fatalf("only %d combinations ran; the sweep is not exhaustive", n)
	}
}

func bruteAny(scale []string, op, lvl string) bool {
	for _, h := range scale {
		if bruteHolds(scale, h, op, lvl) {
			return true
		}
	}
	return false
}

// A remediation clause (disk <= TIGHT) is false on an absent level of a best
// resource, and a credential (unknown = worst) with >= AVAILABLE fails absent.
func TestClause_absentLevelReadsUnknown(t *testing.T) {
	rs := testResources()
	comp, errs := CompileRequires(rs, "role \"r\"", []string{"disk <= TIGHT", "git_auth_x >= AVAILABLE"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	res := levels.Decide(levels.Snapshot{Now: time.Unix(1, 0)}, rs.Registry(), comp.Requirement)
	if res.Admit || len(res.Failing) != 2 {
		t.Fatalf("both clauses must fail on absent levels, got %+v", res)
	}
}

func TestClause_compileAndsClausesPerResourceIntoOneSet(t *testing.T) {
	rs := testResources()
	comp, errs := CompileRequires(rs, "role \"r\"", []string{"disk >= OK", "disk != PLENTY", "git_auth_x == AVAILABLE"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := comp.Allowed["disk"]; !slices.Equal(got, []string{"OK"}) {
		t.Errorf("disk set = %v, want [OK]", got)
	}
	if got := comp.Allowed["git_auth_x"]; !slices.Equal(got, []string{"AVAILABLE"}) {
		t.Errorf("git set = %v", got)
	}
	if len(comp.Requirement.Clauses) != 4 { // three written plus the router's implicit one
		t.Fatalf("clauses = %+v", comp.Requirement.Clauses)
	}
	c0 := comp.Requirement.Clauses[0]
	if c0.Index != 0 || c0.Text != "disk >= OK" || !slices.Equal(c0.Allowed, []string{"PLENTY", "OK"}) {
		t.Errorf("clause 0 must keep its index, text and own set: %+v", c0)
	}
}

func TestClause_ignoredAddsImplicitClause(t *testing.T) {
	rs := testResources()
	comp, errs := CompileRequires(rs, "role \"r\"", []string{"disk >= OK"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	last := comp.Requirement.Clauses[len(comp.Requirement.Clauses)-1]
	if last.Resource != "router" || last.Index != ImplicitClauseIndex || !slices.Equal(last.Allowed, []string{"RUNNING"}) {
		t.Fatalf("implicit router clause = %+v", last)
	}
	// Naming the resource replaces the default.
	comp, errs = CompileRequires(rs, "role \"r\"", []string{"router >= PAUSED"})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(comp.Requirement.Clauses) != 1 || !slices.Equal(comp.Requirement.Clauses[0].Allowed, []string{"RUNNING", "PAUSED"}) {
		t.Fatalf("explicit router clause must replace the default: %+v", comp.Requirement.Clauses)
	}
}

func TestClause_validationRejections(t *testing.T) {
	rs := testResources()
	for text, want := range map[string]string{
		"nosuch >= OK":                 `unknown resource "nosuch"`,
		"disk >= GREAT":                `unknown level "GREAT"`,
		"disk ~= OK":                   `unknown operator "~="`,
		"disk >=":                      "want",
		"disk >= OK extra":             "want",
		"disk > PLENTY":                "allows no level",
		"router > RUNNING":             "allows no level",
		"git_auth_x != AVAILABLE && x": "want",
		"":                             "want",
	} {
		_, errs := CompileRequires(rs, "role \"r\"", []string{text})
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), want) {
			t.Errorf("%q: errs = %v, want one containing %q", text, errs, want)
			continue
		}
		if !strings.Contains(errs[0].Error(), `role "r" requires[0]`) {
			t.Errorf("%q: message must name the registration and clause index: %v", text, errs[0])
		}
	}
	_, errs := CompileRequires(rs, "role \"r\"", []string{"disk >= OK", "disk == TIGHT"})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "requires[1]") || !strings.Contains(errs[0].Error(), "together allow no level") {
		t.Errorf("empty combined set: %v", errs)
	}
}

func TestClause_expandText(t *testing.T) {
	comp, _ := CompileRequires(testResources(), "role \"r\"", []string{"disk >= OK"})
	if got := ExpandClause(comp.Requirement.Clauses[0]); got != "disk >= OK (allowed: PLENTY, OK)" {
		t.Errorf("expansion = %q", got)
	}
	if got := ExpandClause(comp.Requirement.Clauses[1]); !strings.Contains(got, "router == RUNNING (allowed: RUNNING)") || !strings.Contains(got, "ignored") {
		t.Errorf("implicit expansion = %q", got)
	}
}

func TestRequires_loadOnRoleAndLevelSourceQuery(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase+levelsWorkQuery+`
[[role]]
name = "worker"
handler = "/bin/h"
binds = ["e"]
requires = ["disk >= OK"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Roles[0].Requires; len(got) != 1 || got[0] != "disk >= OK" {
		t.Errorf("role requires = %v", got)
	}
	if got := c.Requirements["worker"].Allowed["disk"]; !slices.Equal(got, []string{"PLENTY", "OK"}) {
		t.Errorf("compiled disk set = %v", got)
	}
	if c.Queries[0].LevelSource != "host" || c.Queries[1].LevelSource != "" {
		t.Errorf("level_source flags = %q / %q", c.Queries[0].LevelSource, c.Queries[1].LevelSource)
	}
}

func TestRequires_loadRejectsUnknownResourceAndLevelNamingClause(t *testing.T) {
	absentGlobalConfig(t)
	for _, tc := range []struct{ clause, want string }{
		{"nosuch >= OK", `unknown resource "nosuch"`},
		{"disk >= GREAT", `unknown level "GREAT"`},
	} {
		writeCfg(t, levelsCfgBase+levelsWorkQuery+`
[[role]]
name = "worker"
handler = "/bin/h"
binds = ["e"]
requires = ["`+tc.clause+`"]
`)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `role "worker" requires[0]`) {
			t.Errorf("%s: Load() = %v", tc.clause, err)
		}
	}
}

func TestRequires_pollingUnion(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase+levelsWorkQuery+`
[[query]]
name = "tick"
type = "timer"
emits = ["t"]

[[query]]
name = "unused"
type = "command"
emits = ["u"]
[query.command]
argv = ["x"]
format = "jsonl"

[[query]]
name = "offonly"
type = "command"
emits = ["o"]
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "strict"
handler = "/bin/h"
binds = ["e", "t", "u", "o"]
requires = ["disk >= PLENTY", "git_auth_x >= AVAILABLE"]

[[role]]
name = "loose"
handler = "/bin/h"
binds = ["e"]
requires = ["disk >= TIGHT"]

[[role]]
name = "disabled"
enabled = false
handler = "/bin/h"
binds = ["o"]
requires = ["disk >= PLENTY"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	u := c.PollingUnion()
	// "work" has two consumers: strict (PLENTY) and loose (TIGHT, wider). The
	// union of disk is PLENTY, OK, TIGHT; loose does not mention git_auth_x, so
	// that resource is "any level" (absent from the union).
	w := u["work"]
	if !w.Polled || w.Exempt {
		t.Fatalf("work = %+v", w)
	}
	if got := w.Allowed["disk"]; !slices.Equal(got, []string{"PLENTY", "OK", "TIGHT"}) {
		t.Errorf("disk union = %v", got)
	}
	if _, has := w.Allowed["git_auth_x"]; has {
		t.Errorf("a resource a consumer ignores must be unrestricted: %v", w.Allowed)
	}
	if p := u["probe"]; !p.Polled || !p.Exempt {
		t.Errorf("a level-source query is polled and exempt: %+v", p)
	}
	if p := u["tick"]; !p.Polled || !p.Exempt {
		t.Errorf("the timer query is exempt: %+v", p)
	}
	if p := u["offonly"]; !p.Polled {
		t.Errorf("an enabled consumer exists: %+v", p)
	}
}

func TestRequires_noConsumerOrOnlyDisabledMeansNotPolled(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase+`
[[query]]
name = "off"
type = "command"
emits = ["o"]
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "disabled"
enabled = false
handler = "/bin/h"
binds = ["o"]
requires = ["disk >= PLENTY"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p := c.PollingUnion()["off"]; p.Polled {
		t.Errorf("only disabled consumers: must not be polled: %+v", p)
	}
}

func TestRequires_warnsOnClauseWithNoSensor(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, levelsCfgBase+levelsWorkQuery+`
[[role]]
name = "worker"
handler = "/bin/h"
binds = ["e"]
requires = ["disk >= OK", "git_auth_x >= AVAILABLE"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := c.RequiresWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "requires[1]") || !strings.Contains(w[0], "no sensor configured") {
		t.Fatalf("warnings = %v", w)
	}
	c.MachineSensors = []string{"git"}
	if w := c.RequiresWarnings(); len(w) != 0 {
		t.Errorf("a sensor listed on the machine silences the warning: %v", w)
	}
}
