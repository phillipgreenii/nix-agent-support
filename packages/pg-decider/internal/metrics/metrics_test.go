package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
)

func ev(rule string, o apply.Outcome) apply.Event {
	return apply.Event{Action: action.Action{Op: action.OpUpdate, Rule: rule}, Outcome: o, Err: errors.New("e")}
}

func TestFinishEmitsTheExactCounterLineForAMixedRunWithOneEscalation(t *testing.T) {
	var buf bytes.Buffer
	h := New(nil, "pr", "acme/widgets#42")
	h.Escalated("r.fail")
	evs := []apply.Event{
		ev("r.fail", apply.OutcomeFailed),
		ev("r.ok", apply.OutcomeApplied),
		ev("r.ok", apply.OutcomeApplied),
		ev("r.dup", apply.OutcomeDeduped),
		ev("r.fail", apply.OutcomeSkippedDependency),
	}
	if err := h.Finish(context.Background(), apply.Env{Stderr: &buf}, evs); err != nil {
		t.Fatal(err)
	}
	want := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"acme/widgets#42","rules":{` +
		`"r.dup":{"planned":1,"applied":0,"deduped":1,"failed":0,"skipped":0},` +
		`"r.fail":{"planned":2,"applied":0,"deduped":0,"failed":1,"skipped":1},` +
		`"r.ok":{"planned":2,"applied":2,"deduped":0,"failed":0,"skipped":0}},` +
		`"escalations":1}` + "\n"
	if buf.String() != want {
		t.Fatalf("got  %q\nwant %q", buf.String(), want)
	}
}

func TestARunThatPlannedNothingStillEmitsOneLineWithAnEmptyRulesObject(t *testing.T) {
	var buf bytes.Buffer
	if err := New(nil, "pr", "x").Finish(context.Background(), apply.Env{Stderr: &buf}, nil); err != nil {
		t.Fatal(err)
	}
	want := `{"contract":"pg-decider.run-counters/v1","type":"pr","id":"x","rules":{},"escalations":0}` + "\n"
	if buf.String() != want {
		t.Fatalf("got %q", buf.String())
	}
	var l Line
	if err := json.Unmarshal(buf.Bytes(), &l); err != nil || l.Contract != Contract || l.Rules == nil {
		t.Fatalf("decode: %v %+v", err, l)
	}
}

func TestPlannedIsTheSumOfTheOutcomes(t *testing.T) {
	evs := []apply.Event{
		ev("r", apply.OutcomeApplied), ev("r", apply.OutcomeDeduped), ev("r", apply.OutcomeFailed), ev("r", apply.OutcomeSkippedDependency),
		ev("r", apply.OutcomeSkippedStale),
	}
	c := Counters(evs)["r"]
	if c.Planned != 5 || c.Skipped != 2 || c.Applied+c.Deduped+c.Failed+c.Skipped != c.Planned {
		t.Fatalf("%+v", c)
	}
}

func TestEscalationsAccumulateAcrossRules(t *testing.T) {
	var buf bytes.Buffer
	h := New(nil, "pr", "x")
	h.Escalated("a")
	h.Escalated("b")
	if err := h.Finish(context.Background(), apply.Env{Stderr: &buf}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), `"escalations":2}`+"\n") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestAfterIsANoOp(t *testing.T) {
	if err := New(nil, "pr", "x").After(context.Background(), apply.Env{}, apply.Event{}); err != nil {
		t.Fatal(err)
	}
}
