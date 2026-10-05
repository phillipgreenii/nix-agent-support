package failure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

const entity = "acme/widgets#42"

// fake stands in for pg-connector and pg-desk: it records every exec and
// answers from Go through a tiny shell child.
type fake struct {
	execs   []string
	respond func(line string) (stdout string, exit int)
}

func (f *fake) factory() apply.CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		f.execs = append(f.execs, line)
		out, code := "{\"result\":{\"id\":\"wb-esc\"}}", 0
		if f.respond != nil {
			out, code = f.respond(line)
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$OUT"; exit "$CODE"`)
		cmd.Env = append(os.Environ(), "OUT="+out, fmt.Sprintf("CODE=%d", code))
		return cmd
	}
}

func (f *fake) env(k *int) apply.Env {
	cfg := &config.Config{EscalateAfter: k}
	return apply.Env{Command: f.factory(), Config: cfg, Clock: time.Now, Stderr: &bytes.Buffer{}}
}

func viewWith(state map[string]string, links ...view.Link) *view.View {
	v := &view.View{Type: "pr", ID: entity, Links: links}
	if state != nil {
		v.Annotations.Decider = map[string]map[string]string{"pr-decider": state}
	}
	return v
}

func ev(rule string, o apply.Outcome, seq int64, hasSeq bool, err error) apply.Event {
	return apply.Event{Action: action.Action{Op: action.OpUpdate, Rule: rule}, Outcome: o, Err: err, Seq: seq, HasSeq: hasSeq}
}

func failed(rule string, seq int64) apply.Event {
	return ev(rule, apply.OutcomeFailed, seq, true, errors.New("backend down"))
}

func annot(key, value string) string {
	return fmt.Sprintf("pg-desk pr annotate %s --key decider.pr-decider.%s --value %s --origin decider:pr-decider --actor pg-decider", entity, key, value)
}

func finish(t *testing.T, f *fake, k *int, v *view.View, evs ...apply.Event) error {
	t.Helper()
	h := New(v, "pr", entity)
	return h.Finish(context.Background(), f.env(k), evs)
}

func creates(execs []string) []string {
	var out []string
	for _, l := range execs {
		if strings.HasPrefix(l, "pg-connector issue create") {
			out = append(out, l)
		}
	}
	return out
}

func TestCounterCountsThenEscalatesExactlyOnceOnTheKthRun(t *testing.T) {
	r := "review.head-advanced"

	f := &fake{}
	if err := finish(t, f, nil, viewWith(nil), failed(r, 1)); err != nil {
		t.Fatal(err)
	}
	if want := []string{annot("failures."+r, "1"), annot("failure_seq."+r, "1")}; !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("run 1 execs:\n got %q\nwant %q", f.execs, want)
	}

	f = &fake{}
	if err := finish(t, f, nil, viewWith(map[string]string{"failures." + r: "1", "failure_seq." + r: "1"}), failed(r, 2)); err != nil {
		t.Fatal(err)
	}
	if want := []string{annot("failures."+r, "2"), annot("failure_seq."+r, "2")}; !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("run 2 execs:\n got %q\nwant %q", f.execs, want)
	}

	f = &fake{}
	if err := finish(t, f, nil, viewWith(map[string]string{"failures." + r: "2", "failure_seq." + r: "2"}), failed(r, 3)); err != nil {
		t.Fatal(err)
	}
	cs := creates(f.execs)
	if len(cs) != 1 {
		t.Fatalf("want exactly one escalation create, got %q", f.execs)
	}
	for _, want := range []string{"--labels human", "--issue-type task", r, entity, "backend down"} {
		if !strings.Contains(cs[0], want) {
			t.Fatalf("escalation %q lacks %q", cs[0], want)
		}
	}
	if len(f.execs) != 4 || f.execs[0] != annot("failures."+r, "3") || f.execs[1] != annot("failure_seq."+r, "3") ||
		f.execs[2] != cs[0] || f.execs[3] != annot("escalated."+r, "1") {
		t.Fatalf("run 3 execs: %q", f.execs)
	}
}

func TestAnEscalatedStreakIsNotRewrittenOrEscalatedAgain(t *testing.T) {
	r := "r.fail"
	f := &fake{}
	v := viewWith(map[string]string{"failures." + r: "3", "failure_seq." + r: "3", "escalated." + r: "1"})
	if err := finish(t, f, nil, v, failed(r, 4)); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Fatalf("an escalated rule must write nothing: %q", f.execs)
	}
}

func TestASuccessfulRunResetsTheCounterAndTheEscalatedRecord(t *testing.T) {
	r := "r.fail"
	f := &fake{}
	v := viewWith(map[string]string{"failures." + r: "3", "failure_seq." + r: "3", "escalated." + r: "1"})
	if err := finish(t, f, nil, v, ev(r, apply.OutcomeApplied, 5, true, nil)); err != nil {
		t.Fatal(err)
	}
	want := []string{annot("failures."+r, "0"), annot("escalated."+r, "0"), annot("failure_seq."+r, "0")}
	if !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("execs:\n got %q\nwant %q", f.execs, want)
	}
}

func TestALaterStreakAfterAResetCanEscalateAgain(t *testing.T) {
	r := "r.fail"
	k := 1
	f := &fake{}
	// State as the reset left it: zeros.
	v := viewWith(map[string]string{"failures." + r: "0", "failure_seq." + r: "0", "escalated." + r: "0"})
	if err := finish(t, f, &k, v, failed(r, 9)); err != nil {
		t.Fatal(err)
	}
	if len(creates(f.execs)) != 1 {
		t.Fatalf("a later streak must escalate again: %q", f.execs)
	}
}

func TestASuccessWithNoStateWritesNothing(t *testing.T) {
	f := &fake{}
	if err := finish(t, f, nil, viewWith(nil), ev("r", apply.OutcomeApplied, 1, true, nil)); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Fatalf("writes: %q", f.execs)
	}
}

func TestADedupedActionCountsAsSuccessForTheStreak(t *testing.T) {
	r := "r.create"
	f := &fake{}
	v := viewWith(map[string]string{"failures." + r: "2", "failure_seq." + r: "7"})
	if err := finish(t, f, nil, v, ev(r, apply.OutcomeDeduped, 8, true, nil)); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) == 0 || f.execs[0] != annot("failures."+r, "0") {
		t.Fatalf("execs: %q", f.execs)
	}
}

func TestTheSameRoutedSeqDoesNotIncrementButADifferentOneDoes(t *testing.T) {
	r := "r.fail"
	state := map[string]string{"failures." + r: "1", "failure_seq." + r: "5"}

	f := &fake{}
	if err := finish(t, f, nil, viewWith(state), failed(r, 5)); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Fatalf("same seq must not write: %q", f.execs)
	}

	f = &fake{}
	if err := finish(t, f, nil, viewWith(state), failed(r, 6)); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) == 0 || f.execs[0] != annot("failures."+r, "2") {
		t.Fatalf("different seq must increment: %q", f.execs)
	}
}

func TestARunWithoutAFromItemAlwaysCountsAndRecordsNoSeq(t *testing.T) {
	r := "r.fail"
	f := &fake{}
	v := viewWith(map[string]string{"failures." + r: "1", "failure_seq." + r: "0"})
	if err := finish(t, f, nil, v, ev(r, apply.OutcomeFailed, 0, false, errors.New("x"))); err != nil {
		t.Fatal(err)
	}
	if want := []string{annot("failures."+r, "2")}; !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("execs: %q", f.execs)
	}
}

func TestASkippedDependencyIsNeitherAResetNorAnIncrement(t *testing.T) {
	r := "r.child"
	f := &fake{}
	v := viewWith(map[string]string{"failures." + r: "2", "failure_seq." + r: "4"})
	if err := finish(t, f, nil, v, ev(r, apply.OutcomeSkippedDependency, 9, true, errors.New("anchor"))); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) != 0 {
		t.Fatalf("a skipped dependency must write nothing: %q", f.execs)
	}
}

func TestASkippedDependencyAlongsideARealFailureStillCounts(t *testing.T) {
	r := "r.pair"
	f := &fake{}
	if err := finish(t, f, nil, viewWith(nil), failed(r, 1), ev(r, apply.OutcomeSkippedDependency, 1, true, errors.New("prior"))); err != nil {
		t.Fatal(err)
	}
	if len(f.execs) == 0 || f.execs[0] != annot("failures."+r, "1") {
		t.Fatalf("execs: %q", f.execs)
	}
}

func TestTheThresholdIsConfigK(t *testing.T) {
	r := "r.fail"
	two := 2
	f := &fake{}
	if err := finish(t, f, &two, viewWith(map[string]string{"failures." + r: "1", "failure_seq." + r: "1"}), failed(r, 2)); err != nil {
		t.Fatal(err)
	}
	if len(creates(f.execs)) != 1 {
		t.Fatalf("K=2 must escalate on the second failing run: %q", f.execs)
	}
	f = &fake{}
	if err := finish(t, f, &two, viewWith(nil), failed(r, 1)); err != nil {
		t.Fatal(err)
	}
	if len(creates(f.execs)) != 0 {
		t.Fatalf("K=2 must not escalate on the first failing run: %q", f.execs)
	}
}

func TestARuleWithStateButNoEventIsReset(t *testing.T) {
	f := &fake{}
	v := viewWith(map[string]string{
		"failures.gone": "2", "failure_seq.gone": "3", "escalated.gone": "1",
		"failures.other": "1", "failure_seq.other": "3",
	})
	if err := finish(t, f, nil, v, ev("other", apply.OutcomeSkippedDependency, 4, true, errors.New("x"))); err != nil {
		t.Fatal(err)
	}
	want := []string{annot("failures.gone", "0"), annot("escalated.gone", "0"), annot("failure_seq.gone", "0")}
	if !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("execs:\n got %q\nwant %q", f.execs, want)
	}
}

func TestAnEscalatedRuleWithNoEventIsReset(t *testing.T) {
	f := &fake{}
	v := viewWith(map[string]string{"failures.gone": "3", "escalated.gone": "1"})
	if err := finish(t, f, nil, v); err != nil {
		t.Fatal(err)
	}
	if want := []string{annot("failures.gone", "0"), annot("escalated.gone", "0")}; !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("execs: %q", f.execs)
	}
}

func TestTheEscalationIsParentedUnderTheAnchorWhenOneExists(t *testing.T) {
	r := "r.fail"
	k := 1
	anchor := view.Link{
		Type: "issue", ID: "wb-anchor", State: "open", Title: entity + ": Add retry",
		Metadata: map[string]string{"dedup_key": "pr:" + entity + ":anchor"},
	}
	f := &fake{}
	if err := finish(t, f, &k, viewWith(nil, anchor), failed(r, 1)); err != nil {
		t.Fatal(err)
	}
	cs := creates(f.execs)
	if len(cs) != 1 || !strings.Contains(cs[0], "--parent wb-anchor") {
		t.Fatalf("creates: %q", cs)
	}
	f = &fake{}
	if err := finish(t, f, &k, viewWith(nil), failed(r, 1)); err != nil {
		t.Fatal(err)
	}
	if cs := creates(f.execs); len(cs) != 1 || strings.Contains(cs[0], "--parent") {
		t.Fatalf("no anchor means no parent: %q", cs)
	}
}

func TestAFailedEscalationIsReportedLeavesEscalatedUnsetAndRetries(t *testing.T) {
	r := "r.fail"
	k := 1
	f := &fake{respond: func(line string) (string, int) {
		if strings.HasPrefix(line, "pg-connector issue create") {
			return `{"error":{"code":"unavailable","message":"tracker down"}}`, 1
		}
		return `{"result":{}}`, 0
	}}
	err := finish(t, f, &k, viewWith(nil), failed(r, 1))
	if err == nil || !strings.Contains(err.Error(), "tracker down") {
		t.Fatalf("err = %v", err)
	}
	for _, l := range f.execs {
		if strings.Contains(l, "escalated.") {
			t.Fatalf("escalated must stay unset after a failed create: %q", f.execs)
		}
	}
	// The next failing run (same seq: the count stands) retries the create.
	f = &fake{}
	if err := finish(t, f, &k, viewWith(map[string]string{"failures." + r: "1", "failure_seq." + r: "1"}), failed(r, 1)); err != nil {
		t.Fatal(err)
	}
	if len(creates(f.execs)) != 1 {
		t.Fatalf("retry: %q", f.execs)
	}
}

func TestAFailedCounterWriteIsReportedAndDoesNotEscalate(t *testing.T) {
	k := 1
	f := &fake{respond: func(line string) (string, int) {
		if strings.HasPrefix(line, "pg-desk") {
			return "", 1
		}
		return `{"result":{"id":"x"}}`, 0
	}}
	err := finish(t, f, &k, viewWith(nil), failed("r", 1))
	if err == nil || len(creates(f.execs)) != 0 {
		t.Fatalf("err %v execs %q", err, f.execs)
	}
}

func TestOnEscalateIsCalledPerCreatedItem(t *testing.T) {
	k := 1
	f := &fake{}
	var got []string
	h := New(viewWith(nil), "pr", entity)
	h.OnEscalate(func(rule string) { got = append(got, rule) })
	if err := h.Finish(context.Background(), f.env(&k), []apply.Event{failed("a", 1), failed("b", 1)}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("got %q", got)
	}
}

func TestRulesAreHandledIndependentlyInEventOrder(t *testing.T) {
	f := &fake{}
	v := viewWith(map[string]string{"failures.ok": "1", "failure_seq.ok": "1"})
	if err := finish(t, f, nil, v, failed("bad", 2), ev("ok", apply.OutcomeApplied, 2, true, nil)); err != nil {
		t.Fatal(err)
	}
	want := []string{annot("failures.bad", "1"), annot("failure_seq.bad", "2"), annot("failures.ok", "0"), annot("failure_seq.ok", "0")}
	if !reflect.DeepEqual(f.execs, want) {
		t.Fatalf("execs:\n got %q\nwant %q", f.execs, want)
	}
}

func TestAfterIsANoOp(t *testing.T) {
	if err := New(nil, "pr", entity).After(context.Background(), apply.Env{}, apply.Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestAHookOverAnApplyRunCountsTheRunsFailureOnce(t *testing.T) {
	f := &fake{respond: func(line string) (string, int) {
		if strings.HasPrefix(line, "pg-connector issue update") {
			return `{"error":{"code":"unavailable","message":"down"}}`, 1
		}
		return `{"result":{}}`, 0
	}}
	tgt := "wb-1"
	h := New(viewWith(nil), "pr", entity)
	res := apply.Run(context.Background(), apply.Input{
		Type: "pr", ID: entity, View: viewWith(nil), Env: f.env(nil), Hooks: []apply.Hook{h},
		Actions: []action.Action{
			{Op: action.OpUpdate, Rule: "r.fail", Target: &tgt, Fields: action.Fields{Title: "a"}},
			{Op: action.OpUpdate, Rule: "r.fail", Target: &tgt, Fields: action.Fields{Title: "b"}},
		},
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	n := 0
	for _, l := range f.execs {
		if strings.Contains(l, "--key decider.pr-decider.failures.r.fail ") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a rule with two failed actions must count once: %q", f.execs)
	}
}
