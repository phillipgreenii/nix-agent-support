package levels

import (
	"fmt"
	"testing"
	"time"
)

var testRegistry = Registry{
	"disk":   {Levels: []string{"PLENTY", "OK", "TIGHT", "EXHAUSTED"}, Unknown: "PLENTY"},
	"router": {Levels: []string{"RUNNING", "PAUSED"}, Unknown: "RUNNING"},
	"cred":   {Levels: []string{"AVAILABLE", "UNAVAILABLE"}, Unknown: "UNAVAILABLE"},
}

var allOps = []Op{OpEq, OpNe, OpLt, OpLe, OpGt, OpGe}

// oracleHolds evaluates "level op want" directly by scale position, without
// AllowedLevels or any allowed set.
func oracleHolds(s Scale, level string, op Op, want string) bool {
	idx := func(v string) int {
		for i, l := range s.Levels {
			if l == v {
				return i
			}
		}
		panic("level not on scale: " + v)
	}
	// Greater means better: a smaller index is greater.
	have, w := idx(level), idx(want)
	switch op {
	case OpEq:
		return have == w
	case OpNe:
		return have != w
	case OpLt:
		return have > w
	case OpLe:
		return have >= w
	case OpGt:
		return have < w
	case OpGe:
		return have <= w
	}
	panic("op")
}

type spec struct {
	op    Op
	level string
}

func (s spec) text(res string) string { return fmt.Sprintf("%s %s %s", res, s.op, s.level) }

func specsFor(s Scale) []spec {
	var out []spec
	for _, op := range allOps {
		for _, l := range s.Levels {
			out = append(out, spec{op, l})
		}
	}
	return out
}

func TestAllowedLevels(t *testing.T) {
	s := testRegistry["disk"]
	cases := []struct {
		op   Op
		lvl  string
		want string
	}{
		{OpGe, "OK", "[PLENTY OK]"},
		{OpLe, "TIGHT", "[TIGHT EXHAUSTED]"},
		{OpEq, "OK", "[OK]"},
		{OpNe, "OK", "[PLENTY TIGHT EXHAUSTED]"},
		{OpGt, "OK", "[PLENTY]"},
		{OpLt, "OK", "[TIGHT EXHAUSTED]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(AllowedLevels(s, c.op, c.lvl)); got != c.want {
			t.Errorf("%s %s: got %s want %s", c.op, c.lvl, got, c.want)
		}
	}
	if AllowedLevels(s, OpGe, "NOPE") != nil || AllowedLevels(s, "~", "OK") != nil {
		t.Error("unknown level or op must give nil")
	}
}

// TestExhaustiveSmallUniverse: every resource kind, every operator, every
// level, up to two clauses per resource, with and without an absent level.
func TestExhaustiveSmallUniverse(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * time.Minute)
	cases := 0
	for res, scale := range testRegistry {
		specs := specsFor(scale)
		var combos [][]spec
		combos = append(combos, nil)
		for _, a := range specs {
			combos = append(combos, []spec{a})
			for _, b := range specs {
				combos = append(combos, []spec{a, b})
			}
		}
		states := []State{StateHeld, StateStale}
		held := append([]string{""}, scale.Levels...) // "" is absent
		for _, combo := range combos {
			var req Requirement
			for i, sp := range combo {
				req.Clauses = append(req.Clauses, Clause{
					Index: i, Text: sp.text(res), Resource: res,
					Allowed: AllowedLevels(scale, sp.op, sp.level),
				})
			}
			for _, h := range held {
				for _, st := range states {
					if h == "" && st == StateStale {
						continue
					}
					snap := Snapshot{Now: now, Levels: map[string]Level{}}
					eff := scale.Unknown
					if h != "" {
						eff = h
						snap.Levels[res] = Level{Value: h, State: st, Source: "src", Owner: "own", Since: since}
					}
					got := Decide(snap, testRegistry, req)
					var wantFail []int
					for i, sp := range combo {
						if !oracleHolds(scale, eff, sp.op, sp.level) {
							wantFail = append(wantFail, i)
						}
					}
					cases++
					if got.Admit != (len(wantFail) == 0) {
						t.Fatalf("%s %v held=%q: admit=%v want failing %v", res, combo, h, got.Admit, wantFail)
					}
					if len(got.Failing) != len(wantFail) {
						t.Fatalf("%s %v held=%q: failing %v want indexes %v", res, combo, h, got.Failing, wantFail)
					}
					for k, e := range got.Failing {
						if e.ClauseIndex != wantFail[k] || e.Level != eff || e.Resource != res || e.ClauseText != combo[wantFail[k]].text(res) {
							t.Fatalf("%s %v held=%q: bad entry %+v", res, combo, h, e)
						}
						switch {
						case h == "":
							if e.Kind != KindUnknown || e.Age != 0 || e.Owner != "" {
								t.Fatalf("absent entry %+v", e)
							}
						case st == StateStale:
							if e.Kind != KindStale || e.Age != 7*time.Minute || e.Owner != "own" || e.Source != "src" {
								t.Fatalf("stale entry %+v", e)
							}
						default:
							if e.Kind != KindLevel || e.Age != 7*time.Minute || e.Owner != "own" || e.Source != "src" {
								t.Fatalf("level entry %+v", e)
							}
						}
					}
				}
			}
		}
	}
	if cases < 1000 {
		t.Fatalf("universe too small: %d", cases)
	}
}

func TestAbsentLevelReadsUnknown(t *testing.T) {
	now := time.Now()
	snap := Snapshot{Now: now}
	// A remediation clause on a best-unknown resource is false when absent.
	rem := Requirement{Clauses: []Clause{{
		Index: 0, Text: "disk <= TIGHT", Resource: "disk",
		Allowed: AllowedLevels(testRegistry["disk"], OpLe, "TIGHT"),
	}}}
	if r := Decide(snap, testRegistry, rem); r.Admit || r.Failing[0].Level != "PLENTY" || r.Failing[0].Kind != KindUnknown {
		t.Fatalf("remediation on absent: %+v", r)
	}
	// Credentials read the worst: a requirement for AVAILABLE fails.
	need := Requirement{Clauses: []Clause{{
		Index: 0, Text: "cred >= AVAILABLE", Resource: "cred",
		Allowed: AllowedLevels(testRegistry["cred"], OpGe, "AVAILABLE"),
	}}}
	if r := Decide(snap, testRegistry, need); r.Admit || r.Failing[0].Level != "UNAVAILABLE" {
		t.Fatalf("credential on absent: %+v", r)
	}
	if r := Decide(snap, testRegistry, Requirement{}); !r.Admit || r.Failing != nil {
		t.Fatalf("empty requirement: %+v", r)
	}
}

func TestOriginalClausesListedNotResources(t *testing.T) {
	scale := testRegistry["disk"]
	req := Requirement{Clauses: []Clause{
		{Index: 3, Text: "disk >= OK", Resource: "disk", Allowed: AllowedLevels(scale, OpGe, "OK")},
		{Index: 5, Text: "disk != EXHAUSTED", Resource: "disk", Allowed: AllowedLevels(scale, OpNe, "EXHAUSTED")},
	}}
	snap := Snapshot{Now: time.Now(), Levels: map[string]Level{"disk": {Value: "EXHAUSTED"}}}
	r := Decide(snap, testRegistry, req)
	if len(r.Failing) != 2 || r.Failing[0].ClauseIndex != 3 || r.Failing[1].ClauseIndex != 5 || r.Reason != "" {
		t.Fatalf("%+v", r)
	}
}
