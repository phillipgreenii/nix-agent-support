package rules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/apply"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

const threadPR = "acme/widgets#42"

func threadRec(thread, msg, source string, version int, parent string) map[string]any {
	return map[string]any{
		"version": version, "thread_id": thread, "message_id": msg, "author": "example-reviewer",
		"timestamp": "2026-10-01T12:30:00Z", "excerpt": "please take another look",
		"source": source, "parent": map[string]any{"kind": "pr", "id": parent},
	}
}

// threadView is an open PR view (the committed minimal fixture) carrying the
// given thread change records and decider annotations.
func threadView(t *testing.T, recs []map[string]any, annotated map[string]string) *view.View {
	t.Helper()
	return suitePatch(t, "pr_new_everything", func(m map[string]any) {
		m["thread_changes"] = recs
		d := map[string]any{}
		if len(annotated) > 0 {
			inner := map[string]any{}
			for k, v := range annotated {
				inner[k] = v
			}
			d["pr-decider"] = inner
		}
		suiteAnnotations(m)["decider"] = d
	})
}

func threadActions(res action.PlanResult) []action.Action {
	return suiteActionsOfRule(res, action.ThreadActivityRuleID)
}

func TestThreadActivityOneMessageOneAnnotationNoWorkItem(t *testing.T) {
	v := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)}, nil)
	acts := threadActions(suiteDecide(v))
	if len(acts) != 1 {
		t.Fatalf("want exactly one thread-activity action, got %+v", acts)
	}
	a := acts[0]
	if a.Op != action.OpAnnotate || a.Kind != "" || a.Target == nil ||
		*a.Target != "decider.pr-decider.thread-activity.example-thread-1.example-msg-1" {
		t.Fatalf("unexpected action %+v", a)
	}
	var val action.ThreadActivityValue
	if err := json.Unmarshal([]byte(*a.Fields.Value), &val); err != nil {
		t.Fatal(err)
	}
	want := action.ThreadActivityValue{
		ThreadID: "example-thread-1", MessageID: "example-msg-1", Author: "example-reviewer",
		Timestamp: "2026-10-01T12:30:00Z", Excerpt: "please take another look",
	}
	if val != want {
		t.Fatalf("value = %+v, want %+v", val, want)
	}
	for _, x := range suiteDecide(v).Actions {
		if x.Rule == action.ThreadActivityRuleID && x.Op != action.OpAnnotate {
			t.Fatalf("thread-activity produced a non-annotate action %+v", x)
		}
	}
	// The rule alone creates no work item: compare with the same view minus the record.
	base := suiteDecide(threadView(t, nil, nil))
	if got, want := len(suiteDecide(v).Actions), len(base.Actions)+1; got != want {
		t.Fatalf("actions = %d, want the baseline %d plus one annotation", got, want)
	}
}

func TestThreadActivitySameMessageTwiceOneAnnotation(t *testing.T) {
	r := threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)
	acts := threadActions(suiteDecide(threadView(t, []map[string]any{r, r}, nil)))
	if len(acts) != 1 {
		t.Fatalf("a duplicated record produced %d annotations: %+v", len(acts), acts)
	}
}

func TestThreadActivityAlreadyAnnotatedNoAction(t *testing.T) {
	v := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)},
		map[string]string{action.ThreadActivityViewKey("example-thread-1", "example-msg-1"): "{}"})
	res := suiteDecide(v)
	if acts := threadActions(res); len(acts) != 0 {
		t.Fatalf("an annotated message produced %+v", acts)
	}
	if reason, ok := suiteSkipReason(res, action.ThreadActivityRuleID); !ok || reason != action.ReasonAlreadyHandled {
		t.Fatalf("skip = %q (listed %v), want already handled", reason, ok)
	}
}

func TestThreadActivityUnacceptedSourceNoAction(t *testing.T) {
	v := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "example-forge", 1, threadPR)}, nil)
	if acts := threadActions(suiteDecide(v)); len(acts) != 0 {
		t.Fatalf("a record of an unaccepted source produced %+v", acts)
	}
}

func TestThreadActivityAcceptedSourcesAreData(t *testing.T) {
	v := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "example-forge", 1, threadPR)}, nil)
	cfg := &config.Config{ThreadActivitySources: []string{"example-forge"}}
	res := decide.DecideWith(v, decide.EntityTypePR, cfg)
	if acts := threadActions(res); len(acts) != 1 {
		t.Fatalf("a configured source must be accepted: %+v", acts)
	}
	// ...and the chat default is then no longer accepted.
	v = threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)}, nil)
	if acts := threadActions(decide.DecideWith(v, decide.EntityTypePR, cfg)); len(acts) != 0 {
		t.Fatalf("source list replaced, yet slack still accepted: %+v", acts)
	}
}

func TestThreadActivityUnknownVersionReportedNotCrash(t *testing.T) {
	bad := threadRec("example-thread-1", "example-msg-1", "slack", 2, threadPR)
	bad["parent"] = "a shape version 1 does not know" // must not break view parsing
	v := threadView(t, []map[string]any{bad}, nil)
	res := suiteDecide(v)
	if acts := threadActions(res); len(acts) != 0 {
		t.Fatalf("an unknown version produced %+v", acts)
	}
	for _, s := range res.Skipped {
		if s.Rule == action.ThreadActivityRuleID {
			got, _ := json.Marshal(s.Facts)
			if !strings.Contains(string(got), `"unknown_versions":[2]`) {
				t.Fatalf("the unknown version is not reported: %s", got)
			}
			return
		}
	}
	t.Fatal("thread-activity is not listed in skipped")
}

func TestThreadActivityKnownRecordStillActsBesideUnknownVersion(t *testing.T) {
	v := threadView(t, []map[string]any{
		threadRec("example-thread-1", "example-msg-1", "slack", 9, threadPR),
		threadRec("example-thread-1", "example-msg-2", "slack", 1, threadPR),
	}, nil)
	acts := threadActions(suiteDecide(v))
	if len(acts) != 1 || !strings.HasSuffix(*acts[0].Target, "example-msg-2") {
		t.Fatalf("want one annotation for the known record, got %+v", acts)
	}
	if got, _ := json.Marshal(acts[0].Facts["unknown_versions"]); string(got) != "[9]" {
		t.Fatalf("unknown version not reported on the action: %s", got)
	}
}

func TestThreadActivityTwoLinkedPRsOneAnnotationEach(t *testing.T) {
	// One thread linked to two PRs is two records, one per parent link; each
	// PR's view carries the record naming it and yields one annotation, and a
	// record naming another PR yields none.
	other := "acme/widgets#43"
	recA := threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)
	recB := threadRec("example-thread-1", "example-msg-1", "slack", 1, other)

	vA := threadView(t, []map[string]any{recA, recB}, nil)
	if acts := threadActions(suiteDecide(vA)); len(acts) != 1 {
		t.Fatalf("PR 42 got %d annotations: %+v", len(acts), acts)
	}
	vB := suitePatch(t, "pr_new_everything", func(m map[string]any) {
		m["id"] = other
		m["snapshot"].(map[string]any)["id"] = other
		m["snapshot"].(map[string]any)["number"] = 43
		m["thread_changes"] = []map[string]any{recA, recB}
	})
	acts := threadActions(suiteDecide(vB))
	if len(acts) != 1 || !strings.HasSuffix(*acts[0].Target, "example-msg-1") {
		t.Fatalf("PR 43 got %+v", acts)
	}
	// A view of a PR that no record names gets none.
	vC := threadView(t, []map[string]any{recB}, nil)
	if acts := threadActions(suiteDecide(vC)); len(acts) != 0 {
		t.Fatalf("a record naming another PR produced %+v", acts)
	}
}

func TestThreadActivityNoRecordsOrTerminalPRNoAction(t *testing.T) {
	if acts := threadActions(suiteDecide(threadView(t, nil, nil))); len(acts) != 0 {
		t.Fatalf("no records produced %+v", acts)
	}
	v := suitePatch(t, "pr_new_everything", func(m map[string]any) {
		m["thread_changes"] = []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)}
		m["snapshot"].(map[string]any)["state"] = "merged"
		m["snapshot"].(map[string]any)["merged"] = true
	})
	if acts := threadActions(suiteDecide(v)); len(acts) != 0 {
		t.Fatalf("a merged PR produced %+v", acts)
	}
}

func TestThreadActivityExcerptIsShort(t *testing.T) {
	r := threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)
	r["excerpt"] = strings.Repeat("x", 500)
	acts := threadActions(suiteDecide(threadView(t, []map[string]any{r}, nil)))
	var val action.ThreadActivityValue
	if err := json.Unmarshal([]byte(*acts[0].Fields.Value), &val); err != nil {
		t.Fatal(err)
	}
	if len(val.Excerpt) != threadActivityExcerptMax {
		t.Fatalf("excerpt length %d, want %d", len(val.Excerpt), threadActivityExcerptMax)
	}
}

// Plan mode lists the proposed annotation; the apply path holds it back and
// writes nothing. Apply mode (the operator's flip) lets the write through,
// and a second apply on the view the first one produced writes nothing.
func TestThreadActivityPlanModeListsAndApplyWritesNone(t *testing.T) {
	v := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)}, nil)
	res := suiteDecide(v) // what `plan` prints
	if len(threadActions(res)) != 1 {
		t.Fatalf("plan must list the proposed annotation: %+v", res.Actions)
	}

	var calls []string
	env := apply.Env{
		Runner: func(_ context.Context, c apply.Call) apply.CallResult {
			calls = append(calls, c.Name+" "+strings.Join(c.Args, " "))
			return apply.CallResult{Stdout: []byte(`{"result":{}}`)}
		},
		Config: &config.Config{}, Clock: func() time.Time { return time.Unix(0, 0) },
	}
	run := func(cfg *config.Config) {
		held := HoldPlanOnlyRules(res, cfg)
		var only []action.Action
		only = append(only, threadActions(held)...)
		apply.Run(context.Background(), apply.Input{Type: "pr", ID: threadPR, View: v, Actions: only, Env: env})
	}

	run(&config.Config{}) // plan mode (the default)
	if len(calls) != 0 {
		t.Fatalf("plan mode wrote: %q", calls)
	}
	held := HoldPlanOnlyRules(res, &config.Config{})
	if len(threadActions(held)) != 0 {
		t.Fatalf("plan mode left actions for apply: %+v", held.Actions)
	}
	if reason, ok := suiteSkipReason(held, action.ThreadActivityRuleID); !ok || reason != action.ReasonNotMatched {
		t.Fatalf("held rule skip = %q (listed %v)", reason, ok)
	}

	run(&config.Config{ThreadActivityMode: config.ThreadActivityModeApply})
	if len(calls) != 1 || !strings.Contains(calls[0], "pr annotate "+threadPR+" --key decider.pr-decider.thread-activity.example-thread-1.example-msg-1 --value ") {
		t.Fatalf("apply mode calls = %q", calls)
	}

	// The view after that write lists the annotation: nothing more is planned.
	annotated := threadView(t, []map[string]any{threadRec("example-thread-1", "example-msg-1", "slack", 1, threadPR)},
		map[string]string{action.ThreadActivityViewKey("example-thread-1", "example-msg-1"): "{}"})
	if acts := threadActions(suiteDecide(annotated)); len(acts) != 0 {
		t.Fatalf("second pass planned %+v", acts)
	}
}
