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
	"github.com/phillipgreenii/pg-decider/internal/item"
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

func focusEv(transition string, o apply.Outcome) apply.Event {
	return apply.Event{
		Action:  action.Action{Op: action.OpUpdate, Rule: FocusRuleID, Facts: map[string]any{"transition": transition}},
		Outcome: o,
	}
}

func skip(rule, cause string, withBead bool) action.Skip {
	facts := map[string]any{"cause": cause}
	if withBead {
		facts["bead"] = map[string]any{"id": "bd-1", "state": "open"}
	}
	return action.Skip{Rule: rule, Reason: action.ReasonAlreadyHandled, Facts: facts}
}

func finishLine(t *testing.T, h *Hook, evs []apply.Event) (string, Line) {
	t.Helper()
	var buf bytes.Buffer
	if err := h.Finish(context.Background(), apply.Env{Stderr: &buf}, evs); err != nil {
		t.Fatal(err)
	}
	var l Line
	if err := json.Unmarshal(buf.Bytes(), &l); err != nil || l.Contract != Contract {
		t.Fatalf("the line must parse as JSON with the contract string: %v %q", err, buf.String())
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("one line expected: %q", buf.String())
	}
	return buf.String(), l
}

func TestTransitionsCountEachFocusTransitionUnderItsOutcome(t *testing.T) {
	evs := []apply.Event{
		focusEv("mint", apply.OutcomeApplied),
		focusEv("hold", apply.OutcomeApplied),
		focusEv("hold", apply.OutcomeSkippedStale),
		focusEv("release", apply.OutcomeApplied),
		focusEv("hold_terminal", apply.OutcomeApplied),
		focusEv("mint", apply.OutcomeDeduped),
		ev("r.pr", apply.OutcomeApplied),
	}
	got, l := finishLine(t, New(nil, "issue", "ACME-7"), evs)
	want := `"focus.item":{"planned":6,"applied":4,"deduped":1,"failed":0,"skipped":1,` +
		`"transitions":{"hold":{"applied":1,"skipped-stale":1},"hold_terminal":{"applied":1},` +
		`"mint":{"applied":1,"deduped":1},"release":{"applied":1}}}`
	if !strings.Contains(got, want) {
		t.Fatalf("got %q\nwant it to contain %q", got, want)
	}
	if strings.Contains(got, `"r.pr":{"planned":1,"applied":1,"deduped":0,"failed":0,"skipped":0,`) {
		t.Fatalf("a PR rule must not gain transitions or skips: %q", got)
	}
	if l.Seq != 0 || l.FromItem != "" || l.Failures != nil {
		t.Fatalf("no routed item and no failure: %+v", l)
	}
}

func TestATransitionOutsideTheFourOrOfAnotherRuleIsNotCounted(t *testing.T) {
	other := focusEv("hold", apply.OutcomeApplied)
	other.Action.Rule = "r.other"
	odd := focusEv("sideways", apply.OutcomeApplied)
	got, _ := finishLine(t, New(nil, "issue", "x"), []apply.Event{other, odd})
	if strings.Contains(got, "transitions") {
		t.Fatalf("got %q", got)
	}
}

func TestSkipsCountOnlyFocusItemSkipsOfASourceWithAFocusBead(t *testing.T) {
	h := New(nil, "issue", "ACME-7")
	h.Skipped([]action.Skip{
		skip(FocusRuleID, "claimed", true),
		skip(FocusRuleID, "claimed", true),
		skip(FocusRuleID, "in-play", true),
		skip(FocusRuleID, "not-selected", false),
		skip("pr.review", "x", true),
		{Rule: FocusRuleID, Reason: action.ReasonAlreadyHandled, Facts: map[string]any{"bead": map[string]any{"id": "b"}}},
	})
	got, _ := finishLine(t, h, nil)
	want := `"rules":{"focus.item":{"planned":0,"applied":0,"deduped":0,"failed":0,"skipped":0,` +
		`"skips":{"already handled":1,"claimed":2,"in-play":1}}}`
	if !strings.Contains(got, want) {
		t.Fatalf("got %q\nwant it to contain %q", got, want)
	}
}

func TestSeqAndFromItemComeFromTheRoutedItemAndAreOmittedWithoutOne(t *testing.T) {
	it := &item.Routed{ID: "item-9"}
	it.Metadata.Seq = 17
	h := New(nil, "pr", "x")
	h.Routed(it)
	got, l := finishLine(t, h, nil)
	if !strings.HasSuffix(got, `"escalations":0,"seq":17,"from_item":"item-9"}`+"\n") || l.Seq != 17 || l.FromItem != "item-9" {
		t.Fatalf("got %q", got)
	}
	h = New(nil, "pr", "x")
	h.Routed(nil)
	got, _ = finishLine(t, h, nil)
	if strings.Contains(got, "seq") || strings.Contains(got, "from_item") {
		t.Fatalf("got %q", got)
	}
}

func TestEmitFailureLinePrintsOneCountedFailureAndNoRules(t *testing.T) {
	var buf bytes.Buffer
	if err := EmitFailureLine(&buf, "issue", "ACME-7", "view-lacks-focus_selected"); err != nil {
		t.Fatal(err)
	}
	want := `{"contract":"pg-decider.run-counters/v1","type":"issue","id":"ACME-7","rules":{},"escalations":0,` +
		`"failures":{"view-lacks-focus_selected":1}}` + "\n"
	if buf.String() != want {
		t.Fatalf("got %q\nwant %q", buf.String(), want)
	}
}
