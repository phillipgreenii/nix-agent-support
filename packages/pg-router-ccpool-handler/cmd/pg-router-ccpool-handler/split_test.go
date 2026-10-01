package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type splitFake struct{ calls []string }

func (f *splitFake) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "show":
		return `{"id":"pg2-p","status":"open","labels":["needs-split-review","budget-stop:a"]}`, nil
	case "create":
		return "pg2-c" + string(rune('0'+len(f.calls))) + "\n", nil
	case "dep":
		if args[1] == "list" {
			var parts []string
			for _, c := range f.calls {
				if strings.HasPrefix(c, "dep add pg2-p ") {
					parts = append(parts, `{"id":"`+strings.TrimPrefix(c, "dep add pg2-p ")+`"}`)
				}
			}
			return `{"data":[` + strings.Join(parts, ",") + `]}`, nil
		}
	}
	return "", nil
}

func TestRunSplit_applyAndUnsplittable(t *testing.T) {
	f := &splitFake{}
	var out, errb bytes.Buffer
	plan := `{"rationale":"r","children":[{"title":"a","description":"d","acceptance":"x"},{"title":"b","description":"d","acceptance":"x"}]}`
	if rc := runSplitWith([]string{"apply", "pg2-p"}, strings.NewReader(plan), &out, &errb, f); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	if len(strings.Fields(out.String())) != 2 {
		t.Errorf("want 2 child ids on stdout, got %q", out.String())
	}
	g := &splitFake{}
	if rc := runSplitWith([]string{"unsplittable", "pg2-p"}, strings.NewReader("why"), &out, &errb, g); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	joined := strings.Join(g.calls, "\n")
	if !strings.Contains(joined, "update pg2-p --add-label human") || !strings.Contains(joined, "update pg2-p --remove-label needs-split-review") {
		t.Errorf("calls=%v", g.calls)
	}
}

func TestRunSplit_usageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"apply"}, {"bogus", "pg2-p"}, {"apply", "--evil"}, {"apply", "pg2-p; rm"}} {
		var out, errb bytes.Buffer
		f := &splitFake{}
		if rc := runSplitWith(args, strings.NewReader("{}"), &out, &errb, f); rc != 2 {
			t.Errorf("%v: rc=%d, want 2 (usage)", args, rc)
		}
		if len(f.calls) != 0 {
			t.Errorf("%v: usage error must not call bd: %v", args, f.calls)
		}
	}
	var out, errb bytes.Buffer
	if rc := runSplitWith([]string{"apply", "pg2-p"}, strings.NewReader("{}"), &out, &errb, &splitFake{}); rc == 0 {
		t.Error("an invalid plan must fail")
	}
}
