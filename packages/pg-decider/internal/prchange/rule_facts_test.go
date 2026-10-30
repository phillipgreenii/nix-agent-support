package prchange

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A skipped rule's facts say why it was skipped (wrong relationship versus no
// open comments); they reach the run record and, per rule, one log line.

const planWithSkipFacts = `{"actions":[
 {"op":"create","kind":"anchor","target":null,"fields":{},"rule":"anchor.lazy","facts":{"why":"none"}}],
 "skipped":[
  {"rule":"feedback.digest-changed","reason":"not matched","facts":{"relationship":"none"}},
  {"rule":"conflict.present","reason":"not matched"}]}`

func skipFactsRun(t *testing.T, logSkips bool) (rec RunRecord, stderr string) {
	t.Helper()
	s := hydrated().on("pg-decider plan pr", ok(planWithSkipFacts))
	o, _, errb := opts(t, ModePlan, s)
	o.RunRecordPath = filepath.Join(t.TempDir(), RunRecordFileName)
	o.LogSkips = logSkips
	if got := Run(context.Background(), o); got != ExitOK {
		t.Fatalf("exit %d: %s", got, errb)
	}
	return readRecords(t, o.RunRecordPath)[0], errb.String()
}

func TestSkippedRuleFactsAppearInTheRunRecord(t *testing.T) {
	rec, _ := skipFactsRun(t, false)
	var found bool
	for _, r := range rec.Decide.Rules {
		if r.Rule == "feedback.digest-changed" {
			found = true
			if r.Facts["relationship"] != "none" {
				t.Errorf("facts %v lack relationship=none", r.Facts)
			}
		}
		if r.Rule == "anchor.lazy" && r.Facts["why"] != "none" {
			t.Errorf("planned rule facts %v", r.Facts)
		}
	}
	if !found {
		t.Fatalf("rule missing: %+v", rec.Decide.Rules)
	}
}

func TestOneLogLinePerRuleWithFactsAndEventID(t *testing.T) {
	rec, errs := skipFactsRun(t, true)
	var lines []string
	for _, l := range strings.Split(errs, "\n") {
		if strings.Contains(l, " rule=") {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(rec.Decide.Rules) || len(lines) != 3 {
		t.Fatalf("want 3 rule lines, got %d: %q", len(lines), errs)
	}
	var skip string
	for _, l := range lines {
		if strings.Contains(l, "rule=feedback.digest-changed") {
			skip = l
		}
	}
	for _, want := range []string{"outcome=skipped", `reason="not matched"`, `facts={"relationship":"none"}`, "event=" + testID, "run=" + rec.RunID} {
		if !strings.Contains(skip, want) {
			t.Errorf("line %q lacks %q", skip, want)
		}
	}
}

func TestSkipLinesAreOffByDefaultButActionLinesAreNot(t *testing.T) {
	_, errs := skipFactsRun(t, false)
	if strings.Contains(errs, "rule=feedback.digest-changed") || strings.Contains(errs, "rule=conflict.present") {
		t.Errorf("skip lines logged by default: %q", errs)
	}
	if !strings.Contains(errs, "rule=anchor.lazy") {
		t.Errorf("planned action line missing: %q", errs)
	}
}
