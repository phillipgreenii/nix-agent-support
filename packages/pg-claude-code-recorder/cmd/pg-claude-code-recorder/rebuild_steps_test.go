package main

import (
	"math"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func attrMap(a []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(a); i += 2 {
		m[a[i].(string)] = a[i+1]
	}
	return m
}

func TestStepAttrsOtherIsTheIntervalRemainder(t *testing.T) {
	var d store.StepStats
	d.StepNS[store.StepRead] = 1e9
	d.StepNS[store.StepParse] = 2e9
	d.StepNS[store.StepInsert] = 4e9
	m := attrMap(stepAttrs(d, 10))
	if m["step_read_sec"] != 1.0 || m["step_parse_sec"] != 2.0 || m["step_insert_sec"] != 4.0 || m["step_other_sec"] != 3.0 {
		t.Fatalf("step attrs %v", m)
	}
}

func TestProfileAttrsStepsSumToParseSec(t *testing.T) {
	var p store.RebuildProfile
	p.Steps.StepNS = [store.NumSteps]int64{1e9, 2e9, 4e9, 5e8, 5e8, 0}
	p.Steps.TableNS[store.TableFields] = 3e9
	p.Steps.TableRows[store.TableFields] = 7
	p.Steps.BytesRead, p.Steps.Lines, p.Steps.Records = 100, 5, 6
	slow := []parser.SlowFile{{Hash: "abc", Sec: 1.5, Bytes: 9}}
	m := attrMap(profileAttrs(p, slow, 20*time.Second, 10*time.Second, 6))
	sum := 0.0
	for _, k := range []string{"step_read_sec", "step_parse_sec", "step_insert_sec", "step_savepoint_sec", "step_commit_sec", "step_other_sec"} {
		sum += m[k].(float64)
	}
	if math.Abs(sum-10) > 1e-9 || m["step_other_sec"] != 2.0 || m["step_other_share"] != 0.2 {
		t.Fatalf("steps sum to %v, attrs %v", sum, m)
	}
	if m["insert_fields_sec"] != 3.0 || m["insert_fields_statements"] != int64(7) || m["bytes_read"] != int64(100) || m["lines"] != int64(5) {
		t.Fatalf("work attrs %v", m)
	}
	if got, ok := m["slow_1"].(string); !ok || got != "abc sec=1.500 bytes=9" {
		t.Fatalf("slowest %v", m["slow_1"])
	}
}
