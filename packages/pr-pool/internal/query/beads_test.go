package query

import (
	"context"
	"testing"
)

// fakeBD records args and returns canned bd JSON.
type fakeBD struct {
	args []string
	out  string
	err  error
}

func (f *fakeBD) Run(_ context.Context, args ...string) (string, error) {
	f.args = args
	return f.out, f.err
}

func TestBeadsReady_argsAndPostFilter(t *testing.T) {
	bd := &fakeBD{out: `[
	  {"id":"c1","issue_type":"task","title":"process-feedback: x"},
	  {"id":"c2","issue_type":"task","title":"not a cycle"},
	  {"id":"c3","issue_type":"bug","title":"process-feedback: y"}
	]`}
	q := BeadsReady{
		Labels: []string{"mine"}, ExcludeLabels: []string{"human"},
		TitlePrefix: "process-feedback:", ItemType: "task",
	}
	items, err := q.Run(context.Background(), Env{BD: bd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "c1" {
		t.Fatalf("post-filter wrong: %+v", items)
	}
	want := "ready --label mine --exclude-label human --json --limit 0"
	if got := join(bd.args); got != want {
		t.Fatalf("args = %q want %q", got, want)
	}
}

func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
